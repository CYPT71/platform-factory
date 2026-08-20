package plugins

import (
	"context"
	"encoding/json"
	"testing"
)

func TestSchemaResourceHandlerReturnsValidJSONSchema(t *testing.T) {
	body, mimeType, err := SchemaResourceHandler()(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mimeType != "application/json" {
		t.Fatalf("mimeType=%q, want application/json", mimeType)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("SchemaResourceHandler body is not valid JSON: %v", err)
	}
}
