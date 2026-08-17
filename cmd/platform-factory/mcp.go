package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/CYPT71/platform-factory/internal/mcp"
)

func runMCP(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		printMCPUsage(stderr)
		return 2
	}
	switch args[0] {
	case "-h", "--help", "help":
		printMCPUsage(stdout)
		return 0
	case "serve":
		return runMCPServe(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "platform-factory mcp: unknown subcommand %q\n\n", args[0])
		printMCPUsage(stderr)
		return 2
	}
}

func runMCPServe(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("mcp serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	repo := flags.String("repo", "", "platform-factory repository root (defaults to the current directory)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: platform-factory mcp serve [--repo DIR]")
		return 2
	}

	repoRoot := *repo
	if repoRoot == "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory mcp serve: %v\n", err)
			return 1
		}
		repoRoot = cwd
	}
	repoRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory mcp serve: %v\n", err)
		return 1
	}
	if info, statErr := os.Stat(filepath.Join(repoRoot, "go.mod")); statErr != nil || info.IsDir() {
		fmt.Fprintf(stderr, "platform-factory mcp serve: %s does not look like a Go module root (no go.mod)\n", repoRoot)
		return 2
	}

	server := mcp.NewPlatformFactoryServer(repoRoot, version)
	// stdout carries only MCP protocol messages; every diagnostic goes to
	// stderr, per the transport's own requirement.
	fmt.Fprintf(stderr, "pf-mcp: serving stdio for repository %s\n", repoRoot)
	if err := server.Serve(context.Background(), os.Stdin, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "platform-factory mcp serve: %v\n", err)
		return 1
	}
	return 0
}

func printMCPUsage(output io.Writer) {
	fmt.Fprintln(output, `platform-factory mcp — Model Context Protocol server for this repository

Usage:
  platform-factory mcp serve [--repo DIR]

Runs a stdio MCP server (JSON-RPC 2.0, newline-delimited) that lets an
MCP-speaking client inspect this platform-factory checkout, work with
its plugin system, and propose core changes as a draft pull request for
human review. See docs/mcp.md for the full tool and resource list.`)
}
