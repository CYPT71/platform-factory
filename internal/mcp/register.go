package mcp

import (
	"encoding/json"

	"github.com/CYPT71/platform-factory/internal/mcp/agent"
	"github.com/CYPT71/platform-factory/internal/mcp/core"
	"github.com/CYPT71/platform-factory/internal/mcp/git"
	"github.com/CYPT71/platform-factory/internal/mcp/plugins"
	"github.com/CYPT71/platform-factory/internal/mcp/product"
	"github.com/CYPT71/platform-factory/internal/mcp/project"
)

// NewPlatformFactoryServer builds the real platform-factory MCP server:
// every tool and resource this package's subpackages implement,
// registered against repoRoot/version. Subpackages (project, plugins,
// core, git, agent) never import this package back - they expose plain
// handler functions over stdlib types, and registration happens here,
// the one place allowed to depend on both the generic transport (this
// package) and the concrete tool implementations (the subpackages) -
// which is what keeps the dependency graph acyclic.
func NewPlatformFactoryServer(repoRoot, version string) *Server {
	s := NewServer("platform-factory", version)

	s.AddTool(Tool{
		Name: "pf_project_inspect",
		Description: "Summarize this platform-factory checkout: module, version, current git " +
			"branch and dirty state, its own validation commands, and (at detailed depth) its " +
			"top-level cmd/internal/sdk/plugins components.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"depth": {"type": "string", "enum": ["summary", "detailed"], "description": "summary (default) omits the component list; detailed includes it"}
			}
		}`),
		Handler: project.InspectToolHandler(repoRoot, version),
	})

	s.AddResource(Resource{
		URI:         "pf://project",
		Name:        "project",
		Description: "Detailed project snapshot: module, version, git branch/dirty state, components, validation commands.",
		MimeType:    "application/json",
		Handler:     project.ProjectResourceHandler(repoRoot, version),
	})

	s.AddResource(Resource{
		URI:         "pf://architecture",
		Name:        "architecture",
		Description: "Every internal/ package's own doc comment plus the CLI's top-level commands, read live from the repository.",
		MimeType:    "application/json",
		Handler:     project.ArchitectureResourceHandler(repoRoot),
	})

	s.AddTool(Tool{
		Name:        "pf_plugin_list",
		Description: "List every plugins/<name> directory in this repository, classified as \"rpc\" (has a plugin.json manifest) or \"language-command\" (the sdk/langplugin single-script/binary convention).",
		Handler:     plugins.ListToolHandler(repoRoot),
	})
	s.AddTool(Tool{
		Name:        "pf_plugin_inspect",
		Description: "Inspect one plugins/<name> directory: its manifest (if any), which go module convention it uses, and its test files.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"required": ["plugin"],
			"properties": {"plugin": {"type": "string", "description": "the plugin's directory name under plugins/"}}
		}`),
		Handler: plugins.InspectToolHandler(repoRoot),
	})
	s.AddTool(Tool{
		Name: "pf_plugin_create",
		Description: "Scaffold a brand-new RPC plugin under plugins/<name>: a plugin.json manifest, README, go.mod " +
			"(using this repository's local-replace convention), and a cmd/platform-factory-<name>/main.go that starts " +
			"a real sdk/plugin.Server with one handler stub per requested capability. Refuses to overwrite an existing directory.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"required": ["name", "description", "capabilities"],
			"properties": {
				"name": {"type": "string", "pattern": "^[a-z][a-z0-9-]{0,62}$"},
				"description": {"type": "string"},
				"capabilities": {"type": "array", "items": {"type": "string"}, "minItems": 1},
				"family": {"type": "string", "enum": ["language", "analyzer", "build", "runtime", "deployment", "capability"]},
				"permissions": {
					"type": "object",
					"properties": {
						"network": {"type": "array", "items": {"type": "string"}},
						"filesystem": {"type": "array", "items": {"type": "string"}},
						"secrets": {"type": "array", "items": {"type": "string"}}
					}
				}
			}
		}`),
		Handler: plugins.CreateToolHandler(repoRoot),
	})
	s.AddTool(Tool{
		Name: "pf_plugin_validate",
		Description: "Validate one plugins/<name> directory: manifest schema, executable digest, a real `go build` of " +
			"its own module, language-family permission rules, and whether test files are present.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"required": ["plugin"],
			"properties": {"plugin": {"type": "string", "description": "the plugin's directory name under plugins/"}}
		}`),
		Handler: plugins.ValidateToolHandler(repoRoot),
	})

	s.AddResource(Resource{
		URI:         "pf://plugins",
		Name:        "plugins",
		Description: "Every plugins/<name> directory in this repository, with kind/family/capabilities where known.",
		MimeType:    "application/json",
		Handler:     plugins.PluginsResourceHandler(repoRoot),
	})
	s.AddResource(Resource{
		URI:         "pf://plugins/schema",
		Name:        "plugins-schema",
		Description: "The plugin.json manifest schema, as enforced by internal/plugin.Manifest.Validate().",
		MimeType:    "application/json",
		Handler:     plugins.SchemaResourceHandler(),
	})
	s.AddResource(Resource{
		URI:         "pf://marketplace",
		Name:        "marketplace",
		Description: "This machine's locally tracked marketplace sources and synced index (offline, no network call).",
		MimeType:    "application/json",
		Handler:     plugins.MarketplaceResourceHandler(),
	})

	s.AddTool(Tool{
		Name:        "pf_git_status",
		Description: "Report this repository's current branch and working-tree dirty state.",
		Handler:     git.StatusToolHandler(repoRoot),
	})
	s.AddTool(Tool{
		Name: "pf_git_prepare_branch",
		Description: "Create and check out a new branch from the current (clean) HEAD. Refuses a " +
			"protected (main/master) or unsafe name, an already-existing branch, or a dirty working tree. " +
			"Recommended naming: mcp/<type>/<slug>, e.g. mcp/feat/plugin-detector-api.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"required": ["name"],
			"properties": {"name": {"type": "string"}}
		}`),
		Handler: git.PrepareBranchToolHandler(repoRoot),
	})
	s.AddTool(Tool{
		Name: "pf_git_commit",
		Description: "Stage exactly the given repo-relative paths and commit them with message, on the " +
			"current branch. Refuses to commit directly to main/master.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"required": ["paths", "message"],
			"properties": {
				"paths": {"type": "array", "items": {"type": "string"}, "minItems": 1},
				"message": {"type": "string"}
			}
		}`),
		Handler: git.CommitToolHandler(repoRoot),
	})
	s.AddTool(Tool{
		Name: "pf_core_inspect",
		Description: "Inspect one architectural area's internal/ packages (their doc comments, source files, and test " +
			"files) or every internal/ package when area is \"all\". Areas: marketplace, runtime, builder, registry, " +
			"supply-chain, microvm, cli, all.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {"area": {"type": "string", "enum": ["marketplace", "runtime", "builder", "registry", "supply-chain", "microvm", "cli", "all"]}}
		}`),
		Handler: core.InspectToolHandler(repoRoot),
	})
	s.AddTool(Tool{
		Name: "pf_core_validate",
		Description: "Run one validation profile: \"fast\" (gofmt+vet), \"full\" (fast, plus build/test/archtest), " +
			"\"security\" (local mirrors of ci-security.yml's static checks plus govulncheck), or \"affected\" " +
			"(go test scoped to only the packages the current uncommitted changes touch, via the real reverse-dependency graph).",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {"profile": {"type": "string", "enum": ["fast", "full", "security", "affected"]}}
		}`),
		Handler: core.ValidateToolHandler(repoRoot),
	})
	s.AddTool(Tool{
		Name:        "pf_core_read_file",
		Description: "Read one repository-relative file's contents. Confined to the repository root; refuses any path that would resolve outside it.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"required": ["path"],
			"properties": {"path": {"type": "string"}}
		}`),
		Handler: core.ReadFileToolHandler(repoRoot),
	})
	s.AddTool(Tool{
		Name: "pf_core_write_file",
		Description: "Write content to one repository-relative file, creating parent directories as needed. Confined " +
			"to the repository root; refuses any path that would resolve outside it, and refuses to write through a " +
			"symlink. This is the low-level primitive - pair it with pf_core_self_check and pf_core_validate before " +
			"proposing a branch.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"required": ["path", "content"],
			"properties": {"path": {"type": "string"}, "content": {"type": "string"}}
		}`),
		Handler: core.WriteFileToolHandler(repoRoot),
	})
	s.AddTool(Tool{
		Name: "pf_core_self_check",
		Description: "Run go test ./internal/archtest/... - this repository's own import-boundary rules - against the " +
			"current working tree. Run this before pf_git_prepare_branch/pf_git_commit for any core change.",
		Handler: core.SelfCheckToolHandler(repoRoot),
	})

	s.AddResource(Resource{
		URI:         "pf://core",
		Name:        "core",
		Description: "Every pf_core_inspect area and the internal/ packages it maps to, plus the compatibility rules those packages must respect.",
		MimeType:    "application/json",
		Handler:     core.CoreResourceHandler(repoRoot),
	})
	s.AddResource(Resource{
		URI:         "pf://core/packages",
		Name:        "core-packages",
		Description: "Every internal/ package with its doc comment and source/test file listing.",
		MimeType:    "application/json",
		Handler:     core.PackagesResourceHandler(repoRoot),
	})

	s.AddTool(Tool{
		Name: "pf_core_create_pr",
		Description: "Push the current branch to origin and open a draft pull request against main via the " +
			"already-authenticated gh CLI. There is no corresponding merge tool - opening the PR is the last " +
			"mutating action this server takes on a core change; everything past that point is human review.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"required": ["title"],
			"properties": {
				"title": {"type": "string"},
				"body": {"type": "string"},
				"base": {"type": "string", "description": "defaults to main"},
				"repo": {"type": "string", "description": "owner/repo to open the PR against (e.g. CYPT71/platform-factory); defaults to the PLATFORM_FACTORY_MCP_TARGET_REPO env var, or gh's own inference from the working tree's remotes when both are empty"}
			}
		}`),
		Handler: git.CreatePRToolHandler(repoRoot),
	})

	s.AddTool(Tool{
		Name: "pf_plugin_modify",
		Description: "Server-embedded-agent mode: modify an existing plugin from a free-text request. Requires " +
			"PLATFORM_FACTORY_MCP_ANTHROPIC_API_KEY; without it, returns a structured error naming the " +
			"client-orchestrated primitives (pf_plugin_inspect, pf_core_write_file, pf_plugin_validate) to use instead. " +
			"Runs a bounded plan/edit/validate/retry loop confined to the plugin's own directory.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"required": ["plugin", "request"],
			"properties": {"plugin": {"type": "string"}, "request": {"type": "string"}}
		}`),
		Handler: agent.ModifyPluginToolHandler(repoRoot),
	})
	s.AddTool(Tool{
		Name: "pf_core_patch",
		Description: "Server-embedded-agent mode: propose a core change from a free-text request, confined to the " +
			"given allowed_paths. Requires PLATFORM_FACTORY_MCP_ANTHROPIC_API_KEY; without it, returns a structured " +
			"error naming the client-orchestrated primitives (pf_core_read_file, pf_core_write_file, " +
			"pf_core_self_check) to use instead. Runs a bounded plan/edit/self-check/retry loop; does not create a " +
			"branch, commit, or PR itself - pair it with pf_git_prepare_branch/pf_git_commit/pf_core_create_pr, or " +
			"use pf_implement for the full workflow in one call.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"required": ["request", "allowed_paths"],
			"properties": {
				"request": {"type": "string"},
				"reason": {"type": "string", "description": "why a core change is needed rather than a plugin"},
				"allowed_paths": {"type": "array", "items": {"type": "string"}, "minItems": 1}
			}
		}`),
		Handler: agent.PatchCoreToolHandler(repoRoot),
	})
	s.AddTool(Tool{
		Name: "pf_implement",
		Description: "Server-embedded-agent mode, full workflow: classify a free-text request as plugin-only, " +
			"core-only, or both; implement it (pf_plugin_create/pf_plugin_modify equivalent, and/or a scoped core " +
			"patch); and, if core changed, create a branch, commit, push, and open a draft PR - never merging, " +
			"never pushing to main directly. Requires PLATFORM_FACTORY_MCP_ANTHROPIC_API_KEY; without it, returns a " +
			"structured error naming the client-orchestrated primitives to use instead.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"required": ["request"],
			"properties": {"request": {"type": "string"}}
		}`),
		Handler: agent.ImplementToolHandler(repoRoot),
	})

	s.AddTool(Tool{
		Name: "pf_init",
		Description: "Run `platform-factory init`: scaffold a pf.yaml project from a source directory, " +
			"detecting language/artifact/dependency-mode unless overridden.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"directory": {"type": "string", "description": "source directory to scaffold from, defaults to the current directory"},
				"dry_run": {"type": "boolean"},
				"yes": {"type": "boolean", "description": "skip interactive confirmation"},
				"boot_disk": {"type": "string"},
				"language": {"type": "string"},
				"artifact": {"type": "string"},
				"dependency_mode": {"type": "string"},
				"runtime": {"type": "string"},
				"engine": {"type": "string"},
				"build_command": {"type": "string"},
				"build_args": {"type": "array", "items": {"type": "string"}},
				"extract_to": {"type": "string"},
				"archive_format": {"type": "string"},
				"filename_style": {"type": "string"},
				"extra_args": {"type": "array", "items": {"type": "string"}, "description": "additional raw flags not covered above"}
			}
		}`),
		Handler: product.InitToolHandler(repoRoot),
	})
	s.AddTool(Tool{
		Name: "pf_build",
		Description: "Run `platform-factory build`: build the nearest pf.yaml project (leave executable empty) " +
			"or wrap one compiled executable directly into an OCI image.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"executable": {"type": "string", "description": "path to a compiled executable; empty for project mode"},
				"dry_run": {"type": "boolean"},
				"output": {"type": "string"},
				"config": {"type": "string"},
				"arch": {"type": "string"},
				"os": {"type": "string"},
				"platforms": {"type": "array", "items": {"type": "string"}},
				"entrypoint": {"type": "string"},
				"profile": {"type": "string"},
				"image": {"type": "string"},
				"tag": {"type": "string"},
				"format": {"type": "string"},
				"compression": {"type": "string"},
				"semantic_layers": {"type": "boolean"},
				"rebuild": {"type": "integer"},
				"require_identical": {"type": "boolean"},
				"extra_files": {"type": "array", "items": {"type": "string"}},
				"labels": {"type": "array", "items": {"type": "string"}},
				"extra_args": {"type": "array", "items": {"type": "string"}, "description": "additional raw flags not covered above"}
			}
		}`),
		Handler: product.BuildToolHandler(repoRoot),
	})
	s.AddTool(Tool{
		Name: "pf_publish",
		Description: "Run `platform-factory publish`: push a verified OCI layout to a registry with optional " +
			"signature/SBOM/provenance and a policy gate. Credentials come from this server's own environment, " +
			"never from a tool argument.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"required": ["image"],
			"properties": {
				"layout": {"type": "string"},
				"image": {"type": "string"},
				"dry_run": {"type": "boolean"},
				"yes": {"type": "boolean"},
				"push_only": {"type": "boolean"},
				"deploy_only": {"type": "boolean"},
				"sign": {"type": "boolean"},
				"sbom": {"type": "boolean"},
				"provenance": {"type": "string"},
				"journal": {"type": "string"},
				"key_dir": {"type": "string"},
				"key_name": {"type": "string"},
				"policy": {"type": "string"},
				"evidence": {"type": "string"},
				"allow_incomplete_evidence": {"type": "boolean"},
				"source_ref": {"type": "string"},
				"insecure_registry": {"type": "boolean"},
				"mount_from": {"type": "string"},
				"format": {"type": "string"},
				"reports": {"type": "string"},
				"extra_args": {"type": "array", "items": {"type": "string"}, "description": "additional raw flags not covered above"}
			}
		}`),
		Handler: product.PublishToolHandler(repoRoot),
	})
	s.AddTool(Tool{
		Name: "pf_deploy",
		Description: "Run `platform-factory deploy`: apply a digest-pinned image to Kubernetes. Secret values are " +
			"never accepted directly, only ENV=SECRET/KEY references the cluster itself resolves.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"image": {"type": "string"},
				"name": {"type": "string"},
				"namespace": {"type": "string"},
				"replicas": {"type": "integer"},
				"port": {"type": "integer"},
				"workload": {"type": "string"},
				"schedule": {"type": "string"},
				"cpu_request": {"type": "string"},
				"memory_request": {"type": "string"},
				"runtime_class": {"type": "string"},
				"ingress_host": {"type": "string"},
				"ingress_path": {"type": "string"},
				"config": {"type": "array", "items": {"type": "string"}},
				"secret_env": {"type": "array", "items": {"type": "string"}, "description": "ENV=SECRET/KEY references only, never a secret value"},
				"volumes": {"type": "array", "items": {"type": "string"}},
				"timeout": {"type": "string"},
				"reports": {"type": "string"},
				"policy": {"type": "string"},
				"evidence": {"type": "string"},
				"dry_run": {"type": "boolean"},
				"yes": {"type": "boolean"},
				"extra_args": {"type": "array", "items": {"type": "string"}, "description": "additional raw flags not covered above"}
			}
		}`),
		Handler: product.DeployToolHandler(repoRoot),
	})
	s.AddTool(Tool{
		Name: "pf_project",
		Description: "Run `platform-factory project <action>`: drive a pf.yaml project's full lifecycle - " +
			"show (print resolved config), plan (explain freeze/build/run without changing anything), freeze " +
			"(lock dependencies), build (build the configured OCI layout), run (build if missing, then run it), " +
			"launch (freeze+build+run as needed), or migrate (rewrite the config to the current schema). " +
			"\"run\"/\"launch\" are the only tools in this server that actually start a built image - they shell " +
			"to the host's docker/podman (or a native microVM runtime), so they require that runtime to be " +
			"reachable from wherever this MCP server process itself runs.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"required": ["action"],
			"properties": {
				"action": {"type": "string", "enum": ["show", "plan", "freeze", "build", "run", "launch", "migrate"]},
				"directory": {"type": "string", "description": "project directory, defaults to the current directory"},
				"config": {"type": "string", "description": "project image YAML/JSON config path; otherwise auto-discovered"},
				"dry_run": {"type": "boolean", "description": "freeze/build only: explain without executing or writing"},
				"write": {"type": "boolean", "description": "migrate only: rewrite the config file in place instead of printing the migration"},
				"max_wall_clock": {"type": "string", "description": "build only: e.g. 10m; 0 disables"},
				"max_cpu": {"type": "string", "description": "build only: e.g. 5m; 0 disables"},
				"max_memory": {"type": "string", "description": "build only: e.g. 512MiB; 0 disables"},
				"extra_args": {"type": "array", "items": {"type": "string"}, "description": "additional raw flags not covered above"}
			}
		}`),
		Handler: product.ProjectToolHandler(repoRoot),
	})
	s.AddTool(Tool{
		Name:        "pf_status",
		Description: "Run `platform-factory status`: report build/publish/deploy state for a project and the next safe command.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"directory": {"type": "string"},
				"format": {"type": "string", "enum": ["json", "text"]}
			}
		}`),
		Handler: product.StatusToolHandler(repoRoot),
	})
	s.AddTool(Tool{
		Name:        "pf_doctor",
		Description: "Run `platform-factory doctor`: check local tools, runtimes, and hardware support, optionally scoped to one phase.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"scope": {"type": "string", "enum": ["", "all", "build", "publish", "deploy"]}
			}
		}`),
		Handler: product.DoctorToolHandler(repoRoot),
	})
	s.AddTool(Tool{
		Name:        "pf_detect",
		Description: "Run `platform-factory detect`: classify an application input (language, artifact, dependency mode) without executing it.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"required": ["path"],
			"properties": {
				"path": {"type": "string"},
				"format": {"type": "string", "enum": ["json", "text"]},
				"accept_ambiguous": {"type": "boolean"}
			}
		}`),
		Handler: product.DetectToolHandler(repoRoot),
	})
	s.AddTool(Tool{
		Name:        "pf_verify",
		Description: "Run `platform-factory verify`: strictly validate a local OCI layout.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"required": ["layout"],
			"properties": {
				"layout": {"type": "string"},
				"format": {"type": "string", "enum": ["json", "text"]}
			}
		}`),
		Handler: product.VerifyToolHandler(repoRoot),
	})
	s.AddTool(Tool{
		Name:        "pf_inspect",
		Description: "Run `platform-factory inspect`: summarize a local OCI layout's manifests and platforms.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"required": ["layout"],
			"properties": {
				"layout": {"type": "string"},
				"format": {"type": "string", "enum": ["json", "text"]}
			}
		}`),
		Handler: product.InspectToolHandler(repoRoot),
	})

	return s
}
