//go:build windows

package hooks

import (
	"os"
	"os/exec"
)

func isolateProcess(cmd *exec.Cmd) {}

func cleanupProcess(cmd *exec.Cmd) {}

func ownedByCurrentUser(info os.FileInfo) bool { return true }
