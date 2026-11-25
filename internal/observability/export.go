package observability

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// FormatPrometheus renders metrics in deterministic Prometheus text exposition
// format (version 0.0.4), sanitizing metric names and escaping label values.
func FormatPrometheus(metrics []Metric) string {
	sorted := append([]Metric(nil), metrics...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Name != sorted[j].Name {
			return sorted[i].Name < sorted[j].Name
		}
		return sorted[i].Type < sorted[j].Type
	})
	var b strings.Builder
	emittedHeader := map[string]bool{}
	for _, m := range sorted {
		name := sanitizePromName(m.Name)
		if name == "" {
			continue
		}
		if !emittedHeader[name] {
			emittedHeader[name] = true
			if m.Description != "" {
				fmt.Fprintf(&b, "# HELP %s %s\n", name, escapePromHelp(m.Description))
			}
			promType := "gauge"
			if m.Type == MetricTypeCounter {
				promType = "counter"
			}
			fmt.Fprintf(&b, "# TYPE %s %s\n", name, promType)
		}
		b.WriteString(name)
		if len(m.Labels) > 0 {
			keys := make([]string, 0, len(m.Labels))
			for k := range m.Labels {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			b.WriteByte('{')
			for idx, k := range keys {
				if idx > 0 {
					b.WriteByte(',')
				}
				val := fmt.Sprint(redactField(k, m.Labels[k]))
				fmt.Fprintf(&b, "%s=%q", sanitizePromName(k), val)
			}
			b.WriteByte('}')
		}
		b.WriteByte(' ')
		b.WriteString(strconv.FormatFloat(m.Value, 'g', -1, 64))
		b.WriteByte('\n')
	}
	return b.String()
}

// MetricsHandler exposes registered metrics plus optional dynamic gauges via
// Prometheus text exposition (default) or JSON (?format=json).
func MetricsHandler(registry MetricsRegistry, extra func() []Metric) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var all []Metric
		if registry != nil {
			all = append(all, registry.GetMetrics()...)
		}
		if extra != nil {
			all = append(all, extra()...)
		}
		if r.URL.Query().Get("format") == "json" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(all)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(FormatPrometheus(all)))
	})
}

func sanitizePromName(raw string) string {
	var b strings.Builder
	for i, r := range raw {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || r == ':':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			if i == 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func escapePromHelp(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}
