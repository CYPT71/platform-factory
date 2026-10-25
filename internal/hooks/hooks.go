// Package hooks dispatches explicitly configured AI lifecycle callbacks.
// Nothing is loaded from a repository implicitly. Hook output is discarded;
// hook failure never replaces the application's exit status.
package hooks

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"time"
)

const ConfigEnv = "PLATFORM_FACTORY_HOOKS_CONFIG"
const ActiveEnv = "PLATFORM_FACTORY_HOOKS_ACTIVE"
const Version = "platform-factory.dev/hook-event/v1"

//go:embed autofix.py
var AutofixScript []byte

type Event struct {
	APIVersion string `json:"api_version"`
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Command    string `json:"command"`
	ExitCode   int    `json:"exit_code"`
	Repository string `json:"repository"`
	DurationMS int64  `json:"duration_ms"`
}
type Action struct {
	Command        []string `json:"command"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	PassEnv        []string `json:"pass_env,omitempty"`
}
type Config struct {
	Version int                 `json:"version"`
	Hooks   map[string][]Action `json:"hooks"`
}

func ValidKind(kind string) bool {
	return kind == "command.started" || kind == "command.succeeded" || kind == "command.failed" || kind == "tool.started" || kind == "tool.succeeded" || kind == "tool.failed"
}
func NewEvent(kind, command, repository string, code int, duration time.Duration) Event {
	id := sha256.Sum256([]byte(repository + "\x00" + command + "\x00" + fmt.Sprint(code)))
	return Event{Version, hex.EncodeToString(id[:]), kind, command, code, repository, duration.Milliseconds()}
}
func Load(path string) (Config, error) {
	var result Config
	if !filepath.IsAbs(path) {
		return result, errors.New("hooks config must be an absolute path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return result, err
	}
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && (info.Mode().Perm()&0077 != 0 || !ownedByCurrentUser(info))) || info.Size() > 65536 {
		return result, errors.New("hooks config must be a regular private file at most 64 KiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return result, err
	}
	defer file.Close()
	dec := json.NewDecoder(io.LimitReader(file, 65537))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&result); err != nil {
		return result, errors.New("invalid hooks configuration")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return result, errors.New("hooks config must contain one object")
	}
	if result.Version != 1 {
		return result, errors.New("unsupported hooks version")
	}
	envPattern := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	for kind, actions := range result.Hooks {
		if !ValidKind(kind) || len(actions) > 8 {
			return result, errors.New("invalid hook event or too many actions")
		}
		for _, action := range actions {
			if len(action.Command) == 0 || !filepath.IsAbs(action.Command[0]) || action.TimeoutSeconds < 1 || action.TimeoutSeconds > 1800 {
				return result, errors.New("hook commands need an absolute executable and 1..1800 second timeout")
			}
			for _, key := range action.PassEnv {
				if !envPattern.MatchString(key) || key == ConfigEnv || key == ActiveEnv {
					return result, errors.New("invalid hook environment allowlist")
				}
			}
		}
	}
	return result, nil
}

// Dispatch never invokes a shell. Only explicit local configuration chooses
// executables; repository content and event data are stdin, not commands.
func Dispatch(ctx context.Context, cfg Config, event Event) error {
	if !ValidKind(event.Kind) {
		return errors.New("unknown hook event")
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	for _, action := range cfg.Hooks[event.Kind] {
		runCtx, cancel := context.WithTimeout(ctx, time.Duration(action.TimeoutSeconds)*time.Second)
		cmd := exec.CommandContext(runCtx, action.Command[0], action.Command[1:]...)
		cmd.Dir = event.Repository
		cmd.Stdin = bytes.NewReader(payload)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), ActiveEnv + "=1"}
		for _, key := range action.PassEnv {
			if value, ok := os.LookupEnv(key); ok {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
		}
		cmd.WaitDelay = time.Second
		isolateProcess(cmd)
		err := cmd.Run()
		cleanupProcess(cmd)
		cancel()
		if err != nil {
			return errors.New("configured hook failed or timed out")
		}
	}
	return nil
}
