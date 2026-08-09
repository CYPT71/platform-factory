// Package virtio provides virtio console device.
package virtio

import "io"

// ConsoleDevice represents a virtio console device.
type ConsoleDevice struct {
	Input  io.Reader
	Output io.Writer
}

// NewConsoleDevice creates a new console device.
func NewConsoleDevice(input io.Reader, output io.Writer) *ConsoleDevice {
	return &ConsoleDevice{
		Input:  input,
		Output: output,
	}
}

// Read reads from the console.
func (c *ConsoleDevice) Read(p []byte) (n int, err error) {
	if c.Input == nil {
		return 0, io.EOF
	}
	return c.Input.Read(p)
}

// Write writes to the console.
func (c *ConsoleDevice) Write(p []byte) (n int, err error) {
	if c.Output == nil {
		return len(p), nil
	}
	return c.Output.Write(p)
}
