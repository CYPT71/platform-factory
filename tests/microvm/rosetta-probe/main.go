//go:build linux

package main

import (
	"fmt"
	"runtime"
)

func main() {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		panic("probe was not built for linux/amd64")
	}
	fmt.Println("PLATFORM_FACTORY_ROSETTA_LINUX_AMD64_OK")
}
