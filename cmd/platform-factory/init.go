package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/CYPT71/platform-factory/internal/app/projectinit"
	"github.com/CYPT71/platform-factory/internal/detect"
	"github.com/CYPT71/platform-factory/internal/sourcearchive"
	"github.com/CYPT71/platform-factory/sdk/langplugin"
)

// initFlags holds every pf init flag pointer, built once by newInitFlagSet
// so the flag set behind --help (printInitUsage) can never drift from the
// flag set actually parsed by runInit.
type initFlags struct {
	dryRun, assumeYes                                            *bool
	bootDiskOverride, languageFlag, artifactFlag, dependencyMode *string
	runtimeFlag, engineFlag                                      *string
	buildCommand                                                 *string
	extractTo, archiveFormat                                     *string
	filenameStyle                                                *string
	buildArgs                                                    *stringListFlag
}

type stringListFlag []string

func (values *stringListFlag) String() string { return strings.Join(*values, ",") }
func (values *stringListFlag) Set(value string) error {
	if value == "" || strings.ContainsRune(value, 0) {
		return errors.New("value must be non-empty and NUL-free")
	}
	*values = append(*values, value)
	return nil
}

func newInitFlagSet(output io.Writer) (*flag.FlagSet, initFlags) {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(output)
	var buildArgs stringListFlag
	f := initFlags{
		dryRun:           flags.Bool("dry-run", false, "print the plan without writing anything"),
		assumeYes:        flags.Bool("yes", false, "skip interactive prompts (ecosystem choice, boot-disk choice, final confirmation); non-interactive mode"),
		bootDiskOverride: flags.String("boot-disk", "", "which detected legacy disk is the boot/OS disk, when it can't be (or shouldn't be) auto-detected or prompted for; must match one of the detected disks exactly"),
		languageFlag:     flags.String("language", "", "project language, when it can't be (or shouldn't be) auto-detected or prompted for"),
		artifactFlag:     flags.String("artifact", "", "path to the build artifact, relative to the project directory"),
		dependencyMode:   flags.String("dependency-mode", "", "dependency state: none, manifest, unresolved, external, or unknown"),
		runtimeFlag:      flags.String("runtime", "", "runtime choice: container or microvm"),
		engineFlag:       flags.String("engine", "", "local container engine: docker or podman"),
		buildCommand:     flags.String("build-command", "", "custom build executable; arguments are separate --build-arg values (no shell parsing)"),
		extractTo:        flags.String("extract-to", "", "new destination directory for a tar/tar.gz source archive"),
		archiveFormat:    flags.String("archive-format", "", "source archive format: tar or tar.gz (requires --extract-to)"),
		filenameStyle:    flags.String("filename-style", "short", "project filenames: short (pf.yaml/pf.lock) or long (platform-factory.yaml/platform-factory.lock)"),
		buildArgs:        &buildArgs,
	}
	flags.Var(&buildArgs, "build-arg", "one custom build argument; repeat to preserve argument boundaries")
	return flags, f
}

func printInitUsage(output io.Writer) {
	fmt.Fprintln(output, `platform-factory init — turn a source directory into a pf.yaml project, without writing anything until you approve the plan

Usage:
  platform-factory init [OPTIONS] [DIRECTORY]

DIRECTORY defaults to the current directory. A local tar or tar.gz source must
use --archive-format and --extract-to NEW_DIRECTORY; the destination is
reserved exclusively and removed if extraction or initialization fails. init
never fetches a remote Git or OCI Registry source.

Go isn't one of the interpreter plugins bundled with the CLI - it ships
separately as plugins/lang-go in the platform-factory source tree. To
init a Go project: (cd plugins/lang-go && go build -o platform-factory-lang-go .),
then platform-factory plugin load --from ./plugins/lang-go/platform-factory-lang-go go.

Examples:
  platform-factory init --dry-run .
  platform-factory init --engine docker .
  platform-factory init --yes --language python .
  platform-factory init --language go .
  platform-factory init --archive-format tar.gz --extract-to ./app ./app.tar.gz

Run "platform-factory plugin list" to see which languages are ready now.

Options:`)
	flags, _ := newInitFlagSet(output)
	flags.PrintDefaults()
}

// runInit is the CLI adapter for internal/app/projectinit. It parses user
// intent, performs interactive resolution and confirmation, then hands the
// explicit plan to the application use-case. It never builds or deploys.
func runInit(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		printInitUsage(stdout)
		return 0
	}
	flags, f := newInitFlagSet(stderr)
	dryRun, assumeYes := f.dryRun, f.assumeYes
	bootDiskOverride, languageFlag, artifactFlag := f.bootDiskOverride, f.languageFlag, f.artifactFlag
	dependencyMode, runtimeFlag, engineFlag := f.dependencyMode, f.runtimeFlag, f.engineFlag
	buildCommand, buildArgs := f.buildCommand, f.buildArgs
	extractTo, archiveFormat := f.extractTo, f.archiveFormat
	filenameStyle := f.filenameStyle
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *engineFlag != "" && *engineFlag != "docker" && *engineFlag != "podman" {
		fmt.Fprintln(stderr, "platform-factory init: --engine must be docker or podman")
		return 2
	}
	if *buildCommand == "" && len(*buildArgs) > 0 {
		fmt.Fprintln(stderr, "platform-factory init: --build-arg requires --build-command")
		return 2
	}
	if strings.ContainsRune(*buildCommand, 0) {
		fmt.Fprintln(stderr, "platform-factory init: --build-command must be NUL-free")
		return 2
	}
	if (*extractTo == "") != (*archiveFormat == "") || (*archiveFormat != "" && *archiveFormat != "tar" && *archiveFormat != "tar.gz") {
		fmt.Fprintln(stderr, "platform-factory init: --extract-to and --archive-format=tar|tar.gz must be used together")
		return 2
	}
	if *filenameStyle != "short" && *filenameStyle != "long" {
		fmt.Fprintln(stderr, "platform-factory init: --filename-style must be short or long")
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
	archiveDestination := ""
	keepArchiveDestination := false
	defer func() {
		if archiveDestination != "" && !keepArchiveDestination {
			_ = os.RemoveAll(archiveDestination)
		}
	}()
	if *archiveFormat != "" {
		info, statErr := os.Lstat(source)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			fmt.Fprintf(stderr, "platform-factory init: archive %q must be a regular non-symlink file\n", source)
			return 2
		}
		destination, absErr := filepath.Abs(*extractTo)
		if absErr != nil {
			fmt.Fprintf(stderr, "platform-factory init: resolve extraction destination: %v\n", absErr)
			return 2
		}
		if *dryRun {
			destination, absErr = os.MkdirTemp("", "platform-factory-init-preview-*")
			if absErr != nil {
				fmt.Fprintf(stderr, "platform-factory init: stage archive preview: %v\n", absErr)
				return 1
			}
			_ = os.Remove(destination)
			defer os.RemoveAll(destination)
		} else {
			archiveDestination = destination
		}
		if err := sourcearchive.Extract(source, destination, *archiveFormat); err != nil {
			fmt.Fprintf(stderr, "platform-factory init: %v\n", err)
			return 1
		}
		source = destination
	}

	info, err := os.Stat(source)
	if err != nil || !info.IsDir() {
		fmt.Fprintf(stderr, "platform-factory init: %q is not an existing local directory - git/OCI-registry/archive sources are not supported yet, point pf init at a local directory\n", source)
		return 2
	}
	dir, err := projectinit.ResolveRoot(source)
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
		pluginInspections, err := langplugin.InspectLoaded(dir)
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory init: inspect language plugins: %v\n", err)
			return 1
		}
		allPluginInspections := append([]langplugin.Inspection(nil), pluginInspections...)
		if *languageFlag != "" {
			binary, resolveErr := langplugin.Resolve(*languageFlag)
			if resolveErr != nil {
				fmt.Fprintf(stderr, "platform-factory init: %v\n", resolveErr)
				return 2
			}
			selected, inspectErr := langplugin.RunInspection(binary, dir)
			if inspectErr != nil {
				fmt.Fprintf(stderr, "platform-factory init: inspect language plugin %s: %v\n", *languageFlag, inspectErr)
				return 1
			}
			selected.Match, selected.Language = true, *languageFlag
			pluginInspections = []langplugin.Inspection{selected}
			found := false
			for index := range allPluginInspections {
				if allPluginInspections[index].Language == selected.Language {
					allPluginInspections[index] = selected
					found = true
				}
			}
			if !found {
				allPluginInspections = append(allPluginInspections, selected)
			}
		}
		if *languageFlag == "" {
			allPluginInspections = pluginInspections
		}
		detected := detectionFromPlugins(dir, pluginInspections)
		resolved := resolveEcosystemInteractively(detected, *languageFlag, *artifactFlag, dir, *assumeYes, reader, stdout)
		if !resolved.confident && legacyDisks == nil {
			if !resolved.explained {
				fmt.Fprintln(stderr, "platform-factory init: no loaded language plugin recognized this project; nothing was written")
				if looksLikeGoSource(dir) {
					fmt.Fprintln(stderr, "  next: Go ships as a separate plugin, not bundled with the CLI - from the platform-factory")
					fmt.Fprintln(stderr, "        source tree: (cd plugins/lang-go && go build -o platform-factory-lang-go .),")
					fmt.Fprintln(stderr, "        then `pf plugin load --from ./plugins/lang-go/platform-factory-lang-go go`, then retry `pf init`")
				} else {
					fmt.Fprintln(stderr, "  next: run `pf plugin list`, load the matching plugin with `pf plugin load LANGUAGE`, then retry `pf init`")
				}
			}
			return 2
		}
		if !resolved.confident {
			// A pure legacy-disk project has no application language, artifact,
			// dependency manager, or container runtime to resolve.
			ecosystem = projectinit.Ecosystem{}
		} else {
			selected, found := selectLanguageInspection(pluginInspections, resolved.result.Kind)
			if !found {
				fmt.Fprintf(stderr, "platform-factory init: language plugin %q did not return an inspection; load it with `pf plugin load %s`; nothing was written\n", resolved.result.Kind, resolved.result.Kind)
				return 2
			}
			inspection := projectinit.EnrichOperationalHints(dir, applicationInspectionFromPlugin(dir, selected))
			if *buildCommand != "" {
				inspection.BuildCommand = append([]string{*buildCommand}, (*buildArgs)...)
			}
			artifact := resolved.artifact
			if artifact == "" {
				artifact = inspection.Artifact
			}
			mode, ok := resolveDependencyMode(inspection.Dependencies.Mode, *dependencyMode, *assumeYes || *dryRun, reader, stdout)
			if *dryRun && !ok {
				mode, ok = inspection.Dependencies.Mode, true
			}
			if !ok {
				fmt.Fprintln(stderr, "platform-factory init: dependency state requires a decision; use --dependency-mode; nothing was written")
				return 2
			}
			inspection.Dependencies.Mode = mode
			runtime, ok := resolveRuntime(*runtimeFlag, *assumeYes, reader, stdout)
			if !ok {
				fmt.Fprintln(stderr, "platform-factory init: runtime requires a decision; use --runtime; nothing was written")
				return 2
			}
			inspection.Runtime.Selected = runtime
			inspection.Runtime.Unknowns = nil
			inspection.Unknowns = filterResolvedUnknowns(inspection.Unknowns, artifact, mode)
			if artifact == "" && legacyDisks == nil && !*dryRun {
				fmt.Fprintln(stderr, "platform-factory init: build artifact or entrypoint requires a decision; use --artifact; nothing was written")
				return 2
			}
			allInspections := make([]projectinit.ApplicationInspection, 0, len(allPluginInspections))
			for _, candidate := range allPluginInspections {
				observed := projectinit.EnrichOperationalHints(dir, applicationInspectionFromPlugin(dir, candidate))
				if candidate.Language == resolved.result.Kind {
					observed = inspection
					observed.Artifact = artifact
				}
				allInspections = append(allInspections, observed)
			}
			ecosystem = projectinit.Ecosystem{Result: resolved.result, Artifact: artifact, Confident: resolved.confident, Inspection: inspection, Inspections: allInspections, Runtime: runtime, RuntimeEngine: *engineFlag}
		}
	}

	plan, err := projectinit.BuildPlanWithFilenameStyle(dir, ecosystem, legacyDisks, projectinit.Observe(dir, time.Now()), *filenameStyle)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory init: %v\n", err)
		return 1
	}
	if *dryRun {
		for _, finding := range plan.Findings {
			fmt.Fprintln(stdout, finding)
		}
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
	for _, finding := range plan.Findings {
		fmt.Fprintln(stdout, finding)
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
		fmt.Fprintln(stdout, "\nreview pf.yaml before running `pf build`")
	}
	if ecosystem.Result.Kind != "" {
		autoProvisionRuntime(context.Background(), dir, ecosystem.Result.Kind, stdout, stderr)
	}
	keepArchiveDestination = true
	return 0
}

func detectionFromPlugins(dir string, inspections []langplugin.Inspection) detect.Result {
	result := detect.Result{Path: dir, Kind: "unknown", Profile: "unknown", Evidence: []string{"no loaded language plugin matched"}}
	if len(inspections) == 1 {
		item := inspections[0]
		result.Kind, result.Profile, result.Evidence = item.Language, item.Profile, append([]string(nil), item.Evidence...)
		result.Candidates = []string{item.Language}
		return result
	}
	if len(inspections) > 1 {
		result.Kind, result.Profile, result.Ambiguous = "ambiguous", "unknown", true
		result.Evidence = nil
		for _, item := range inspections {
			result.Candidates = append(result.Candidates, item.Language)
			result.Evidence = append(result.Evidence, item.Evidence...)
		}
		slices.Sort(result.Candidates)
		slices.Sort(result.Evidence)
	}
	return result
}

func selectLanguageInspection(inspections []langplugin.Inspection, language string) (langplugin.Inspection, bool) {
	for _, inspection := range inspections {
		if inspection.Language == language {
			return inspection, true
		}
	}
	return langplugin.Inspection{}, false
}

func applicationInspectionFromPlugin(dir string, item langplugin.Inspection) projectinit.ApplicationInspection {
	return projectinit.ApplicationInspection{Detection: detect.Result{Path: dir, Kind: item.Language, Profile: item.Profile, Evidence: append([]string(nil), item.Evidence...)}, BuildCommand: append([]string(nil), item.BuildCommand...), Artifact: item.Artifact, Entrypoint: item.Entrypoint, Dependencies: projectinit.DependencyState{Mode: item.Dependencies.Mode, Manifest: item.Dependencies.Manifest, Imports: append([]string(nil), item.Dependencies.Imports...), Reason: item.Dependencies.Reason}}
}
