// Package vmm preserves the original native microVM API import path.
//
// Deprecated: use github.com/CYPT71/secure-oci-base/sdk/microvm.
package vmm

import sdk "github.com/CYPT71/secure-oci-base/sdk/microvm"

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
