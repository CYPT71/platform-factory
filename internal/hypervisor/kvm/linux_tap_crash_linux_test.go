//go:build linux && amd64

package kvm

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestCrashedTAPOwnerLeavesNoHostInterface verifies the non-persistent TAP
// contract against the real Linux TUN driver. The helper is SIGKILLed so no
// user-space cleanup can run; closing its descriptor table must make the
// kernel remove the interface.
func TestCrashedTAPOwnerLeavesNoHostInterface(t *testing.T) {
	if support := ProbeTAPSupport(); !support.Available {
		if os.Getenv("PLATFORM_FACTORY_REQUIRE_TAP_CRASH_TEST") == "1" {
			t.Fatalf("required real TAP crash test unavailable: %s", support.Reason)
		}
		t.Skip(support.Reason)
	}
	if _, err := os.Stat(tunDevicePath); err != nil {
		if os.Getenv("PLATFORM_FACTORY_REQUIRE_TAP_CRASH_TEST") == "1" {
			t.Fatalf("required %s unavailable: %v", tunDevicePath, err)
		}
		t.Skipf("%s unavailable: %v", tunDevicePath, err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestTAPCrashHelper$", "-test.count=1")
	child.Env = append(os.Environ(), "PLATFORM_FACTORY_TAP_CRASH_HELPER=1")
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	}()
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatalf("TAP helper did not report its interface: %v", scanner.Err())
	}
	name, ok := strings.CutPrefix(scanner.Text(), "TAP=")
	if !ok || name == "" {
		t.Fatalf("invalid TAP helper response %q", scanner.Text())
	}
	if _, err := net.InterfaceByName(name); err != nil {
		t.Fatalf("helper-reported TAP %q is not visible: %v", name, err)
	}
	if err := child.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err == nil {
		t.Fatal("SIGKILLed TAP helper exited successfully")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, err := net.InterfaceByName(name)
		if errors.Is(err, net.ErrClosed) {
			break
		}
		if err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := net.InterfaceByName(name); err == nil {
		t.Fatalf("non-persistent TAP interface %q survived owner crash", name)
	}
}

func TestTAPCrashHelper(t *testing.T) {
	if os.Getenv("PLATFORM_FACTORY_TAP_CRASH_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	tap, name, err := OpenTAP("")
	if err != nil {
		t.Fatal(err)
	}
	defer tap.Close()
	fmt.Printf("TAP=%s\n", name)
	for {
		runtime.KeepAlive(tap)
		time.Sleep(time.Hour)
	}
}
