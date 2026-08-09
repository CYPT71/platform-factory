//go:build !linux

package main

import "io"

func reapExitedChildren(io.Writer) int {
	return 0
}
