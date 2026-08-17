// Package conformance is the public conformance suite for the
// secure-oci stable v1 pipeline API (including its v1alpha1/v1beta1
// compatibility inputs) and the v1 plugin protocol. The
// golden vectors pin validation outcomes, canonical fingerprints and
// stage cache keys so an independent implementation (or a future
// version of this one) can prove compatibility; the plugin checks
// exercise a plugin executable's protocol behavior from the outside.
// The suite ships as the platform-factory-conformance binary with the vectors
// embedded, so it runs without this repository.
package conformance

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/CYPT71/platform-factory/internal/cache"
	"github.com/CYPT71/platform-factory/internal/pipeline"
	apiplugin "github.com/CYPT71/platform-factory/sdk/plugin"
)

//go:embed vectors/*.json
var embeddedVectors embed.FS

// EmbeddedVectors returns the vector corpus compiled into this build.
func EmbeddedVectors() fs.FS { return embeddedVectors }

// Vector pins the expected engine behavior for one pipeline document.
type Vector struct {
	Name     string          `json:"name"`
	Pipeline json.RawMessage `json:"pipeline"`
	Expect   Expect          `json:"expect"`
}

// Expect describes the pinned outcome of decoding a vector's pipeline.
// For invalid pipelines Issues holds the exact sorted validation
// issues. For valid pipelines Fingerprint pins the canonical SHA-256,
// Order the topological order, and StageKeys the cache key of every
// stage computed with the fixed conformance inputs (engine version
// "conformance/1", the empty-string sha256 as base digest,
// linux/amd64, and per-input digests derived from
// sha256("stage/name")).
type Expect struct {
	Valid       bool              `json:"valid"`
	Issues      []pipeline.Issue  `json:"issues,omitempty"`
	Fingerprint string            `json:"fingerprint,omitempty"`
	Order       []string          `json:"order,omitempty"`
	StageKeys   map[string]string `json:"stage_keys,omitempty"`
}

// Result is the outcome of one conformance check.
type Result struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

const conformanceEngineVersion = "conformance/1"

func emptyDigest() string {
	digest := sha256.Sum256(nil)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func conformanceInputDigest(stage, name string) string {
	digest := sha256.Sum256([]byte(stage + "/" + name))
	return "sha256:" + hex.EncodeToString(digest[:])
}

// Evaluate computes the actual outcome for one pipeline document with
// the fixed conformance inputs.
func Evaluate(raw json.RawMessage) (Expect, error) {
	definition, graph, err := pipeline.Decode(bytes.NewReader(raw))
	if err != nil {
		var validation *pipeline.ValidationError
		if errors.As(err, &validation) {
			return Expect{Valid: false, Issues: validation.Issues}, nil
		}
		return Expect{}, err
	}
	fingerprint, err := pipeline.Fingerprint(definition)
	if err != nil {
		return Expect{}, err
	}
	keys := map[string]string{}
	for _, stage := range definition.Stages {
		digests := make([]string, len(stage.Inputs))
		for index, input := range stage.Inputs {
			digests[index] = conformanceInputDigest(input.Stage, input.Name)
		}
		key, err := cache.StageKey(cache.StageKeyInputs{
			EngineVersion: conformanceEngineVersion, Stage: stage,
			BaseDigest: emptyDigest(), InputDigests: digests, Platform: "linux/amd64",
		})
		if err != nil {
			return Expect{}, fmt.Errorf("stage %s: %w", stage.ID, err)
		}
		keys[stage.ID] = key
	}
	return Expect{Valid: true, Fingerprint: fingerprint, Order: graph.Order, StageKeys: keys}, nil
}

// RunVectors evaluates every vector in fsys against the engine and
// reports mismatches. Vectors are processed in file-name order.
func RunVectors(fsys fs.FS) ([]Result, error) {
	names, err := fs.Glob(fsys, "vectors/*.json")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, errors.New("conformance: no vectors found")
	}
	var results []Result
	for _, name := range names {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		var vector Vector
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&vector); err != nil {
			return nil, fmt.Errorf("conformance: %s: %w", name, err)
		}
		actual, err := Evaluate(vector.Pipeline)
		if err != nil {
			results = append(results, Result{Name: vector.Name, Passed: false, Detail: err.Error()})
			continue
		}
		results = append(results, compareExpectations(vector.Name, vector.Expect, actual))
	}
	return results, nil
}

func compareExpectations(name string, expected, actual Expect) Result {
	expectedJSON, _ := json.Marshal(expected)
	actualJSON, _ := json.Marshal(actual)
	if bytes.Equal(expectedJSON, actualJSON) {
		return Result{Name: name, Passed: true}
	}
	return Result{Name: name, Passed: false,
		Detail: fmt.Sprintf("expected %s, got %s", expectedJSON, actualJSON)}
}

// RunPlugin exercises a plugin executable's protocol conformance from
// the outside: handshake correctness, exact protocol version, unknown
// method rejection with code 404, and rejection of malformed and
// oversized frames. Each check starts a fresh plugin process because
// protocol violations are fatal by design.
func RunPlugin(ctx context.Context, executable string) ([]Result, error) {
	checks := []struct {
		name string
		run  func(ctx context.Context, executable string) error
	}{
		{"handshake-reports-protocol-v1", checkHandshake},
		{"detect-freeze-plan-contract", checkLanguageContract},
		{"unknown-method-returns-404", checkUnknownMethod},
		{"missing-content-type-is-fatal", checkMissingContentType},
		{"oversized-frame-is-rejected", checkOversizedFrame},
	}
	var results []Result
	for _, check := range checks {
		checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := check.run(checkCtx, executable)
		cancel()
		result := Result{Name: check.name, Passed: err == nil}
		if err != nil {
			result.Detail = err.Error()
		}
		results = append(results, result)
	}
	return results, nil
}

type pluginProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *bufio.Reader
}

func startPlugin(ctx context.Context, executable string) (*pluginProcess, error) {
	cmd := exec.CommandContext(ctx, executable)
	// Preserve only PATH so interpreter-backed plugins (Python, Node, dotnet)
	// are language-neutral while the conformance process still withholds the
	// caller's credentials and other environment variables.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &pluginProcess{cmd: cmd, stdin: stdin, reader: bufio.NewReader(stdout)}, nil
}

func (p *pluginProcess) close() {
	_ = p.stdin.Close()
	done := make(chan struct{})
	go func() { _ = p.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = p.cmd.Process.Kill()
		<-done
	}
}

func (p *pluginProcess) call(method string, params any) (apiplugin.Response, error) {
	var raw json.RawMessage
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return apiplugin.Response{}, err
		}
		raw = data
	}
	if err := apiplugin.WriteMessage(p.stdin, apiplugin.Request{ID: "1", Method: method, Params: raw}); err != nil {
		return apiplugin.Response{}, err
	}
	body, err := apiplugin.ReadMessage(p.reader)
	if err != nil {
		return apiplugin.Response{}, err
	}
	var response apiplugin.Response
	if err := json.Unmarshal(body, &response); err != nil {
		return apiplugin.Response{}, err
	}
	return response, nil
}

func checkHandshake(ctx context.Context, executable string) error {
	process, err := startPlugin(ctx, executable)
	if err != nil {
		return err
	}
	defer process.close()
	response, err := process.call("v1.hello", nil)
	if err != nil {
		return fmt.Errorf("hello call: %w", err)
	}
	if response.Error != nil {
		return fmt.Errorf("hello returned error %d: %s", response.Error.Code, response.Error.Message)
	}
	var hello apiplugin.HelloResult
	if err := json.Unmarshal(response.Result, &hello); err != nil {
		return fmt.Errorf("hello result: %w", err)
	}
	if hello.APIVersion != apiplugin.ProtocolVersion {
		return fmt.Errorf("api_version %q, want %q", hello.APIVersion, apiplugin.ProtocolVersion)
	}
	if hello.Name == "" || len(hello.Capabilities) == 0 {
		return fmt.Errorf("hello must report a name and at least one capability, got %+v", hello)
	}
	required := map[string]bool{
		apiplugin.CapabilityDetect: false,
		apiplugin.CapabilityFreeze: false,
		apiplugin.CapabilityPlan:   false,
	}
	for _, capability := range hello.Capabilities {
		if _, ok := required[capability]; ok {
			required[capability] = true
		}
	}
	for capability, present := range required {
		if !present {
			return fmt.Errorf("hello does not advertise required capability %q", capability)
		}
	}
	return nil
}

func checkLanguageContract(ctx context.Context, executable string) error {
	process, err := startPlugin(ctx, executable)
	if err != nil {
		return err
	}
	defer process.close()

	detectResponse, err := process.call("v1.detect", apiplugin.DetectParams{Path: "."})
	if err != nil || detectResponse.Error != nil {
		return fmt.Errorf("detect call: transport=%v rpc=%v", err, detectResponse.Error)
	}
	var detected apiplugin.DetectResult
	if err := json.Unmarshal(detectResponse.Result, &detected); err != nil || detected.Kind == "" {
		return fmt.Errorf("detect result must contain kind: result=%s error=%v", detectResponse.Result, err)
	}

	freezeResponse, err := process.call("v1.freeze", apiplugin.FreezeParams{Language: detected.Kind, Root: "."})
	if err != nil || freezeResponse.Error != nil {
		return fmt.Errorf("freeze call: transport=%v rpc=%v", err, freezeResponse.Error)
	}
	var frozen apiplugin.FreezeResult
	if err := json.Unmarshal(freezeResponse.Result, &frozen); err != nil || len(frozen.Steps) == 0 {
		return fmt.Errorf("freeze result must contain at least one step: result=%s error=%v", freezeResponse.Result, err)
	}
	for index, step := range frozen.Steps {
		if len(step) == 0 {
			return fmt.Errorf("freeze step %d is empty", index)
		}
		for _, argument := range step {
			if argument == "" || strings.ContainsRune(argument, 0) {
				return fmt.Errorf("freeze step %d contains an unsafe argument", index)
			}
		}
	}

	planResponse, err := process.call("v1.plan", apiplugin.PlanParams{Language: detected.Kind, Root: "."})
	if err != nil || planResponse.Error != nil {
		return fmt.Errorf("plan call: transport=%v rpc=%v", err, planResponse.Error)
	}
	var planned apiplugin.PlanResult
	if err := json.Unmarshal(planResponse.Result, &planned); err != nil {
		return fmt.Errorf("plan result: %w", err)
	}
	return nil
}

func checkUnknownMethod(ctx context.Context, executable string) error {
	process, err := startPlugin(ctx, executable)
	if err != nil {
		return err
	}
	defer process.close()
	response, err := process.call("v1.does-not-exist", nil)
	if err != nil {
		return fmt.Errorf("call: %w", err)
	}
	if response.Error == nil || response.Error.Code != 404 {
		return fmt.Errorf("expected error code 404, got %+v", response.Error)
	}
	return nil
}

func checkMissingContentType(ctx context.Context, executable string) error {
	process, err := startPlugin(ctx, executable)
	if err != nil {
		return err
	}
	defer process.close()
	if _, err := io.WriteString(process.stdin, "Content-Length: 2\r\n\r\n{}"); err != nil {
		return err
	}
	_ = process.stdin.Close()
	if _, err := apiplugin.ReadMessage(process.reader); err == nil {
		return errors.New("plugin answered a frame without Content-Type")
	}
	return nil
}

func checkOversizedFrame(ctx context.Context, executable string) error {
	process, err := startPlugin(ctx, executable)
	if err != nil {
		return err
	}
	defer process.close()
	header := fmt.Sprintf("Content-Type: %s\r\nContent-Length: %d\r\n\r\n", apiplugin.ContentType, (1<<20)+1)
	if _, err := io.WriteString(process.stdin, header); err != nil {
		return err
	}
	if _, err := io.WriteString(process.stdin, strings.Repeat("x", 4096)); err != nil {
		// The plugin may close stdin immediately after rejecting the
		// header; a write failure here is the expected refusal.
		return nil
	}
	_ = process.stdin.Close()
	if _, err := apiplugin.ReadMessage(process.reader); err == nil {
		return errors.New("plugin answered an oversized frame")
	}
	return nil
}
