package provenance

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CYPT71/platform-factory/internal/signing"
)

func validPluginInputs() PluginBuildInputs {
	return PluginBuildInputs{
		PluginName: "kubevirt", PluginVersion: "1.0.0",
		ArtifactDigest: "sha256:" + strings.Repeat("a", 64),
		SourceCommit:   strings.Repeat("b", 40),
		ModulePath:     "github.com/CYPT71/platform-factory/plugins/kubevirt",
		GoVersion:      "go1.25.12", BuilderID: "https://platform-factory.dev/builder/v1",
	}
}

func TestGeneratePluginProvenanceAssociatesSourceBuilderArtifact(t *testing.T) {
	inputs := validPluginInputs()
	record, err := GeneratePluginProvenance(inputs)
	if err != nil {
		t.Fatal(err)
	}
	if record.ArtifactID != inputs.ArtifactDigest {
		t.Fatalf("ArtifactID=%q, want %q", record.ArtifactID, inputs.ArtifactDigest)
	}
	if record.WorkerID != inputs.BuilderID {
		t.Fatalf("WorkerID=%q, want %q", record.WorkerID, inputs.BuilderID)
	}
	if record.BuildID != inputs.PluginName+"@"+inputs.ArtifactDigest {
		t.Fatalf("BuildID=%q", record.BuildID)
	}
	if len(record.Materials) != 1 || record.Materials[0].Digest != inputs.SourceCommit {
		t.Fatalf("Materials=%+v, want source commit %q", record.Materials, inputs.SourceCommit)
	}
	if record.Invocation.ConfigSource.Digest != inputs.SourceCommit {
		t.Fatalf("ConfigSource=%+v", record.Invocation.ConfigSource)
	}
	if record.Timestamp.IsZero() {
		t.Fatal("Timestamp is zero")
	}
}

func TestGeneratePluginProvenanceRejectsIncompleteInputs(t *testing.T) {
	base := validPluginInputs()
	cases := []func(*PluginBuildInputs){
		func(i *PluginBuildInputs) { i.PluginName = "" },
		func(i *PluginBuildInputs) { i.ArtifactDigest = "" },
		func(i *PluginBuildInputs) { i.ArtifactDigest = "not-a-digest" },
		func(i *PluginBuildInputs) { i.SourceCommit = "" },
		func(i *PluginBuildInputs) { i.BuilderID = "" },
	}
	for index, mutate := range cases {
		inputs := base
		mutate(&inputs)
		if _, err := GeneratePluginProvenance(inputs); err == nil {
			t.Fatalf("case %d: accepted incomplete inputs %+v", index, inputs)
		}
	}
}

func hasGit(t *testing.T) bool {
	t.Helper()
	_, err := exec.LookPath("git")
	return err == nil
}

func initGitRepoWithCommit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "main.go")
	run("commit", "-q", "-m", "initial")
	return dir
}

// TestCapturePluginSourceCommit proves this against a real, freshly
// created git repository - not a stubbed git binary - so it exercises the
// exact commands (git rev-parse HEAD, git status --porcelain) a real build
// environment would run.
func TestCapturePluginSourceCommit(t *testing.T) {
	if !hasGit(t) {
		t.Skip("git not available")
	}
	dir := initGitRepoWithCommit(t)

	commit, dirty, err := CapturePluginSourceCommit(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(commit) != 40 {
		t.Fatalf("commit=%q, want a 40-character sha1", commit)
	}
	if dirty {
		t.Fatal("clean tree reported dirty")
	}

	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n// changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, dirty, err = CapturePluginSourceCommit(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !dirty {
		t.Fatal("modified tree not reported dirty")
	}
}

func TestCapturePluginSourceCommitFailsClosedWithoutGitHistory(t *testing.T) {
	if !hasGit(t) {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	if _, _, err := CapturePluginSourceCommit(dir); err == nil {
		t.Fatal("accepted a directory with no git history")
	}
}

func TestDigestPluginExecutableMatchesSHA256(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugin-binary")
	if err := os.WriteFile(path, []byte("fake plugin executable bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	digest, err := DigestPluginExecutable(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(digest, "sha256:") || len(digest) != 71 {
		t.Fatalf("digest=%q", digest)
	}
	again, err := DigestPluginExecutable(path)
	if err != nil || again != digest {
		t.Fatalf("digest not stable: %q vs %q (err=%v)", digest, again, err)
	}
}

func TestSignAndVerifyPluginProvenance(t *testing.T) {
	store, err := signing.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record, err := GeneratePluginProvenance(validPluginInputs())
	if err != nil {
		t.Fatal(err)
	}
	signed, err := SignPluginProvenance(record, store, "plugin-provenance")
	if err != nil {
		t.Fatal(err)
	}
	if signed.Signature == "" || signed.SignedBy != "plugin-provenance" {
		t.Fatalf("signed=%+v", signed)
	}
	if err := VerifyPluginProvenance(signed, store, "plugin-provenance"); err != nil {
		t.Fatalf("verify: %v", err)
	}

	// Tampering with any signed field must invalidate the signature.
	tampered := signed
	tampered.ArtifactID = "sha256:" + strings.Repeat("f", 64)
	if err := VerifyPluginProvenance(tampered, store, "plugin-provenance"); err == nil {
		t.Fatal("verified a tampered record")
	}

	// A different key must not verify it either.
	otherStore, err := signing.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPluginProvenance(signed, otherStore, "plugin-provenance"); err == nil {
		t.Fatal("verified against the wrong key")
	}
}

func TestVerifyPluginProvenanceRejectsUnsignedRecord(t *testing.T) {
	store, err := signing.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record, err := GeneratePluginProvenance(validPluginInputs())
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPluginProvenance(record, store, "plugin-provenance"); err == nil {
		t.Fatal("verified an unsigned record")
	}
}
