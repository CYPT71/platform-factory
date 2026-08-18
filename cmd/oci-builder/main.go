// oci-builder creates a secure OCI Image Layout from a compiled Go binary.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/CYPT71/platform-factory/internal/layout"
	"github.com/CYPT71/platform-factory/oci"
)

func Run(args []string) int { return run(args, os.Stdout, os.Stderr) }
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "compose" {
		return runCompose(args[1:], stdout, stderr)
	}
	traceID := os.Getenv("PLATFORM_FACTORY_TRACE_ID")
	if traceID == "" {
		var id [16]byte
		if _, err := rand.Read(id[:]); err == nil {
			traceID = hex.EncodeToString(id[:])
		} else {
			traceID = fmt.Sprintf("local-%d", time.Now().UnixNano())
		}
	}
	logEvent := func(level, phase, message string, fields map[string]any) {
		event := map[string]any{
			"time": time.Now().UTC().Format(time.RFC3339Nano), "level": level,
			"component": "cmd/oci-builder", "operation": "build",
			"phase": phase, "trace_id": traceID, "message": message,
		}
		for key, value := range fields {
			event[key] = value
		}
		_ = json.NewEncoder(stderr).Encode(event)
	}
	observer := func(event oci.Event) {
		fields := map[string]any{
			"time": event.Time.Format(time.RFC3339Nano), "level": event.Level,
			"component": event.Component, "operation": event.Operation,
			"phase": event.Phase, "trace_id": event.TraceID,
			"message": event.Message,
		}
		if event.Duration > 0 {
			fields["duration_ms"] = event.Duration.Milliseconds()
		}
		for key, value := range event.Fields {
			fields[key] = value
		}
		_ = json.NewEncoder(stderr).Encode(fields)
	}
	fs := flag.NewFlagSet("oci-builder", flag.ContinueOnError)
	fs.SetOutput(stderr)
	binary := fs.String("binary", "", "path to statically compiled executable")
	output := fs.String("output", "", "new OCI layout directory")
	arch := fs.String("arch", "", "OCI architecture (default: host architecture)")
	osName := fs.String("os", "linux", "OCI operating system")
	entrypoint := fs.String("entrypoint", "/app/service", "absolute command path in the image")
	profile := fs.String("profile", "static", "runtime profile: static, glibc, musl, python, node, java, or dotnet")
	configFile := fs.String("config", "", "strict declarative runtime configuration (JSON)")
	image := fs.String("image", "platform-factory", "image name annotation")
	tag := fs.String("tag", "latest", "image tag annotation")
	compression := fs.String("compression", "best", "gzip mode: best or fast (use fast for very large images)")
	created := fs.String("created", "1970-01-01T00:00:00Z", "RFC3339 creation time")
	var labels labelFlags
	fs.Var(&labels, "label", "image label (key=value); repeatable")
	var extraFiles labelFlags
	fs.Var(&extraFiles, "extra-file", "additional file (/container/path=host/path); repeatable - for a dynamically-linked or multi-file binary, one entry per shared library plus the dynamic linker (see scripts/local/package-dynamic-binary.sh)")
	if len(args) == 0 {
		fs.SetOutput(stdout)
		fs.PrintDefaults()
		return 0
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	createdAt, err := time.Parse(time.RFC3339, *created)
	if err != nil {
		logEvent("error", "parse-arguments", "invalid creation timestamp", map[string]any{"error": err.Error()})
		fmt.Fprintf(stderr, "invalid -created: %v\n", err)
		return 2
	}
	parsedLabels, err := oci.LabelsFromPairs(labels)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	parsedExtraFiles, err := oci.ExtraFilesFromPairs(extraFiles)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	var runtimeConfig oci.BuildConfig
	if *configFile != "" {
		runtimeConfig, err = oci.LoadBuildConfig(*configFile)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		*entrypoint = runtimeConfig.Entrypoint
		if runtimeConfig.Profile != "" {
			*profile = runtimeConfig.Profile
		}
	}
	for _, systemFile := range []struct {
		destination string
		source      string
	}{
		{"/etc/ssl/certs/ca-certificates.crt", runtimeConfig.SystemFiles.CACertificates},
		{"/etc/localtime", runtimeConfig.SystemFiles.Timezone},
		{"/usr/lib/locale/locale-archive", runtimeConfig.SystemFiles.LocaleArchive},
	} {
		if systemFile.source != "" {
			parsedExtraFiles = append(parsedExtraFiles, oci.ExtraFile{
				Dest: systemFile.destination, Source: systemFile.source, Mode: 0444,
			})
		}
	}
	logEvent("info", "start", "OCI layout build started", map[string]any{
		"architecture": *arch, "os": *osName, "image_ref": *image + ":" + *tag,
	})
	digest, err := oci.Build(oci.Options{
		Binary: *binary, Output: *output, Architecture: *arch, OS: *osName,
		Entrypoint: *entrypoint, ImageName: *image, Tag: *tag, Created: createdAt,
		Labels: parsedLabels, ExtraFiles: parsedExtraFiles,
		Args: runtimeConfig.Args, WorkingDir: runtimeConfig.WorkingDir,
		Profile: *profile,
		Env:     runtimeConfig.Env, User: runtimeConfig.User, Ports: runtimeConfig.Ports,
		Home: runtimeConfig.Home, IdentityFiles: runtimeConfig.IdentityFiles,
		Volumes: runtimeConfig.Volumes, WritablePaths: runtimeConfig.WritablePaths,
		Healthcheck: runtimeConfig.Healthcheck,
		Compression: *compression,
		TraceID:     traceID, Observer: observer,
	})
	if err != nil {
		logEvent("error", "complete", "OCI layout build failed", map[string]any{"error": err.Error()})
		fmt.Fprintf(stderr, "oci-builder: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "created OCI layout %s (%s)\n", *output, digest)
	return 0
}

func runCompose(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("oci-builder compose", flag.ContinueOnError)
	fs.SetOutput(stderr)
	output := fs.String("output", "", "new composed OCI layout directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *output == "" || fs.NArg() < 2 {
		fmt.Fprintln(stderr, "usage: oci-builder compose -output LAYOUT INPUT_LAYOUT INPUT_LAYOUT [...]")
		return 2
	}
	report, err := layout.Compose(*output, fs.Args())
	if err != nil {
		fmt.Fprintf(stderr, "oci-builder compose: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "created OCI layout %s (%d manifests, %d blobs)\n", *output, report.Manifests, report.Blobs)
	return 0
}

type labelFlags []string

func (l *labelFlags) String() string         { return "" }
func (l *labelFlags) Set(value string) error { *l = append(*l, value); return nil }
func main()                                  { os.Exit(Run(os.Args[1:])) }
