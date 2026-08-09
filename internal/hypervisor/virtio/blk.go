// Package virtio provides virtio block device.
package virtio

// BlockConfig for virtio-blk
type BlockConfig struct {
	Capacity  uint64
	SizeMax   uint32
	SegMax    uint32
	MinIOSize uint16
	OptIOSize uint32
	BlkSize   uint32
	NumQueues uint16
}

// BlockDevice represents a virtio block device.
type BlockDevice struct {
	Config BlockConfig
}

// NewBlockDevice creates a new block device.
func NewBlockDevice(capacity uint64) *BlockDevice {
	return &BlockDevice{
		Config: BlockConfig{
			Capacity:  capacity,
			SizeMax:   4096,
			SegMax:    128,
			BlkSize:   512,
			NumQueues: 1,
		},
	}
}
