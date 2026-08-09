//go:build linux

package main

import (
	"fmt"
	"os/exec"
	"syscall"
)

func applyProcessIdentity(command *exec.Cmd, identity processIdentity) {
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{
		Uid: identity.UID, Gid: identity.GID, Groups: identity.Groups,
	}}
}

func applyProcessUmask(mask uint32) {
	syscall.Umask(int(mask))
}

func applyProcessRlimits(limits []processRlimit) error {
	resources := map[string]int{
		// These values are the stable Linux uapi resource numbers shared by
		// every architecture supported by the guest kernel.
		"RLIMIT_CPU": 0, "RLIMIT_FSIZE": 1, "RLIMIT_DATA": 2, "RLIMIT_STACK": 3,
		"RLIMIT_CORE": 4, "RLIMIT_RSS": 5, "RLIMIT_NPROC": 6, "RLIMIT_NOFILE": 7,
		"RLIMIT_MEMLOCK": 8, "RLIMIT_AS": 9, "RLIMIT_LOCKS": 10,
		"RLIMIT_SIGPENDING": 11, "RLIMIT_MSGQUEUE": 12, "RLIMIT_NICE": 13,
		"RLIMIT_RTPRIO": 14, "RLIMIT_RTTIME": 15,
	}
	for _, limit := range limits {
		resource, ok := resources[limit.Type]
		if !ok {
			return fmt.Errorf("unsupported process rlimit %q", limit.Type)
		}
		if err := syscall.Setrlimit(resource, &syscall.Rlimit{Cur: limit.Soft, Max: limit.Hard}); err != nil {
			return fmt.Errorf("apply %s: %w", limit.Type, err)
		}
	}
	return nil
}
