package main

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/CYPT71/secure-oci-base/internal/app/projectinit"
	"github.com/CYPT71/secure-oci-base/internal/detect"
)

// initLanguageMenu is the fixed, numbered list `pf init` offers when it
// can't pick a language on its own. A beginner shouldn't have to
// already know this project's internal language-string vocabulary
// (e.g. that Node.js is spelled "node", not "javascript") to answer a
// free-text prompt correctly - a number is impossible to typo into an
// unsupported value.
var initLanguageMenu = []struct {
	value           string
	label           string
	artifactExample string
}{
	{"go", "Go", "cmd/app/main.go"},
	{"node", "Node.js (JavaScript/TypeScript)", "src/index.js"},
	{"python", "Python", "app.py"},
	{"java", "Java", "target/app.jar"},
	{"dotnet", ".NET (C#/F#)", "bin/Release/net8.0/app.dll"},
	{"rust", "Rust", "target/release/app"},
	{"ruby", "Ruby", "app.rb"},
	{"php", "PHP", "public/index.php"},
	{"custom", "Custom (you write your own build_command/freeze_command)", "dist/app"},
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
func resolveEcosystemInteractively(result detect.Result, languageFlag, artifactFlag string, assumeYes bool, stdin *bufio.Reader, stdout io.Writer) resolvedEcosystem {
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
		fmt.Fprintf(stdout, "platform-factory init: this looks like it could be more than one kind of project: %s\n", strings.Join(result.Candidates, ", "))
	} else {
		fmt.Fprintln(stdout, "platform-factory init: couldn't tell what kind of project this is automatically")
	}
	fmt.Fprintln(stdout, "platform-factory init: what language is this project written in?")
	for i, choice := range initLanguageMenu {
		fmt.Fprintf(stdout, "  %d) %s\n", i+1, choice.label)
	}
	fmt.Fprintf(stdout, "platform-factory init: enter a number 1-%d, or press Enter to stop without changing anything: ", len(initLanguageMenu))
	answer := readLine(stdin)
	if answer == "" {
		return resolvedEcosystem{result: result, artifact: artifactFlag}
	}
	index, err := strconv.Atoi(answer)
	if err != nil || index < 1 || index > len(initLanguageMenu) {
		fmt.Fprintf(stdout, "platform-factory init: %q isn't one of the numbers above, so stopping without changing anything - run `pf init` again to retry\n", answer)
		return resolvedEcosystem{result: result, artifact: artifactFlag}
	}
	choice := initLanguageMenu[index-1]

	artifact := artifactFlag
	if artifact == "" {
		fmt.Fprintf(stdout, "platform-factory init: where's the file your app starts from, relative to this directory? (example: %s) - press Enter to fill this in later: ", choice.artifactExample)
		artifact = readLine(stdin)
	}

	result.Kind = choice.value
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
	fmt.Fprintf(stdout, "platform-factory init: about to create in %s:\n", dir)
	for _, action := range plan.Actions {
		fmt.Fprintln(stdout, "  "+action.Description())
	}
	for _, unknown := range plan.Unknowns {
		fmt.Fprintln(stdout, "  "+unknown.Description())
	}
	if plan.System != nil {
		for _, description := range plan.System.Descriptions() {
			fmt.Fprintln(stdout, "  "+description)
		}
	}
	fmt.Fprint(stdout, "platform-factory init: proceed? [y/N] ")
	answer := strings.ToLower(readLine(stdin))
	return answer == "y" || answer == "yes"
}

func readLine(reader *bufio.Reader) string {
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return ""
	}
	return strings.TrimSpace(line)
}
