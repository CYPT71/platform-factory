package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/CYPT71/platform-factory/internal/mcp/git"
	"github.com/CYPT71/platform-factory/internal/mcp/plugins"
)

const classifySystemPrompt = `You are the planning step of platform-factory's pf_implement tool. Given a ` +
	`feature request, decide whether it can be implemented entirely as a plugin (plugins/<name>/), entirely ` +
	`as a core change (internal/ or cmd/), or needs both - a new/modified plugin AND a core change (e.g. a new ` +
	`extension point the plugin needs that the core does not expose yet). Prefer the plugin-only path whenever ` +
	`the existing plugin protocol already supports what is being asked; only choose a core change when a new ` +
	`public interface, capability, or protocol extension is genuinely required.

Reply with ONLY this JSON object (no explanation text):
{
  "strategy": "plugin_only" | "core_only" | "core_and_plugin",
  "reasoning": "<one or two sentences>",
  "explanation": {
    "root_cause": "<if this request describes a bug or defect, the underlying root cause; omit or leave empty for a plain feature request>",
    "fix": "<if this request describes a bug or defect, a clear description of what the fix does and why it resolves the root cause; omit or leave empty for a plain feature request>"
  },
  "plugin": {
    "name": "<plugin directory name>",
    "action": "create" | "modify",
    "description": "<used only when action is create>",
    "capabilities": ["<used only when action is create>"],
    "request": "<the change to make - used for both create and modify, phrased as an instruction>"
  },
  "core": {
    "reason": "<why a core change is required>",
    "allowed_paths": ["<repo-relative paths this change may touch, existing or new>"],
    "request": "<the change to make, phrased as an instruction>"
  }
}
Omit "plugin" entirely when strategy is "core_only". Omit "core" entirely when strategy is "plugin_only". Omit "explanation" entirely (or leave both its fields empty) when the request is not describing a bug.`

// Explanation is a bug-report-shaped elaboration of a Plan: the root
// cause the request traces to and what the fix actually does about it -
// this is what lets a PR opened from a bug report explain itself
// instead of just listing changed files.
type Explanation struct {
	RootCause string `json:"root_cause"`
	Fix       string `json:"fix"`
}

// Plan is the pf_implement classification output.
type Plan struct {
	Strategy    string       `json:"strategy"`
	Reasoning   string       `json:"reasoning"`
	Explanation *Explanation `json:"explanation,omitempty"`
	Plugin      *PluginStep  `json:"plugin"`
	Core        *CoreStep    `json:"core"`
}

// PluginStep is the plugin half of a Plan: either "create" a brand-new
// plugin (Description/Capabilities used) or "modify" an existing one
// (Request used either way, phrased as an instruction).
type PluginStep struct {
	Name         string   `json:"name"`
	Action       string   `json:"action"`
	Description  string   `json:"description"`
	Capabilities []string `json:"capabilities"`
	Request      string   `json:"request"`
}

// CoreStep is the core half of a Plan.
type CoreStep struct {
	Reason       string   `json:"reason"`
	AllowedPaths []string `json:"allowed_paths"`
	Request      string   `json:"request"`
}

func classify(ctx context.Context, client *Client, request string) (Plan, error) {
	reply, err := client.Complete(ctx, classifySystemPrompt, request)
	if err != nil {
		return Plan{}, err
	}
	text := strings.TrimSpace(reply)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	var plan Plan
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &plan); err != nil {
		return Plan{}, fmt.Errorf("model reply was not a valid plan JSON object: %w", err)
	}
	switch plan.Strategy {
	case "plugin_only", "core_only", "core_and_plugin":
	default:
		return Plan{}, fmt.Errorf("model returned an unrecognized strategy %q", plan.Strategy)
	}
	return plan, nil
}

// ImplementResult is the pf_implement payload.
type ImplementResult struct {
	Strategy    string        `json:"strategy"`
	Reasoning   string        `json:"reasoning"`
	Plugin      *ModifyResult `json:"plugin,omitempty"`
	Core        *ModifyResult `json:"core,omitempty"`
	PullRequest *string       `json:"pull_request,omitempty"`
}

// Implement runs the full classify -> branch -> plugin and/or core ->
// (on success) commit+push+draft-PR workflow. It never touches main
// directly and never merges - see internal/mcp/git's own protected-
// branch and merge-tool-absence guarantees, which this function relies
// on rather than re-implementing.
//
// The branch is created FIRST, before any edit - PrepareBranch requires
// a clean working tree (so the eventual diff is exactly, and only, this
// change), and ModifyPlugin/PatchCore write files directly into
// repoRoot as they iterate. Branching after editing, as an earlier
// version of this function did, meant the tree was already dirty by
// the time PrepareBranch ran and every automatic PR attempt failed.
func Implement(ctx context.Context, client *Client, repoRoot, request string) (ImplementResult, error) {
	plan, err := classify(ctx, client, request)
	if err != nil {
		return ImplementResult{}, err
	}
	result := ImplementResult{Strategy: plan.Strategy, Reasoning: plan.Reasoning}

	branchName := "mcp/feat/" + slugify(plan.Plugin, request)
	if plan.Plugin != nil || plan.Core != nil {
		if _, err := git.New(repoRoot).PrepareBranch(ctx, branchName); err != nil {
			return result, fmt.Errorf("prepare branch: %w", err)
		}
	}

	if plan.Plugin != nil {
		if plan.Plugin.Action == "create" {
			created, err := plugins.Create(ctx, repoRoot, plugins.CreateRequest{
				Name: plan.Plugin.Name, Description: plan.Plugin.Description, Capabilities: plan.Plugin.Capabilities,
			})
			if err != nil {
				return result, fmt.Errorf("create plugin %q: %w", plan.Plugin.Name, err)
			}
			pluginResult := &ModifyResult{Success: true, FilesEdited: created.Files}
			if plan.Plugin.Request != "" {
				modified, err := ModifyPlugin(ctx, client, repoRoot, ModifyPluginRequest{
					Plugin: plan.Plugin.Name, Request: plan.Plugin.Request,
				})
				if err != nil {
					return result, fmt.Errorf("implement plugin %q: %w", plan.Plugin.Name, err)
				}
				pluginResult = &modified
				pluginResult.FilesEdited = append(created.Files, modified.FilesEdited...)
			}
			result.Plugin = pluginResult
		} else {
			modified, err := ModifyPlugin(ctx, client, repoRoot, ModifyPluginRequest{
				Plugin: plan.Plugin.Name, Request: plan.Plugin.Request,
			})
			if err != nil {
				return result, fmt.Errorf("modify plugin %q: %w", plan.Plugin.Name, err)
			}
			result.Plugin = &modified
		}
	}

	if plan.Core != nil {
		patched, err := PatchCore(ctx, client, repoRoot, PatchCoreRequest{
			Request: plan.Core.Request, Reason: plan.Core.Reason, AllowedPaths: plan.Core.AllowedPaths,
		})
		if err != nil {
			return result, fmt.Errorf("patch core: %w", err)
		}
		result.Core = &patched
	}

	// A successful plugin-only or core-only change is proposed as a PR
	// exactly the same way; core_and_plugin gets one PR covering both
	// file sets rather than two, since it is one logical request.
	var filesEdited []string
	if result.Plugin != nil && result.Plugin.Success {
		filesEdited = append(filesEdited, result.Plugin.FilesEdited...)
	}
	if result.Core != nil && result.Core.Success {
		filesEdited = append(filesEdited, result.Core.FilesEdited...)
	}
	if len(filesEdited) > 0 {
		url, err := proposeChange(ctx, repoRoot, branchName, request, plan, result, filesEdited)
		if err != nil {
			return result, fmt.Errorf("change validated but commit/push/PR failed: %w", err)
		}
		result.PullRequest = &url
	}

	return result, nil
}

// proposeChange commits exactly the given files on the already-checked-
// out branchName, pushes, and opens a draft PR - the same primitives
// pf_git_commit/pf_core_create_pr expose, called in-process here so
// pf_implement is one tool call end-to-end instead of requiring a
// client to drive them separately. The PR targets git.TargetRepoEnv
// when set, so a caller can pin every PR this server opens to one
// specific GitHub repository (e.g. the upstream platform-factory
// project) regardless of what "origin" resolves to for repoRoot.
func proposeChange(ctx context.Context, repoRoot, branchName, request string, plan Plan, result ImplementResult, filesEdited []string) (string, error) {
	repo := git.New(repoRoot)
	message := fmt.Sprintf("mcp: %s", request)
	if err := repo.Commit(ctx, filesEdited, message); err != nil {
		return "", err
	}
	if err := repo.Push(ctx, "origin"); err != nil {
		return "", err
	}
	return repo.CreatePR(ctx, git.PullRequest{
		Head:  branchName,
		Title: "mcp: " + request,
		Body:  buildPRBody(request, plan, result),
		Repo:  git.TargetRepoFromEnv(),
	})
}

// buildPRBody produces a comprehensive PR description: what was asked
// for, why, the root cause and fix when the request was a bug report,
// every file touched broken out by plugin/core, and what validated the
// change - so a reviewer (human or the upstream project's own CI) gets
// the full "bug and fix" explanation in the PR itself, not just a file
// list.
func buildPRBody(request string, plan Plan, result ImplementResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Summary\n%s\n\n", request)
	if plan.Reasoning != "" {
		fmt.Fprintf(&b, "## Reasoning\n%s\n\n", plan.Reasoning)
	}
	if plan.Explanation != nil && (plan.Explanation.RootCause != "" || plan.Explanation.Fix != "") {
		b.WriteString("## Bug: Root Cause\n")
		if plan.Explanation.RootCause != "" {
			fmt.Fprintf(&b, "%s\n\n", plan.Explanation.RootCause)
		} else {
			b.WriteString("(not provided)\n\n")
		}
		b.WriteString("## Fix\n")
		if plan.Explanation.Fix != "" {
			fmt.Fprintf(&b, "%s\n\n", plan.Explanation.Fix)
		} else {
			b.WriteString("(not provided)\n\n")
		}
	}
	if plan.Core != nil && plan.Core.Reason != "" {
		fmt.Fprintf(&b, "## Why A Core Change Was Needed\n%s\n\n", plan.Core.Reason)
	}
	if result.Plugin != nil && len(result.Plugin.FilesEdited) > 0 {
		b.WriteString("## Plugin Changes\n")
		for _, f := range result.Plugin.FilesEdited {
			fmt.Fprintf(&b, "- %s\n", f)
		}
		b.WriteString("\n")
	}
	if result.Core != nil && len(result.Core.FilesEdited) > 0 {
		b.WriteString("## Core Changes\n")
		for _, f := range result.Core.FilesEdited {
			fmt.Fprintf(&b, "- %s\n", f)
		}
		b.WriteString("\n")
	}
	b.WriteString("## Testing\n")
	if result.Plugin != nil {
		fmt.Fprintf(&b, "- pf_plugin_validate: %d iteration(s), success=%t\n", result.Plugin.Iterations, result.Plugin.Success)
	}
	if result.Core != nil {
		fmt.Fprintf(&b, "- go test ./internal/archtest/... (self-check): %d iteration(s), success=%t\n", result.Core.Iterations, result.Core.Success)
	}
	b.WriteString("\n## Risk\n")
	if plan.Core != nil {
		fmt.Fprintf(&b, "Core changes were confined to: %s. ", strings.Join(plan.Core.AllowedPaths, ", "))
	}
	b.WriteString("This PR was opened as a draft by an automated tool; nothing here has been merged or pushed to main, and no merge tool exists in this server's registry - human review is required before it lands.\n\n")
	b.WriteString("## Generated by\npf_implement (platform-factory MCP server)\n")
	return b.String()
}

func slugify(pluginPart *PluginStep, request string) string {
	source := request
	if pluginPart != nil && pluginPart.Name != "" {
		source = pluginPart.Name
	}
	var b strings.Builder
	lastWasDash := false
	for _, r := range strings.ToLower(source) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			lastWasDash = false
		default:
			if !lastWasDash && b.Len() > 0 {
				b.WriteByte('-')
				lastWasDash = true
			}
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > 40 {
		slug = slug[:40]
	}
	if slug == "" {
		slug = "change"
	}
	return slug
}

type implementArguments struct {
	Request string `json:"request"`
}

// ImplementToolHandler returns the pf_implement handler.
func ImplementToolHandler(repoRoot string) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, arguments json.RawMessage) (string, error) {
		client, ok := FromEnv()
		if !ok {
			return "", unavailableError("pf_plugin_create", "pf_plugin_modify", "pf_core_patch", "pf_git_prepare_branch", "pf_git_commit", "pf_core_create_pr")
		}
		var args implementArguments
		if err := json.Unmarshal(arguments, &args); err != nil {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}
		result, err := Implement(ctx, client, repoRoot, args.Request)
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
