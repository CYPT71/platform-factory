package vmm_test

import (
	legacy "github.com/CYPT71/secure-oci-base/api/vmm"
	sdk "github.com/CYPT71/secure-oci-base/sdk/microvm"
)

var _ sdk.MachineSpec = legacy.MachineSpec{}
var _ legacy.MachineSpec = sdk.MachineSpec{}
