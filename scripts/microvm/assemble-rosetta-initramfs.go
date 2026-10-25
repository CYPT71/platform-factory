//go:build ignore

// Assemble the test-only heterogeneous Rosetta guest using the native CPIO
// writer. Production OCI image architecture validation remains unchanged.
package main

import (
	"debug/elf"
	"fmt"
	"os"
	"path/filepath"

	"github.com/CYPT71/platform-factory/internal/rootfs"
)

func main() {
	if len(os.Args) != 3 {
		fatal(fmt.Errorf("usage: assemble-rosetta-initramfs ROOT OUTPUT"))
	}
	for name, machine := range map[string]elf.Machine{"init": elf.EM_AARCH64, "amd64-probe": elf.EM_X86_64} {
		binary, err := elf.Open(filepath.Join(os.Args[1], name))
		if err != nil {
			fatal(err)
		}
		if binary.Machine != machine || binary.Class != elf.ELFCLASS64 {
			fatal(fmt.Errorf("invalid fixture architecture: %s", name))
		}
		for _, program := range binary.Progs {
			if program.Type == elf.PT_INTERP {
				fatal(fmt.Errorf("fixture must be static: %s", name))
			}
		}
		if err := binary.Close(); err != nil {
			fatal(err)
		}
	}
	output, err := os.OpenFile(os.Args[2], os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		fatal(err)
	}
	if err := rootfs.WriteInitramfs(os.Args[1], output); err != nil {
		output.Close()
		fatal(err)
	}
	if err := output.Close(); err != nil {
		fatal(err)
	}
}

func fatal(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
