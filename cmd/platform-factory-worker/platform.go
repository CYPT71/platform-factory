package main

import "runtime"

func defaultPlatform() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}
