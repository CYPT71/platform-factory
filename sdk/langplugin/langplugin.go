package langplugin

import (
	"archive/tar"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
)

var ErrUsage = errors.New("language plugin: usage")

type Handler func([]string) error

// Dispatch invokes one named language-plugin command without owning process
// exit or presentation policy.
func Dispatch(args []string, handlers map[string]Handler) error {
	if len(args) == 0 {
		return ErrUsage
	}
	handler := handlers[args[0]]
	if handler == nil {
		return ErrUsage
	}
	return handler(args[1:])
}

// ParseRootFlag implements the common --root contract shared by language
// plugin inspect and freeze commands.
func ParseRootFlag(subcommand string, args []string) (string, error) {
	flags := flag.NewFlagSet(subcommand, flag.ContinueOnError)
	root := flags.String("root", "", "project root directory")
	if err := flags.Parse(args); err != nil {
		return "", err
	}
	if *root == "" {
		return "", errors.New("--root is required")
	}
	return *root, nil
}

func FileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func RunIn(directory, name string, args ...string) error {
	command := exec.Command(name, args...)
	command.Dir, command.Stdout, command.Stderr = directory, os.Stderr, os.Stderr
	return command.Run()
}

// ParseBuildLayerFlags implements the common build-layer CLI contract.
func ParseBuildLayerFlags(args []string) (root, output, dest string, err error) {
	flags := flag.NewFlagSet("build-layer", flag.ContinueOnError)
	flags.StringVar(&root, "root", "", "project root directory")
	flags.StringVar(&output, "output", "", "path to write the uncompressed tar layer to")
	flags.StringVar(&dest, "dest", "", "container path prefix every entry in the layer is rooted at")
	if err = flags.Parse(args); err != nil {
		return
	}
	if root == "" || output == "" || dest == "" {
		err = errors.New("--root, --output, and --dest are all required")
	}
	return
}

// BuildLayer validates the dependency directory and packages it using the
// deterministic layer format shared by every language plugin.
func BuildLayer(args []string, dependenciesPath, pluginName string) error {
	root, output, dest, err := ParseBuildLayerFlags(args)
	if err != nil {
		return err
	}
	source := filepath.Join(root, dependenciesPath)
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("%s does not exist - run `%s freeze` first: %w", dependenciesPath, pluginName, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dependenciesPath)
	}
	return WriteDeterministicTar(source, dest, output)
}

// WriteDeterministicTar tars every regular file and directory under
// source into output, with entry names rewritten from source-relative
// to destPrefix-prefixed, sorted, and every timestamp zeroed. Symlinks
// are rejected outright: package managers legitimately create them
// (e.g. npm's .bin/ wrapper scripts), and the host's own validation
// (internal/oci/extralayers.go) would reject them anyway - refusing
// here gives a clearer, earlier error.
func WriteDeterministicTar(source, destPrefix, output string) error {
	type entry struct {
		relPath string
		absPath string
		info    os.FileInfo
	}
	var entries []entry
	err := filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == source {
			return nil
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to package symlink %s (host-supplied layers may not contain them)", rel)
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return fmt.Errorf("refusing to package non-regular file %s (mode %s)", rel, info.Mode())
		}
		entries = append(entries, entry{relPath: filepath.ToSlash(rel), absPath: path, info: info})
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk %s: %w", source, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].relPath < entries[j].relPath })

	out, err := os.Create(output)
	if err != nil {
		return fmt.Errorf("create %s: %w", output, err)
	}
	defer out.Close()
	tw := tar.NewWriter(out)
	for _, e := range entries {
		name := filepath.ToSlash(filepath.Join(destPrefix, e.relPath))
		if e.info.IsDir() {
			name += "/"
		}
		header := &tar.Header{Name: name, Mode: int64(e.info.Mode().Perm())}
		if e.info.IsDir() {
			header.Typeflag = tar.TypeDir
		} else {
			header.Typeflag = tar.TypeReg
			header.Size = e.info.Size()
		}
		if err := tw.WriteHeader(header); err != nil {
			return fmt.Errorf("write header for %s: %w", name, err)
		}
		if !e.info.IsDir() {
			if err := copyFileInto(tw, e.absPath); err != nil {
				return fmt.Errorf("write content for %s: %w", name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return out.Close()
}

func copyFileInto(w *tar.Writer, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(w, file)
	return err
}
