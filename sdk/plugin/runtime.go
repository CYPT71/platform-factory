package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// LanguageExtension is the complete stable contract for a language plugin.
// Implementations classify a project, describe dependency freezing, and add
// advisory planning notes. The host remains responsible for executing steps.
type LanguageExtension interface {
	Detect(context.Context, DetectParams) (DetectResult, error)
	Freeze(context.Context, FreezeParams) (FreezeResult, error)
	Plan(context.Context, PlanParams) (PlanResult, error)
}

// Runtime is the reference Go implementation of the language-neutral v1 wire
// protocol. Extension authors only implement LanguageExtension.
type Runtime struct{ server *Server }

func NewRuntime(name, version string, extension LanguageExtension) (*Runtime, error) {
	if name == "" || version == "" || extension == nil {
		return nil, fmt.Errorf("plugin: runtime requires name, version, and extension")
	}
	server := NewServer(name, version)
	server.Handle(CapabilityDetect, typedHandler(extension.Detect))
	server.Handle(CapabilityFreeze, typedHandler(extension.Freeze))
	server.Handle(CapabilityPlan, typedHandler(extension.Plan))
	return &Runtime{server: server}, nil
}

func typedHandler[Params, Result any](handler func(context.Context, Params) (Result, error)) Handler {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var params Params
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&params); err != nil {
			return nil, fmt.Errorf("decode params: %w", err)
		}
		return handler(ctx, params)
	}
}

// RegisterTyped exposes strict native Go handlers for any capability family.
// Plugin authors keep ordinary structs and context.Context while the SDK owns
// JSON decoding, unknown-field rejection and protocol dispatch.
func RegisterTyped[Params, Result any](server *Server, capability string, handler func(context.Context, Params) (Result, error)) error {
	if server == nil || handler == nil {
		return fmt.Errorf("plugin: typed handler requires server and function")
	}
	if err := ValidateCapability(capability); err != nil {
		return err
	}
	server.Handle(capability, typedHandler(handler))
	return nil
}

// Serve runs the reference protocol over the supplied streams.
func (r *Runtime) Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	return r.server.Serve(ctx, input, output)
}
