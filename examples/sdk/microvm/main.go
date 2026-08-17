package main

import (
	"fmt"
	"log"

	"github.com/CYPT71/platform-factory/sdk/microvm"
)

func main() {
	forward, err := microvm.ParseForward("127.0.0.1:8080:80/tcp")
	if err != nil {
		log.Fatal(err)
	}
	spec := microvm.Spec{
		Name: "sdk-demo", Layout: "/absolute/path/to/verified-layout",
		Arch: "arm64", Listen: "127.0.0.1", MemoryMiB: 256, VCPUs: 1, Port: 8080,
		Forwards: []microvm.Forward{forward},
	}
	if err := spec.ValidateCommon(); err != nil {
		log.Fatal(err)
	}
	status := microvm.MachineStatus{ID: spec.Name, State: microvm.StateCreated}
	fmt.Printf("microvm=%s state=%s host-port=%d guest-port=%d\n",
		status.ID, status.State, forward.HostPort, forward.GuestPort)
}
