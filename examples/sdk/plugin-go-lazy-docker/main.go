package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/CYPT71/platform-factory/sdk/plugin"
)

type container struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Image string `json:"image"`
	State string `json:"state"`
	Ports string `json:"ports"`
}
type nativeContainer struct {
	ID     string
	Names  string
	Name   string
	Image  string
	State  string
	Status string
	Ports  string
}
type statusParams struct {
	Engine string `json:"engine"`
}
type logsParams struct {
	Engine string `json:"engine"`
	Name   string `json:"name"`
	Tail   int    `json:"tail"`
}

func engine(requested string) (string, error) {
	if requested != "" && requested != "auto" && requested != "docker" && requested != "podman" {
		return "", errors.New("engine must be auto, docker, or podman")
	}
	if requested == "docker" || requested == "podman" {
		if _, err := exec.LookPath(requested); err != nil {
			return "", err
		}
		return requested, nil
	}
	for _, candidate := range []string{"docker", "podman"} {
		if _, err := exec.LookPath(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", errors.New("Docker or Podman was not found on PATH")
}

func status(_ context.Context, raw json.RawMessage) (any, error) {
	var params statusParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, err
		}
	}
	selected, err := engine(params.Engine)
	if err != nil {
		return nil, err
	}
	out, err := exec.Command(selected, "ps", "--all", "--format", "{{json .}}").Output()
	if err != nil {
		return nil, err
	}
	var result []container
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		var row nativeContainer
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, err
		}
		name := row.Names
		if name == "" {
			name = row.Name
		}
		state := row.State
		if state == "" {
			state = row.Status
		}
		result = append(result, container{row.ID, name, row.Image, state, row.Ports})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return map[string]any{"engine": selected, "containers": result}, nil
}

func logs(_ context.Context, raw json.RawMessage) (any, error) {
	var params logsParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, err
	}
	if params.Name == "" || strings.ContainsRune(params.Name, 0) {
		return nil, errors.New("name is required")
	}
	if params.Tail == 0 {
		params.Tail = 50
	}
	if params.Tail < 1 || params.Tail > 500 {
		return nil, errors.New("tail must be between 1 and 500")
	}
	selected, err := engine(params.Engine)
	if err != nil {
		return nil, err
	}
	out, err := exec.Command(selected, "logs", "--tail", fmt.Sprint(params.Tail), params.Name).Output()
	if err != nil {
		return nil, err
	}
	return map[string]any{"engine": selected, "name": params.Name, "lines": strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")}, nil
}

func main() {
	server := plugin.NewServer("lazy-docker-go", "1.0.0")
	server.Handle("detect", func(context.Context, json.RawMessage) (any, error) { return plugin.DetectResult{Kind: "unknown"}, nil })
	server.Handle("freeze", func(context.Context, json.RawMessage) (any, error) {
		return plugin.FreezeResult{Steps: [][]string{{"go", "version"}}, Profile: "runtime-monitor"}, nil
	})
	server.Handle("plan", func(context.Context, json.RawMessage) (any, error) {
		return plugin.PlanResult{Notes: []string{"read-only Docker/Podman monitoring"}}, nil
	})
	if err := plugin.RegisterTyped(server, plugin.CapabilityRuntimeStatus, func(ctx context.Context, params statusParams) (any, error) {
		raw, _ := json.Marshal(params)
		return status(ctx, raw)
	}); err != nil {
		panic(err)
	}
	server.Handle(plugin.CapabilityRuntimeLogs, logs)
	if err := server.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		panic(err)
	}
}
