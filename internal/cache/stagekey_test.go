package cache

import (
	"testing"

	api "github.com/CYPT71/platform-factory/internal/core"
)

func baseStageKeyInputs() StageKeyInputs {
	return StageKeyInputs{
		EngineVersion: "v0.1.0",
		Stage: api.Stage{
			ID:        "compile",
			DependsOn: []string{"source"},
			Command:   api.Command{Executable: "/bin/compiler", Args: []string{"build"}},
			Env:       map[string]string{"LANG": "C", "PATH": "/usr/bin"},
			Secrets:   []api.SecretReference{{ID: "registry-token", Target: "/run/secrets/token"}},
			Inputs:    []api.ArtifactReference{{Stage: "source", Name: "tree"}},
		},
		BaseDigest:   validDigest,
		InputDigests: []string{validDigest},
		Platform:     "linux/amd64",
	}
}

func mustStageKey(t *testing.T, in StageKeyInputs) string {
	t.Helper()
	key, err := StageKey(in)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestStageKeyIsStableAcrossCalls(t *testing.T) {
	in := baseStageKeyInputs()
	first := mustStageKey(t, in)
	second := mustStageKey(t, baseStageKeyInputs())
	if first != second {
		t.Fatalf("keys differ: %s vs %s", first, second)
	}
}

func TestStageKeyIgnoresIDAndDependsOn(t *testing.T) {
	in := baseStageKeyInputs()
	want := mustStageKey(t, in)

	in.Stage.ID = "different-id"
	if got := mustStageKey(t, in); got != want {
		t.Fatalf("changing ID changed the key: %s vs %s", got, want)
	}

	in.Stage.DependsOn = []string{"other", "stages"}
	if got := mustStageKey(t, in); got != want {
		t.Fatalf("changing DependsOn changed the key: %s vs %s", got, want)
	}
}

func TestStageKeyDependsOnSecretIdentityNotValue(t *testing.T) {
	// The API models a secret only by identity, so two runs differing
	// solely in the secret's value produce the same key. Changing the
	// declared secret ID, which selects a different secret, must change
	// the key.
	first := mustStageKey(t, baseStageKeyInputs())
	second := mustStageKey(t, baseStageKeyInputs())
	if first != second {
		t.Fatalf("identical secret identities produced different keys: %s vs %s", first, second)
	}
	changed := baseStageKeyInputs()
	changed.Stage.Secrets = []api.SecretReference{{ID: "other-token", Target: "/run/secrets/token"}}
	if got := mustStageKey(t, changed); got == first {
		t.Fatal("a different secret identity did not change the key")
	}
	targetOnly := baseStageKeyInputs()
	targetOnly.Stage.Secrets = []api.SecretReference{{ID: "registry-token", Target: "/run/secrets/elsewhere"}}
	if got := mustStageKey(t, targetOnly); got == first {
		t.Fatal("a different secret mount target did not change the key")
	}
}

func TestStageKeyChangesWithContent(t *testing.T) {
	base := mustStageKey(t, baseStageKeyInputs())

	cases := map[string]func(*StageKeyInputs){
		"engine version": func(in *StageKeyInputs) { in.EngineVersion = "v0.2.0" },
		"base digest":    func(in *StageKeyInputs) { in.BaseDigest = "sha256:" + "0" + validDigest[8:] },
		"platform":       func(in *StageKeyInputs) { in.Platform = "linux/arm64" },
		"command":        func(in *StageKeyInputs) { in.Stage.Command.Args = []string{"test"} },
		"env":            func(in *StageKeyInputs) { in.Stage.Env["LANG"] = "en_US" },
		"secret identity": func(in *StageKeyInputs) {
			in.Stage.Secrets = []api.SecretReference{{ID: "other-secret", Target: "/run/secrets/token"}}
		},
		"input digests order": func(in *StageKeyInputs) {
			in.InputDigests = append(in.InputDigests, validDigest)
			in.Stage.Inputs = append(in.Stage.Inputs,
				api.ArtifactReference{Stage: "source", Name: "metadata"})
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := baseStageKeyInputs()
			mutate(&in)
			if got := mustStageKey(t, in); got == base {
				t.Fatalf("expected key to change for %s mutation", name)
			}
		})
	}
}

func TestStageKeyInputDigestOrderIsSignificant(t *testing.T) {
	other := "sha256:" + "0" + validDigest[8:]

	inA := baseStageKeyInputs()
	inA.InputDigests = []string{validDigest, other}
	inA.Stage.Inputs = append(inA.Stage.Inputs,
		api.ArtifactReference{Stage: "source", Name: "metadata"})

	inB := baseStageKeyInputs()
	inB.InputDigests = []string{other, validDigest}
	inB.Stage.Inputs = append(inB.Stage.Inputs,
		api.ArtifactReference{Stage: "source", Name: "metadata"})

	if mustStageKey(t, inA) == mustStageKey(t, inB) {
		t.Fatal("expected digest-to-reference mapping to affect the key")
	}
}

func TestStageKeyCanonicalizesSemanticOrder(t *testing.T) {
	first := baseStageKeyInputs()
	first.Stage.Secrets = append(first.Stage.Secrets,
		api.SecretReference{ID: "second", Target: "/run/secrets/second"})
	first.Stage.Inputs = append(first.Stage.Inputs,
		api.ArtifactReference{Stage: "source", Name: "metadata"})
	first.InputDigests = append(first.InputDigests,
		"sha256:0cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824")
	second := first
	second.Stage.Secrets = []api.SecretReference{first.Stage.Secrets[1], first.Stage.Secrets[0]}
	second.Stage.Inputs = []api.ArtifactReference{first.Stage.Inputs[1], first.Stage.Inputs[0]}
	second.InputDigests = []string{first.InputDigests[1], first.InputDigests[0]}
	if mustStageKey(t, first) != mustStageKey(t, second) {
		t.Fatal("semantic collection order changed the stage key")
	}
}

func TestStageKeyRejectsInvalidInputs(t *testing.T) {
	for name, mutate := range map[string]func(*StageKeyInputs){
		"engine":   func(in *StageKeyInputs) { in.EngineVersion = "" },
		"base":     func(in *StageKeyInputs) { in.BaseDigest = "bad" },
		"platform": func(in *StageKeyInputs) { in.Platform = "windows/amd64" },
		"count":    func(in *StageKeyInputs) { in.InputDigests = nil },
		"input":    func(in *StageKeyInputs) { in.InputDigests[0] = "bad" },
	} {
		t.Run(name, func(t *testing.T) {
			in := baseStageKeyInputs()
			mutate(&in)
			if _, err := StageKey(in); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
