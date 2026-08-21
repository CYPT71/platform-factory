package vmdisk

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestDetectLinuxSystemFromExtEvidence(t *testing.T) {
	disk, volume := buildExtSystemFixture(t)
	filesystem, err := InspectExtFilesystem(disk, FormatRAW, volume)
	if err != nil {
		t.Fatal(err)
	}
	system, err := DetectLinuxSystem(disk, FormatRAW, volume, filesystem)
	if err != nil {
		t.Fatal(err)
	}
	if system.Distribution != "alpine" || system.Version != "3.20" || system.Architecture != "amd64" {
		t.Fatalf("unexpected system: %#v", system)
	}
	if system.Kernel != "6.8.0-pf" || strings.Join(system.InitSystems, ",") != "busybox,openrc,systemd,sysv" || len(system.Services) != 1 || len(system.SystemdTimers) != 1 || len(system.StartupFiles) != 1 || len(system.CronFiles) != 1 {
		t.Fatalf("unexpected boot/service inventory: %#v", system)
	}
	applications, err := AnalyzeExtApplicationContent(disk, FormatRAW, volume, filesystem, DetectApplications(filesystem))
	if err != nil {
		t.Fatal(err)
	}
	if applications.MainProcess == nil || applications.MainProcess.Path != "/bin/sh" || len(applications.SpecialPaths) != 3 {
		t.Fatalf("unexpected content analysis: %#v", applications)
	}
	if len(system.Users) != 1 || system.Users[0].Name != "app" || system.Users[0].UID != 1000 || len(system.Groups) != 1 || system.Groups[0].Name != "app" || len(system.Groups[0].Members) != 1 {
		t.Fatalf("unexpected accounts: users=%#v groups=%#v", system.Users, system.Groups)
	}
	if len(system.Mounts) != 1 || system.Mounts[0].MountPoint != "/srv/data" || system.Mounts[0].Filesystem != "ext4" {
		t.Fatalf("unexpected persistent mounts: %#v", system.Mounts)
	}
	if len(system.NetworkServices) != 1 || system.NetworkServices[0].Name != "sshd" || len(system.ProbablePorts) != 1 || system.ProbablePorts[0] != 2222 {
		t.Fatalf("unexpected network detection: services=%#v ports=%#v", system.NetworkServices, system.ProbablePorts)
	}
	encoded, err := json.Marshal(system)
	if err != nil || strings.Contains(string(encoded), "secret-hash") || strings.Contains(string(encoded), "group-secret") {
		t.Fatalf("account password field leaked: %s err=%v", encoded, err)
	}
	for _, kind := range []string{"distribution", "version", "architecture"} {
		found := false
		for _, fact := range system.Facts {
			if fact.Kind == kind && fact.Confidence == "high" && fact.Evidence != "" {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing high-confidence %s fact: %#v", kind, system.Facts)
		}
	}
}

func TestParseOSReleaseRejectsMalformedInput(t *testing.T) {
	for _, input := range []string{"bad line", "ID=\"unterminated", "ID=ok\x00hidden", "lower=value"} {
		if _, err := parseOSRelease([]byte(input)); err == nil {
			t.Fatalf("expected malformed input %q to fail", input)
		}
	}
}

func TestDiscoveryReportCarriesDetectedLinuxSystem(t *testing.T) {
	disk, _ := buildExtSystemFixture(t)
	report, err := BuildDiscoveryReport([]string{disk}, disk)
	if err != nil {
		t.Fatal(err)
	}
	entry := report.Disks[0]
	if entry.System == nil || entry.OperatingSystem != "alpine 3.20" || !strings.Contains(entry.System.Facts[0].Evidence, "os-release") {
		t.Fatalf("unexpected report system: %#v", entry)
	}
	if len(entry.PersistentData) != 1 || entry.PersistentData[0] != "/srv/data" {
		t.Fatalf("persistent data was not propagated: %#v", entry.PersistentData)
	}
	if len(entry.Applications) != 1 || entry.Applications[0].MainProcess == nil || entry.Applications[0].MainProcess.Path != "/bin/sh" {
		t.Fatalf("main process was not propagated: %#v", entry.Applications)
	}
	if len(entry.ExcludedServices) != 1 || entry.ExcludedServices[0] != "/etc/init.d/sshd" {
		t.Fatalf("excluded services=%#v", entry.ExcludedServices)
	}
	for _, special := range []string{"/proc", "/sys", "/dev"} {
		found := false
		for _, risk := range entry.MigrationRisks {
			if strings.Contains(risk, special) {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing %s migration risk: %#v", special, entry.MigrationRisks)
		}
	}
}

func TestParseSystemdServiceConfigurationConvertsNonSecretRuntimeFields(t *testing.T) {
	unit := []byte("[Unit]\nDescription=demo\n[Service]\nExecStart=/opt/app/service --listen 8443\nEnvironment=MODE=production API_TOKEN=must-not-leak 'GREETING=hello world'\nWorkingDirectory=/opt/app\nUser=app\nGroup=app\nEnvironmentFile=/etc/demo.env\n")
	configuration, err := parseSystemdServiceConfiguration("/etc/systemd/system/demo.service", unit)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Entrypoint != "/opt/app/service" || len(configuration.Args) != 2 || configuration.Args[1] != "8443" || configuration.Environment["MODE"] != "production" || configuration.Environment["GREETING"] != "hello world" {
		t.Fatalf("configuration=%+v", configuration)
	}
	if _, leaked := configuration.Environment["API_TOKEN"]; leaked || len(configuration.SecretEnvironmentKeys) != 1 || configuration.SecretEnvironmentKeys[0] != "API_TOKEN" {
		t.Fatalf("secret environment leaked: %+v", configuration)
	}
	if configuration.WorkingDirectory != "/opt/app" || configuration.User != "app" || configuration.Group != "app" || len(configuration.IncompleteReasons) != 1 {
		t.Fatalf("configuration=%+v", configuration)
	}
}

func TestParseSystemdServiceConfigurationRejectsUnsafeSyntax(t *testing.T) {
	for _, unit := range []string{"[Service]\nExecStart=relative\n", "[Service]\nEnvironment='UNTERMINATED=x\n", "[Service]\nUser=bad user\n"} {
		configuration, err := parseSystemdServiceConfiguration("demo.service", []byte(unit))
		if err == nil && len(configuration.IncompleteReasons) == 0 {
			t.Fatalf("unsafe unit accepted: %q => %+v", unit, configuration)
		}
	}
}

func buildExtSystemFixture(t *testing.T) (string, Volume) {
	t.Helper()
	disk, volume := buildExtFixture(t, "ext4", false)
	file, err := os.OpenFile(disk, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	image, err := os.ReadFile(disk)
	if err != nil {
		t.Fatal(err)
	}
	base := int(volume.StartBytes)
	putLE32(image[base+1024:], 0, 40)
	putLE32(image[base+1024:], 40, 40)
	inodeTable := image[base+5*1024:]
	setFixtureInode := func(number int, mode uint16, size uint32, block uint32) {
		inode := inodeTable[(number-1)*128 : number*128]
		binary.LittleEndian.PutUint16(inode[0:2], mode)
		putLE32(inode, 4, size)
		binary.LittleEndian.PutUint16(inode[26:28], 1)
		setInodeBlock(inode, "ext4", block)
	}
	setFixtureInode(13, 0x41ed, 1024, 13) // /etc
	osRelease := []byte("ID=alpine\nVERSION_ID=\"3.20\"\nNAME=Alpine Linux\n")
	setFixtureInode(14, 0x81a4, uint32(len(osRelease)), 40)
	setFixtureInode(15, 0x41ed, 1024, 15) // /bin
	elf := make([]byte, 64)
	copy(elf, []byte{0x7f, 'E', 'L', 'F'})
	elf[4], elf[5] = 2, 1
	binary.LittleEndian.PutUint16(elf[18:20], 62)
	setFixtureInode(16, 0x81ed, uint32(len(elf)), 41)
	setFixtureInode(17, 0x41ed, 1024, 17) // /boot
	setFixtureInode(18, 0x81a4, 1, 42)
	setFixtureInode(19, 0x41ed, 1024, 19) // /etc/systemd
	setFixtureInode(20, 0x41ed, 1024, 20) // /etc/systemd/system
	serviceUnit := []byte("[Service]\nExecStart=/bin/sh /srv/app\n")
	setFixtureInode(21, 0x81a4, uint32(len(serviceUnit)), 43) // demo.service
	setFixtureInode(22, 0x41ed, 1024, 22)                     // /etc/init.d
	initScript := []byte("#!/bin/sh\ncat /proc/self/status /sys/kernel/uevent_seqnum /dev/null\n")
	setFixtureInode(23, 0x81ed, uint32(len(initScript)), 44) // init script
	setFixtureInode(24, 0x41ed, 1024, 24)                    // /etc/runlevels
	setFixtureInode(25, 0x41ed, 1024, 25)                    // default runlevel
	setFixtureInode(26, 0xa1ff, 4, 0)                        // OpenRC service link, never followed
	copy(inodeTable[(26-1)*128+40:], "sshd")
	setFixtureInode(27, 0x81ed, uint32(len(elf)), 45) // busybox
	setFixtureInode(28, 0x81a4, 1, 46)                // crontab
	setFixtureInode(29, 0x81a4, 1, 47)                // cleanup.timer
	passwd := []byte("app:$6$secret-hash:1000:1000:App:/srv/app:/bin/sh\n")
	group := []byte("app:$6$group-secret:1000:app\n")
	fstab := []byte("UUID=data /srv/data ext4 defaults 0 2\nproc /proc proc defaults 0 0\n/dev/sdb2 none swap sw 0 0\n")
	setFixtureInode(30, 0x81a4, uint32(len(passwd)), 48)
	setFixtureInode(31, 0x81a4, uint32(len(group)), 49)
	setFixtureInode(32, 0x81a4, uint32(len(fstab)), 50)
	sshdConfig := []byte("# legacy override\nPort 2222\nPasswordAuthentication no\n")
	setFixtureInode(33, 0x41ed, 1024, 51)
	setFixtureInode(34, 0x81a4, uint32(len(sshdConfig)), 52)
	root := image[base+10*1024 : base+11*1024]
	writeFixtureDirectory(root, 2, 2, []fixtureDirEntry{{12, "hello.txt", 1}, {13, "etc", 2}, {15, "bin", 2}, {17, "boot", 2}})
	etc := image[base+13*1024 : base+14*1024]
	writeFixtureDirectory(etc, 13, 2, []fixtureDirEntry{{14, "os-release", 1}, {19, "systemd", 2}, {22, "init.d", 2}, {24, "runlevels", 2}, {28, "crontab", 1}, {30, "passwd", 1}, {31, "group", 1}, {32, "fstab", 1}, {33, "ssh", 2}})
	copy(image[base+40*1024:], osRelease)
	bin := image[base+15*1024 : base+16*1024]
	writeFixtureDirectory(bin, 15, 2, []fixtureDirEntry{{16, "sh", 1}, {27, "busybox", 1}})
	copy(image[base+41*1024:], elf)
	copy(image[base+43*1024:], serviceUnit)
	copy(image[base+44*1024:], initScript)
	copy(image[base+45*1024:], elf)
	copy(image[base+48*1024:], passwd)
	copy(image[base+49*1024:], group)
	copy(image[base+50*1024:], fstab)
	writeFixtureDirectory(image[base+51*1024:base+52*1024], 33, 13, []fixtureDirEntry{{34, "sshd_config", 1}})
	copy(image[base+52*1024:], sshdConfig)
	writeFixtureDirectory(image[base+17*1024:base+18*1024], 17, 2, []fixtureDirEntry{{18, "vmlinuz-6.8.0-pf", 1}})
	writeFixtureDirectory(image[base+19*1024:base+20*1024], 19, 13, []fixtureDirEntry{{20, "system", 2}})
	writeFixtureDirectory(image[base+20*1024:base+21*1024], 20, 19, []fixtureDirEntry{{21, "demo.service", 1}, {29, "cleanup.timer", 1}})
	writeFixtureDirectory(image[base+22*1024:base+23*1024], 22, 13, []fixtureDirEntry{{23, "sshd", 1}})
	writeFixtureDirectory(image[base+24*1024:base+25*1024], 24, 13, []fixtureDirEntry{{25, "default", 2}})
	writeFixtureDirectory(image[base+25*1024:base+26*1024], 25, 24, []fixtureDirEntry{{26, "sshd", 7}})
	if _, err := file.WriteAt(image, 0); err != nil {
		t.Fatal(err)
	}
	return disk, volume
}

type fixtureDirEntry struct {
	inode    uint32
	name     string
	typeByte byte
}

func writeFixtureDirectory(block []byte, self, parent uint32, entries []fixtureDirEntry) {
	for i := range block {
		block[i] = 0
	}
	all := append([]fixtureDirEntry{{self, ".", 2}, {parent, "..", 2}}, entries...)
	offset := 0
	for index, entry := range all {
		length := (8 + len(entry.name) + 3) &^ 3
		if index == len(all)-1 {
			length = len(block) - offset
		}
		writeExtDirEntry(block[offset:offset+length], entry.inode, length, entry.name, entry.typeByte)
		offset += length
	}
}
