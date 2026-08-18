package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/CYPT71/platform-factory/internal/app/projectinit"
	"github.com/CYPT71/platform-factory/internal/detect"
)

// looksLikeGoSource does a cheap, non-recursive scan for a go.mod or any
// *.go file directly in dir - the same signal a human glancing at `ls`
// would use. Go ships as a separate plugin (plugins/lang-go) built from
// the platform-factory source tree rather than as one of the interpreter
// plugins bundled with the CLI, so a project that is obviously Go still
// fails ecosystem resolution until that plugin is built and loaded; this
// lets the failure message name the real, actionable next step instead of
// pointing at `pf plugin list`, which never lists Go at all.
func looksLikeGoSource(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if entry.Name() == "go.mod" || strings.HasSuffix(entry.Name(), ".go") {
			return true
		}
	}
	return false
}

// resolvedEcosystem is what ecosystem resolution decided: a
// detect.Result plus an explicit artifact path (which may still be
// empty - a real, present human can knowingly leave it for later; see
// Confident). runInit uses Confident to decide whether it's safe to
// write a config at all - never automatically, silently, with a made-up
// language nobody actually chose.
type resolvedEcosystem struct {
	result    detect.Result
	artifact  string // empty means "no artifact decided yet"
	confident bool   // true once a real (detected, typed, or --language/--artifact-flagged) language is known
	explained bool   // true once the interactive panel already told the user why nothing was written
}

// resolveEcosystemInteractively decides the project's language and
// (when possible) artifact path. Priority order: an explicit
// --language/--artifact flag always wins outright (no prompting, no
// second-guessing an informed non-interactive choice); then a
// confident detect.Path result; then, only when a real interactive
// session exists (stdin non-nil, assumeYes false), asking on
// stdout/stdin. Whenever none of those resolve the language,
// Confident is false - callers must not write a config with a made-up
// language in that case (see the LegacyDisks exemption in
// internal/project.Validate and runInit's refusal otherwise).
func resolveEcosystemInteractively(result detect.Result, languageFlag, artifactFlag, dir string, assumeYes bool, stdin *bufio.Reader, stdout io.Writer) resolvedEcosystem {
	if languageFlag != "" {
		result.Kind = languageFlag
		result.Ambiguous = false
		result.Evidence = []string{"specified via --language"}
		return resolvedEcosystem{result: result, artifact: artifactFlag, confident: true}
	}

	confident := !result.Ambiguous && result.Kind != "" && result.Kind != "unknown"
	if confident {
		return resolvedEcosystem{result: result, artifact: artifactFlag, confident: true}
	}
	if assumeYes || stdin == nil {
		return resolvedEcosystem{result: result, artifact: artifactFlag}
	}

	if result.Ambiguous {
		initPanel(stdout, "1/3", "Choose a language", "Several loaded plugins recognize this project")
		fmt.Fprintf(stdout, "│ Candidates  %s\n", strings.Join(result.Candidates, ", "))
	} else {
		initPanel(stdout, "1/3", "Language not recognized", "No loaded language plugin recognizes this project")
		if looksLikeGoSource(dir) {
			fmt.Fprintln(stdout, "│ Next        this looks like Go - see `pf init --help` for how to load Go support")
		} else {
			fmt.Fprintln(stdout, "│ Next        pf plugin list · pf plugin load LANGUAGE")
		}
		fmt.Fprintln(stdout, "╰────────────────────────────────────────────────────────────")
		return resolvedEcosystem{result: result, artifact: artifactFlag, explained: true}
	}
	for i, candidate := range result.Candidates {
		fmt.Fprintf(stdout, "│  [%d] %s\n", i+1, candidate)
	}
	fmt.Fprintln(stdout, "╰────────────────────────────────────────────────────────────")
	fmt.Fprintf(stdout, "Select 1-%d · Enter cancels safely: ", len(result.Candidates))
	answer := readLine(stdin)
	if answer == "" {
		return resolvedEcosystem{result: result, artifact: artifactFlag}
	}
	index, err := strconv.Atoi(answer)
	if err != nil || index < 1 || index > len(result.Candidates) {
		fmt.Fprintf(stdout, "✗ %q is not a valid choice. Nothing changed; run `pf init` to retry.\n", answer)
		return resolvedEcosystem{result: result, artifact: artifactFlag}
	}
	choice := result.Candidates[index-1]

	artifact := artifactFlag
	if artifact == "" {
		initPanel(stdout, "2/3", "Confirm the entrypoint", "Leave empty to accept the language plugin recommendation")
		fmt.Fprintln(stdout, "╰────────────────────────────────────────────────────────────")
		fmt.Fprint(stdout, "Entrypoint: ")
		artifact = readLine(stdin)
	}

	result.Kind = choice
	result.Ambiguous = false
	result.Evidence = []string{"chosen from the pf init menu"}
	return resolvedEcosystem{result: result, artifact: artifact, confident: true}
}

// confirmPlan prints plan and asks the user to proceed, when there is
// anyone to ask: assumeYes or a nil stdin both mean "proceed without
// asking" (matching every other command's --yes convention and keeping
// non-interactive/scripted `pf init` calls working exactly as before -
// this UX layer only ever adds a prompt, never requires one).
func confirmPlan(plan projectinit.Plan, dir string, assumeYes bool, stdin *bufio.Reader, stdout io.Writer) bool {
	if assumeYes || stdin == nil {
		return true
	}
	initPanel(stdout, "3/3", "Review and apply", dir)
	for _, finding := range plan.Findings {
		fmt.Fprintln(stdout, "│ ✓ "+finding)
	}
	for _, action := range plan.Actions {
		fmt.Fprintln(stdout, "│ + "+action.Description())
	}
	for _, unknown := range plan.Unknowns {
		fmt.Fprintln(stdout, "│ ? "+unknown.Description())
	}
	if plan.System != nil {
		for _, description := range plan.System.Descriptions() {
			fmt.Fprintln(stdout, "│ • "+description)
		}
	}
	fmt.Fprintln(stdout, "│ No build or deployment runs during init.")
	fmt.Fprintln(stdout, "╰────────────────────────────────────────────────────────────")
	fmt.Fprint(stdout, "Apply this plan? [y/N] ")
	answer := strings.ToLower(readLine(stdin))
	return answer == "y" || answer == "yes"
}

func initPanel(stdout io.Writer, step, title, subtitle string) {
	fmt.Fprintln(stdout, "╭─ Platform Factory · Init ──────────────────────────────────")
	fmt.Fprintf(stdout, "│ %s  %s\n", step, title)
	if subtitle != "" {
		fmt.Fprintf(stdout, "│ %s\n", subtitle)
	}
}

func readLine(reader *bufio.Reader) string {
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return ""
	}
	return strings.TrimSpace(line)
}

func resolveDependencyMode(detected, explicit string, assumeYes bool, stdin *bufio.Reader, stdout io.Writer) (string, bool) {
	valid := func(mode string) bool {
		switch mode {
		case "none", "manifest", "unresolved", "external", "unknown":
			return true
		}
		return false
	}
	if explicit != "" {
		return explicit, valid(explicit)
	}
	if detected == "none" || detected == "manifest" {
		return detected, true
	}
	if assumeYes || stdin == nil {
		return "", false
	}
	fmt.Fprintf(stdout, "platform-factory init: dependency state is %s. Choose: 1) continue unresolved 2) external management 3) no dependencies 4) stop: ", detected)
	switch readLine(stdin) {
	case "1":
		return "unresolved", true
	case "2":
		return "external", true
	case "3":
		return "none", true
	default:
		return "", false
	}
}

func resolveRuntime(explicit string, assumeYes bool, stdin *bufio.Reader, stdout io.Writer) (projectinit.RuntimeMode, bool) {
	if explicit != "" {
		mode := projectinit.RuntimeMode(explicit)
		return mode, mode == projectinit.RuntimeContainer || mode == projectinit.RuntimeMicroVM
	}
	// The complete plan confirmation accepts this explained recommendation.
	// An explicit --runtime remains available when the operator disagrees.
	return projectinit.RuntimeContainer, true
}
