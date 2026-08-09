// platform-factory-lang-java is the Java language plugin - see
// plugins/lang-python/main.go for the full pattern this mirrors and
// docs/language-plugin-layers.md for the architecture. Only the
// freeze/deps-location specifics differ per language; every plugin
// shares its tar-packaging logic via sdk/langplugin instead of
// duplicating it.
//
//	platform-factory-lang-java freeze --root DIR
//	platform-factory-lang-java build-layer --root DIR --output TAR --dest PREFIX
//
// freeze detects the build tool the same way the host's own built-in
// Java freeze step does - mvnw first, then gradlew, then a bare pom.xml
// (see cmd/platform-factory/project.go's freezeSteps) - and runs the
// same command. Unlike pip/npm/Bundler/Composer, neither Maven nor
// Gradle has a clean project-local install flag: both default to a
// shared, unbounded, per-user global cache (~/.m2, ~/.gradle). That
// cache can't be packaged into a layer as-is, so this plugin deliberately
// deviates from byte-identical built-in behavior and redirects it to a
// project-local directory instead: -Dmaven.repo.local for Maven,
// GRADLE_USER_HOME for Gradle.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/CYPT71/secure-oci-base/sdk/langplugin"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "freeze":
		err = runFreeze(os.Args[2:])
	case "build-layer":
		err = runBuildLayer(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "platform-factory-lang-java: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: platform-factory-lang-java <freeze|build-layer> [OPTIONS]")
	fmt.Fprintln(os.Stderr, "  freeze --root DIR")
	fmt.Fprintln(os.Stderr, "  build-layer --root DIR --output TAR --dest PREFIX")
}

// mavenDepsRelPath and gradleDepsRelPath are the project-local cache
// directories this plugin redirects Maven/Gradle into - see the package
// doc comment for why redirection is necessary here but not for the
// other built-in languages.
const (
	mavenDepsRelPath  = ".platform-factory/deps/java/m2"
	gradleDepsRelPath = ".platform-factory/deps/java/gradle"
)

// buildTool identifies which tool freeze/build-layer detected in root,
// mirroring the host's own detection order exactly (mvnw, then gradlew,
// then a bare pom.xml).
type buildTool int

const (
	toolNone buildTool = iota
	toolMavenWrapper
	toolGradleWrapper
	toolMavenBare
)

func detectBuildTool(root string) (buildTool, error) {
	exists := func(name string) bool {
		info, err := os.Stat(filepath.Join(root, name))
		return err == nil && info.Mode().IsRegular()
	}
	switch {
	case exists("mvnw"):
		return toolMavenWrapper, nil
	case exists("gradlew"):
		return toolGradleWrapper, nil
	case exists("pom.xml"):
		return toolMavenBare, nil
	default:
		return toolNone, errors.New("no Maven (pom.xml/mvnw) or Gradle (gradlew) files found in root")
	}
}

func depsRelPathFor(tool buildTool) string {
	if tool == toolGradleWrapper {
		return gradleDepsRelPath
	}
	return mavenDepsRelPath
}

func runFreeze(args []string) error {
	root, err := parseRootFlag("freeze", args)
	if err != nil {
		return err
	}
	tool, err := detectBuildTool(root)
	if err != nil {
		return err
	}
	switch tool {
	case toolMavenWrapper:
		repo := filepath.Join(root, mavenDepsRelPath)
		return runInWithEnv(root, nil, wrapperCommand("./mvnw"), "-B", "dependency:go-offline", "-Dmaven.repo.local="+repo)
	case toolMavenBare:
		repo := filepath.Join(root, mavenDepsRelPath)
		return runInWithEnv(root, nil, "mvn", "-B", "dependency:go-offline", "-Dmaven.repo.local="+repo)
	case toolGradleWrapper:
		home := filepath.Join(root, gradleDepsRelPath)
		env := []string{"GRADLE_USER_HOME=" + home}
		return runInWithEnv(root, env, wrapperCommand("./gradlew"), "dependencies", "--write-locks")
	default:
		return fmt.Errorf("unhandled build tool %v", tool)
	}
}

func runBuildLayer(args []string) error {
	root, output, dest, err := parseBuildLayerFlags(args)
	if err != nil {
		return err
	}
	tool, err := detectBuildTool(root)
	if err != nil {
		return err
	}
	depsRelPath := depsRelPathFor(tool)
	source := filepath.Join(root, depsRelPath)
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("%s does not exist - run `platform-factory-lang-java freeze` first: %w", depsRelPath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", depsRelPath)
	}
	return langplugin.WriteDeterministicTar(source, dest, output)
}

func wrapperCommand(value string) string {
	if runtime.GOOS == "windows" {
		return value + ".bat"
	}
	return value
}
func runInWithEnv(dir string, extraEnv []string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	return cmd.Run()
}

func parseRootFlag(subcommand string, args []string) (root string, err error) {
	flags := flag.NewFlagSet(subcommand, flag.ContinueOnError)
	rootFlag := flags.String("root", "", "project root directory")
	if err := flags.Parse(args); err != nil {
		return "", err
	}
	if *rootFlag == "" {
		return "", errors.New("--root is required")
	}
	return *rootFlag, nil
}

func parseBuildLayerFlags(args []string) (root, output, dest string, err error) {
	flags := flag.NewFlagSet("build-layer", flag.ContinueOnError)
	rootFlag := flags.String("root", "", "project root directory")
	outputFlag := flags.String("output", "", "path to write the uncompressed tar layer to")
	destFlag := flags.String("dest", "", "container path prefix every entry in the layer is rooted at")
	if err := flags.Parse(args); err != nil {
		return "", "", "", err
	}
	if *rootFlag == "" || *outputFlag == "" || *destFlag == "" {
		return "", "", "", errors.New("--root, --output, and --dest are all required")
	}
	return *rootFlag, *outputFlag, *destFlag, nil
}
