// Package virtio provides minimal virtio device implementations for KVM-based
// microVMs.
package virtio

// DeviceType represents a virtio device type identifier.
type DeviceType uint16

// Standard virtio device types.
const (
	DevInvalid DeviceType = iota
	DevNet
	DevBlock
	DevConsole
)
