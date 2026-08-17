//go:build darwin && cgo

package directboot

import (
	"context"

	"github.com/CYPT71/platform-factory/internal/hypervisor/hvf"
)

func Run(ctx context.Context, config Config) (Result, error) {
	if _, err := readPinned(config.KernelPath, config.KernelDigest, "kernel", true); err != nil {
		return Result{}, err
	}
	if _, err := readPinned(config.InitramfsPath, config.InitramfsDigest, "initramfs", false); err != nil {
		return Result{}, err
	}
	result, err := hvf.RunLinuxHVF(ctx, config.KernelPath, config.InitramfsPath,
		config.CommandLine, "", config.MemoryMiB<<20, config.VCPUs, "", nil)
	return Result{Serial: result.Serial}, err
}
