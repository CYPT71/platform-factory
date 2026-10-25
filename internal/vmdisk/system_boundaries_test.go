package vmdisk

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestAccountPortAndMountMetadataRejectMalformedValues(t *testing.T) {
	for _, input := range []string{"Port", "Port 1 extra", "Port zero", "Port 0", "Port 65536"} {
		if _, err := parseSSHPorts([]byte(input)); !errors.Is(err, ErrCorruptFilesystem) {
			t.Fatalf("bad SSH port accepted: %q", input)
		}
	}
	ports, err := parseSSHPorts([]byte("# comment\nPort 2222\nPort 22\nPort 2222\n"))
	if err != nil || !reflect.DeepEqual(ports, []int{22, 2222}) {
		t.Fatal(ports, err)
	}
	if ports, err := parseSSHPorts(nil); err != nil || !reflect.DeepEqual(ports, []int{22}) {
		t.Fatal("missing default SSH port")
	}
	for _, input := range []string{"bad", "bad,name:x:1:1::/:/bin/sh", "app:x:-1:1::/:/bin/sh", "app:x:1:4294967296::/:/bin/sh", "app:x:1:1::/bad\x00path:/bin/sh"} {
		if _, err := parsePasswd([]byte(input)); !errors.Is(err, ErrCorruptFilesystem) {
			t.Fatalf("bad passwd accepted: %q", input)
		}
	}
	for _, input := range []string{"bad", "bad,name:x:1:", "app:x:-1:", "app:x:1:bad\x00member"} {
		if _, err := parseGroup([]byte(input)); !errors.Is(err, ErrCorruptFilesystem) {
			t.Fatalf("bad group accepted: %q", input)
		}
	}
	if groups, err := parseGroup([]byte("app:x:1:\n\nother:x:2:app")); err != nil || len(groups) != 2 || len(groups[0].Members) != 0 {
		t.Fatal(groups, err)
	}
	for _, input := range []string{"dev /data", "dev relative ext4 defaults", "dev /a/../data ext4 defaults", "dev /data ext4 bad\x00option"} {
		if _, err := parseFSTab([]byte(input)); !errors.Is(err, ErrCorruptFilesystem) {
			t.Fatalf("bad fstab accepted: %q", input)
		}
	}
	mounts, err := parseFSTab([]byte("proc /proc proc defaults\ndev /z ext4 defaults\ndev /a ext4 ro\n"))
	if err != nil || len(mounts) != 2 || mounts[0].MountPoint != "/a" {
		t.Fatal(mounts, err)
	}
}
func TestSystemdMetadataClassifiesUnsupportedFeaturesWithoutLeakingSecrets(t *testing.T) {
	for _, input := range []string{"[Service", "[Service]\nbad", "[Service]\nEnvironment=NOT_AN_ASSIGNMENT", "[Service]\nGroup=bad group", "[Service]\x00"} {
		if _, err := parseSystemdServiceConfiguration("a.service", []byte(input)); !errors.Is(err, ErrCorruptFilesystem) {
			t.Fatalf("bad unit accepted: %q", input)
		}
	}
	unit := "[Unit]\nIgnored=outside\n[Service]\nExecStart=/bin/app 'one argument'\nExecStart=/bin/other\nEnvironment='API_KEY=never-copy-this' MODE=prod\nEnvironmentFile=/etc/app.env\nWorkingDirectory=relative\n"
	result, err := parseSystemdServiceConfiguration("a.service", []byte(unit))
	if err != nil || len(result.IncompleteReasons) < 3 || result.Entrypoint != "/bin/app" || len(result.SecretEnvironmentKeys) != 1 || strings.Contains(strings.Join(result.Args, " "), "never-copy-this") {
		t.Fatalf("metadata: %+v %v", result, err)
	}
	if result.Environment["API_KEY"] != "" || result.Environment["MODE"] != "prod" {
		t.Fatal("secret environment copied")
	}
	for _, input := range []string{"'unterminated", `trailing\`} {
		if _, err := splitSystemdWords(input); err == nil {
			t.Fatal("unterminated words accepted")
		}
	}
	values, err := parseOSRelease([]byte("# comment\nID='alpine'\nVERSION_ID=\"3.20\"\n"))
	if err != nil || values["ID"] != "alpine" {
		t.Fatal(values, err)
	}
	for _, input := range []string{"ID='unfinished", "ID=" + strings.Repeat("x", 4097)} {
		if _, err := parseOSRelease([]byte(input)); err == nil {
			t.Fatal("invalid os-release accepted")
		}
	}
}
