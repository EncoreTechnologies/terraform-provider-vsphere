// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package vsphere

import (
	"fmt"
	"os"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-provider-vsphere/vsphere/internal/helper/testhelper"
)

func TestAccDataSourceVSphereHostConfigSyslog_UsingHostSystemID(t *testing.T) {
	resourceName := "data.vsphere_host_config_syslog.h1"

	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			RunSweepers()
			testAccPreCheck(t)
			testAccCheckEnvVariablesF(t, []string{"ESX_LOG_HOST", "TF_VAR_VSPHERE_ESXI1"})
		},
		Providers: testAccProviders,
		Steps: []resource.TestStep{
			{
				Config: testAccDataSourceVSphereHostConfigSyslogConfig(false),
				Check: resource.ComposeTestCheckFunc(
					resource.TestMatchResourceAttr(
						resourceName,
						"id",
						regexp.MustCompile("^host-"),
					),
				),
			},
			{
				Config: testAccDataSourceVSphereHostConfigSyslogConfig(true),
				Check: resource.ComposeTestCheckFunc(
					resource.TestMatchResourceAttr(
						resourceName,
						"id",
						regexp.MustCompile("^host-"),
					),
				),
				ExpectError: regexp.MustCompile("Attribute 'id' didn't match"),
			},
		},
	})
}

func testAccDataSourceVSphereHostConfigSyslogConfig(useHostname bool) string {
	idStr := "host_system_id = data.vsphere_host.roothost1.id"

	if useHostname {
		idStr = `hostname = "` + os.Getenv("TF_VAR_VSPHERE_ESXI1") + `"`
	}

	return fmt.Sprintf(
		`
		%s

		resource "vsphere_host_config_syslog" "h1" {
			%s
			log_host = "%s"
		}

		data "vsphere_host_config_syslog" "h1" {
			%s
		}
		`,
		testhelper.CombineConfigs(
			testhelper.ConfigDataRootDC1(),
			testhelper.ConfigDataRootComputeCluster1(),
			testhelper.ConfigDataRootHost1(),
		),
		idStr,
		os.Getenv("ESXI_LOG_HOST"),
		idStr,
	)
}
