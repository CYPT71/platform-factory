// platform-factory-conformance runs the public conformance suite. The golden
// vectors are embedded, so the binary proves API and plugin protocol
// compatibility outside this repository:
//
//	platform-factory-conformance vectors [DIR]     pipeline API vectors
//	platform-factory-conformance plugin EXECUTABLE plugin protocol checks
//	platform-factory-conformance backend [DIR]     execution backend vectors
//
// DIR, when given, must contain a vectors/ (or, for backend,
// vectors-backend/) directory replacing the embedded corpus. Exit code 0
// means every check passed; 1 means at least one failed; 2 marks usage
// errors.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/CYPT71/secure-oci-base/conformance"
)

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: platform-factory-conformance <vectors [DIR] | plugin EXECUTABLE | backend [DIR]>")
		return 2
	}
	var results []conformance.Result
	var err error
	switch args[0] {
	case "vectors":
		var corpus fs.FS = conformance.EmbeddedVectors()
		if len(args) == 2 {
			corpus = os.DirFS(args[1])
		} else if len(args) > 2 {
			fmt.Fprintln(stderr, "usage: platform-factory-conformance vectors [DIR]")
			return 2
		}
		results, err = conformance.RunVectors(corpus)
	case "plugin":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "usage: platform-factory-conformance plugin EXECUTABLE")
			return 2
		}
		results, err = conformance.RunPlugin(context.Background(), args[1])
	case "backend":
		var corpus fs.FS = conformance.EmbeddedBackendVectors()
		if len(args) == 2 {
			corpus = os.DirFS(args[1])
		} else if len(args) > 2 {
			fmt.Fprintln(stderr, "usage: platform-factory-conformance backend [DIR]")
			return 2
		}
		results, err = conformance.RunBackend(corpus)
	default:
		fmt.Fprintf(stderr, "platform-factory-conformance: unknown command %q\n", args[0])
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory-conformance: %v\n", err)
		return 2
	}
	failed := 0
	for _, result := range results {
		if !result.Passed {
			failed++
		}
	}
	report, _ := json.MarshalIndent(map[string]any{
		"checks": results, "failed": failed, "passed": len(results) - failed,
	}, "", "  ")
	fmt.Fprintln(stdout, string(report))
	if failed > 0 {
		return 1
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
