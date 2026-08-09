package pipeline

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestDecodeStrictValidatedPipeline(t *testing.T) {
	data, err := json.Marshal(validPipeline())
	if err != nil {
		t.Fatal(err)
	}
	definition, graph, err := Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if definition.Name != "example" || len(graph.Order) != 3 {
		t.Fatalf("definition=%+v graph=%+v", definition, graph)
	}
}

func TestDecodeRejectsUnknownTrailingInvalidAndOversizedData(t *testing.T) {
	valid, err := json.Marshal(validPipeline())
	if err != nil {
		t.Fatal(err)
	}
	unknown := bytes.Replace(valid, []byte(`"name":"example"`),
		[]byte(`"name":"example","unknown":true`), 1)
	invalidDefinition := validPipeline()
	invalidDefinition.APIVersion = "invalid"
	invalid, _ := json.Marshal(invalidDefinition)
	for name, data := range map[string][]byte{
		"unknown":   unknown,
		"trailing":  append(append([]byte(nil), valid...), []byte(` {}`)...),
		"invalid":   invalid,
		"oversized": bytes.Repeat([]byte(" "), maxDefinitionBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := Decode(bytes.NewReader(data)); err == nil {
				t.Fatal("expected decode error")
			}
		})
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read failure") }

func TestDecodeReportsReadAndSyntaxFailures(t *testing.T) {
	if _, _, err := Decode(failingReader{}); err == nil || !strings.Contains(err.Error(), "read pipeline") {
		t.Fatalf("err=%v", err)
	}
	if _, _, err := Decode(strings.NewReader("{")); err == nil || !strings.Contains(err.Error(), "decode pipeline") {
		t.Fatalf("err=%v", err)
	}
}

func TestDecodeAcceptsReaderEndingWithEOF(t *testing.T) {
	data, _ := json.Marshal(validPipeline())
	reader := io.MultiReader(bytes.NewReader(data), strings.NewReader(""))
	if _, _, err := Decode(reader); err != nil {
		t.Fatal(err)
	}
}
