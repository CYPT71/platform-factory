package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/CYPT71/platform-factory/internal/extensions"
	sdk "github.com/CYPT71/platform-factory/sdk/plugin"
	"io"
)

// NewServer shares the native framed RPC/handshake contract used by other
// signed plugins; no arbitrary commands, network or host paths are accepted.
func newServer(name string) (*sdk.Server, error) {
	if _, err := extensions.Lookup(name); err != nil {
		return nil, err
	}
	s := sdk.NewServer(name, "1.0.0")
	s.Handle("analyzer.scan", func(ctx context.Context, raw json.RawMessage) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(raw) > extensions.MaxInputBytes {
			return nil, errors.New("extension request exceeds 1 MiB")
		}
		var input struct {
			Document json.RawMessage `json:"document"`
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&input); err != nil {
			return nil, errors.New("expected document field")
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			return nil, errors.New("expected one request")
		}
		document, err := extensions.DecodeDocument(bytes.NewReader(input.Document))
		if err != nil {
			return nil, err
		}
		return extensions.Analyze(name, document)
	})
	return s, nil
}
