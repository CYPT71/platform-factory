// Package v1 defines the stable backend-neutral VMM contract.
package v1

import sdk "github.com/CYPT71/platform-factory/sdk/microvm"

const APIVersion = sdk.APIVersion

type MachineState = sdk.MachineState

const (
	StateCreated = sdk.StateCreated
	StateRunning = sdk.StateRunning
	StateStopped = sdk.StateStopped
	StateFailed  = sdk.StateFailed
)

type Port = sdk.Port
type Volume = sdk.Volume
type Resources = sdk.Resources
type BootBundle = sdk.BootBundle
type MachineSpec = sdk.MachineSpec
type MachineStatus = sdk.MachineStatus
type Device = sdk.Device
type GuestAgent = sdk.GuestAgent
type Machine = sdk.Machine
type VMM = sdk.VMM
type Capabilities = sdk.Capabilities
type StateStore = sdk.StateStore
