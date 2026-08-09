package langplugin

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

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
