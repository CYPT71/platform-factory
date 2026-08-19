package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/CYPT71/platform-factory/internal/mcp/core"
	"github.com/CYPT71/platform-factory/internal/mcp/plugins"
	"github.com/CYPT71/platform-factory/internal/mcp/toolerror"
)

// maxIterations bounds every orchestration loop in this package: the
// model gets this many attempts to produce a change that passes
// validation before the tool gives up with a clear error. This is
// deliberately small - a bounded, inspectable loop, not an open-ended
// autonomous agent.
const maxIterations = 3

// fileEdit is one file the model asked to write. The model is prompted
// to reply with exactly this JSON shape (a bare array of edits); parsing
// enforces it strictly.
type fileEdit struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// parseEdits extracts a JSON array of fileEdit from the model's reply,
// tolerating a ```json ... ``` fence around it (the most common way a
// chat model wraps structured output) but nothing more permissive than
// that - a reply that isn't recognizably one JSON array is treated as a
// hard failure, not guessed at.
func parseEdits(reply string) ([]fileEdit, error) {
	text := strings.TrimSpace(reply)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	var edits []fileEdit
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&edits); err != nil {
		return nil, fmt.Errorf("model reply was not a JSON array of {path, content} edits: %w", err)
	}
	return edits, nil
}

// unavailableError is returned by every tool in this package when no
// Anthropic API key is configured - a structured, actionable message
// naming the client-orchestrated primitives to use instead, never a
// silent no-op.
func unavailableError(primitives ...string) error {
	return toolerror.New(toolerror.ErrAgentUnavailable, "%s is not set; drive this change yourself with: %s",
		apiKeyEnv, strings.Join(primitives, ", "))
}

// ModifyPluginRequest is the pf_plugin_modify input.
type ModifyPluginRequest struct {
	Plugin  string `json:"plugin"`
	Request string `json:"request"`
}

// ModifyResult is the pf_plugin_modify / pf_core_patch(request mode)
// output: what actually happened, not just "done".
type ModifyResult struct {
	Iterations  int      `json:"iterations"`
	Success     bool     `json:"success"`
	FilesEdited []string `json:"files_edited"`
	Validation  any      `json:"validation"`
	Issues      []string `json:"issues,omitempty"`
}

const pluginModifySystemPrompt = `You are modifying one existing plugin inside the platform-factory ` +
	`repository. You will be given the plugin's current manifest, module kind, and file list, then a ` +
	`natural-language request describing the change to make. Reply with ONLY a JSON array of edits, each ` +
	`{"path": "<repo-relative path under the plugin's own directory>", "content": "<full new file content>"}. ` +
	`Every path MUST start with the plugin's own plugins/<name>/ prefix. Do not include explanation text, ` +
	`only the JSON array. If plugin.json needs a version bump, include it with an appropriately incremented ` +
	`SemVer patch or minor version per the nature of the change.`

// ModifyPlugin drives a bounded plan -> edit -> validate -> retry loop
// for one existing plugin, using the same in-process
// internal/mcp/plugins functions the client-orchestrated tools use (no
// self-referential MCP round-trip). Every edit is confined to
// plugins/<name>/ - a path outside that prefix is rejected without
// being written.
func ModifyPlugin(ctx context.Context, client *Client, repoRoot string, req ModifyPluginRequest) (ModifyResult, error) {
	if strings.TrimSpace(req.Request) == "" {
		return ModifyResult{}, toolerror.New(toolerror.ErrInvalidArgument, "request must not be empty")
	}
	detail, err := plugins.InspectPlugin(repoRoot, req.Plugin)
	if err != nil {
		return ModifyResult{}, err
	}
	detailJSON, err := json.MarshalIndent(detail, "", "  ")
	if err != nil {
		return ModifyResult{}, err
	}
	allowedPrefix := detail.Path + "/"

	result := ModifyResult{}
	feedback := ""
	for iteration := 1; iteration <= maxIterations; iteration++ {
		result.Iterations = iteration
		user := fmt.Sprintf("Current plugin state:\n%s\n\nRequest: %s", detailJSON, req.Request)
		if feedback != "" {
			user += "\n\nThe previous attempt failed validation:\n" + feedback
		}
		reply, err := client.Complete(ctx, pluginModifySystemPrompt, user)
		if err != nil {
			return result, err
		}
		edits, err := parseEdits(reply)
		if err != nil {
			feedback = err.Error()
			continue
		}

		var edited []string
		var rejectionErr error
		for _, edit := range edits {
			if !strings.HasPrefix(edit.Path, allowedPrefix) {
				rejectionErr = fmt.Errorf("refusing edit to %q: outside %s", edit.Path, allowedPrefix)
				break
			}
			if _, err := core.WriteFile(repoRoot, edit.Path, edit.Content); err != nil {
				rejectionErr = fmt.Errorf("write %q: %w", edit.Path, err)
				break
			}
			edited = append(edited, edit.Path)
		}
		if rejectionErr != nil {
			feedback = rejectionErr.Error()
			continue
		}
		result.FilesEdited = edited

		report, err := plugins.Validate(ctx, repoRoot, req.Plugin)
		if err != nil {
			return result, err
		}
		result.Validation = report
		if report.Valid {
			result.Success = true
			return result, nil
		}
		result.Issues = report.Issues
		feedback = strings.Join(report.Issues, "; ")
	}
	return result, toolerror.New(toolerror.ErrValidationFailed, "did not reach a valid state in %d iterations; last issues: %s", maxIterations, strings.Join(result.Issues, "; "))
}

type modifyPluginArguments struct {
	Plugin  string `json:"plugin"`
	Request string `json:"request"`
}

// ModifyPluginToolHandler returns the pf_plugin_modify handler. It
// checks agent availability itself so the tool degrades to a clear
// structured error, per this package's doc comment, rather than the
// caller needing to check first.
func ModifyPluginToolHandler(repoRoot string) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, arguments json.RawMessage) (string, error) {
		client, ok := FromEnv()
		if !ok {
			return "", unavailableError("pf_plugin_inspect", "pf_core_write_file", "pf_plugin_validate")
		}
		var args modifyPluginArguments
		if err := json.Unmarshal(arguments, &args); err != nil {
			return "", toolerror.New(toolerror.ErrInvalidArgument, "invalid arguments: %v", err)
		}
		result, err := ModifyPlugin(ctx, client, repoRoot, ModifyPluginRequest{Plugin: args.Plugin, Request: args.Request})
		if err != nil {
			return "", err
		}
		encoded, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}
}

// PatchCoreRequest is the pf_core_patch input: a free-text request
// explicitly scoped to a caller-supplied allow-list, per this
// repository's plan for the agent tier ("changes confined to files
// under plugins/<one plugin>/ or a small explicit allow-list for core
// patches - not an open-ended, unbounded loop").
type PatchCoreRequest struct {
	Request      string   `json:"request"`
	Reason       string   `json:"reason"`
	AllowedPaths []string `json:"allowed_paths"`
}

const corePatchSystemPrompt = `You are proposing a scoped change to the platform-factory repository's ` +
	`core (internal/ or cmd/ packages). You will be given the current content of every file you are allowed ` +
	`to touch, a request, and the reason a core change (rather than a plugin) is needed. Reply with ONLY a ` +
	`JSON array of edits, each {"path": "<repo-relative path>", "content": "<full new file content>"}. Every ` +
	`path MUST be one of the allowed paths given to you - you may not introduce a new file outside that list. ` +
	`Preserve public API compatibility unless the request explicitly asks to change it. Update or add tests ` +
	`for the behavior you change. Do not include explanation text, only the JSON array.`

// PatchCore drives the same bounded plan -> edit -> validate -> retry
// loop as ModifyPlugin, confined to req.AllowedPaths, and finishes with
// SelfCheck (internal/archtest) rather than a full test suite run - the
// same pre-PR gate pf_core_self_check exposes directly.
func PatchCore(ctx context.Context, client *Client, repoRoot string, req PatchCoreRequest) (ModifyResult, error) {
	if strings.TrimSpace(req.Request) == "" {
		return ModifyResult{}, toolerror.New(toolerror.ErrInvalidArgument, "request must not be empty")
	}
	if len(req.AllowedPaths) == 0 {
		return ModifyResult{}, toolerror.New(toolerror.ErrInvalidArgument, "allowed_paths must name at least one file this patch may touch")
	}
	allowed := make(map[string]bool, len(req.AllowedPaths))
	for _, p := range req.AllowedPaths {
		allowed[p] = true
	}

	var currentState strings.Builder
	for _, path := range req.AllowedPaths {
		read, err := core.ReadFile(repoRoot, path)
		if err != nil {
			fmt.Fprintf(&currentState, "%s: (does not exist yet)\n\n", path)
			continue
		}
		fmt.Fprintf(&currentState, "%s:\n%s\n\n", path, read.Content)
	}

	result := ModifyResult{}
	feedback := ""
	for iteration := 1; iteration <= maxIterations; iteration++ {
		result.Iterations = iteration
		user := fmt.Sprintf("Allowed paths: %s\n\nCurrent content:\n%s\nRequest: %s\nReason: %s",
			strings.Join(req.AllowedPaths, ", "), currentState.String(), req.Request, req.Reason)
		if feedback != "" {
			user += "\n\nThe previous attempt failed:\n" + feedback
		}
		reply, err := client.Complete(ctx, corePatchSystemPrompt, user)
		if err != nil {
			return result, err
		}
		edits, err := parseEdits(reply)
		if err != nil {
			feedback = err.Error()
			continue
		}

		var edited []string
		var rejectionErr error
		for _, edit := range edits {
			if !allowed[edit.Path] {
				rejectionErr = fmt.Errorf("refusing edit to %q: not in allowed_paths", edit.Path)
				break
			}
			if _, err := core.WriteFile(repoRoot, edit.Path, edit.Content); err != nil {
				rejectionErr = fmt.Errorf("write %q: %w", edit.Path, err)
				break
			}
			edited = append(edited, edit.Path)
		}
		if rejectionErr != nil {
			feedback = rejectionErr.Error()
			continue
		}
		result.FilesEdited = edited

		step := core.SelfCheck(ctx, repoRoot)
		result.Validation = step
		if step.Status == "ok" {
			result.Success = true
			return result, nil
		}
		result.Issues = []string{step.Output}
		feedback = step.Output
	}
	return result, toolerror.New(toolerror.ErrValidationFailed, "did not reach a valid state in %d iterations; last issues: %s", maxIterations, strings.Join(result.Issues, "; "))
}

type patchCoreArguments struct {
	Request      string   `json:"request"`
	Reason       string   `json:"reason"`
	AllowedPaths []string `json:"allowed_paths"`
}

// PatchCoreToolHandler returns the pf_core_patch handler (the free-text-
// request, server-embedded-agent mode - the client-orchestrated
// primitives are pf_core_read_file/pf_core_write_file/pf_core_self_check).
func PatchCoreToolHandler(repoRoot string) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, arguments json.RawMessage) (string, error) {
		client, ok := FromEnv()
		if !ok {
			return "", unavailableError("pf_core_read_file", "pf_core_write_file", "pf_core_self_check")
		}
		var args patchCoreArguments
		if err := json.Unmarshal(arguments, &args); err != nil {
			return "", toolerror.New(toolerror.ErrInvalidArgument, "invalid arguments: %v", err)
		}
		result, err := PatchCore(ctx, client, repoRoot, PatchCoreRequest{
			Request: args.Request, Reason: args.Reason, AllowedPaths: args.AllowedPaths,
		})
		if err != nil {
			return "", err
		}
		encoded, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}
}
