---
subcategory: "Host and Cluster Management"
layout: "vsphere"
page_title: "VMware vSphere: vsphere_vnic"
sidebar_current: "docs-vsphere-resource-vnic"
description: |-
  Provides a VMware vSphere vnic resource..
---

# vsphere\_vnic

Provides a VMware vSphere vnic resource.

## Example Usages

**Create a vnic attached to a distributed virtual switch using the vmotion TCP/IP stack:**

```hcl
data "vsphere_datacenter" "dc" {
  name = "mydc"
}

data "vsphere_host" "h1" {
  name          = "esxi1.host.test"
  datacenter_id = data.vsphere_datacenter.dc.id
}

resource "vsphere_distributed_virtual_switch" "d1" {
  name          = "dc_DVPG0"
  datacenter_id = data.vsphere_datacenter.dc.id
  host {
    host_system_id = data.vsphere_host.h1.id
    devices        = ["vnic3"]
  }
}

resource "vsphere_distributed_port_group" "p1" {
  name                            = "test-pg"
  vlan_id                         = 1234
  distributed_virtual_switch_uuid = vsphere_distributed_virtual_switch.d1.id
}

resource "vsphere_vnic" "v1" {
  host_system_id          = data.vsphere_host.h1.id
  distributed_switch_port = vsphere_distributed_virtual_switch.d1.id
  distributed_port_group  = vsphere_distributed_port_group.p1.id
  ipv4 {
    dhcp = true
  }
  netstack = "vmotion"
}
```

**Create a vnic attached to a portgroup using the default TCP/IP stack:**

```hcl
data "vsphere_datacenter" "dc" {
  name = "mydc"
}

data "vsphere_host" "h1" {
  name          = "esxi1.host.test"
  datacenter_id = data.vsphere_datacenter.dc.id
}

resource "vsphere_host_virtual_switch" "hvs1" {
  name             = "dc_HPG0"
  hostname         = data.vsphere_host.h1.hostname
  network_adapters = ["vmnic3", "vmnic4"]
  active_nics      = ["vmnic3"]
  standby_nics     = ["vmnic4"]
}

resource "vsphere_host_port_group" "p1" {
  name                = "my-pg"
  virtual_switch_name = vsphere_host_virtual_switch.hvs1.name
  hostname      = data.vsphere_host.h1.hostname
}

resource "vsphere_vnic" "v1" {
  hostname            = data.vsphere_host.h1.hostname
  portgroup           = vsphere_host_port_group.p1.name
  ipv4 {
    dhcp = true
  }
  services = ["vsan", "management"]
}
```

## Argument Reference

* `portgroup` - (Optional) Portgroup to attach the nic to. Do not set if you set distributed_switch_port.
* `distributed_switch_port` - (Optional) UUID of the DVSwitch the nic will be attached to. Do not set if you set portgroup.
* `distributed_port_group` - (Optional) Key of the distributed portgroup the nic will connect to.
* `ipv4` - (Optional) IPv4 settings. Either this or `ipv6` needs to be set. See [IPv4 options](#ipv4-options) below.
* `ipv6` - (Optional) IPv6 settings. Either this or `ipv6` needs to be set. See [IPv6 options](#ipv6-options) below.
* `mac` - (Optional) MAC address of the interface.
* `mtu` - (Optional) MTU of the interface.
* `netstack` - (Optional) TCP/IP stack setting for this interface. Possible values are `defaultTcpipStack``, 'vmotion', 'vSphereProvisioning'. Changing this will force the creation of a new interface since it's not possible to change the stack once it gets created. (Default:`defaultTcpipStack`)
* `services` - (Optional) Enabled services setting for this interface. Currently support values are `vmotion`, `management`, and `vsan`.
* `host_system_id` - (Required/Optional) The host id of the host the vnic is connected to
* `hostname` - (Required/Optional) The hostname of the host the vnic is connected to

~> **NOTE:** Must choose either `host_system_id` or `hostname` but not both

### IPv4 Options

Configures the IPv4 settings of the network interface. Either DHCP or Static IP has to be set.

* `dhcp` - Use DHCP to configure the interface's IPv4 stack.
* `ip` - Address of the interface, if DHCP is not set.
* `netmask` - Netmask of the interface, if DHCP is not set.
* `gw` - IP address of the default gateway, if DHCP is not set.

### IPv6 Options

Configures the IPv6 settings of the network interface. Either DHCP or Autoconfig or Static IP has to be set.

* `dhcp` - Use DHCP to configure the interface's IPv6 stack.
* `autoconfig` - Use IPv6 Autoconfiguration (RFC2462).
* `addresses` -  List of IPv6 addresses
* `gw` - IP address of the default gateway, if DHCP or autoconfig is not set.

## Attribute Reference

* `id` - The ID of the vNic.

## Importing

An existing vNic can be [imported][docs-import] into this resource
via supplying the vNic's ID. An example is below:

[docs-import]: /docs/import/index.html

```
terraform import vsphere_vnic.v1 host-123_vmk2
```

The above would import the vnic `vmk2` from host with ID `host-123`.

We can also import by using hostname of host

```
terraform import vsphere_vnic.v1 host.example.com_vmk2
```

The above would import the vnic `vmk2` from host with hostname `host.example.com`.
