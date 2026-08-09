//go:build linux

package main

import (
	"os"
	"testing"
)

func TestDup2NoopWhenFDsMatch(t *testing.T) {
	if err := dup2(3, 3); err != nil {
		t.Fatalf("dup2(fd, fd) = %v, want nil", err)
	}
}

func TestDup2DuplicatesOntoTarget(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()

	target, err := os.CreateTemp(t.TempDir(), "dup2-target")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()

	if err := dup2(int(write.Fd()), int(target.Fd())); err != nil {
		t.Fatalf("dup2: %v", err)
	}

	const message = "hello"
	if _, err := target.WriteString(message); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, len(message))
	if _, err := read.Read(buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != message {
		t.Fatalf("read %q through the duplicated fd, want %q", buf, message)
	}
}

func TestPrepareConsoleFailsWithoutAConsoleDevice(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root can mount devtmpfs and may find a real console")
	}
	if err := prepareConsole(); err == nil {
		t.Fatal("prepareConsole succeeded without permission to mount /dev or a console device present")
	}
}
