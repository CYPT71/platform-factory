// Package extensions implements offline policy analyzers shared by fifty signed
// plugins. Inputs are supplied JSON, never paths to files on the plugin host.
package extensions

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

//go:embed catalog.json
var catalogJSON []byte

const MaxInputBytes = 1 << 20
const MaxFindings = 100

type Check struct {
	Path    string `json:"path"`
	Op      string `json:"op"`
	Value   any    `json:"value"`
	Message string `json:"message"`
}
type Profile struct {
	Name            string         `json:"name"`
	Category        string         `json:"category"`
	Description     string         `json:"description"`
	Source          string         `json:"source"`
	Checks          []Check        `json:"checks"`
	Example         map[string]any `json:"example"`
	RejectedExample map[string]any `json:"rejected_example"`
}
type Finding struct {
	Rule    string `json:"rule"`
	Path    string `json:"path"`
	Message string `json:"message"`
}
type Report struct {
	APIVersion string    `json:"api_version"`
	Plugin     string    `json:"plugin"`
	Passed     bool      `json:"passed"`
	Truncated  bool      `json:"truncated,omitempty"`
	Checks     int       `json:"checks"`
	Findings   []Finding `json:"findings"`
}

func Catalog() []Profile {
	var profiles []Profile
	if err := json.Unmarshal(catalogJSON, &profiles); err != nil {
		panic(err)
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	return profiles
}

// Summary leaves rules and examples out of routine discovery responses.
type Summary struct {
	Name        string `json:"name"`
	Category    string `json:"category"`
	Description string `json:"description"`
}

func Summaries() []Summary {
	rows := []Summary{}
	for _, p := range Catalog() {
		rows = append(rows, Summary{p.Name, p.Category, p.Description})
	}
	return rows
}

func Lookup(name string) (Profile, error) {
	for _, p := range Catalog() {
		if p.Name == name {
			return p, nil
		}
	}
	return Profile{}, fmt.Errorf("unknown extension %q", name)
}

// DecodeDocument bounds memory and rejects non-object or trailing inputs.
func DecodeDocument(r io.Reader) (map[string]any, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxInputBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxInputBytes {
		return nil, errors.New("extension document exceeds 1 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	var document map[string]any
	if err := d.Decode(&document); err != nil {
		return nil, errors.New("extension input must be a JSON object")
	}
	if document == nil {
		return nil, errors.New("extension input must be a JSON object")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, errors.New("extension input must contain one JSON object")
	}
	return document, nil
}

type match struct {
	path   string
	value  any
	exists bool
}

func escape(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1") }

// selectPath implements JSON pointers plus array/object wildcards. Missing and
// empty wildcard branches remain visible, so empty inventories cannot pass.
func selectPath(document any, pointer string) []match {
	parts := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	current := []match{{"", document, true}}
	for _, part := range parts {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		next := []match{}
		for _, m := range current {
			path := m.path + "/" + escape(part)
			if part == "*" {
				switch value := m.value.(type) {
				case []any:
					for i, v := range value {
						next = append(next, match{m.path + "/" + strconv.Itoa(i), v, true})
					}
				case map[string]any:
					keys := make([]string, 0, len(value))
					for k := range value {
						keys = append(keys, k)
					}
					sort.Strings(keys)
					for _, k := range keys {
						next = append(next, match{m.path + "/" + escape(k), value[k], true})
					}
				}
				if len(next) == 0 || (len(next) > 0 && !hasBranch(next, m.path)) {
					next = append(next, match{path, nil, false})
				}
			} else {
				switch value := m.value.(type) {
				case map[string]any:
					v, ok := value[part]
					next = append(next, match{path, v, m.exists && ok})
				case []any:
					i, err := strconv.Atoi(part)
					if err == nil && i >= 0 && i < len(value) {
						next = append(next, match{path, value[i], m.exists})
					} else {
						next = append(next, match{path, nil, false})
					}
				default:
					next = append(next, match{path, nil, false})
				}
			}
		}
		current = next
	}
	return current
}
func hasBranch(ms []match, prefix string) bool {
	for _, m := range ms {
		if strings.HasPrefix(m.path, prefix+"/") {
			return true
		}
	}
	return false
}
func equal(a, b any) bool { x, _ := json.Marshal(a); y, _ := json.Marshal(b); return bytes.Equal(x, y) }
func length(v any) int {
	switch x := v.(type) {
	case []any:
		return len(x)
	case map[string]any:
		return len(x)
	case string:
		return len(x)
	}
	return -1
}
func valid(c Check, m match, document map[string]any) bool {
	if c.Op == "absent" {
		return !m.exists
	}
	if !m.exists {
		return false
	}
	switch c.Op {
	case "eq":
		return equal(m.value, c.Value)
	case "not-eq":
		return !equal(m.value, c.Value)
	case "nonempty":
		return length(m.value) > 0
	case "pattern":
		s, ok := m.value.(string)
		pattern, yes := c.Value.(string)
		return ok && yes && regexp.MustCompile(pattern).MatchString(s)
	case "range":
		n, ok := m.value.(float64)
		bounds := c.Value.([]any)
		return ok && !math.IsNaN(n) && n >= bounds[0].(float64) && n <= bounds[1].(float64)
	case "length-min":
		n := length(m.value)
		return n >= int(c.Value.(float64))
	case "length-max":
		n := length(m.value)
		return n >= 0 && n <= int(c.Value.(float64))
	case "one-of":
		for _, v := range c.Value.([]any) {
			if equal(m.value, v) {
				return true
			}
		}
		return false
	case "contains":
		vs, ok := m.value.([]any)
		if !ok {
			return false
		}
		for _, v := range vs {
			if equal(v, c.Value) {
				return true
			}
		}
		return false
	case "same", "gte-path":
		other := selectPath(document, c.Value.(string))
		if len(other) != 1 || !other[0].exists {
			return false
		}
		if c.Op == "same" {
			return equal(m.value, other[0].value)
		}
		a, ok := m.value.(float64)
		b, yes := other[0].value.(float64)
		return ok && yes && a >= b
	case "sum-field":
		rows, ok := m.value.([]any)
		if !ok || len(rows) == 0 {
			return false
		}
		args := c.Value.([]any)
		sum := 0.0
		for _, row := range rows {
			obj, ok := row.(map[string]any)
			if !ok {
				return false
			}
			n, ok := obj[args[0].(string)].(float64)
			if !ok {
				return false
			}
			sum += n
		}
		return sum == args[1].(float64)
	}
	return false
}

func Analyze(name string, document map[string]any) (Report, error) {
	p, err := Lookup(name)
	if err != nil {
		return Report{}, err
	}
	// Keep reports bounded even if a malicious input expands wildcards.
	result := Report{APIVersion: "platform-factory.dev/extension-report/v1", Plugin: name, Passed: true, Findings: []Finding{}}
	for i, c := range p.Checks {
		for _, m := range selectPath(document, c.Path) {
			result.Checks++
			if !valid(c, m, document) {
				result.Passed = false
				result.Findings = append(result.Findings, Finding{fmt.Sprintf("%s/%d", name, i+1), m.path, c.Message})
				if len(result.Findings) >= MaxFindings {
					result.Truncated = true
					return result, nil
				}
			}
		}
	}
	return result, nil
}
