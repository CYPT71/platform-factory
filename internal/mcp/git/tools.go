package git

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/CYPT71/platform-factory/internal/mcp/toolerror"
)

// StatusToolHandler returns the pf_git_status handler.
func StatusToolHandler(repoDir string) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, arguments json.RawMessage) (string, error) {
		status, err := New(repoDir).Status(ctx)
		if err != nil {
			return "", err
		}
		encoded, err := json.MarshalIndent(status, "", "  ")
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}
}

type prepareBranchArguments struct {
	Name string `json:"name"`
}

// PrepareBranchToolHandler returns the pf_git_prepare_branch handler.
func PrepareBranchToolHandler(repoDir string) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, arguments json.RawMessage) (string, error) {
		var args prepareBranchArguments
		if err := json.Unmarshal(arguments, &args); err != nil {
			return "", toolerror.New(toolerror.ErrInvalidArgument, "invalid arguments: %v", err)
		}
		startedFrom, err := New(repoDir).PrepareBranch(ctx, args.Name)
		if err != nil {
			return "", err
		}
		encoded, err := json.MarshalIndent(map[string]string{
			"branch": args.Name, "started_from": startedFrom,
		}, "", "  ")
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}
}

type commitArguments struct {
	Paths   []string `json:"paths"`
	Message string   `json:"message"`
}

// CommitToolHandler returns the pf_git_commit handler.
func CommitToolHandler(repoDir string) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, arguments json.RawMessage) (string, error) {
		var args commitArguments
		if err := json.Unmarshal(arguments, &args); err != nil {
			return "", toolerror.New(toolerror.ErrInvalidArgument, "invalid arguments: %v", err)
		}
		if err := New(repoDir).Commit(ctx, args.Paths, args.Message); err != nil {
			return "", err
		}
		status, err := New(repoDir).Status(ctx)
		if err != nil {
			return "", err
		}
		encoded, err := json.MarshalIndent(map[string]any{
			"committed": true, "branch": status.Branch, "paths": args.Paths,
		}, "", "  ")
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}
}

type createPRArguments struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Base  string `json:"base"`
	Repo  string `json:"repo"`
}

// CreatePRToolHandler returns the pf_core_create_pr handler: it pushes
// the current branch to origin, then opens a draft PR against base (or
// "main" when empty), targeting repo when given or TargetRepoEnv when
// not. There is no corresponding merge tool anywhere in this server's
// registry.
func CreatePRToolHandler(repoDir string) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, arguments json.RawMessage) (string, error) {
		var args createPRArguments
		if err := json.Unmarshal(arguments, &args); err != nil {
			return "", toolerror.New(toolerror.ErrInvalidArgument, "invalid arguments: %v", err)
		}
		repo := New(repoDir)
		status, err := repo.Status(ctx)
		if err != nil {
			return "", err
		}
		if err := repo.Push(ctx, "origin"); err != nil {
			return "", err
		}
		targetRepo := strings.TrimSpace(args.Repo)
		if targetRepo == "" {
			targetRepo = TargetRepoFromEnv()
		}
		url, err := repo.CreatePR(ctx, PullRequest{
			Base: args.Base, Head: status.Branch, Title: args.Title, Body: args.Body, Repo: targetRepo,
		})
		if err != nil {
			return "", err
		}
		encoded, err := json.MarshalIndent(map[string]string{
			"url": url, "head": status.Branch,
		}, "", "  ")
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}
}
