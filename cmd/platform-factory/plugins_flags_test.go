package main

import (
	"context"
	"flag"
	"testing"
)

func TestPluginOptionsStartWithoutDirIsEmpty(t *testing.T) {
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	options := registerPluginFlags(flags)
	if err := flags.Parse(nil); err != nil {
		t.Fatal(err)
	}
	host, err := options.start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if len(host.clients) != 0 {
		t.Fatalf("clients=%d", len(host.clients))
	}
	// detect/freeze/planNotes on an empty host are all no-ops.
	if _, _, ok := host.detect(context.Background(), "."); ok {
		t.Fatal("empty host detected something")
	}
	if notes := host.planNotes(context.Background(), "go", "."); notes != nil {
		t.Fatalf("notes=%v", notes)
	}
}

func TestPluginOptionsStartRejectsMissingKey(t *testing.T) {
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	options := registerPluginFlags(flags)
	if err := flags.Parse([]string{"--plugin-dir", t.TempDir(), "--plugin-key", "/does/not/exist.pem"}); err != nil {
		t.Fatal(err)
	}
	if _, err := options.start(context.Background()); err == nil {
		t.Fatal("missing key accepted")
	}
}
