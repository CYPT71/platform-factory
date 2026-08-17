package main

import (
	"fmt"
	"log"
	"os"

	pipeline "github.com/CYPT71/platform-factory/api/pipeline/v1"
)

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: pipeline-sdk PIPELINE.json")
	}
	input, err := os.Open(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	defer input.Close()
	definition, graph, err := pipeline.Decode(input)
	if err != nil {
		log.Fatal(err)
	}
	fingerprint, err := pipeline.Fingerprint(definition)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("name=%s\norder=%v\nfingerprint=%s\n", definition.Name, graph.Order, fingerprint)
}
