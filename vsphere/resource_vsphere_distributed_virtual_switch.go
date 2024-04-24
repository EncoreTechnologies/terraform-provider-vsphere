// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package vsphere

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-provider-vsphere/vsphere/internal/helper/customattribute"
	"github.com/hashicorp/terraform-provider-vsphere/vsphere/internal/helper/folder"
	"github.com/hashicorp/terraform-provider-vsphere/vsphere/internal/helper/hostsystem"
	"github.com/hashicorp/terraform-provider-vsphere/vsphere/internal/helper/structure"
	"github.com/hashicorp/terraform-provider-vsphere/vsphere/internal/helper/viapi"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
)

func resourceVSphereDistributedVirtualSwitch() *schema.Resource {
	s := map[string]*schema.Schema{
		"datacenter_id": {
			Type:        schema.TypeString,
			Description: "The ID of the datacenter to create this virtual switch in.",
			Required:    true,
			ForceNew:    true,
		},
		"folder": {
			Type:        schema.TypeString,
			Description: "The folder to create this virtual switch in, relative to the datacenter.",
			Optional:    true,
			ForceNew:    true,
		},
		"network_resource_control_enabled": {
			Type:        schema.TypeBool,
			Description: "Whether or not to enable network resource control, enabling advanced traffic shaping and resource control features.",
			Optional:    true,
		},
		// Tagging
		vSphereTagAttributeKey:    tagsSchema(),
		customattribute.ConfigKey: customattribute.ConfigSchema(),
	}
	structure.MergeSchema(s, schemaDVSCreateSpec())

	return &schema.Resource{
		Create:        resourceVSphereDistributedVirtualSwitchCreate,
		Read:          resourceVSphereDistributedVirtualSwitchRead,
		Update:        resourceVSphereDistributedVirtualSwitchUpdate,
		Delete:        resourceVSphereDistributedVirtualSwitchDelete,
		CustomizeDiff: resourceVSphereDistributedVirtualSwitchCustomDiff,
		Importer: &schema.ResourceImporter{
			State: resourceVSphereDistributedVirtualSwitchImport,
		},
		Schema: s,
	}
}

func resourceVSphereDistributedVirtualSwitchCreate(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*Client).vimClient
	if err := viapi.ValidateVirtualCenter(client); err != nil {
		return err
	}
	tagsClient, err := tagsManagerIfDefined(d, meta)
	if err != nil {
		return err
	}
	// Verify a proper vCenter before proceeding if custom attributes are defined
	attrsProcessor, err := customattribute.GetDiffProcessorIfAttributesDefined(client, d)
	if err != nil {
		return err
	}

	dc, err := datacenterFromID(client, d.Get("datacenter_id").(string))
	if err != nil {
		return fmt.Errorf("cannot locate datacenter: %s", err)
	}
	fo, err := folder.FromPath(client, d.Get("folder").(string), folder.VSphereFolderTypeNetwork, dc)
	if err != nil {
		return fmt.Errorf("cannot locate folder: %s", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultAPITimeout)
	defer cancel()
	spec, err := expandDVSCreateSpec(d, client)
	if err != nil {
		return fmt.Errorf("error creating spec: %s", err)
	}

	task, err := fo.CreateDVS(ctx, spec)
	if err != nil {
		return fmt.Errorf("error creating DVS: %s", err)
	}
	tctx, tcancel := context.WithTimeout(context.Background(), defaultAPITimeout)
	defer tcancel()
	info, err := task.WaitForResult(tctx, nil)
	if err != nil {
		return fmt.Errorf("error waiting for DVS creation to complete: %s", err)
	}

	dvs, err := dvsFromMOID(client, info.Result.(types.ManagedObjectReference).Value)
	if err != nil {
		return fmt.Errorf("error fetching DVS after creation: %s", err)
	}
	props, err := dvsProperties(dvs)
	if err != nil {
		return fmt.Errorf("error fetching DVS properties after creation: %s", err)
	}

	d.SetId(props.Uuid)

	// Enable network resource I/O control if it needs to be enabled
	if d.Get("network_resource_control_enabled").(bool) {
		err = enableDVSNetworkResourceManagement(client, dvs, true)
		if err != nil {
			return err
		}
	}

	// Apply any pending tags now
	if tagsClient != nil {
		if err := processTagDiff(tagsClient, d, object.NewReference(client.Client, dvs.Reference())); err != nil {
			return fmt.Errorf("error updating tags: %s", err)
		}
	}

	// Set custom attributes
	if attrsProcessor != nil {
		if err := attrsProcessor.ProcessDiff(dvs); err != nil {
			return err
		}
	}

	return resourceVSphereDistributedVirtualSwitchRead(d, meta)
}

func resourceVSphereDistributedVirtualSwitchRead(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*Client).vimClient
	if err := viapi.ValidateVirtualCenter(client); err != nil {
		return err
	}
	id := d.Id()
	dvs, err := dvsFromUUID(client, id)
	if err != nil {
		return fmt.Errorf("could not find DVS %q: %s", id, err)
	}
	props, err := dvsProperties(dvs)
	if err != nil {
		return fmt.Errorf("error fetching DVS properties: %s", err)
	}

	// Set the datacenter ID, for completion's sake when importing
	dcp, err := folder.RootPathParticleNetwork.SplitDatacenter(dvs.InventoryPath)
	if err != nil {
		return fmt.Errorf("error parsing datacenter from inventory path: %s", err)
	}
	dc, err := getDatacenter(client, dcp)
	if err != nil {
		return fmt.Errorf("error locating datacenter: %s", err)
	}
	_ = d.Set("datacenter_id", dc.Reference().Value)

	// Set the folder
	f, err := folder.RootPathParticleNetwork.SplitRelativeFolder(dvs.InventoryPath)
	if err != nil {
		return fmt.Errorf("error parsing DVS path %q: %s", dvs.InventoryPath, err)
	}
	_ = d.Set("folder", folder.NormalizePath(f))

	// Read in config info
	if err := flattenVMwareDVSConfigInfo(d, client, props.Config.(*types.VMwareDVSConfigInfo)); err != nil {
		return err
	}

	// Read tags if we have the ability to do so
	if tagsClient, _ := meta.(*Client).TagsManager(); tagsClient != nil {
		if err := readTagsForResource(tagsClient, dvs, d); err != nil {
			return fmt.Errorf("error reading tags: %s", err)
		}
	}

	// Read set custom attributes
	if customattribute.IsSupported(client) {
		customattribute.ReadFromResource(props.Entity(), d)
	}

	return nil
}

func resourceVSphereDistributedVirtualSwitchUpdate(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*Client).vimClient
	if err := viapi.ValidateVirtualCenter(client); err != nil {
		return err
	}
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
	dvs, err := dvsFromUUID(client, id)
	if err != nil {
		return fmt.Errorf("could not find DVS %q: %s", id, err)
	}

	// If we have a pending version upgrade, do that first.
	if d.HasChange("version") {
		old, newValue := d.GetChange("version")
		var ovi, nvi int
		for n, v := range dvsVersions {
			if old.(string) == v {
				ovi = n
			}
			if newValue.(string) == v {
				nvi = n
			}
		}
		if nvi < ovi {
			return fmt.Errorf("downgrading dvSwitches are not allowed (old: %s new: %s)", old, newValue)
		}
		if err := upgradeDVS(client, dvs, newValue.(string)); err != nil {
			return fmt.Errorf("could not upgrade DVS: %s", err)
		}
		props, err := dvsProperties(dvs)
		if err != nil {
			return fmt.Errorf("could not get DVS properties after upgrade: %s", err)
		}
		// ConfigVersion increments after a DVS upgrade, which means this needs to
		// be updated before the post-update read to ensure that we don't run into
		// ConcurrentAccess errors on the update operation below.
		_ = d.Set("config_version", props.Config.(*types.VMwareDVSConfigInfo).ConfigVersion)
	}

	expandCfg := dvswitchExpandConfig{}
	removedUplinks := []interface{}{}

	if d.HasChange("uplinks") {
		o, n := d.GetChange("uplinks")
		oldList := o.([]interface{})
		newList := n.([]interface{})

		if len(newList) < len(oldList) {
			expandCfg.IsUplinksRemoved = true

			for _, oLink := range oldList {
				found := false

				for _, nLink := range newList {
					if oLink == nLink {
						found = true
					}
				}

				if !found {
					removedUplinks = append(removedUplinks, oLink)
				}
			}
		} else if len(oldList) < len(newList) {
			expandCfg.IsUplinksAdded = true
		}
	}

	spec, err := expandVMwareDVSConfigSpec(d, client, dvs, expandCfg)
	if err != nil {
		return fmt.Errorf("error retrieving config: %s", err)
	}

	if err := updateDVSConfiguration(dvs, spec); err != nil {
		return fmt.Errorf("could not update DVS on first update: %s", err)
	}

	// If the "uplinks" attribute is updated to add or remove uplink entries,
	// we have to call the "updateDVSConfiguration" function twice, once to update
	// the uplinks and once to update all the hosts's devices
	//
	// If we don't seperate the calls, vmware throws an error indicating a resource
	// is in use when you remove an uplink and will throw error that the host is
	// configured with a wrong nic when adding an uplink
	if expandCfg.IsUplinksRemoved || expandCfg.IsUplinksAdded {
		// This is a hack around the fact that the "updateDVSConfiguration" function
		// does not fully complete the distributed switch configuration
		//
		// In theory the "updateDVSConfiguration" function waits for the configuration
		// to finish before proceeding but if we try to call the "updateDVSConfiguration"
		// function again right after, it throws an error so we have to continuously loop
		// through each host to verify that the removed nic cards are no longer connected
		if expandCfg.IsUplinksRemoved {
			currentHosts := d.Get("host").(*schema.Set).List()

			for _, h := range currentHosts {
				hostMap := h.(map[string]interface{})
				var tfID string

				if hostMap["host_system_id"] != "" {
					tfID = hostMap["host_system_id"].(string)
				} else {
					tfID = hostMap["hostname"].(string)
				}

				host, _, err := hostsystem.CheckIfHostnameOrID(client, tfID)
				if err != nil {
					return fmt.Errorf("error retrieving host on uplink removal: %s", err)
				}

				hsProps, err := hostsystem.Properties(host)
				if err != nil {
					return fmt.Errorf("error retrieving host properties on uplink removal: %s", err)
				}

				hns := object.NewHostNetworkSystem(client.Client, *hsProps.ConfigManager.NetworkSystem)

				// The overall process below is:
				// 1. Loop through each dvswitch the current host is connected to and match to the
				// one we are currently updating
				// 2. Loop through all the physical switches that are connected to the dvswitch from host
				// and compare against the removed uplink list
				// 3. If any of the removed unlink entries are found within the current host's nic list, continue
				// looping until they are no longer there
				for {
					var moHns mo.HostNetworkSystem

					if err = hns.Properties(context.Background(), hns.Reference(), nil, &moHns); err != nil {
						return fmt.Errorf("error retrieving host network properties on uplink removal: %s", err)
					}

					found := false

					for _, s := range moHns.NetworkConfig.ProxySwitch {
						backing := s.Spec.Backing.(*types.DistributedVirtualSwitchHostMemberPnicBacking)

						for _, removedUplink := range removedUplinks {
							for _, nic := range backing.PnicSpec {
								if removedUplink == nic.PnicDevice {
									found = true
								}
							}
						}
					}

					if !found {
						break
					}

					time.Sleep(time.Second * 5)
				}
			}
		}

		if expandCfg.IsUplinksAdded {
			// Looping to verify that the uplinks have been fully added to dvswitch so that hosts
			// can connect without erroring out
			for {
				dvsProps, err := dvsProperties(dvs)
				if err != nil {
					return fmt.Errorf("could not get DVS properties after uplinks added: %s", err)
				}

				if len(dvsProps.Config.GetDVSConfigInfo().UplinkPortPolicy.(*types.DVSNameArrayUplinkPortPolicy).UplinkPortName) == len(d.Get("uplinks").([]interface{})) {
					break
				}

				time.Sleep(time.Second * 1)
			}
		}

		if spec, err = expandVMwareDVSConfigSpec(d, client, dvs, dvswitchExpandConfig{
			IsUplinksRemoved: false,
			IsUplinksAdded:   false,
		}); err != nil {
			return fmt.Errorf("error retrieving config: %s", err)
		}
		if err = updateDVSConfiguration(dvs, spec); err != nil {
			return fmt.Errorf("could not update DVS on second update: %s", err)
		}
	}

	// Modify network I/O control if necessary
	if d.HasChange("network_resource_control_enabled") {
		err = enableDVSNetworkResourceManagement(client, dvs, d.Get("network_resource_control_enabled").(bool))
		if err != nil {
			return err
		}
	}

	// Apply any pending tags now
	if tagsClient != nil {
		if err := processTagDiff(tagsClient, d, object.NewReference(client.Client, dvs.Reference())); err != nil {
			return fmt.Errorf("error updating tags: %s", err)
		}
	}

	// Apply custom attribute updates
	if attrsProcessor != nil {
		if err := attrsProcessor.ProcessDiff(dvs); err != nil {
			return err
		}
	}

	return resourceVSphereDistributedVirtualSwitchRead(d, meta)
}

func resourceVSphereDistributedVirtualSwitchDelete(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*Client).vimClient
	if err := viapi.ValidateVirtualCenter(client); err != nil {
		return err
	}
	id := d.Id()
	dvs, err := dvsFromUUID(client, id)
	if err != nil {
		return fmt.Errorf("could not find DVS %q: %s", id, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultAPITimeout)
	defer cancel()
	task, err := dvs.Destroy(ctx)
	if err != nil {
		return fmt.Errorf("error deleting DVS: %s", err)
	}
	tctx, tcancel := context.WithTimeout(context.Background(), defaultAPITimeout)
	defer tcancel()
	if err := task.Wait(tctx); err != nil {
		return fmt.Errorf("error waiting for DVS deletion to complete: %s", err)
	}

	return nil
}

func resourceVSphereDistributedVirtualSwitchImport(d *schema.ResourceData, meta interface{}) ([]*schema.ResourceData, error) {
	// Due to the relative difficulty in trying to fetch a DVS's UUID, we use the
	// inventory path to the DVS instead, and just run it through finder. A full
	// path is required unless the default datacenter can be utilized.
	client := meta.(*Client).vimClient
	if err := viapi.ValidateVirtualCenter(client); err != nil {
		return nil, err
	}
	p := d.Id()
	dvs, err := dvsFromPath(client, p, nil)
	if err != nil {
		return nil, fmt.Errorf("error locating DVS: %s", err)
	}
	props, err := dvsProperties(dvs)
	if err != nil {
		return nil, fmt.Errorf("error fetching DVS properties: %s", err)
	}
	d.SetId(props.Uuid)
	return []*schema.ResourceData{d}, nil
}

func resourceVSphereDistributedVirtualSwitchCustomDiff(ctx context.Context, rd *schema.ResourceDiff, meta interface{}) error {
	hosts := rd.Get("host").(*schema.Set).List()

	for _, val := range hosts {
		host := val.(map[string]interface{})

		usingHostSystemID := false
		usingHostname := false

		if host["host_system_id"] != "" {
			usingHostSystemID = true
		}
		if host["hostname"] != "" {
			usingHostname = true
		}

		if usingHostname && usingHostSystemID {
			return fmt.Errorf("can't set both 'host_system_id' and 'hostname' attribute for a resource in the 'host' resource list")
		}
		if !usingHostname && !usingHostSystemID {
			return fmt.Errorf("must set either 'host_system_id' or 'hostname' attribute for all resources in the 'host' resource list")
		}
	}

	return nil
}
