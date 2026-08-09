// Package virtio provides virtio network device.
package virtio

import "net"

// NetConfig for virtio-net
type NetConfig struct {
	MAC   [6]uint8
	MTU   uint16
	Speed uint32
}

// NetworkDevice represents a virtio network device.
type NetworkDevice struct {
	Config NetConfig
}

// NewNetworkDevice creates a new network device.
func NewNetworkDevice(mac net.HardwareAddr, mtu uint16) *NetworkDevice {
	var macBytes [6]uint8
	copy(macBytes[:], mac)
	return &NetworkDevice{
		Config: NetConfig{
			MAC:   macBytes,
			MTU:   mtu,
			Speed: 10000,
		},
	}
}

// MAC returns the MAC address.
func (d *NetworkDevice) MAC() net.HardwareAddr {
	return net.HardwareAddr(d.Config.MAC[:])
}
