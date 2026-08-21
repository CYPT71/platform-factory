package vmdisk

import (
	"encoding/binary"
	"reflect"
	"testing"
)

func TestDetectApplicationsClassifiesMetadataWithoutReadingSecrets(t *testing.T) {
	filesystem := FilesystemInventory{Files: []InventoryFile{
		{Path: "/usr/bin/python3", Type: "file", Mode: 0o755},
		{Path: "/usr/lib/libdemo.so.1", Type: "file", Mode: 0o644},
		{Path: "/etc/demo.conf", Type: "file", Mode: 0o600},
		{Path: "/etc/tls/server.crt", Type: "file", Mode: 0o600},
		{Path: "/srv/app/.env", Type: "file", Mode: 0o600},
		{Path: "/run/demo.sock", Type: "socket"},
		{Path: "/dev/demo", Type: "character_device"},
		{Path: "/lib/modules/6.8/demo.ko", Type: "file", Mode: 0o644},
	}}
	result := DetectApplications(filesystem)
	for name, count := range map[string]int{
		"executables": len(result.Executables), "interpreters": len(result.Interpreters), "runtimes": len(result.Runtimes),
		"libraries": len(result.SharedLibraries), "certificates": len(result.Certificates),
		"secrets": len(result.SecretCandidates), "sockets": len(result.UnixSockets), "devices": len(result.Devices), "modules": len(result.KernelModules),
	} {
		if count != 1 {
			t.Fatalf("%s count=%d result=%#v", name, count, result)
		}
	}
	if len(result.Configuration) != 2 {
		t.Fatalf("configuration count=%d result=%#v", len(result.Configuration), result.Configuration)
	}
	if result.SecretCandidates[0].Evidence == "" || result.SecretCandidates[0].Path != "/srv/app/.env" {
		t.Fatalf("unexpected secret finding: %#v", result.SecretCandidates)
	}
}

func TestApplicationContentParsersFindELFDependenciesSpecialPathsAndMainProcess(t *testing.T) {
	if got := importedELFLibraries(minimalDynamicELF()); !reflect.DeepEqual(got, []string{"libc.so"}) {
		t.Fatalf("libraries=%v", got)
	}
	for _, root := range []string{"/proc", "/sys", "/dev"} {
		if !containsAbsolutePathReference([]byte("open "+root+"/example\n"), root) {
			t.Fatalf("did not find %s", root)
		}
	}
	unit := []byte("[Service]\nExecStart=-/usr/bin/demo --serve\n")
	if got := parseSystemdExecStart(unit); got != "/usr/bin/demo" {
		t.Fatalf("main process=%q", got)
	}
	for _, unsafe := range [][]byte{[]byte("ExecStart=relative\n"), []byte("ExecStart=/usr/../bin/demo\n"), append([]byte("ExecStart=/bin/demo"), 0)} {
		if got := parseSystemdExecStart(unsafe); got != "" {
			t.Fatalf("unsafe unit returned %q", got)
		}
	}
}

func minimalDynamicELF() []byte {
	b := make([]byte, 0x2c0)
	copy(b[:16], []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(b[16:18], 3)
	binary.LittleEndian.PutUint16(b[18:20], 62)
	binary.LittleEndian.PutUint32(b[20:24], 1)
	binary.LittleEndian.PutUint64(b[40:48], 0x200)
	binary.LittleEndian.PutUint16(b[52:54], 64)
	binary.LittleEndian.PutUint16(b[58:60], 64)
	binary.LittleEndian.PutUint16(b[60:62], 3)
	copy(b[0x100:], "\x00libc.so\x00")
	binary.LittleEndian.PutUint64(b[0x110:0x118], 1)
	binary.LittleEndian.PutUint64(b[0x118:0x120], 1)
	// Section 1: .dynstr.
	binary.LittleEndian.PutUint32(b[0x240+4:0x240+8], 3)
	binary.LittleEndian.PutUint64(b[0x240+24:0x240+32], 0x100)
	binary.LittleEndian.PutUint64(b[0x240+32:0x240+40], 9)
	// Section 2: .dynamic linked to dynstr.
	binary.LittleEndian.PutUint32(b[0x280+4:0x280+8], 6)
	binary.LittleEndian.PutUint64(b[0x280+24:0x280+32], 0x110)
	binary.LittleEndian.PutUint64(b[0x280+32:0x280+40], 32)
	binary.LittleEndian.PutUint32(b[0x280+40:0x280+44], 1)
	binary.LittleEndian.PutUint64(b[0x280+56:0x280+64], 16)
	return b
}
