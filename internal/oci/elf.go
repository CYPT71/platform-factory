package oci

import (
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"
)

func validateELFClosure(binary, architecture, profile string, extras []ExtraFile) error {
	entry, err := inspectELF(binary)
	if err != nil {
		return fmt.Errorf("inspect entrypoint ELF: %w", err)
	}
	if entry == nil {
		return nil
	}
	if profile == "static" && (entry.interpreter != "" || len(entry.needed) != 0) {
		return errors.New("static profile rejects dynamically linked ELF input")
	}
	if profile == "glibc" && entry.interpreter != "" && !strings.Contains(entry.interpreter, "ld-linux") {
		return fmt.Errorf("glibc profile rejects interpreter %q", entry.interpreter)
	}
	if profile == "musl" && entry.interpreter != "" && !strings.Contains(entry.interpreter, "ld-musl") {
		return fmt.Errorf("musl profile rejects interpreter %q", entry.interpreter)
	}
	if want := elfMachine(architecture); want != 0 && entry.machine != want {
		return fmt.Errorf("entrypoint ELF architecture %s does not match requested %s", entry.machine, architecture)
	}
	provided := map[string]bool{}
	bySource := map[string]string{}
	for _, extra := range extras {
		provided[path.Base(extra.Dest)] = true
		bySource[extra.Source] = extra.Dest
	}
	if entry.interpreter != "" {
		found := false
		for _, extra := range extras {
			if extra.Dest == entry.interpreter {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("dynamic linker %q is required but was not provided", entry.interpreter)
		}
	}
	required := append([]string(nil), entry.needed...)
	for source, destination := range bySource {
		info, inspectErr := inspectELF(source)
		if inspectErr != nil {
			return fmt.Errorf("inspect ELF dependency %s: %w", destination, inspectErr)
		}
		if info == nil {
			continue
		}
		if info.machine != entry.machine {
			return fmt.Errorf("ELF dependency %s has architecture %s, want %s", destination, info.machine, entry.machine)
		}
		required = append(required, info.needed...)
	}
	for _, library := range required {
		if !provided[filepath.Base(library)] {
			return fmt.Errorf("ELF dependency %q is required but was not provided", library)
		}
	}
	return nil
}

type elfInfo struct {
	machine     elf.Machine
	interpreter string
	needed      []string
}

func inspectELF(filename string) (*elfInfo, error) {
	file, err := elf.Open(filename)
	if err != nil {
		var format *elf.FormatError
		if errors.As(err, &format) || errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()
	info := &elfInfo{machine: file.Machine}
	for _, program := range file.Progs {
		if program.Type != elf.PT_INTERP {
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(program.Open(), 4096))
		if readErr != nil {
			return nil, readErr
		}
		if len(data) == 0 || data[len(data)-1] != 0 {
			return nil, errors.New("ELF interpreter is not NUL terminated")
		}
		info.interpreter = string(data[:len(data)-1])
	}
	needed, err := file.DynString(elf.DT_NEEDED)
	if err != nil && !errors.Is(err, elf.ErrNoSymbols) {
		return nil, err
	}
	info.needed = needed
	return info, nil
}

func elfMachine(architecture string) elf.Machine {
	switch architecture {
	case "amd64":
		return elf.EM_X86_64
	case "arm64":
		return elf.EM_AARCH64
	default:
		return 0
	}
}
