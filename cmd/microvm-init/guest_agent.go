package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/CYPT71/platform-factory/internal/guesttransport"
)

const maxGuestDiagnosticBytes = 256 << 10

type guestExecFunc func(context.Context, []string, []byte) (int, []byte, []byte, error)

type guestEndpointHandler struct {
	exec     guestExecFunc
	signal   func(os.Signal) error
	state    func() string
	logs     func() ([]byte, error)
	shutdown func() error
}

func (h guestEndpointHandler) Handle(ctx context.Context, request guesttransport.Request) guesttransport.Response {
	switch request.Operation {
	case guesttransport.OpExec:
		if h.exec == nil {
			return guestError(errors.New("exec is unavailable"))
		}
		if len(request.Args) == 0 || !filepath.IsAbs(request.Args[0]) {
			return guestError(errors.New("exec path must be absolute"))
		}
		for _, arg := range request.Args {
			if strings.ContainsRune(arg, 0) {
				return guestError(errors.New("exec argument contains NUL"))
			}
		}
		code, stdout, stderr, err := h.exec(ctx, request.Args, request.Stdin)
		response := guesttransport.Response{
			ExitCode: code,
			Stdout:   boundedBytes(stdout),
			Stderr:   boundedBytes(stderr),
		}
		if err != nil {
			response.Error = boundedError(err)
		}
		return response
	case guesttransport.OpSignal:
		if h.signal == nil {
			return guestError(errors.New("signal is unavailable"))
		}
		sig, err := guestSignal(request.Signal)
		if err != nil {
			return guestError(err)
		}
		if err := h.signal(sig); err != nil {
			return guestError(err)
		}
		return guesttransport.Response{}
	case guesttransport.OpState:
		if h.state == nil {
			return guestError(errors.New("state is unavailable"))
		}
		return guesttransport.Response{State: boundedString(h.state())}
	case guesttransport.OpLogs:
		if h.logs == nil {
			return guestError(errors.New("logs are unavailable"))
		}
		logs, err := h.logs()
		if err != nil {
			return guestError(err)
		}
		return guesttransport.Response{Logs: boundedBytes(logs)}
	case guesttransport.OpShutdown:
		if h.shutdown == nil {
			return guestError(errors.New("shutdown is unavailable"))
		}
		if err := h.shutdown(); err != nil {
			return guestError(err)
		}
		return guesttransport.Response{}
	default:
		return guestError(fmt.Errorf("unsupported operation %q", request.Operation))
	}
}

func serveGuestEndpoint(ctx context.Context, conn io.ReadWriteCloser, sessionKey []byte, handler guestEndpointHandler) error {
	if conn == nil {
		return errors.New("microvm-init: guest endpoint connection is required")
	}
	defer conn.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	codec, err := guesttransport.NewCodec(conn, conn, sessionKey)
	if err != nil {
		return fmt.Errorf("microvm-init: guest endpoint: %w", err)
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := guesttransport.ServeOne(ctx, codec, handler)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("microvm-init: guest endpoint: %w", err)
		}
	}
}

// loadGuestSessionKey reads boot-provisioned key material. It accepts hex or
// base64 but never invents a default: absence, weak material and oversized
// files all fail closed.
func loadGuestSessionKey(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("load guest session key: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 4096 {
		return nil, errors.New("load guest session key: key must be a non-empty regular file no larger than 4 KiB")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("load guest session key: key file must not be accessible by group or other users")
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load guest session key: %w", err)
	}
	value := strings.TrimSpace(string(encoded))
	key, hexErr := hex.DecodeString(value)
	if hexErr != nil {
		key, err = base64.StdEncoding.DecodeString(value)
		if err != nil {
			return nil, errors.New("load guest session key: expected hex or base64")
		}
	}
	if len(key) < 32 {
		return nil, errors.New("load guest session key: key must contain at least 32 bytes")
	}
	return key, nil
}

func runGuestCommand(ctx context.Context, args []string, stdin []byte) (int, []byte, []byte, error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr boundedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return 0, stdout.Bytes(), stderr.Bytes(), nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), stdout.Bytes(), stderr.Bytes(), nil
	}
	return 1, stdout.Bytes(), stderr.Bytes(), err
}

type boundedBuffer struct {
	data bytes.Buffer
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	remaining := maxGuestDiagnosticBytes - b.data.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.data.Write(p)
	}
	return original, nil
}

func (b *boundedBuffer) Bytes() []byte { return b.data.Bytes() }

func boundedBytes(value []byte) []byte {
	if len(value) > maxGuestDiagnosticBytes {
		value = value[:maxGuestDiagnosticBytes]
	}
	return append([]byte(nil), value...)
}

func boundedString(value string) string {
	return string(boundedBytes([]byte(value)))
}

func boundedError(err error) string {
	if err == nil {
		return ""
	}
	return boundedString(err.Error())
}

func guestError(err error) guesttransport.Response {
	return guesttransport.Response{Error: boundedError(err)}
}
