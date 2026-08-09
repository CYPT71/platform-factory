package plugin

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/CYPT71/secure-oci-base/internal/core"
	"github.com/CYPT71/secure-oci-base/internal/idempotency"
	"github.com/CYPT71/secure-oci-base/internal/observability"
)

func useTestJournal(t *testing.T) *idempotency.FileJournal {
	t.Helper()
	journal, err := idempotency.NewFileJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return journal
}

func encodedResponse(t *testing.T, response Response) *bufio.Reader {
	t.Helper()
	var wire bytes.Buffer
	if err := WriteMessage(&wire, response); err != nil {
		t.Fatal(err)
	}
	return bufio.NewReader(&wire)
}

func TestDirectStartIdentityCannotPerformIdempotentMutation(t *testing.T) {
	client := &Client{hello: HelloResult{Name: "declared-only"}}
	err := client.CallWithIdempotency(context.Background(), core.OperationID("operation"), "v1.deployment.apply", map[string]string{"x": "y"}, nil)
	if err == nil || !strings.Contains(err.Error(), "verified plugin identity") {
		// The exact error is intentionally not exported; what matters is that no
		// journal claim or request can occur without a verified digest.
		t.Fatalf("unverified mutation error = %v", err)
	}
}

func TestMutationScopeIncludesVerifiedPluginDigest(t *testing.T) {
	first, err := mutationScope("sha256:first", "v1.deployment.apply", map[string]string{"resource": "same"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := mutationScope("sha256:second", "v1.deployment.apply", map[string]string{"resource": "same"})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("different verified plugin content produced the same operation scope")
	}
}

func TestVerifiedMutationWithoutJournalFailsClosed(t *testing.T) {
	client := &Client{verifiedDigest: "sha256:verified"}
	err := client.CallWithIdempotency(context.Background(), "operation", "v1.deployment.apply", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "requires an operation journal") {
		t.Fatalf("error = %v", err)
	}
}

func TestOnlyLocalPreDispatchFailureIsTerminal(t *testing.T) {
	terminal, outcome := classifyMutationFailure(io.EOF)
	if terminal || !errors.Is(outcome, core.ErrOperationIndeterminate) || !errors.Is(outcome, io.EOF) {
		t.Fatalf("EOF classification = %v, %v; want indeterminate preserving EOF", terminal, outcome)
	}
	rpcFailure := error(&RPCError{Code: 404, Message: "method was not dispatched"})
	terminal, outcome = classifyMutationFailure(rpcFailure)
	if terminal || !errors.Is(outcome, core.ErrOperationIndeterminate) || !errors.Is(outcome, rpcFailure) {
		t.Fatalf("RPC 404 classification = %v, %v; want indeterminate", terminal, outcome)
	}
	terminal, outcome = classifyMutationFailure(&RPCError{Code: 500, Message: "handler failed after applying"})
	if terminal || !errors.Is(outcome, core.ErrOperationIndeterminate) {
		t.Fatalf("post-handler RPC classification = %v, %v; want indeterminate", terminal, outcome)
	}
	localFailure := context.Canceled
	terminal, outcome = classifyMutationFailure(&preDispatchError{err: localFailure})
	if !terminal || !errors.Is(outcome, localFailure) {
		t.Fatalf("local pre-dispatch classification = %v, %v; want terminal", terminal, outcome)
	}
}

func TestCallRejectsUnknownFutureMigrationMethodWithoutDispatch(t *testing.T) {
	var request bytes.Buffer
	client := &Client{stdin: nopWriteCloser{Writer: &request}}
	err := client.Call(context.Background(), "v1.migration.relocate", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "CallWithIdempotency is required") {
		t.Fatalf("Call error = %v", err)
	}
	if request.Len() != 0 {
		t.Fatal("unsafe method was dispatched without OperationID")
	}
}

func TestFutureMigrationMethodDefaultsToMutation(t *testing.T) {
	client := &Client{hello: HelloResult{Name: "declared-only"}}
	err := client.CallWithIdempotency(context.Background(), "", "v1.migration.relocate", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "requires a valid operation ID") {
		t.Fatalf("future mutation error = %v", err)
	}
}

func TestEOFPostMutationRequestIsIndeterminate(t *testing.T) {
	var request bytes.Buffer
	client := &Client{
		stdin:  nopWriteCloser{Writer: &request},
		reader: bufio.NewReader(bytes.NewReader(nil)),
	}
	err := client.callWithOperationID(context.Background(), "v1.deployment.apply", map[string]string{"resource": "x"}, nil, "op-eof")
	if !errors.Is(err, io.EOF) {
		t.Fatalf("call error = %v, want EOF after request write", err)
	}
	terminal, outcome := classifyMutationFailure(err)
	if terminal || !errors.Is(outcome, core.ErrOperationIndeterminate) {
		t.Fatalf("classification = %v, %v; want indeterminate", terminal, outcome)
	}
	if request.Len() == 0 {
		t.Fatal("mutation request was not written before EOF")
	}
}

func TestCallWithIdempotencyLifecycle(t *testing.T) {
	journal := useTestJournal(t)
	const digest = "sha256:verified"
	params := map[string]string{"resource": "workload"}

	t.Run("completion is durable and duplicate with no result is safe", func(t *testing.T) {
		var request bytes.Buffer
		client := &Client{
			stdin:          nopWriteCloser{Writer: &request},
			reader:         encodedResponse(t, Response{ID: "1", OperationID: "complete", Result: jsonRaw(`{"state":"ready"}`)}),
			verifiedDigest: digest,
			journal:        journal,
		}
		var result map[string]string
		if err := client.CallWithIdempotency(context.Background(), "complete", "v1.deployment.apply", params, &result); err != nil {
			t.Fatal(err)
		}
		if result["state"] != "ready" {
			t.Fatalf("result = %#v", result)
		}
		record, ok := journal.Lookup("complete")
		if !ok || record.Status != core.OperationCompleted {
			t.Fatalf("record = %#v, %v", record, ok)
		}
		before := request.Len()
		if err := client.CallWithIdempotency(context.Background(), "complete", "v1.deployment.apply", params, nil); err != nil {
			t.Fatalf("duplicate completion: %v", err)
		}
		if request.Len() != before {
			t.Fatal("duplicate completion was dispatched")
		}
		if err := client.CallWithIdempotency(context.Background(), "complete", "v1.deployment.apply", params, &result); err == nil || !strings.Contains(err.Error(), "without a replayable result") {
			t.Fatalf("duplicate result error = %v", err)
		}
	})

	t.Run("same id with changed mutation is rejected", func(t *testing.T) {
		client := &Client{verifiedDigest: digest, journal: journal}
		err := client.CallWithIdempotency(context.Background(), "complete", "v1.deployment.delete", params, nil)
		if err == nil || !strings.Contains(err.Error(), "collides") {
			t.Fatalf("collision error = %v", err)
		}
	})

	t.Run("pre-dispatch cancellation becomes terminal failure", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		client := &Client{verifiedDigest: digest, journal: journal}
		err := client.CallWithIdempotency(ctx, "cancelled", "v1.deployment.apply", params, nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
		record, ok := journal.Lookup("cancelled")
		if !ok || record.Status != core.OperationFailed {
			t.Fatalf("record = %#v, %v", record, ok)
		}
		err = client.CallWithIdempotency(context.Background(), "cancelled", "v1.deployment.apply", params, nil)
		if err == nil || !strings.Contains(err.Error(), "previously failed") {
			t.Fatalf("duplicate failure = %v", err)
		}
	})

	t.Run("post-dispatch protocol fault stays started", func(t *testing.T) {
		client := &Client{
			stdin:          nopWriteCloser{Writer: io.Discard},
			reader:         encodedResponse(t, Response{ID: "wrong", OperationID: "indeterminate"}),
			verifiedDigest: digest,
			journal:        journal,
		}
		err := client.CallWithIdempotency(context.Background(), "indeterminate", "v1.deployment.apply", params, nil)
		if !errors.Is(err, core.ErrOperationIndeterminate) {
			t.Fatalf("error = %v", err)
		}
		record, ok := journal.Lookup("indeterminate")
		if !ok || record.Status != core.OperationStarted {
			t.Fatalf("record = %#v, %v", record, ok)
		}
		err = client.CallWithIdempotency(context.Background(), "indeterminate", "v1.deployment.apply", params, nil)
		if !errors.Is(err, core.ErrOperationIndeterminate) {
			t.Fatalf("duplicate started error = %v", err)
		}
	})

	t.Run("durable failed record without raw cause remains terminal", func(t *testing.T) {
		scope, err := mutationScope(digest, "v1.deployment.apply", params)
		if err != nil {
			t.Fatal(err)
		}
		if started, startErr := journal.Start("sanitized-failure", scope); startErr != nil || !started {
			t.Fatalf("Start = %v, %v", started, startErr)
		}
		if failErr := journal.Fail("sanitized-failure"); failErr != nil {
			t.Fatal(failErr)
		}
		client := &Client{verifiedDigest: digest, journal: journal}
		err = client.CallWithIdempotency(context.Background(), "sanitized-failure", "v1.deployment.apply", params, nil)
		if err == nil || !strings.Contains(err.Error(), "previously failed") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestCallWithIdempotencyReadOnlyAndScopeEncoding(t *testing.T) {
	client := &Client{
		stdin:  nopWriteCloser{Writer: io.Discard},
		reader: encodedResponse(t, Response{ID: "1", Result: jsonRaw(`{"observed":true}`)}),
	}
	var result map[string]bool
	if err := client.CallWithIdempotency(context.Background(), "", "v1.deployment.observe", nil, &result); err != nil {
		t.Fatal(err)
	}
	if !result["observed"] {
		t.Fatalf("result = %#v", result)
	}
	if _, err := mutationScope("", "method", nil); err == nil {
		t.Fatal("empty verified digest accepted")
	}
	if _, err := mutationScope("sha256:verified", "method", make(chan int)); err == nil || !strings.Contains(err.Error(), "encode operation scope") {
		t.Fatalf("scope encoding error = %v", err)
	}
}

func TestCallWithOperationIDRejectsMalformedResponses(t *testing.T) {
	tests := []struct {
		name     string
		response Response
		result   any
		want     string
	}{
		{name: "response id", response: Response{ID: "2", OperationID: "op"}, want: "does not match request id"},
		{name: "operation id", response: Response{ID: "1", OperationID: "other"}, want: "does not match request operation_id"},
		{name: "rpc error", response: Response{ID: "1", OperationID: "op", Error: &RPCError{Code: 500, Message: "rejected"}}, want: "rejected"},
		{name: "invalid result shape", response: Response{ID: "1", OperationID: "op", Result: jsonRaw(`42`)}, result: &map[string]string{}, want: "decode result"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &Client{stdin: nopWriteCloser{Writer: io.Discard}, reader: encodedResponse(t, tt.response)}
			err := client.callWithOperationID(context.Background(), "v1.deployment.apply", nil, tt.result, "op")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
	client := &Client{stdin: nopWriteCloser{Writer: io.Discard}}
	if err := client.callWithOperationID(context.Background(), "method", make(chan int), nil, "op"); err == nil || !strings.Contains(err.Error(), "encode params") {
		t.Fatalf("encode error = %v", err)
	}
}

func TestClientRejectsMismatchedTraceIDEcho(t *testing.T) {
	client := &Client{stdin: nopWriteCloser{Writer: io.Discard}, reader: encodedResponse(t, Response{ID: "1", TraceID: "wrong"})}
	ctx := observability.ContextWithTraceID(context.Background(), "expected")
	if err := client.Call(ctx, "v1.migration.observe", nil, nil); err == nil || !strings.Contains(err.Error(), "trace_id") {
		t.Fatalf("err=%v", err)
	}
}

func TestReadOnlyCallRejectsLocalAndUntrustedProtocolFailures(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (&Client{}).Call(cancelled, "v1.deployment.observe", nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Call = %v", err)
	}
	if err := (&Client{}).Call(context.Background(), "v1.deployment.observe", make(chan int), nil); err == nil || !strings.Contains(err.Error(), "encode params") {
		t.Fatalf("encoding Call = %v", err)
	}

	tests := []struct {
		name     string
		reader   *bufio.Reader
		response Response
		result   any
		want     string
	}{
		{name: "eof", reader: bufio.NewReader(bytes.NewReader(nil)), want: "read response"},
		{name: "response id", response: Response{ID: "2"}, want: "does not match request id"},
		{name: "rpc error", response: Response{ID: "1", Error: &RPCError{Code: 403, Message: "denied"}}, want: "denied"},
		{name: "invalid result shape", response: Response{ID: "1", Result: jsonRaw(`42`)}, result: &map[string]string{}, want: "decode result"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := tt.reader
			if reader == nil {
				reader = encodedResponse(t, tt.response)
			}
			client := &Client{stdin: nopWriteCloser{Writer: io.Discard}, reader: reader}
			err := client.Call(context.Background(), "v1.deployment.observe", nil, tt.result)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}

	client := &Client{stdin: nopWriteCloser{Writer: failingWriter{}}, reader: bufio.NewReader(bytes.NewReader(nil))}
	if err := client.Call(context.Background(), "v1.deployment.observe", nil, nil); err == nil || !strings.Contains(err.Error(), "write request") {
		t.Fatalf("write error = %v", err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("forced write failure") }

func jsonRaw(value string) []byte { return []byte(value) }

func TestReadOnlyMethodClassificationRejectsVerbSmuggling(t *testing.T) {
	for _, method := range []string{"v1.observe.apply", "v1.plan.delete", "v1.discover.update"} {
		if isReadOnlyMethod(method) {
			t.Errorf("%s bypassed mutation journal", method)
		}
	}
	if !isReadOnlyMethod("v1.deployment.observe") {
		t.Fatal("canonical deployment observation was not read-only")
	}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
