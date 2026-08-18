package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"

	observeapp "github.com/CYPT71/platform-factory/internal/app/observe"
)

func runProjectObservation(command string, args []string, stdout, stderr io.Writer, execute containerExecutor) int {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	follow := flags.Bool("follow", false, "follow log output")
	tail := flags.Int("tail", 200, "maximum initial log lines")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || *tail < 1 || (command == "events" && *follow) {
		fmt.Fprintf(stderr, "usage: platform-factory %s [--tail N]", command)
		if command == "logs" {
			fmt.Fprint(stderr, " [--follow]")
		}
		fmt.Fprintln(stderr)
		return 2
	}
	state, err := observeapp.LoadDeployedProject()
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory %s: no deployed project (run `pf deploy` first): %v\n", command, err)
		return 1
	}
	var runtimeArgs []string
	if command == "logs" {
		resource := "deployment/" + state.Name
		if state.Workload == "job" {
			resource = "job/" + state.Name
		}
		runtimeArgs = []string{"logs", resource, "--namespace", state.Namespace, "--tail", strconv.Itoa(*tail)}
		if *follow {
			runtimeArgs = append(runtimeArgs, "--follow")
		}
	} else {
		runtimeArgs = []string{"get", "events", "--namespace", state.Namespace,
			"--field-selector", "involvedObject.name=" + state.Name, "--sort-by=.lastTimestamp"}
	}
	if err := execute("kubectl", runtimeArgs, nil, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "platform-factory %s: %v\n", command, err)
		return 1
	}
	return 0
}
