// platform-factory-packager creates deterministic, relocatable release archives
// from an environment produced by scripts/local/bootstrap.sh - see
// internal/app/packager for the actual collect/pack/install logic.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/CYPT71/platform-factory/internal/app/packager"
)

func run(args []string) error {
	flags := flag.NewFlagSet("platform-factory-packager", flag.ContinueOnError)
	env := flags.String("env", "", "bootstrap environment")
	verify := flags.String("verify", "", "verify an extracted distribution without executing it")
	out := flags.String("out", "", "output archive")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *verify != "" {
		if *env != "" || *out != "" || flags.NArg() != 0 {
			return errors.New("--verify cannot be combined with packaging flags")
		}
		_, err := packager.Verify(*verify)
		return err
	}
	if *env == "" || *out == "" || flags.NArg() != 0 {
		return errors.New("usage: platform-factory-packager --env DIR --out ARCHIVE | --verify DIR")
	}
	return packager.Package(*env, *out)
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		if !strings.Contains(err.Error(), "flag provided") {
			fmt.Fprintln(os.Stderr, "platform-factory-packager:", err)
		}
		os.Exit(2)
	}
}
