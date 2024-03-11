// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package vsphere

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-provider-vsphere/vsphere/internal/helper/hostsystem"
	"github.com/hashicorp/terraform-provider-vsphere/vsphere/internal/helper/iscsi"
)

func resourceVSphereIscsiSoftwareAdapter() *schema.Resource {
	return &schema.Resource{
		Create: resourceVSphereIscsiSoftwareAdapterCreate,
		Read:   resourceVSphereIscsiSoftwareAdapterRead,
		Update: resourceVSphereIscsiSoftwareAdapterUpdate,
		Delete: resourceVSphereIscsiSoftwareAdapterDelete,
		Importer: &schema.ResourceImporter{
			StateContext: resourceVSphereIscsiSoftwareAdapterImport,
		},

		Schema: map[string]*schema.Schema{
			"host_system_id": {
				Type:         schema.TypeString,
				Optional:     true,
				ForceNew:     true,
				Description:  "Host to enable iscsi software adapter",
				ExactlyOneOf: []string{"hostname"},
			},
			"hostname": {
				Type:        schema.TypeString,
				Optional:    true,
				ForceNew:    true,
				Description: "Hostname of host system to enable software adapter",
			},
			"iscsi_name": {
				Type:        schema.TypeString,
				Computed:    true,
				Optional:    true,
				Description: "The unique iqn name for the iscsi software adapter if enabled.  If left blank, vmware will generate the iqn name",
			},
			"adapter_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Iscsi adapter name that is created when enabling software adapter.  This will be in the form of 'vmhb<unique_name>'",
			},
		},
	}
}

func resourceVSphereIscsiSoftwareAdapterCreate(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*Client).vimClient
	host, hr, err := hostsystem.FromHostnameOrID(client, d)
	if err != nil {
		return fmt.Errorf("error retrieving host for iscsi: %s", err)
	}

	hss, err := hostsystem.GetHostStorageSystemFromHost(client, host)
	if err != nil {
		return err
	}

	if err = iscsi.UpdateSoftwareInternetScsi(client, hss.Reference(), host.Name(), true); err != nil {
		return err
	}

	if err = hss.RescanAllHba(context.Background()); err != nil {
		return fmt.Errorf(
			"error trying to rescan storage adapters after enabling iscsi software adapter for host '%s': %s",
			host.Name(),
			err,
		)
	}

	hssProps, err := hostsystem.HostStorageSystemProperties(hss)
	if err != nil {
		return err
	}

	adapter, err := iscsi.GetIscsiSoftwareAdater(hssProps, host.Name())
	if err != nil {
		return err
	}

	d.SetId(fmt.Sprintf("%s:%s", hr.Value, adapter.Device))
	d.Set("adapter_id", adapter.Device)

	if name, ok := d.GetOk("iscsi_name"); ok {
		if err = iscsi.UpdateIscsiName(host.Name(), adapter.Device, name.(string), client, hssProps.Reference()); err != nil {
			return err
		}

		d.Set("iscsi_name", name.(string))
	} else {
		d.Set("iscsi_name", adapter.IScsiName)
	}

	return resourceVSphereIscsiSoftwareAdapterRead(d, meta)
}

func resourceVSphereIscsiSoftwareAdapterRead(d *schema.ResourceData, meta interface{}) error {
	return iscsiSoftwareAdapterRead(d, meta, false)
}

func resourceVSphereIscsiSoftwareAdapterUpdate(d *schema.ResourceData, meta interface{}) error {
	var err error

	client := meta.(*Client).vimClient
	host, _, err := hostsystem.FromHostnameOrID(client, d)
	if err != nil {
		return fmt.Errorf("error retrieving host for iscsi update: %s", err)
	}

	hssProps, err := hostsystem.GetHostStorageSystemPropertiesFromHost(client, host)
	if err != nil {
		return fmt.Errorf("error retrieving host system storage properties on update for host '%s': %s", host.Name(), err)
	}

	if d.HasChange("iscsi_name") {
		_, iscsiName := d.GetChange("iscsi_name")
		adapter, err := iscsi.GetIscsiSoftwareAdater(hssProps, host.Name())
		if err != nil {
			return fmt.Errorf("error retrieving iscsi software adapter on update for host '%s': %s", host.Name(), err)
		}

		if err = iscsi.UpdateIscsiName(
			host.Name(),
			adapter.Device,
			iscsiName.(string),
			client,
			hssProps.Reference(),
		); err != nil {
			return fmt.Errorf("error updating iscsi software name on update for host '%s': %s", host.Name(), err)
		}
	}

	return nil
}

func resourceVSphereIscsiSoftwareAdapterDelete(d *schema.ResourceData, meta interface{}) error {
	var err error
	client := meta.(*Client).vimClient
	host, _, err := hostsystem.FromHostnameOrID(client, d)
	if err != nil {
		return fmt.Errorf("error retrieving host for iscsi delete: %s", err)
	}

	hssProps, err := hostsystem.GetHostStorageSystemPropertiesFromHost(client, host)
	if err != nil {
		return fmt.Errorf(
			"error retrieving host system storage properties on delete for host '%s': %s",
			host.Name(),
			err,
		)
	}

	return iscsi.UpdateSoftwareInternetScsi(client, hssProps.Reference(), host.Name(), false)
}

func resourceVSphereIscsiSoftwareAdapterImport(ctx context.Context, d *schema.ResourceData, meta interface{}) ([]*schema.ResourceData, error) {
	client := meta.(*Client).vimClient
	idSplit := strings.Split(d.Id(), ":")

	if len(idSplit) != 2 {
		return nil, fmt.Errorf("invalid import format.  Format should be <host_system_id | hostname>:<adapter_name>")
	}

	host, hr, err := hostsystem.CheckIfHostnameOrID(client, idSplit[0])
	if err != nil {
		return nil, fmt.Errorf("error retrieving host for iscsi import: %s", err)
	}

	hssProps, err := hostsystem.GetHostStorageSystemPropertiesFromHost(client, host)
	if err != nil {
		return nil, fmt.Errorf(
			"error retrieving host system storage properties on import for host '%s': %s",
			host.Name(),
			err,
		)
	}

	adapter, err := iscsi.GetIscsiSoftwareAdater(hssProps, host.Name())
	if err != nil {
		return nil, fmt.Errorf("error retrieving iscsi software adapter on import for host '%s': %s", host.Name(), err)
	}

	d.SetId(fmt.Sprintf("%s:%s", hr.Value, adapter.Device))
	d.Set(hr.IDName, hr.Value)
	return []*schema.ResourceData{d}, nil
}

func iscsiSoftwareAdapterRead(d *schema.ResourceData, meta interface{}, isDataSource bool) error {
	client := meta.(*Client).vimClient
	host, _, err := hostsystem.FromHostnameOrID(client, d)
	if err != nil {
		return fmt.Errorf("error retrieving host for iscsi read: %s", err)
	}

	hssProps, err := hostsystem.GetHostStorageSystemPropertiesFromHost(client, host)
	if err != nil {
		return err
	}

	if hssProps.StorageDeviceInfo.SoftwareInternetScsiEnabled {
		adapter, err := iscsi.GetIscsiSoftwareAdater(hssProps, host.Name())
		if err != nil {
			return err
		}

		d.Set("iscsi_name", adapter.IScsiName)
		d.Set("adapter_id", adapter.Device)
	} else if isDataSource {
		return fmt.Errorf("iscsi software adapter is not enabled for host '%s'", host.Name())
	}

	return nil
}
