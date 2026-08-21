//go:build !linux

package main

import "errors"

// prepareConsole is unsupported outside Linux; this binary only ever runs
// as a Linux kernel's PID 1, but the package must still build and test on
// the host platforms used for local development.
func prepareConsole() error {
	return errors.New("prepareConsole is only supported on linux")
}

func prepareProc() error {
	return errors.New("prepareProc is only supported on linux")
}

func prepareMessageQueue() error {
	return errors.New("prepareMessageQueue is only supported on linux")
}

func prepareContainerEnv() error {
	return errors.New("prepareContainerEnv is only supported on linux")
}

func prepareSharedMemory() error {
	return errors.New("prepareSharedMemory is only supported on linux")
}
