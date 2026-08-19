package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunPipelineRunRejectsInvalidPipelineFile(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runPipeline([]string{"run", "--sandbox", "off", "/does/not/exist.json"}, &stdout, &stderr); code != 1 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(stderr.String(), "pipeline run") {
		t.Fatalf("stderr=%s", stderr.String())
	}
}
