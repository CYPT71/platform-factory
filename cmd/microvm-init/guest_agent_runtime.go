package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

const (
	guestAgentConfigPath = "/etc/platform-factory/guest-transport.json"
	guestAgentDevice     = "/dev/ttyS1"
	guestAgentKeyPath    = "/etc/platform-factory/guest-session.key"
)

type openGuestDeviceFunc func(string, int, os.FileMode) (io.ReadWriteCloser, error)

type guestEndpointConfig struct {
	Device         string `json:"device"`
	SessionKeyPath string `json:"session_key_path"`
}

// runWithOptionalGuestEndpoint preserves the PID 1 lifecycle and only adds a
// control endpoint when a boot-provisioned manifest explicitly opts in. A requested
// endpoint that cannot load its key or device is never downgraded to an
// unauthenticated channel; the supervised workload still runs.
func runWithOptionalGuestEndpoint(
	child childProcess,
	sigCh <-chan os.Signal,
	stdout, stderr io.Writer,
	poweroffFn func() error,
	loadConfig func(string) (guestEndpointConfig, error),
	loadKey func(string) ([]byte, error),
	openDevice openGuestDeviceFunc,
) int {
	tracked := &trackedGuestChild{child: child, state: "created"}
	logs := &concurrentBoundedBuffer{}
	out := io.MultiWriter(stdout, logs)
	errOut := io.MultiWriter(stderr, logs)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	endpointDone := make(chan error, 1)
	endpointStarted := false
	if loadConfig != nil {
		config, configErr := loadConfig(guestAgentConfigPath)
		if configErr != nil && !errors.Is(configErr, os.ErrNotExist) {
			fmt.Fprintf(errOut, "level=ERROR component=microvm-init operation=guest-agent phase=activate error=%q\n", configErr)
		}
		if configErr == nil {
			var key []byte
			var err error
			if loadKey == nil {
				err = errors.New("guest session key loader is unavailable")
			} else {
				key, err = loadKey(config.SessionKeyPath)
			}
			if err == nil {
				var conn io.ReadWriteCloser
				if openDevice == nil {
					err = errors.New("guest device opener is unavailable")
				} else {
					conn, err = openDevice(config.Device, os.O_RDWR, 0)
				}
				if err == nil {
					endpointStarted = true
					handler := runtimeGuestHandler(tracked, logs)
					go func() {
						defer clear(key)
						endpointDone <- serveGuestEndpoint(ctx, conn, key, handler)
					}()
				}
			}
			if err != nil {
				clear(key)
				fmt.Fprintf(errOut, "level=ERROR component=microvm-init operation=guest-agent phase=activate error=%q\n", err)
			}
		}
	}

	code := realMainChild(tracked, sigCh, out, errOut, poweroffFn)
	cancel()
	if endpointStarted {
		if err := <-endpointDone; err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(stderr, "level=WARN component=microvm-init operation=guest-agent phase=serve error=%q\n", err)
		}
	}
	return code
}

func loadGuestEndpointConfig(path string) (guestEndpointConfig, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return guestEndpointConfig{}, fmt.Errorf("load guest endpoint config: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 4096 {
		return guestEndpointConfig{}, errors.New("load guest endpoint config: config must be a regular file containing 1..4096 bytes")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return guestEndpointConfig{}, fmt.Errorf("load guest endpoint config: %w", err)
	}
	var config guestEndpointConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return guestEndpointConfig{}, fmt.Errorf("load guest endpoint config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return guestEndpointConfig{}, errors.New("load guest endpoint config: trailing JSON value")
	}
	if config.Device != guestAgentDevice || config.SessionKeyPath != guestAgentKeyPath {
		return guestEndpointConfig{}, errors.New("load guest endpoint config: device or session key path is not allowed")
	}
	return config, nil
}

func runtimeGuestHandler(child *trackedGuestChild, logs *concurrentBoundedBuffer) guestEndpointHandler {
	return guestEndpointHandler{
		exec:   runGuestCommand,
		signal: child.Signal,
		state:  child.State,
		logs: func() ([]byte, error) {
			return logs.Bytes(), nil
		},
		shutdown: func() error {
			signal, err := guestSignal("TERM")
			if err != nil {
				return err
			}
			return child.Signal(signal)
		},
	}
}

type trackedGuestChild struct {
	child childProcess
	mu    sync.RWMutex
	state string
}

func (c *trackedGuestChild) Start() error {
	err := c.child.Start()
	c.mu.Lock()
	if err != nil {
		c.state = "failed"
	} else {
		c.state = "running"
	}
	c.mu.Unlock()
	return err
}

func (c *trackedGuestChild) Wait() (int, error) {
	code, err := c.child.Wait()
	c.mu.Lock()
	if err != nil {
		c.state = "failed"
	} else {
		c.state = "stopped"
	}
	c.mu.Unlock()
	return code, err
}

func (c *trackedGuestChild) Signal(signal os.Signal) error {
	return c.child.Signal(signal)
}

func (c *trackedGuestChild) State() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

type concurrentBoundedBuffer struct {
	mu   sync.Mutex
	data boundedBuffer
}

func (b *concurrentBoundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(p)
}

func (b *concurrentBoundedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.data.Bytes()...)
}
