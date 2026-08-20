package v1

import (
	"testing"
	"time"

	"github.com/CYPT71/platform-factory/oci"
)

func TestToInternalExtraFilesConvertsAndHandlesEmpty(t *testing.T) {
	if got := toInternalExtraFiles(nil); got != nil {
		t.Fatalf("toInternalExtraFiles(nil)=%+v, want nil", got)
	}
	if got := toInternalExtraFiles([]ExtraFile{}); got != nil {
		t.Fatalf("toInternalExtraFiles([]ExtraFile{})=%+v, want nil", got)
	}

	files := []ExtraFile{{Dest: "/lib/x.so", Source: "/host/x.so", Mode: 0o555, Category: "dependencies"}}
	got := toInternalExtraFiles(files)
	if len(got) != 1 || got[0].Dest != "/lib/x.so" || got[0].Source != "/host/x.so" || got[0].Mode != 0o555 || got[0].Category != "dependencies" {
		t.Fatalf("toInternalExtraFiles(%+v)=%+v", files, got)
	}
}

func TestToInternalHealthcheckHandlesNilAndPopulated(t *testing.T) {
	if got := toInternalHealthcheck(nil); got != nil {
		t.Fatalf("toInternalHealthcheck(nil)=%+v, want nil", got)
	}

	h := &Healthcheck{Command: []string{"curl", "-f", "http://localhost/health"}, Interval: "5s", Timeout: "1s", Retries: 3}
	got := toInternalHealthcheck(h)
	if got == nil || got.Interval != "5s" || got.Timeout != "1s" || got.Retries != 3 || len(got.Command) != 3 {
		t.Fatalf("toInternalHealthcheck(%+v)=%+v", h, got)
	}
}

func TestToInternalObserverHandlesNilAndForwardsEvents(t *testing.T) {
	if got := toInternalObserver(nil); got != nil {
		t.Fatal("toInternalObserver(nil) should return nil")
	}

	var received BuildEvent
	adapted := toInternalObserver(func(e BuildEvent) { received = e })
	now := time.Now()
	adapted(oci.Event{
		Time: now, Level: "info", Component: "oci", Operation: "build", Phase: "layer",
		TraceID: "trace-1", Message: "packing layer", Duration: time.Second,
		Fields: map[string]any{"k": "v"},
	})

	if received.Level != "info" || received.Component != "oci" || received.Operation != "build" ||
		received.Phase != "layer" || received.TraceID != "trace-1" || received.Message != "packing layer" ||
		received.Duration != time.Second || received.Fields["k"] != "v" || !received.Time.Equal(now) {
		t.Fatalf("observer received unexpected event: %+v", received)
	}
}
