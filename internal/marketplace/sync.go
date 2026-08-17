package marketplace

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

// SyncResult reports what one repository sync found.
type SyncResult struct {
	Plugin      PluginEntry
	NewTags     []string // tags indexed for the first time this sync
	SkippedTags map[string]error
}

// SyncSource fetches every SemVer tag from repository that is not already
// indexed in existing, reads plugin.yaml at each new tag, and returns the
// resulting PluginEntry. Tags already present in existing are trusted and
// never re-fetched - re-syncing a repository is incremental by design.
// A tag whose plugin.yaml fails to parse or validate is skipped, not
// fatal to the whole sync, and reported in SkippedTags so the caller can
// surface it without losing the tags that did work.
func SyncSource(ctx context.Context, repository string, existing PluginEntry) (SyncResult, error) {
	return SyncSourceWithKeys(ctx, repository, existing, nil)
}

// SyncSourceWithKeys syncs a repository and marks signatures verified only
// when one of trustedKeys validates them.
func SyncSourceWithKeys(ctx context.Context, repository string, existing PluginEntry, trustedKeys []ed25519.PublicKey) (SyncResult, error) {
	if strings.TrimSpace(repository) == "" {
		return SyncResult{}, errors.New("marketplace: repository URL is required")
	}
	tags, err := listSemverTags(ctx, repository)
	if err != nil {
		return SyncResult{}, fmt.Errorf("marketplace: list tags for %s: %w", repository, err)
	}
	indexed := make(map[string]int, len(existing.Releases))
	for index, release := range existing.Releases {
		indexed[release.Tag] = index
	}

	result := SyncResult{Plugin: existing, SkippedTags: map[string]error{}}
	result.Plugin.Repository = repository
	for _, tag := range tags {
		index, alreadyIndexed := indexed[tag]
		if alreadyIndexed && (len(trustedKeys) == 0 || result.Plugin.Releases[index].Verified) {
			continue
		}
		release, manifest, err := fetchRelease(ctx, repository, tag, trustedKeys)
		if err != nil {
			result.SkippedTags[tag] = err
			continue
		}
		if alreadyIndexed {
			result.Plugin.Releases[index] = release
		} else {
			result.Plugin.Releases = append(result.Plugin.Releases, release)
			result.NewTags = append(result.NewTags, tag)
		}
		if result.Plugin.Name == "" {
			result.Plugin.Name = manifest.Name
		}
		if result.Plugin.Description == "" {
			result.Plugin.Description = manifest.Description
		}
		if result.Plugin.Author == "" {
			result.Plugin.Author = manifest.Author
		}
		if len(result.Plugin.Tags) == 0 {
			result.Plugin.Tags = manifest.Tags
		}
	}
	sort.Slice(result.Plugin.Releases, func(i, j int) bool {
		return semver.Compare(normalizeVersion(result.Plugin.Releases[i].Version), normalizeVersion(result.Plugin.Releases[j].Version)) < 0
	})
	if latest := latestVersion(result.Plugin.Releases); latest != "" {
		result.Plugin.LatestVersion = latest
	}
	result.Plugin.SyncedAt = time.Now().UTC()
	return result, nil
}

func latestVersion(releases []ReleaseEntry) string {
	latest := ""
	for _, release := range releases {
		if latest == "" || semver.Compare(normalizeVersion(release.Version), normalizeVersion(latest)) > 0 {
			latest = release.Version
		}
	}
	return latest
}

// gitEnv disables anything that could make a background sync hang waiting
// on a human: no credential/terminal prompts, no pager.
func gitEnv() []string {
	return append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "GIT_PAGER=cat", "GIT_CONFIG_NOSYSTEM=1")
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// listSemverTags lists every tag repository publishes that is valid
// SemVer, sorted ascending.
func listSemverTags(ctx context.Context, repository string) ([]string, error) {
	output, err := runGit(ctx, "", "ls-remote", "--tags", "--refs", repository)
	if err != nil {
		return nil, err
	}
	var tags []string
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		idx := strings.Index(line, "refs/tags/")
		if idx < 0 {
			continue
		}
		tag := strings.TrimSpace(line[idx+len("refs/tags/"):])
		if !tagPattern.MatchString(tag) {
			continue
		}
		if !semver.IsValid(normalizeVersion(tag)) {
			continue
		}
		tags = append(tags, tag)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.Slice(tags, func(i, j int) bool { return semver.Compare(normalizeVersion(tags[i]), normalizeVersion(tags[j])) < 0 })
	return tags, nil
}

// fetchRelease shallow-clones repository at exactly tag, reads and
// validates plugin.yaml, and hashes the entrypoint it declares.
func fetchRelease(ctx context.Context, repository, tag string, trustedKeys []ed25519.PublicKey) (ReleaseEntry, Manifest, error) {
	workdir, err := os.MkdirTemp("", "platform-factory-marketplace-sync-*")
	if err != nil {
		return ReleaseEntry{}, Manifest{}, err
	}
	defer os.RemoveAll(workdir)

	if _, err := runGit(ctx, "", "clone", "--depth", "1", "--branch", tag, "--single-branch", "--", repository, workdir); err != nil {
		return ReleaseEntry{}, Manifest{}, err
	}
	manifestPath := filepath.Join(workdir, ManifestFileName)
	file, err := os.Open(manifestPath)
	if err != nil {
		return ReleaseEntry{}, Manifest{}, fmt.Errorf("open %s at tag %s: %w", ManifestFileName, tag, err)
	}
	manifest, err := DecodeManifest(file)
	closeErr := file.Close()
	if err != nil {
		return ReleaseEntry{}, Manifest{}, err
	}
	if closeErr != nil {
		return ReleaseEntry{}, Manifest{}, closeErr
	}
	if normalizeVersion(manifest.Version) != normalizeVersion(tag) {
		return ReleaseEntry{}, Manifest{}, fmt.Errorf("plugin.yaml version %q does not match tag %q", manifest.Version, tag)
	}
	checksum, err := hashEntrypoint(workdir, manifest.Entrypoint)
	if err != nil {
		return ReleaseEntry{}, Manifest{}, err
	}
	publishedAt, err := tagCommitTime(ctx, workdir)
	if err != nil {
		return ReleaseEntry{}, Manifest{}, err
	}
	verified := manifest.Signature != nil && manifest.VerifySignature(trustedKeys) == nil
	return ReleaseEntry{
		Version:       manifest.Version,
		Tag:           tag,
		Checksum:      checksum,
		Compatibility: manifest.Compatibility,
		Permissions:   manifest.Permissions,
		Verified:      verified,
		PublishedAt:   publishedAt,
	}, manifest, nil
}

func tagCommitTime(ctx context.Context, workdir string) (time.Time, error) {
	output, err := runGit(ctx, workdir, "log", "-1", "--format=%cI")
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339, strings.TrimSpace(output))
}

// hashEntrypoint returns a deterministic sha256 digest of entrypoint
// (relative to root), whether it names a single file or a directory tree
// - a plugin.yaml declaring a Go/C# source directory as its entrypoint
// still gets one stable checksum covering everything inside it.
func hashEntrypoint(root, entrypoint string) (string, error) {
	full := filepath.Join(root, filepath.FromSlash(entrypoint))
	info, err := os.Lstat(full)
	if err != nil {
		return "", fmt.Errorf("entrypoint %q: %w", entrypoint, err)
	}
	hash := sha256.New()
	if info.Mode().IsRegular() {
		file, err := os.Open(full)
		if err != nil {
			return "", err
		}
		defer file.Close()
		if _, err := io.Copy(hash, file); err != nil {
			return "", err
		}
		return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
	}
	if !info.IsDir() {
		return "", fmt.Errorf("entrypoint %q is neither a regular file nor a directory", entrypoint)
	}
	var relPaths []string
	if err := filepath.WalkDir(full, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(full, path)
		if err != nil {
			return err
		}
		relPaths = append(relPaths, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		return "", err
	}
	sort.Strings(relPaths)
	for _, rel := range relPaths {
		file, err := os.Open(filepath.Join(full, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		fileHash := sha256.New()
		_, copyErr := io.Copy(fileHash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		fmt.Fprintf(hash, "%s  %s\n", hex.EncodeToString(fileHash.Sum(nil)), rel)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}
