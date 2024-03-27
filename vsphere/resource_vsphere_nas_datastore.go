// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package vsphere

import (
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-provider-vsphere/vsphere/internal/helper/customattribute"
	"github.com/hashicorp/terraform-provider-vsphere/vsphere/internal/helper/datastore"
	"github.com/hashicorp/terraform-provider-vsphere/vsphere/internal/helper/folder"
	"github.com/hashicorp/terraform-provider-vsphere/vsphere/internal/helper/hostsystem"
	"github.com/hashicorp/terraform-provider-vsphere/vsphere/internal/helper/structure"
	"github.com/hashicorp/terraform-provider-vsphere/vsphere/internal/helper/viapi"
	"github.com/vmware/govmomi/vim25/types"
)

func resourceVSphereNasDatastore() *schema.Resource {
	s := map[string]*schema.Schema{
		"name": {
			Type:        schema.TypeString,
			Description: "The name of the datastore.",
			Required:    true,
		},
		"host_system_ids": {
			Type:         schema.TypeSet,
			Optional:     true,
			Description:  "The managed object IDs of the hosts to mount the datastore on.",
			Elem:         &schema.Schema{Type: schema.TypeString},
			ExactlyOneOf: []string{"hostnames"},
		},
		"hostnames": {
			Type:        schema.TypeSet,
			Optional:    true,
			Description: "The hostnames of the hosts to mount the datastore on.",
			Elem:        &schema.Schema{Type: schema.TypeString},
		},
		"folder": {
			Type:          schema.TypeString,
			Description:   "The path to the datastore folder to put the datastore in.",
			Optional:      true,
			ConflictsWith: []string{"datastore_cluster_id"},
			StateFunc:     folder.NormalizePath,
		},
		"datastore_cluster_id": {
			Type:          schema.TypeString,
			Description:   "The managed object ID of the datastore cluster to place the datastore in.",
			Optional:      true,
			ConflictsWith: []string{"folder"},
		},
	}
	structure.MergeSchema(s, schemaHostNasVolumeSpec())
	structure.MergeSchema(s, schemaDatastoreSummary())

	// Add tags schema
	s[vSphereTagAttributeKey] = tagsSchema()
	// Add custom attribute schema
	s[customattribute.ConfigKey] = customattribute.ConfigSchema()

	return &schema.Resource{
		Create: resourceVSphereNasDatastoreCreate,
		Read:   resourceVSphereNasDatastoreRead,
		Update: resourceVSphereNasDatastoreUpdate,
		Delete: resourceVSphereNasDatastoreDelete,
		Importer: &schema.ResourceImporter{
			State: resourceVSphereNasDatastoreImport,
		},
		Schema: s,
	}
}

func resourceVSphereNasDatastoreCreate(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*Client).vimClient

	// Load up the tags client, which will validate a proper vCenter before
	// attempting to proceed if we have tags defined.
	tagsClient, err := tagsManagerIfDefined(d, meta)
	if err != nil {
		return err
	}
	// Verify a proper vCenter before proceeding if custom attributes are defined
	attrsProcessor, err := customattribute.GetDiffProcessorIfAttributesDefined(client, d)
	if err != nil {
		return err
	}

	var hosts []string

	if len(d.Get("host_system_ids").(*schema.Set).List()) > 0 {
		hosts = structure.SliceInterfacesToStrings(d.Get("host_system_ids").(*schema.Set).List())
	} else {
		hosts = structure.SliceInterfacesToStrings(d.Get("hostnames").(*schema.Set).List())
	}

	volSpec, err := expandHostNasVolumeSpec(d)
	if err != nil {
		return err
	}
	p := &nasDatastoreMountProcessor{
		client:   client,
		oldHSIDs: nil,
		newHSIDs: hosts,
		volSpec:  volSpec,
	}
	ds, err := p.processMountOperations()
	if ds != nil {
		d.SetId(ds.Reference().Value)
	}
	if err != nil {
		return fmt.Errorf("error mounting datastore: %s", err)
	}

	// Move the datastore to the correct folder or datastore cluster first, if
	// specified.
	f, err := resourceVSphereDatastoreApplyFolderOrStorageClusterPath(d, meta)
	if err != nil {
		return err
	}
	if !folder.PathIsEmpty(f) {
		if err := datastore.MoveToFolderRelativeHostSystemID(client, ds, hosts[0], f); err != nil {
			return fmt.Errorf("error moving datastore to folder: %s", err)
		}
	}

	// Apply any pending tags now
	if tagsClient != nil {
		if err := processTagDiff(tagsClient, d, ds); err != nil {
			return err
		}
	}

	// Set custom attributes
	if attrsProcessor != nil {
		if err := attrsProcessor.ProcessDiff(ds); err != nil {
			return err
		}
	}

	// Done
	return resourceVSphereNasDatastoreRead(d, meta)
}

func resourceVSphereNasDatastoreRead(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*Client).vimClient
	id := d.Id()
	ds, err := datastore.FromID(client, id)
	if err != nil {
		return fmt.Errorf("cannot find datastore: %s", err)
	}
	props, err := datastore.Properties(ds)
	if err != nil {
		return fmt.Errorf("could not get properties for datastore: %s", err)
	}
	if err := flattenDatastoreSummary(d, &props.Summary); err != nil {
		return err
	}

	// Set the folder
	if err := resourceVSphereDatastoreReadFolderOrStorageClusterPath(d, ds); err != nil {
		return err
	}

	// Update NAS spec
	if err := flattenHostNasVolume(d, props.Info.(*types.NasDatastoreInfo).Nas); err != nil {
		return err
	}

	var hostTfID string

	if len(d.Get("host_system_ids").(*schema.Set).List()) > 0 {
		hostTfID = "host_system_ids"
	} else {
		hostTfID = "hostnames"
	}

	// Update mounted hosts
	var mountedHosts []string
	for _, mount := range props.Host {
		if hostTfID == "host_system_ids" {
			mountedHosts = append(mountedHosts, mount.Key.Value)
		} else {
			host, _, err := hostsystem.CheckIfHostnameOrID(client, mount.Key.Value)
			if err != nil {
				return fmt.Errorf("error finding host for datastore: %s", err)
			}

			mountedHosts = append(mountedHosts, host.Name())
		}
	}

	if err = d.Set(hostTfID, mountedHosts); err != nil {
		return err
	}

	// Read tags if we have the ability to do so
	if tagsClient, _ := meta.(*Client).TagsManager(); tagsClient != nil {
		if err := readTagsForResource(tagsClient, ds, d); err != nil {
			return err
		}
	}

	// Read custom attributes
	if customattribute.IsSupported(client) {
		customattribute.ReadFromResource(props.Entity(), d)
	}

	return nil
}

func resourceVSphereNasDatastoreUpdate(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*Client).vimClient

	// Load up the tags client, which will validate a proper vCenter before
	// attempting to proceed if we have tags defined.
	tagsClient, err := tagsManagerIfDefined(d, meta)
	if err != nil {
		return err
	}
	// Verify a proper vCenter before proceeding if custom attributes are defined
	attrsProcessor, err := customattribute.GetDiffProcessorIfAttributesDefined(client, d)
	if err != nil {
		return err
	}

	id := d.Id()
	ds, err := datastore.FromID(client, id)
	if err != nil {
		return fmt.Errorf("cannot find datastore: %s", err)
	}

	// Rename this datastore if our name has drifted.
	if d.HasChange("name") {
		if err := viapi.RenameObject(client, ds.Reference(), d.Get("name").(string)); err != nil {
			return err
		}
	}

	// Update folder or datastore cluster if necessary
	if d.HasChange("folder") || d.HasChange("datastore_cluster_id") {
		f, err := resourceVSphereDatastoreApplyFolderOrStorageClusterPath(d, meta)
		if err != nil {
			return err
		}
		if err := datastore.MoveToFolder(client, ds, f); err != nil {
			return fmt.Errorf("could not move datastore to folder %q: %s", f, err)
		}
	}

	// Apply any pending tags now
	if tagsClient != nil {
		if err := processTagDiff(tagsClient, d, ds); err != nil {
			return err
		}
	}

	// Apply custom attribute updates
	if attrsProcessor != nil {
		if err := attrsProcessor.ProcessDiff(ds); err != nil {
			return err
		}
	}

	var hostTfID string

	if len(d.Get("host_system_ids").(*schema.Set).List()) > 0 {
		hostTfID = "host_system_ids"
	} else {
		hostTfID = "hostnames"
	}

	// Process mount/unmount operations.
	o, n := d.GetChange(hostTfID)
	volSpec, err := expandHostNasVolumeSpec(d)
	if err != nil {
		return err
	}
	p := &nasDatastoreMountProcessor{
		client:   client,
		oldHSIDs: structure.SliceInterfacesToStrings(o.(*schema.Set).List()),
		newHSIDs: structure.SliceInterfacesToStrings(n.(*schema.Set).List()),
		volSpec:  volSpec,
		ds:       ds,
	}
	// Unmount first
	if err := p.processUnmountOperations(); err != nil {
		return fmt.Errorf("error unmounting hosts: %s", err)
	}
	// Now mount
	if _, err := p.processMountOperations(); err != nil {
		return fmt.Errorf("error mounting hosts: %s", err)
	}

	// Should be done with the update here.
	return resourceVSphereNasDatastoreRead(d, meta)
}

func resourceVSphereNasDatastoreDelete(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*Client).vimClient
	dsID := d.Id()
	ds, err := datastore.FromID(client, dsID)
	if err != nil {
		return fmt.Errorf("cannot find datastore: %s", err)
	}

	var hostTfID string

	if len(d.Get("host_system_ids").(*schema.Set).List()) > 0 {
		hostTfID = "host_system_ids"
	} else {
		hostTfID = "hostnames"
	}

	// Unmount the datastore from every host. Once the last host is unmounted we
	// are done and the datastore will delete itself.
	hosts := structure.SliceInterfacesToStrings(d.Get(hostTfID).(*schema.Set).List())
	volSpec, err := expandHostNasVolumeSpec(d)
	if err != nil {
		return err
	}
	p := &nasDatastoreMountProcessor{
		client:   client,
		oldHSIDs: hosts,
		newHSIDs: nil,
		volSpec:  volSpec,
		ds:       ds,
	}
	if err := p.processUnmountOperations(); err != nil {
		return fmt.Errorf("error unmounting hosts: %s", err)
	}

	return nil
}

func resourceVSphereNasDatastoreImport(d *schema.ResourceData, meta interface{}) ([]*schema.ResourceData, error) {
	// We support importing a MoRef - so we need to load the datastore and check
	// to make sure 1) it exists, and 2) it's a VMFS datastore. If it is, we are
	// good to go (rest of the stuff will be handled by read on refresh).
	client := meta.(*Client).vimClient
	id := d.Id()
	ds, err := datastore.FromID(client, id)
	if err != nil {
		return nil, fmt.Errorf("cannot find datastore: %s", err)
	}
	props, err := datastore.Properties(ds)
	if err != nil {
		return nil, fmt.Errorf("could not get properties for datastore: %s", err)
	}

	t := types.HostFileSystemVolumeFileSystemType(props.Summary.Type)
	if !isNasVolume(t) {
		return nil, fmt.Errorf("datastore ID %q is not a NAS datastore", id)
	}

	var accessMode string
	for _, hostMount := range props.Host {
		switch {
		case accessMode == "":
			accessMode = hostMount.MountInfo.AccessMode
		case accessMode != "" && accessMode != hostMount.MountInfo.AccessMode:
			// We don't support selective mount modes across multiple hosts. This
			// should almost never happen (there's no way to do it in the UI so it
			// would need to be done manually). Nonetheless we need to fail here.
			return nil, errors.New("access_mode is inconsistent across configured hosts")
		}
	}
	_ = d.Set("access_mode", accessMode)
	_ = d.Set("type", t)
	return []*schema.ResourceData{d}, nil
}
