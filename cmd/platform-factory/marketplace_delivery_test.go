package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/CYPT71/platform-factory/internal/marketplace"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestMarketplacePublishesTaggedReleaseAndRetriesIdempotently(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=CYPT71", "-c", "user.email=cyprientaib1@gmail.com"}, args...)...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-b", "main")
	origin := "https://203.0.113.10/acme/plugin.git"
	git("remote", "add", "origin", origin)
	manifest := "api_version: " + marketplace.ManifestAPIVersion + "\nname: acme\nversion: v1.0.0\nentrypoint: plugin.py\n"
	if err := os.WriteFile(filepath.Join(root, "plugin.yaml"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "plugin.py"), []byte("print('ok')\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "fixture")
	git("tag", "v1.0.0")
	catalog := marketplace.Catalog{Schema: marketplace.CatalogSchema, Repositories: []string{}}
	puts := 0
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"v1"`)
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode(catalog)
		case http.MethodPut:
			if r.Header.Get("If-Match") != `"v1"` {
				t.Error("catalog update lost conditional write")
			}
			if err := json.NewDecoder(r.Body).Decode(&catalog); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			puts++
			w.WriteHeader(204)
		default:
			w.WriteHeader(405)
		}
	}))
	defer server.Close()
	for _, format := range []string{"json", "text"} {
		var out, diagnostics bytes.Buffer
		code := runMarketplacePublish([]string{"--dir", root, "--catalog-url", server.URL, "--format", format}, &out, &diagnostics)
		if code != 0 {
			t.Fatalf("publish=%d: %s", code, &diagnostics)
		}
		if format == "json" {
			var result map[string]any
			if err := json.Unmarshal(out.Bytes(), &result); err != nil || result["status"] != "published" || result["repository"] != origin {
				t.Fatalf("publish result=%s", &out)
			}
		} else if !strings.Contains(out.String(), "already listed") {
			t.Fatalf("retry was not idempotent: %s", &out)
		}
	}
	mu.Lock()
	if puts != 1 || len(catalog.Repositories) != 1 || catalog.Repositories[0] != origin {
		t.Fatalf("unexpected catalog mutations: %d %+v", puts, catalog)
	}
	mu.Unlock()
	var out, diagnostics bytes.Buffer
	if code := runMarketplacePublish([]string{"--dir", root, "--catalog-url", server.URL, "--format", "json"}, &out, &diagnostics); code != 0 || !strings.Contains(out.String(), "already_listed") {
		t.Fatal("JSON retry status missing")
	}
}

type refusedOutputWriter struct{}

func (refusedOutputWriter) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }
func TestMarketplaceMachineOutputReportsWriteFailures(t *testing.T) {
	var diagnostics bytes.Buffer
	for name, write := range map[string]func(io.Writer) int{
		"publish": func(w io.Writer) int {
			return writeMarketplacePublishJSON(w, &diagnostics, "repo", "plugin", "v1.0.0", "catalog", "published")
		},
		"mutation": func(w io.Writer) int {
			return writeMarketplaceMutationJSON(w, &diagnostics, "install", "plugin", "v1.0.0", "installed")
		},
		"sync": func(w io.Writer) int { return writeMarketplaceSyncJSON(w, &diagnostics, 1, 1, 0, 0, 1, nil) },
	} {
		if code := write(refusedOutputWriter{}); code != 1 {
			t.Fatalf("%s hid output failure", name)
		}
	}
	var out bytes.Buffer
	if code := writeMarketplaceSyncJSON(&out, &diagnostics, 0, 2, 0, 0, 0, map[string]error{"z": errors.New("failed-z"), "a": errors.New("failed-a")}); code != 1 {
		t.Fatal("source failures did not fail sync")
	}
	if strings.Index(out.String(), `"a"`) > strings.Index(out.String(), `"z"`) {
		t.Fatal("failure output not deterministic")
	}
}
