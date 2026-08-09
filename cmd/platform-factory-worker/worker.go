// platform-factory-worker registers with a platform-factory-control-plane over mutual
// TLS, polls for leases, and reports completion. Its own identity - the
// one the control plane trusts - is the CommonName of the client
// certificate it presents, not anything it can claim in a request body.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	typederrors "github.com/CYPT71/secure-oci-base/internal/errors"
	"github.com/CYPT71/secure-oci-base/internal/observability"
	"github.com/CYPT71/secure-oci-base/internal/provenance"
)

// Client talks to one control plane. Execute is called for every lease
// this worker is assigned and must return the result to report; it is
// injected so worker.go's polling loop stays free of any assumption
// about what "the work" actually is.
type Client struct {
	HTTP    *http.Client
	BaseURL string
	Execute func(ctx context.Context, lease Lease) (result string, err error)
	// MaxParallel bounds concurrent lease polls/executions. Zero selects one.
	MaxParallel int
	// RequestTimeout bounds each control-plane exchange independently of
	// task execution. Zero selects 30 seconds.
	RequestTimeout time.Duration
	// CancellationPollInterval controls how quickly assigned work observes a
	// durable remote cancellation. Zero selects 100 milliseconds.
	CancellationPollInterval time.Duration
	// Signer, if set, is this worker's persistent workload identity. Its
	// public key is sent with Register, and every /lease/complete this
	// worker reports is signed with it - see completeRequest.
	Signer *provenance.WorkloadSigner
	// WorkerID identifies this process in the provenance records Signer
	// produces (control.Lease's own Worker field is the control plane's
	// independent, mTLS-derived source of truth for who completed a
	// lease; this must match it or the control plane rejects the
	// completion - see cmd/platform-factory-control-plane's
	// verifyCompletionProvenance). Required when Signer is set.
	WorkerID string
}

type Registration struct {
	Platform      string   `json:"platform"`
	Capabilities  []string `json:"capabilities,omitempty"`
	CachedContent []string `json:"cached_content,omitempty"`
	MaxParallel   int      `json:"max_parallel,omitempty"`
	// PublicKey is this worker's base64-encoded Ed25519 public key, set
	// automatically from Client.Signer's identity when one is configured.
	PublicKey string `json:"public_key,omitempty"`
}

const maxResponseBodyBytes = 1 << 20

// Lease mirrors internal/control.Lease's full wire shape without
// importing internal/control, keeping the worker protocol explicit at the
// process boundary. It must stay a complete mirror because the strict
// response decoder rejects fields the worker does not declare.
type Lease struct {
	ID                   string   `json:"id"`
	Payload              string   `json:"payload"`
	RequiredPlatform     string   `json:"required_platform,omitempty"`
	RequiredCapabilities []string `json:"required_capabilities,omitempty"`
	PreferredContent     string   `json:"preferred_content,omitempty"`
	State                string   `json:"state"`
	Worker               string   `json:"worker,omitempty"`
	Attempt              int      `json:"attempt"`
	Result               string   `json:"result,omitempty"`
	AssignedAt           string   `json:"assigned_at,omitempty"`
	CompletedBy          string   `json:"completed_by,omitempty"`
	CompletedAt          string   `json:"completed_at,omitempty"`
	CanceledBy           string   `json:"canceled_by,omitempty"`
	CanceledAt           string   `json:"canceled_at,omitempty"`
}

func (c *Client) leaseStatus(ctx context.Context, leaseID string) (Lease, error) {
	if c.HTTP == nil {
		return Lease{}, typederrors.New(typederrors.CodeInvalidArgument, "worker: HTTP client is required")
	}
	if strings.TrimSpace(c.BaseURL) == "" {
		return Lease{}, typederrors.New(typederrors.CodeInvalidArgument, "worker: control-plane URL is required")
	}
	timeout := c.RequestTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet,
		strings.TrimRight(c.BaseURL, "/")+"/lease/status?id="+url.QueryEscape(leaseID), nil)
	if err != nil {
		return Lease{}, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Lease{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Lease{}, typederrors.Newf(typederrors.CodeUnavailable, "/lease/status: unexpected status %d", resp.StatusCode)
	}
	limited := io.LimitReader(resp.Body, maxResponseBodyBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return Lease{}, err
	}
	if len(data) > maxResponseBodyBytes {
		return Lease{}, typederrors.New(typederrors.CodeInvalidArgument, "/lease/status: response exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var lease Lease
	if err := decoder.Decode(&lease); err != nil {
		return Lease{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Lease{}, typederrors.New(typederrors.CodeInvalidArgument, "worker: response must contain exactly one JSON value")
	}
	return lease, nil
}

func (c *Client) post(ctx context.Context, path string, body, out any) (status int, err error) {
	if c.HTTP == nil {
		return 0, typederrors.New(typederrors.CodeInvalidArgument, "worker: HTTP client is required")
	}
	if strings.TrimSpace(c.BaseURL) == "" {
		return 0, typederrors.New(typederrors.CodeInvalidArgument, "worker: control-plane URL is required")
	}
	timeout := c.RequestTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return 0, err
		}
	}
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+path, &buf)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes+1))
	if err != nil {
		return resp.StatusCode, typederrors.Wrapf(typederrors.CodeUnavailable, "%s: read response", err, path)
	}
	if len(responseBody) > maxResponseBodyBytes {
		return resp.StatusCode, typederrors.Newf(typederrors.CodeInvalidArgument, "%s: response exceeds 1 MiB", path)
	}
	if out != nil && resp.StatusCode == http.StatusOK {
		decoder := json.NewDecoder(bytes.NewReader(responseBody))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(out); err != nil {
			return resp.StatusCode, err
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return resp.StatusCode, typederrors.New(typederrors.CodeInvalidArgument, "worker: response must contain exactly one JSON value")
		}
	}
	if resp.StatusCode >= 300 {
		return resp.StatusCode, typederrors.Newf(typederrors.CodeUnavailable, "%s: unexpected status %d", path, resp.StatusCode)
	}
	return resp.StatusCode, nil
}

// Register registers this worker's platform with the control plane. It
// is safe to call again after a restart - the control plane treats a
// repeat registration as proof the previous process holding this
// identity is gone, and immediately reclaims any lease still assigned to
// it, rather than as a plain liveness refresh (see
// internal/control.ControlPlane.RegisterWorker). Call it exactly once
// per process lifetime; Heartbeat is what a live process sends
// repeatedly.
func (c *Client) Register(ctx context.Context, platform string) error {
	return c.RegisterWithOptions(ctx, Registration{Platform: platform, MaxParallel: 1})
}

func (c *Client) RegisterWithOptions(ctx context.Context, registration Registration) error {
	if strings.TrimSpace(registration.Platform) == "" {
		return typederrors.New(typederrors.CodeInvalidArgument, "worker: platform is required")
	}
	if registration.MaxParallel == 0 {
		registration.MaxParallel = 1
	}
	if registration.MaxParallel < 1 || registration.MaxParallel > 1024 {
		return typederrors.New(typederrors.CodeInvalidArgument, "worker: max parallel must be between 1 and 1024")
	}
	if c.Signer != nil {
		if strings.TrimSpace(c.WorkerID) == "" {
			return typederrors.New(typederrors.CodeInvalidArgument, "worker: WorkerID is required when Signer is set")
		}
		registration.PublicKey = c.Signer.Identity().PublicKey
	}
	c.MaxParallel = registration.MaxParallel
	_, err := c.post(ctx, "/register", registration, nil)
	return err
}

func (c *Client) Heartbeat(ctx context.Context) error {
	_, err := c.post(ctx, "/heartbeat", nil, nil)
	return err
}

// PollOnce fetches and executes at most one lease. ok is false if the
// control plane had nothing pending - a normal, expected outcome.
func (c *Client) PollOnce(ctx context.Context) (ok bool, err error) {
	if c.Execute == nil {
		return false, typederrors.New(typederrors.CodeInvalidArgument, "worker: lease executor is required")
	}
	var lease Lease
	status, err := c.post(ctx, "/lease/next", nil, &lease)
	if err != nil {
		return false, err
	}
	if status == http.StatusNoContent {
		return false, nil
	}

	span, ctx := observability.StartSpanWithContext(ctx, "worker.lease", observability.WithTags(map[string]any{
		"lease_id":          lease.ID,
		"required_platform": lease.RequiredPlatform,
		"attempt":           lease.Attempt,
	}))
	defer func() {
		if span == nil {
			return
		}
		if err != nil {
			observability.EndWithError(span, err)
			return
		}
		observability.End(span)
	}()

	executionCtx, cancelExecution := context.WithCancel(ctx)
	defer cancelExecution()
	type executionResult struct {
		result string
		err    error
	}
	done := make(chan executionResult, 1)
	go func() {
		result, err := c.Execute(executionCtx, lease)
		done <- executionResult{result: result, err: err}
	}()
	interval := c.CancellationPollInterval
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var execution executionResult
	for {
		select {
		case execution = <-done:
			if execution.err != nil {
				return true, fmt.Errorf("execute lease %q: %w", lease.ID, execution.err)
			}
			goto completed
		case <-ticker.C:
			current, err := c.leaseStatus(ctx, lease.ID)
			if err != nil {
				cancelExecution()
				<-done
				return true, fmt.Errorf("poll cancellation of lease %q: %w", lease.ID, err)
			}
			if current.State == "canceled" {
				cancelExecution()
				<-done
				return true, nil
			}
		case <-ctx.Done():
			cancelExecution()
			<-done
			return true, ctx.Err()
		}
	}

completed:
	result := execution.result
	completion := completeRequest{LeaseID: lease.ID, Result: result}
	if c.Signer != nil {
		record := &provenance.ProvenanceRecord{BuildID: lease.ID, WorkerID: c.WorkerID}
		if err := c.Signer.Sign(record); err != nil {
			return true, fmt.Errorf("sign provenance for lease %q: %w", lease.ID, err)
		}
		completion.Provenance = record
	}
	if _, err := c.post(ctx, "/lease/complete", completion, nil); err != nil {
		// A 409 here means the control plane already reassigned this
		// lease (this worker was presumed lost and is now late) - that
		// is the documented, correct outcome of the idempotency guard,
		// not a bug in the worker, so it is returned as a plain error
		// for the caller to log, not retried or treated as data loss.
		return true, fmt.Errorf("report completion of lease %q: %w", lease.ID, err)
	}
	return true, nil
}

// completeRequest mirrors cmd/platform-factory-control-plane's completeRequest
// wire shape, kept as an explicit local copy for the same reason Lease is:
// this package deliberately does not import the control-plane binary.
type completeRequest struct {
	LeaseID    string                       `json:"lease_id"`
	Result     string                       `json:"result"`
	Provenance *provenance.ProvenanceRecord `json:"provenance,omitempty"`
}

// Run heartbeats and polls for work until ctx is done. Heartbeats run in a
// dedicated goroutine, so a long Execute call cannot make a healthy worker
// lose its lease.
func (c *Client) Run(ctx context.Context, heartbeatInterval, pollInterval time.Duration) error {
	if heartbeatInterval <= 0 || pollInterval <= 0 {
		return typederrors.New(typederrors.CodeInvalidArgument, "worker: heartbeat and poll intervals must be greater than zero")
	}
	runCtx, cancel := context.WithCancel(ctx)
	heartbeatErrors := make(chan error, 1)
	workerErrors := make(chan error, 1)
	var heartbeats sync.WaitGroup
	var executions sync.WaitGroup
	heartbeats.Add(1)
	go func() {
		defer heartbeats.Done()
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if err := c.Heartbeat(runCtx); err != nil && runCtx.Err() == nil {
					select {
					case heartbeatErrors <- err:
					default:
					}
					cancel()
					return
				}
			}
		}
	}()
	defer func() {
		cancel()
		heartbeats.Wait()
		executions.Wait()
	}()
	parallelism := c.MaxParallel
	if parallelism == 0 {
		parallelism = 1
	}
	if parallelism < 1 || parallelism > 1024 {
		return typederrors.New(typederrors.CodeInvalidArgument, "worker: max parallel must be between 1 and 1024")
	}
	slots := make(chan struct{}, parallelism)

	poll := time.NewTicker(pollInterval)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-heartbeatErrors:
			return fmt.Errorf("heartbeat: %w", err)
		case err := <-workerErrors:
			return fmt.Errorf("poll: %w", err)
		case <-poll.C:
			select {
			case slots <- struct{}{}:
				executions.Add(1)
				go func() {
					defer executions.Done()
					defer func() { <-slots }()
					if _, err := c.PollOnce(runCtx); err != nil && runCtx.Err() == nil {
						select {
						case workerErrors <- err:
							cancel()
						default:
						}
					}
				}()
			default:
				// All advertised execution slots are occupied.
			}
		}
	}
}
