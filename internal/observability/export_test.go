package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSpanTagAndEventRedactSecrets(t *testing.T) {
	span := StartSpan("deploy")
	AddSpanTag(span, "authorization", "Bearer super-secret-token")
	AddSpanTag(span, "registry_password", "hunter2")
	AddSpanTag(span, "trace_step", "publish")
	AddSpanEvent(span, "auth", map[string]any{
		"api_token": "tok_12345",
		"host":      "registry.example",
	})
	if span.Tags["authorization"] != redactedValue {
		t.Fatalf("authorization tag leaked: %v", span.Tags["authorization"])
	}
	if span.Tags["registry_password"] != redactedValue {
		t.Fatalf("password tag leaked: %v", span.Tags["registry_password"])
	}
	if span.Tags["trace_step"] != "publish" {
		t.Fatalf("non-sensitive tag altered: %v", span.Tags["trace_step"])
	}
	if len(span.Events) != 1 || span.Events[0].Tags["api_token"] != redactedValue || span.Events[0].Tags["host"] != "registry.example" {
		t.Fatalf("span event redaction failed: %+v", span.Events)
	}
}

func TestFormatPrometheusAndMetricsHandler(t *testing.T) {
	reg := newDefaultMetricsRegistry()
	if err := reg.RegisterCounter("pf_builds_total", "Total builds"); err != nil {
		t.Fatal(err)
	}
	if err := reg.IncBy("pf_builds_total", 3); err != nil {
		t.Fatal(err)
	}
	handler := MetricsHandler(reg, func() []Metric {
		return []Metric{{
			Name:        "pf_active_workers",
			Type:        MetricTypeGauge,
			Value:       2,
			Labels:      map[string]string{"tenant": "alpha", "secret_token": "must-not-leak"},
			Description: "Active workers",
		}}
	})

	// Prometheus text format
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"# HELP pf_builds_total Total builds",
		"# TYPE pf_builds_total counter",
		"pf_builds_total 3",
		"# TYPE pf_active_workers gauge",
		`pf_active_workers{secret_token="[redacted]",tenant="alpha"} 2`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in metrics output:\n%s", want, body)
		}
	}
	if strings.Contains(body, "must-not-leak") {
		t.Fatalf("secret leaked in metrics output:\n%s", body)
	}

	// JSON format
	reqJSON := httptest.NewRequest(http.MethodGet, "/metrics?format=json", nil)
	recJSON := httptest.NewRecorder()
	handler.ServeHTTP(recJSON, reqJSON)
	if recJSON.Code != http.StatusOK || !strings.Contains(recJSON.Body.String(), "pf_builds_total") {
		t.Fatalf("json status=%d body=%s", recJSON.Code, recJSON.Body.String())
	}
}

func TestSanitizePromNameReplacesDisallowedCharactersAndLeadingDigits(t *testing.T) {
	cases := map[string]string{
		"pf_builds_total":  "pf_builds_total",
		"pf.builds-total":  "pf_builds_total",
		"pf:builds_total":  "pf:builds_total",
		"1st_metric":       "_1st_metric",
		"metric with spaces": "metric_with_spaces",
	}
	for input, want := range cases {
		if got := sanitizePromName(input); got != want {
			t.Errorf("sanitizePromName(%q) = %q, want %q", input, got, want)
		}
	}
}
