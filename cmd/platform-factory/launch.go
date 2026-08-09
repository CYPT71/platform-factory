package main

import (
	"fmt"
	"io"
	"strings"
)

func runLaunch(args []string, stdout, stderr io.Writer, containerExecute containerExecutor, microVMExecute microVMExecutor) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: platform-factory launch --isolation=<container|microvm> [OPTIONS]")
		return 2
	}
	var isolation string
	remaining := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		if strings.HasPrefix(args[index], "--isolation=") {
			if isolation != "" {
				fmt.Fprintln(stderr, "platform-factory launch: isolation may be specified only once")
				return 2
			}
			isolation = strings.TrimPrefix(args[index], "--isolation=")
			continue
		}
		if args[index] == "--isolation" {
			if isolation != "" || index+1 >= len(args) {
				fmt.Fprintln(stderr, "platform-factory launch: --isolation requires one value")
				return 2
			}
			isolation = args[index+1]
			index++
			continue
		}
		remaining = append(remaining, args[index])
	}
	switch isolation {
	case "container":
		return runContainer(remaining, stdout, stderr, containerExecute)
	case "microvm":
		return runMicroVM(append([]string{"run"}, remaining...), stdout, stderr, microVMExecute)
	default:
		fmt.Fprintln(stderr, "platform-factory launch: isolation must be container or microvm")
		return 2
	}
}

func hasIsolationFlag(args []string) bool {
	for _, value := range args {
		if value == "--isolation" || strings.HasPrefix(value, "--isolation=") {
			return true
		}
	}
	return false
}
