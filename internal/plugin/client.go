// Package plugin is the host side of the out-of-process plugin
// boundary: it launches plugin subprocesses, performs the handshake,
// verifies signed digest-pinned manifests and discovers installed
// plugins. The wire protocol and the plugin-side SDK live in the public
// sdk/plugin package, the only package a third-party Go plugin imports.
package plugin

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CYPT71/platform-factory/internal/core"
	"github.com/CYPT71/platform-factory/internal/observability"
)

// HelloResult is the host-owned representation of the v1 handshake.
type HelloResult struct {
	APIVersion   string   `json:"api_version"`
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Capabilities []string `json:"capabilities"`
}

// pluginSandboxWrapper is a var, not a direct call to wrapWithPluginSandbox,
// solely so tests can substitute a stub that reports the sandbox as
// unavailable - the real unavailable case (namespace creation refused by
// the kernel) is not something a portable test can force.
var pluginSandboxWrapper func(cmd *exec.Cmd, family PluginFamily, permissions PluginPermissions) error = wrapWithPluginSandbox

// Client talks to one plugin subprocess.
type Client struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser

	mu        sync.Mutex
	reader    *bufio.Reader
	nextID    atomic.Int64
	closed    atomic.Bool
	exited    atomic.Bool
	done      chan error
	cleanupMu sync.Mutex
	cleanup   func()

	hello HelloResult
	// verifiedDigest is populated only by VerifyAndStart after executable,
	// signature, policy, and handshake verification all succeed.
	verifiedDigest       string
	journal              core.OperationJournal
	runtimeGrantEvidence *RuntimeGrantEvidence
}

// Start launches executable as a plugin subprocess and performs the
// version/capability handshake. env, if non-empty, replaces the process's
// environment entirely. Otherwise, the plugin's environment is derived from
// the host's own, filtered through filterPluginEnvironment per permissions:
// PATH, PLATFORM_FACTORY_*, LANG/LC_* and TMPDIR always pass through;
// everything else, including KUBECONFIG and HOME, is dropped unless
// permissions.Secrets declares "kubeconfig" - a plugin does not inherit the
// host's environment wholesale by default.
//
// The subprocess must run in a fresh user, network, IPC and UTS namespace with
// no host network access (see wrapWithPluginSandbox). If that isolation cannot
// be applied, Start refuses to launch. The explicitly named degradation entry
// point is StartAllowingUnsandboxed.
//
// Start applies defaultPermissionProfile's resource ceilings (see
// permission_profile.go) because it has no manifest to resolve a family
// from; VerifyAndStart, which does, uses StartWithFamily instead.
func Start(ctx context.Context, executable string, args, env []string) (*Client, error) {
	return start(ctx, executable, args, env, false, "", PluginPermissions{})
}

// StartAllowingUnsandboxed launches a plugin even when the host cannot apply
// the process sandbox. This is an explicit security degradation intended only
// for a policy that has deliberately accepted the loss of isolation.
func StartAllowingUnsandboxed(ctx context.Context, executable string, args, env []string) (*Client, error) {
	return start(ctx, executable, args, env, true, "", PluginPermissions{})
}

// StartWithFamily is Start, but resolves resource ceilings from family's
// PermissionProfile (permission_profile.go) instead of the family-less
// default - the entry point VerifyAndStart uses once it has a manifest's
// declared Family available.
func StartWithFamily(ctx context.Context, executable string, args, env []string, family PluginFamily) (*Client, error) {
	return start(ctx, executable, args, env, false, family, PluginPermissions{})
}

// StartAllowingUnsandboxedWithFamily combines StartAllowingUnsandboxed and
// StartWithFamily.
func StartAllowingUnsandboxedWithFamily(ctx context.Context, executable string, args, env []string, family PluginFamily) (*Client, error) {
	return start(ctx, executable, args, env, true, family, PluginPermissions{})
}

// StartWithManifest is StartWithFamily, but additionally threads the
// manifest's declared Permissions into the sandbox: a non-language-family
// plugin that declares Permissions.Network gets the host's real network
// namespace instead of the isolated, connectivity-less one every other
// plugin gets (see wrapWithPluginSandbox), and one that declares
// "kubeconfig" in Permissions.Secrets gets KUBECONFIG/HOME passed through
// so it can actually find cluster credentials. VerifyAndStart uses this
// once it has a manifest's declared Permissions available, the same way it
// already uses StartWithFamily for Family.
func StartWithManifest(ctx context.Context, executable string, args, env []string, family PluginFamily, permissions PluginPermissions) (*Client, error) {
	return start(ctx, executable, args, env, false, family, permissions)
}

// StartAllowingUnsandboxedWithManifest combines StartAllowingUnsandboxed and
// StartWithManifest.
func StartAllowingUnsandboxedWithManifest(ctx context.Context, executable string, args, env []string, family PluginFamily, permissions PluginPermissions) (*Client, error) {
	return start(ctx, executable, args, env, true, family, permissions)
}

func start(ctx context.Context, executable string, args, env []string, allowUnsandboxed bool, family PluginFamily, permissions PluginPermissions) (*Client, error) {
	cmd, stdin, stdout, err := newPluginCommand(ctx, executable, args, env, permissions)
	if err != nil {
		return nil, err
	}
	if wrapErr := pluginSandboxWrapper(cmd, family, permissions); wrapErr == nil {
		if startErr := cmd.Start(); startErr == nil {
			// Starting in namespaces is not evidence that the operation-specific
			// filesystem, network, and credential grants were actually enforced.
			// A runtime adapter must attach independently measured grant evidence;
			// until then artifact operations fail closed.
			return finishStart(ctx, cmd, stdin, stdout)
		} else if !allowUnsandboxed {
			return nil, fmt.Errorf("plugin: required sandbox start failed for %s: %w", executable, startErr)
		}
		// Sandbox setup itself failed inside the fork (for example,
		// unprivileged user namespaces disabled by sysctl). cmd is no
		// longer usable after a failed Start, so build a fresh,
		// unwrapped command for the fallback below.
		fmt.Fprintf(os.Stderr, "plugin: policy permits unsandboxed execution for %s\n", executable)
		cmd, stdin, stdout, err = newPluginCommand(ctx, executable, args, env, permissions)
		if err != nil {
			return nil, err
		}
	} else if !allowUnsandboxed {
		return nil, fmt.Errorf("plugin: required sandbox unavailable for %s: %w", executable, wrapErr)
	} else {
		fmt.Fprintf(os.Stderr, "plugin: policy permits unsandboxed execution for %s: %v\n", executable, wrapErr)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("plugin: start %s: %w", executable, err)
	}
	client, err := finishStart(ctx, cmd, stdin, stdout)
	return client, err
}

// newPluginCommand builds the exec.Cmd a plugin subprocess starts from.
// When the caller supplies an explicit, non-empty env, that overrides
// everything else - the contract Start's doc comment already promises. When
// it does not (every manifest-driven start today: VerifyAndStart always
// passes nil, see manifest.go's verifyAndStart), the command's environment
// is computed from the host's own os.Environ(), filtered through
// filterPluginEnvironment per permissions - not a hardcoded PATH-only stub.
// This is the seed both the unsandboxed path (this becomes the plugin's
// final environment directly) and the sandboxed path (this becomes what the
// re-exec'd sandbox helper inherits into its own os.Environ(), which it
// filters again in execPlugin - a harmless no-op re-application of the same
// predicate) build on; previously this always produced
// []string{"PATH=/usr/bin:/bin"}, which as a non-nil cmd.Env replaces (not
// merges with) the child's environment, silently discarding the real host
// environment - including KUBECONFIG/HOME - before either path could ever
// see it.
func newPluginCommand(ctx context.Context, executable string, args, env []string, permissions PluginPermissions) (*exec.Cmd, io.WriteCloser, io.ReadCloser, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	if len(env) > 0 {
		cmd.Env = env
	} else {
		cmd.Env = filterPluginEnvironment(os.Environ(), permissions)
	}
	// The RPC protocol only uses stdin/stdout; leaving Stderr at its zero
	// value sends it to /dev/null (see os/exec's docs for a nil Stderr),
	// silently discarding every "plugin sandbox: ..." degradation
	// warning this package's own sandbox helper prints when isolation
	// partially fails (see sandbox_linux.go's isolateTempDirectory and
	// applyResourceLimits, both intentionally best-effort). Forwarding it
	// to the host's own stderr turns a silent security degradation into
	// a visible one.
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("plugin: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("plugin: stdout pipe: %w", err)
	}
	return cmd, stdin, stdout, nil
}

func finishStart(ctx context.Context, cmd *exec.Cmd, stdin io.WriteCloser, stdout io.ReadCloser) (*Client, error) {
	client := &Client{cmd: cmd, stdin: stdin, reader: bufio.NewReader(stdout), done: make(chan error, 1)}
	go func() {
		err := cmd.Wait()
		client.exited.Store(true)
		client.done <- err
		close(client.done)
		client.runCleanup()
	}()
	if err := client.handshake(ctx); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

func (c *Client) handshake(ctx context.Context) error {
	if err := c.Call(ctx, "v1.hello", nil, &c.hello); err != nil {
		return fmt.Errorf("plugin: handshake: %w", err)
	}
	if c.hello.APIVersion != ProtocolVersion {
		return fmt.Errorf("plugin: %s reports protocol %q, this host only supports %q",
			c.hello.Name, c.hello.APIVersion, ProtocolVersion)
	}
	return nil
}

// Hello returns the plugin's self-reported name, version and capabilities
// from the handshake.
func (c *Client) Hello() HelloResult { return c.hello }

// HasCapability reports whether the plugin declared capability at the
// handshake.
func (c *Client) HasCapability(capability string) bool {
	return slices.Contains(c.hello.Capabilities, capability)
}

// Call issues one read-only RPC and decodes its result into result, if non-nil.
// Requests are serialized: only one Call may be in flight at a time per
// Client. Methods that are not part of the host's read-only vocabulary fail
// closed and must be invoked through CallWithIdempotency. In particular, a
// newly introduced migration method cannot accidentally bypass OperationID
// and the durable journal merely because this host version does not know it.
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	if !isReadOnlyMethod(method) {
		return fmt.Errorf("plugin: method %q is not proven read-only; CallWithIdempotency is required", method)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	var rawParams json.RawMessage
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("plugin: encode params: %w", err)
		}
		rawParams = data
	}

	traceID := observability.TraceIDFromContext(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()

	id := strconv.FormatInt(c.nextID.Add(1), 10)
	if err := WriteMessage(c.stdin, Request{ID: id, Method: method, Params: rawParams, TraceID: traceID}); err != nil {
		return fmt.Errorf("plugin: write request: %w", err)
	}
	raw, err := ReadMessage(c.reader)
	if err != nil {
		return fmt.Errorf("plugin: read response: %w", err)
	}
	var resp Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("plugin: decode response: %w", err)
	}
	if resp.ID != id {
		return fmt.Errorf("plugin: response id %q does not match request id %q", resp.ID, id)
	}
	if resp.TraceID != traceID {
		return fmt.Errorf("plugin: response trace_id %q does not match request trace_id %q", resp.TraceID, traceID)
	}
	if resp.Error != nil {
		return resp.Error
	}
	if result != nil && len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return fmt.Errorf("plugin: decode result: %w", err)
		}
	}
	return nil
}

// isReadOnlyMethod is an exact host-owned allowlist. Matching individual path
// components is unsafe: names such as observe.apply can conceal a mutation.
// Every unknown method therefore requires the mutation journal.
func isReadOnlyMethod(method string) bool {
	switch method {
	case "v1.hello", "v1.detect", "v1.freeze", "v1.plan",
		"v1.migration.discover", "v1.migration.inspect", "v1.migration.observe", "v1.migration.artifact-observe", "v1.migration.export", "v1.migration.verify",
		"v1.deployment.plan", "v1.deployment.observe",
		"v1.runtime.logs", "v1.runtime.status",
		"v1.analyzer.scan", "v1.analyzer.verify", "v1.registry.list",
		"v1.observe.net-probe", "v1.observe.priv-probe", "v1.observe.isolation-probe", "v1.observe.tmp-probe", "v1.observe.netns-probe",
		"v1.observe.memory-bomb", "v1.observe.fork-bomb", "v1.observe.env-probe":
		return true
	default:
		return false
	}
}

// CallWithIdempotency executes a plugin method with idempotency guarantees.
// If the method is not a proven read-only operation,
// it will:
//  1. Require an OperationID supplied by the owner of the logical mutation.
//  2. Durably record the start before invoking the plugin.
//  3. Return a durable terminal status for exact duplicate calls. Plugin
//     response bodies are not persisted because they may contain secrets; a
//     duplicate that needs a response body must re-observe state explicitly.
//  4. Report a durable non-terminal record as indeterminate for re-observation.
//
// For non-mutating methods, this behaves identically to Call.
//
// un OperationID, relancer la même opération après un crash ne doit pas créer
// un deuxième workload."
func (c *Client) CallWithIdempotency(ctx context.Context, operationID core.OperationID, method string, params, result any) error {
	// Only explicitly proven observations bypass the mutation journal. Unknown
	// methods fail into this path, so future protocol additions are safe by
	// default.
	if isReadOnlyMethod(method) {
		return c.Call(ctx, method, params, result)
	}
	if !core.ValidOperationID(operationID) {
		return fmt.Errorf("plugin: mutating method %q requires a valid operation ID", method)
	}
	if c.verifiedDigest == "" {
		return fmt.Errorf("plugin: mutating method %q requires a verified plugin identity", method)
	}
	operationScope, err := mutationScope(c.verifiedDigest, method, params)
	if err != nil {
		return err
	}

	// Slow path: mutating method with idempotency

	journal := c.journal
	if journal == nil {
		return errors.New("plugin: mutating call requires an operation journal")
	}

	// Check if this operation has already been recorded
	if record, exists := journal.Lookup(operationID); exists {
		if record.Scope != operationScope {
			return fmt.Errorf("plugin: operation id %q collides with a different mutation", operationID)
		}
		switch record.Status {
		case core.OperationCompleted:
			if result != nil {
				return fmt.Errorf("plugin: operation %q completed without a replayable result", operationID)
			}
			return nil
		case core.OperationFailed:
			return fmt.Errorf("plugin: operation %q previously failed", operationID)
		case core.OperationStarted:
			return fmt.Errorf("plugin: operation %q: %w", operationID, core.ErrOperationIndeterminate)
		default:
			// Should not happen, but proceed with regular call
		}
	}

	// Mark operation as started in the journal
	// If Start returns false, another goroutine already started this operation
	started, err := journal.Start(operationID, operationScope)
	if err != nil {
		return fmt.Errorf("plugin: start operation %q: %w", operationID, err)
	}
	if !started {
		return fmt.Errorf("plugin: operation %q: %w", operationID, core.ErrOperationIndeterminate)
	}

	// Execute the operation with the OperationID in the request
	err = c.callWithOperationID(ctx, method, params, result, operationID)

	// Record the outcome in the journal
	if err != nil {
		terminal, outcomeErr := classifyMutationFailure(err)
		if !terminal {
			// Once a mutating request has been written, transport/protocol errors
			// cannot prove whether the plugin applied it. Keep Started durable.
			return outcomeErr
		}
		if journalErr := journal.Fail(operationID); journalErr != nil {
			return errors.Join(err, fmt.Errorf("plugin: persist terminal operation failure: %w", journalErr))
		}
	} else {
		if journalErr := journal.Complete(operationID); journalErr != nil {
			return fmt.Errorf("plugin: persist operation completion: %w", journalErr)
		}
	}

	return err
}

func classifyMutationFailure(err error) (terminal bool, outcome error) {
	var local *preDispatchError
	if errors.As(err, &local) {
		return true, local.err
	}
	// Once any bytes may have been written, no plugin response -- including a
	// 404 -- proves that no effect happened. A non-conforming or compromised
	// plugin may dispatch before constructing that response.
	return false, errors.Join(core.ErrOperationIndeterminate, err)
}

type preDispatchError struct{ err error }

func (e *preDispatchError) Error() string { return e.err.Error() }
func (e *preDispatchError) Unwrap() error { return e.err }

func mutationScope(verifiedDigest, method string, params any) (string, error) {
	if verifiedDigest == "" {
		return "", errors.New("plugin: verified digest is required for mutation scope")
	}
	encodedParams, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("plugin: encode operation scope: %w", err)
	}
	scopeDigest := sha256.Sum256(append([]byte(verifiedDigest+"\x00"+method+"\x00"), encodedParams...))
	return fmt.Sprintf("sha256:%x", scopeDigest), nil
}

// callWithOperationID executes a plugin method with the given OperationID.
// This is used internally by CallWithIdempotency.
func (c *Client) callWithOperationID(ctx context.Context, method string, params, result any, operationID core.OperationID) error {
	if err := ctx.Err(); err != nil {
		return &preDispatchError{err: err}
	}

	var rawParams json.RawMessage
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return &preDispatchError{err: fmt.Errorf("plugin: encode params: %w", err)}
		}
		rawParams = data
	}

	traceID := observability.TraceIDFromContext(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()

	id := strconv.FormatInt(c.nextID.Add(1), 10)
	if err := WriteMessage(c.stdin, Request{
		ID:          id,
		Method:      method,
		Params:      rawParams,
		TraceID:     traceID,
		OperationID: string(operationID),
	}); err != nil {
		return fmt.Errorf("plugin: write request: %w", err)
	}
	raw, err := ReadMessage(c.reader)
	if err != nil {
		return fmt.Errorf("plugin: read response: %w", err)
	}
	var resp Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("plugin: decode response: %w", err)
	}
	if resp.ID != id {
		return fmt.Errorf("plugin: response id %q does not match request id %q", resp.ID, id)
	}
	if resp.TraceID != traceID {
		return fmt.Errorf("plugin: response trace_id %q does not match request trace_id %q", resp.TraceID, traceID)
	}
	// Verify the OperationID is echoed back in the response
	if resp.OperationID != string(operationID) {
		return fmt.Errorf("plugin: response operation_id %q does not match request operation_id %q",
			resp.OperationID, operationID)
	}
	if resp.Error != nil {
		return resp.Error
	}
	if result != nil && len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return fmt.Errorf("plugin: decode result: %w", err)
		}
	}
	return nil
}

// Close closes the plugin's stdin and waits for it to exit, killing it if
// it does not exit promptly.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.closed.Store(true)
	closeErr := c.stdin.Close()
	if c.done == nil {
		return closeErr
	}
	select {
	case err := <-c.done:
		return err
	case <-time.After(5 * time.Second):
		_ = c.cmd.Process.Kill()
		return <-c.done
	}
}

func (c *Client) isAlive() bool {
	return c != nil && !c.closed.Load() && !c.exited.Load()
}

func (c *Client) setCleanup(cleanup func()) {
	c.cleanupMu.Lock()
	c.cleanup = cleanup
	c.cleanupMu.Unlock()
	if c.exited.Load() && cleanup != nil {
		c.runCleanup()
	}
}

func (c *Client) runCleanup() {
	c.cleanupMu.Lock()
	cleanup := c.cleanup
	c.cleanup = nil
	c.cleanupMu.Unlock()
	if cleanup != nil {
		cleanup()
	}
}
