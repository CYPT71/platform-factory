// platform-factory-toolkit is copied with each extension's executable name.
// Identity comes from that signed filename, never from request parameters.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(os.Args[0]), "platform-factory-"), ".exe")
	server, err := newServer(name)
	if err == nil {
		err = server.Serve(context.Background(), os.Stdin, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
