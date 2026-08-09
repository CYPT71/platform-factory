package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/CYPT71/secure-oci-base/internal/app/projectinit"
	"github.com/CYPT71/secure-oci-base/internal/detect"
)

// runInit is the CLI adapter for internal/app/projectinit. It parses user
// intent, performs interactive resolution and confirmation, then hands the
// explicit plan to the application use-case. It never builds or deploys.
func runInit(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dryRun := flags.Bool("dry-run", false, "print the plan without writing anything")
	assumeYes := flags.Bool("yes", false, "skip interactive prompts (ecosystem choice, boot-disk choice, final confirmation); non-interactive mode")
	bootDiskOverride := flags.String("boot-disk", "", "which detected legacy disk is the boot/OS disk, when it can't be (or shouldn't be) auto-detected or prompted for; must match one of the detected disks exactly")
	languageFlag := flags.String("language", "", "project language, when it can't be (or shouldn't be) auto-detected or prompted for")
	artifactFlag := flags.String("artifact", "", "path to the build artifact, relative to the project directory")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	positionals := flags.Args()
	if len(positionals) > 1 {
		fmt.Fprintln(stderr, "platform-factory init: at most one source argument is accepted")
		return 2
	}
	source := "."
	if len(positionals) == 1 {
		source = positionals[0]
	}

	info, err := os.Stat(source)
	if err != nil || !info.IsDir() {
		fmt.Fprintf(stderr, "platform-factory init: %q is not an existing local directory - git/OCI-registry/archive sources are not supported yet (see Meine-Graal v6.1 \"Sources applicatives\")\n", source)
		return 2
	}
	dir, err := filepath.Abs(source)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory init: %v\n", err)
		return 1
	}

	var reader *bufio.Reader
	if stdin != nil {
		reader = bufio.NewReader(stdin)
	}
	legacyDisks, err := detectAndResolveLegacyDisks(dir, *bootDiskOverride, *assumeYes, reader, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory init: %v\n", err)
		return 2
	}

	var ecosystem projectinit.Ecosystem
	if projectinit.NeedsEcosystemResolution(dir) {
		detected, err := detect.Path(dir)
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory init: detect project ecosystem: %v\n", err)
			return 1
		}
		resolved := resolveEcosystemInteractively(detected, *languageFlag, *artifactFlag, *assumeYes, reader, stdout)
		if !resolved.confident && legacyDisks == nil {
			fmt.Fprintln(stderr, "platform-factory init: could not determine the project's language automatically - re-run interactively (a terminal, not --yes/piped input) or pass --language explicitly; nothing was written")
			return 2
		}
		ecosystem = projectinit.Ecosystem{Result: resolved.result, Artifact: resolved.artifact, Confident: resolved.confident}
	}

	plan, err := projectinit.BuildPlan(dir, ecosystem, legacyDisks, projectinit.Observe(dir, time.Now()))
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory init: %v\n", err)
		return 1
	}
	if *dryRun {
		for _, action := range plan.Actions {
			fmt.Fprintln(stdout, action.Description())
		}
		for _, unknown := range plan.Unknowns {
			fmt.Fprintln(stdout, unknown.Description())
		}
		if plan.System != nil {
			for _, description := range plan.System.Descriptions() {
				fmt.Fprintln(stdout, description)
			}
		}
		return 0
	}
	if !confirmPlan(plan, dir, *assumeYes, reader, stdout) {
		fmt.Fprintln(stdout, "platform-factory init: aborted, nothing written")
		return 0
	}

	receipt, err := projectinit.Execute(plan)
	if err != nil {
		if rollbackErr := projectinit.Rollback(receipt); rollbackErr != nil {
			fmt.Fprintf(stderr, "platform-factory init: %v; rollback incomplete: %v\n", err, rollbackErr)
		} else {
			fmt.Fprintf(stderr, "platform-factory init: %v (rolled back)\n", err)
		}
		return 1
	}
	for _, action := range plan.Actions {
		fmt.Fprintln(stdout, "created "+action.Description())
	}
	for _, unknown := range plan.Unknowns {
		fmt.Fprintln(stdout, unknown.Description())
	}
	if plan.System != nil {
		for _, description := range plan.System.Descriptions() {
			fmt.Fprintln(stdout, description)
		}
	}
	if plan.HasPlaceholder() {
		fmt.Fprintln(stdout, "\nedit platform-factory.yaml (artifact/language) before running `platform-factory project build`")
	}
	return 0
}
