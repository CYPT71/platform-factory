//go:build linux

package ociruntime

import (
	"testing"
	"time"
)

func TestSupervisorRequestIdentityIsIndependentFromNamespacePIDPortable(t *testing.T) {
	launched := State{ID: "isolated-vmm", PID: 424242, Created: time.Unix(100, 0).UTC()}
	incarnation := stateIncarnation(launched)
	request := startResult{Command: "start", ID: launched.ID, PID: launched.PID, Incarnation: incarnation}
	if !validSupervisorRequest(request, launched, launched, incarnation, "start") {
		t.Fatal("host-visible PID should authenticate independently from the namespace-local PID")
	}
	bad := request
	bad.PID++
	if validSupervisorRequest(bad, launched, launched, incarnation, "start") {
		t.Fatal("different host PID accepted")
	}
	bad = request
	bad.Incarnation = "stale"
	if validSupervisorRequest(bad, launched, launched, incarnation, "start") {
		t.Fatal("stale incarnation accepted")
	}
	current := launched
	current.PID++
	if validSupervisorRequest(request, current, launched, incarnation, "start") {
		t.Fatal("request accepted after persisted supervisor changed")
	}
}
