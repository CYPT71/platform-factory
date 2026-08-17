package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/CYPT71/platform-factory/sdk/langplugin"
)

func main() {
	err := langplugin.Dispatch(os.Args[1:], map[string]langplugin.Handler{
		"inspect": runInspect, "freeze": runFreeze, "build-layer": runBuildLayer,
	})
	if err == langplugin.ErrUsage {
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "platform-factory-lang-java: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: platform-factory-lang-java <inspect|freeze|build-layer> [OPTIONS]")
	fmt.Fprintln(os.Stderr, "  inspect --root DIR")
	fmt.Fprintln(os.Stderr, "  freeze --root DIR")
	fmt.Fprintln(os.Stderr, "  build-layer --root DIR --output TAR --dest PREFIX")
}

func runInspect(args []string) error {
	root, err := langplugin.ParseRootFlag("inspect", args)
	if err != nil {
		return err
	}
	result, err := langplugin.Inspect(root, langplugin.Definition{Language: "java", Profile: "java", Markers: []string{"pom.xml", "mvnw", "gradlew", "build.gradle", "build.gradle.kts"}, SourceExtensions: []string{".java"}, Manifests: []string{"pom.xml", "build.gradle", "build.gradle.kts"}})
	if err != nil {
		return err
	}
	return langplugin.WriteInspection(result)
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
	root, err := langplugin.ParseRootFlag("freeze", args)
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
	root, output, dest, err := langplugin.ParseBuildLayerFlags(args)
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
