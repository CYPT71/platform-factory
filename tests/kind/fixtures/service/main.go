// A network-only acceptance fixture: no filesystem, credentials or external API.
package main

import (
	"fmt"
	"log"
	"net/http"
	"time"
)

var revision = "unset"

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, revision) })
	log.Printf("acceptance service revision=%s", revision)
	server := &http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(server.ListenAndServe())
}
