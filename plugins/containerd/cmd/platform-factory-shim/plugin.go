//go:build linux

package main

import (
	"runtime"

	"github.com/containerd/containerd/v2/plugins"
	"github.com/containerd/plugin"
	"github.com/containerd/plugin/registry"
)

// hostArch reports the OCI-spec platform architecture string for the host
// this shim runs on (amd64 or arm64, matching what internal/hypervisor and this
// project's kernel builds support).
var hostArch = runtime.GOARCH

func init() {
	registry.Register(&plugin.Registration{
		Type: plugins.TTRPCPlugin,
		ID:   "task",
		InitFn: func(ic *plugin.InitContext) (interface{}, error) {
			return newTaskService(), nil
		},
	})
	registry.Register(&plugin.Registration{
		Type: plugins.TTRPCPlugin,
		ID:   "sandbox",
		InitFn: func(ic *plugin.InitContext) (interface{}, error) {
			return newSandboxService(), nil
		},
	})
}
