// Command secure-oci-sandboxprobe is a separate Go module, importing only
// the public sdk/plugin SDK, used solely to prove from inside a plugin
// subprocess that internal/plugin.Start's namespace sandbox actually took
// effect: that outbound network access is cut off, and that no_new_privs is
// set. It reports observations rather than asserting them itself so the
// real assertions live in the host-side Go test, next to the rest of the
// project's test suite.
package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	plugin "github.com/CYPT71/secure-oci-base/sdk/plugin"
)

func handleNetProbe(_ context.Context, _ json.RawMessage) (any, error) {
	conn, err := net.DialTimeout("tcp", "8.8.8.8:53", 3*time.Second)
	if err != nil {
		return map[string]string{"result": "denied", "detail": err.Error()}, nil
	}
	_ = conn.Close()
	return map[string]string{"result": "reached"}, nil
}

func handleIsolationProbe(_ context.Context, _ json.RawMessage) (any, error) {
	limits := map[string]string{}
	for name, resource := range map[string]int{
		"core": syscall.RLIMIT_CORE, "file_size": syscall.RLIMIT_FSIZE,
		"open_files": syscall.RLIMIT_NOFILE, "cpu": syscall.RLIMIT_CPU,
	} {
		var limit syscall.Rlimit
		if err := syscall.Getrlimit(resource, &limit); err != nil {
			return nil, err
		}
		limits[name] = strconv.FormatUint(limit.Cur, 10)
	}
	return limits, nil
}

func handlePrivProbe(_ context.Context, _ json.RawMessage) (any, error) {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if name, value, found := strings.Cut(line, ":"); found && name == "NoNewPrivs" {
			return map[string]string{"no_new_privs": strings.TrimSpace(value)}, nil
		}
	}
	return map[string]string{"no_new_privs": "absent"}, nil
}

func main() {
	server := plugin.NewServer("sandboxprobe", "v0.1.0")
	server.Handle("observe.net-probe", handleNetProbe)
	server.Handle("observe.priv-probe", handlePrivProbe)
	server.Handle("observe.isolation-probe", handleIsolationProbe)
	if err := server.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		os.Exit(1)
	}
}
