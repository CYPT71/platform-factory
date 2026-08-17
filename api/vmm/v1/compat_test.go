package v1_test

import (
	api "github.com/CYPT71/platform-factory/api/vmm/v1"
	sdk "github.com/CYPT71/platform-factory/sdk/microvm"
)

var _ sdk.MachineSpec = api.MachineSpec{}
var _ api.MachineSpec = sdk.MachineSpec{}
