//go:build linux && amd64

package directboot

import (
	"context"
	"errors"

	"github.com/CYPT71/secure-oci-base/internal/hypervisor/kvm"
)

func Run(ctx context.Context, config Config) (Result, error) {
	return runLinux(ctx, config, nil)
}

// RunWithGuestAgent boots the same non-OCI MicroVM as Run while attaching an
// explicitly provisioned authenticated guest channel. OCI is not involved.
func RunWithGuestAgent(ctx context.Context, config Config, options GuestAgentOptions) (Result, error) {
	return runLinux(ctx, config, &options)
}

func runLinux(ctx context.Context, config Config, guestOptions *GuestAgentOptions) (Result, error) {
	if config.VCPUs != 1 {
		return Result{}, errors.New("direct boot: current KVM backend requires exactly one vCPU")
	}
	kernel, err := readPinned(config.KernelPath, config.KernelDigest, "kernel", true)
	if err != nil {
		return Result{}, err
	}
	initramfs, err := readPinned(config.InitramfsPath, config.InitramfsDigest, "initramfs", false)
	if err != nil {
		return Result{}, err
	}
	if guestOptions == nil {
		result, err := kvm.RunLinux(ctx, config.MemoryMiB<<20, kernel, initramfs, config.CommandLine, 1<<20)
		return Result{Serial: result.Serial}, err
	}
	if len(initramfs) == 0 {
		return Result{}, errors.New("direct boot: guest agent requires a pinned, pre-provisioned initramfs")
	}
	agent, guestChannel, err := prepareGuestAgent(ctx, *guestOptions)
	if err != nil {
		return Result{}, err
	}
	if closer, ok := agent.(interface{ Close() error }); ok {
		defer closer.Close()
	}
	result, err := kvm.RunLinuxWithOptions(ctx, config.MemoryMiB<<20, kernel, initramfs, config.CommandLine, 1<<20,
		kvm.LinuxRunOptions{GuestChannel: guestChannel})
	return Result{Serial: result.Serial}, err
}
