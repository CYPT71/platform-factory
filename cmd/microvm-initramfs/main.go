// microvm-initramfs assembles a Linux kernel initramfs natively from a
// verified local OCI image layout: it converts the layout's rootfs with
// internal/rootfs.Convert, installs the project's PID 1 (built separately
// from cmd/microvm-init) and an optional fixed entrypoint, then packs the
// result into a deterministic gzip-compressed cpio archive with
// internal/rootfs.WriteInitramfs. It never invokes an external tar or cpio
// binary.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/CYPT71/secure-oci-base/internal/rootfs"
)

type stringList []string

func (s *stringList) String() string { return fmt.Sprint([]string(*s)) }
func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

type assembleResult struct {
	ManifestDigest string `json:"manifest_digest"`
	RootFSDigest   string `json:"rootfs_digest"`
	Files          int    `json:"files"`
	Bytes          int64  `json:"bytes"`
	InitramfsBytes int64  `json:"initramfs_bytes"`
}

// Run is the tested entry point; main only adapts it to os.Exit.
func Run(args []string) int { return run(args, os.Stdout, os.Stderr) }

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("microvm-initramfs", flag.ContinueOnError)
	flags.SetOutput(stderr)
	layout := flags.String("layout", "", "verified local OCI image layout directory")
	platform := flags.String("platform", "linux/amd64", "manifest platform to select")
	reference := flags.String("reference", "", "manifest tag annotation to select, if the layout holds more than one")
	initBinary := flags.String("init", "", "path to the project's PID 1 binary (built from cmd/microvm-init)")
	output := flags.String("output", "", "output path for the gzip-compressed cpio initramfs")
	var entrypoint stringList
	flags.Var(&entrypoint, "entrypoint", "fixed entrypoint argument; repeatable; first must be an absolute path")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *layout == "" || *initBinary == "" || *output == "" {
		fmt.Fprintln(stderr, "usage: microvm-initramfs -layout DIR -init PATH -output PATH "+
			"[-platform linux/amd64] [-reference TAG] [-entrypoint ARG]...")
		return 2
	}

	outcome, err := assemble(*layout, *platform, *reference, *initBinary, []string(entrypoint), *output)
	if err != nil {
		fmt.Fprintf(stderr, "microvm-initramfs: %v\n", err)
		return 1
	}
	encoded, err := json.MarshalIndent(outcome, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "microvm-initramfs: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, string(encoded))
	return 0
}

func assemble(layout, platform, reference, initBinary string, entrypoint []string, output string) (out assembleResult, err error) {
	if _, statErr := os.Lstat(output); !errors.Is(statErr, os.ErrNotExist) {
		if statErr == nil {
			return assembleResult{}, errors.New("output already exists")
		}
		return assembleResult{}, fmt.Errorf("inspect output: %w", statErr)
	}
	parent := filepath.Dir(filepath.Clean(output))
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return assembleResult{}, fmt.Errorf("create output parent: %w", err)
	}

	rootDir, err := os.MkdirTemp(parent, ".platform-factory-initramfs-root-*")
	if err != nil {
		return assembleResult{}, fmt.Errorf("create temporary rootfs workspace: %w", err)
	}
	defer os.RemoveAll(rootDir)

	// Convert installs its own output atomically and refuses to overwrite
	// an existing directory, so it needs a path inside rootDir that does
	// not exist yet, rather than rootDir itself.
	converted := filepath.Join(rootDir, "rootfs")
	convertResult, err := rootfs.Convert(rootfs.Options{
		Layout: layout, Output: converted, Platform: platform, Reference: reference,
	})
	if err != nil {
		return assembleResult{}, fmt.Errorf("convert rootfs: %w", err)
	}
	if err := rootfs.InstallInit(converted, initBinary, entrypoint); err != nil {
		return assembleResult{}, fmt.Errorf("install init: %w", err)
	}

	temporary, err := os.CreateTemp(parent, ".platform-factory-initramfs-*")
	if err != nil {
		return assembleResult{}, fmt.Errorf("create temporary initramfs: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := rootfs.WriteInitramfs(converted, temporary); err != nil {
		_ = temporary.Close()
		return assembleResult{}, fmt.Errorf("write initramfs: %w", err)
	}
	info, err := temporary.Stat()
	if err != nil {
		_ = temporary.Close()
		return assembleResult{}, fmt.Errorf("stat initramfs: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return assembleResult{}, fmt.Errorf("close initramfs: %w", err)
	}
	if err := os.Rename(temporaryName, output); err != nil {
		return assembleResult{}, fmt.Errorf("install initramfs: %w", err)
	}
	return assembleResult{
		ManifestDigest: convertResult.ManifestDigest,
		RootFSDigest:   convertResult.RootFSDigest,
		Files:          convertResult.Files,
		Bytes:          convertResult.Bytes,
		InitramfsBytes: info.Size(),
	}, nil
}

func main() {
	os.Exit(Run(os.Args[1:]))
}
