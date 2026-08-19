package main

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CYPT71/platform-factory/oci"
)

func TestRunContainerAutomaticallyImportsLocalLayout(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/service", "v1")
	loaded := false
	runCalled := false
	execute := func(name string, args []string, stdin io.Reader, _, _ io.Writer) error {
		if name != "podman" {
			t.Fatalf("runtime=%s", name)
		}
		switch args[0] {
		case "image":
			if loaded {
				return nil
			}
			return errors.New("image not found")
		case "load":
			entries := map[string]bool{}
			archive := tar.NewReader(stdin)
			for {
				header, err := archive.Next()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				entries[header.Name] = true
				if _, err := io.Copy(io.Discard, archive); err != nil {
					t.Fatal(err)
				}
			}
			if !entries["oci-layout"] || !entries["index.json"] {
				t.Fatalf("archive entries=%v", entries)
			}
			loaded = true
			return nil
		case "run":
			runCalled = true
			if args[len(args)-1] != "example/service:v1" {
				t.Fatalf("run args=%v", args)
			}
			return nil
		default:
			t.Fatalf("unexpected args=%v", args)
			return nil
		}
	}
	var stdout, stderr bytes.Buffer
	if code := runContainer([]string{"--runtime=podman", layoutName}, &stdout, &stderr, execute); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !loaded || !runCalled {
		t.Fatalf("loaded=%v runCalled=%v", loaded, runCalled)
	}
}

func TestPrepareContainerImageAlwaysReimportsEvenWhenATagAlreadyExists(t *testing.T) {
	// A tag existing locally must never short-circuit the import:
	// docker/podman's own "image exists"/"image inspect" only check the
	// name, not content, so skipping the load whenever the tag is
	// already present would keep serving a stale image forever after
	// any rebuild - exactly the bug pf run's rebuild-on-change and
	// --watch exist to avoid. This stub reports the tag as already
	// present from the very first call, and still expects "load".
	layoutName := buildPublishLayout(t, "example/service", "v1")
	var calls [][]string
	execute := func(name string, args []string, stdin io.Reader, _, _ io.Writer) error {
		calls = append(calls, append([]string{name}, args...))
		if len(args) > 0 && args[0] == "load" {
			if _, err := io.Copy(io.Discard, stdin); err != nil {
				return err
			}
		}
		return nil
	}
	image, err := prepareContainerImage("docker", "example/service:v1", layoutName, io.Discard, execute)
	if err != nil {
		t.Fatal(err)
	}
	if image != "example/service:v1" {
		t.Fatalf("image=%s", image)
	}
	if len(calls) != 2 || calls[0][0] != "docker" || calls[0][1] != "load" ||
		strings.Join(calls[1], " ") != "docker image inspect example/service:v1" {
		t.Fatalf("calls=%v", calls)
	}
}

func TestRunImportLoadsVerifiedLayoutWithoutStartingContainer(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/service", "v1")
	loaded := false
	execute := func(name string, args []string, stdin io.Reader, _, _ io.Writer) error {
		if name != "docker" {
			t.Fatalf("runtime=%s", name)
		}
		switch args[0] {
		case "image":
			if loaded {
				return nil
			}
			return errors.New("not loaded")
		case "load":
			archive := tar.NewReader(stdin)
			for {
				header, err := archive.Next()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if header.Name == "manifest.json" {
					loaded = true
				}
			}
			return nil
		default:
			t.Fatalf("import started an unexpected operation: %v", args)
			return nil
		}
	}
	var stdout, stderr bytes.Buffer
	code := runImport([]string{"--runtime=docker", "--layout", layoutName, "example/service:v1"}, &stdout, &stderr, execute)
	if code != 0 || !loaded || strings.TrimSpace(stdout.String()) != "example/service:v1" {
		t.Fatalf("code=%d loaded=%t stdout=%q stderr=%q", code, loaded, stdout.String(), stderr.String())
	}
}

func TestRunImportValidatesArguments(t *testing.T) {
	for _, args := range [][]string{{}, {"--runtime=other", "--layout=/tmp/x", "image"}, {"--layout=/tmp/x"}} {
		if code := runImport(args, io.Discard, io.Discard, nil); code != 2 {
			t.Fatalf("args=%v code=%d", args, code)
		}
	}
}

func TestPrepareContainerImageLoadsALayoutContainingASecretShapedBinary(t *testing.T) {
	// pf import/pf run load a layout into the LOCAL runtime and never
	// push it anywhere, so unlike pf publish they must not be blocked by
	// layout.Verify's embedded-secret-marker scan self-flagging a binary
	// that (like platform-factory's own) happens to contain a
	// "password="-shaped string in its own compiled rodata.
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte("password=hunter2"), 0o755); err != nil {
		t.Fatal(err)
	}
	layoutName := filepath.Join(root, "layout")
	if _, err := oci.Build(oci.Options{
		Binary: binary, Output: layoutName, ImageName: "example/service", Tag: "v1",
	}); err != nil {
		t.Fatal(err)
	}
	imported := false
	execute := func(name string, args []string, stdin io.Reader, _, _ io.Writer) error {
		if len(args) > 0 && args[0] == "load" {
			if _, err := io.Copy(io.Discard, stdin); err != nil {
				return err
			}
			imported = true
			return nil
		}
		if !imported {
			return errors.New("image not present yet")
		}
		return nil
	}
	if _, err := prepareContainerImage("podman", "example/service:v1", layoutName, io.Discard, execute); err != nil {
		t.Fatalf("expected a secret-shaped local binary to still import: %v", err)
	}
	if !imported {
		t.Fatal("expected the layout to actually be loaded")
	}
}

func TestPrepareContainerImageRejectsInvalidOrMismatchedLayout(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/service", "v1")
	if _, err := prepareContainerImage(
		"podman", "other/service:v1", layoutName, io.Discard, nil,
	); err == nil || !strings.Contains(err.Error(), "does not contain") {
		t.Fatalf("mismatch error=%v", err)
	}
	invalid := t.TempDir()
	if _, err := prepareContainerImage("podman", invalid, "", io.Discard, nil); err == nil {
		t.Fatal("invalid layout accepted")
	}
}
