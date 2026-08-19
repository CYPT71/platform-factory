package main

import (
	"context"
	"encoding/json"

	"github.com/CYPT71/platform-factory/internal/core"
	"github.com/CYPT71/platform-factory/internal/plugin"
	api "github.com/CYPT71/platform-factory/sdk/plugin"
)

// stubDeploymentPlugin is a pluginClient double for
// deployToCluster/rollbackCluster/dispatchObservation's own routing logic
// (which capability, Call vs CallWithIdempotency, params selection per
// workload/observation kind) - the same separation stubKubeVirtPlugin
// (main_test.go) already gives dispatchKubeVirt, so these tests need
// neither a real Kubernetes API client nor a live cluster.
// TestRunDeployThroughRealKubernetesPluginFailsWithoutClusterAccess
// (kubernetes_plugin_test.go) covers the real discover->verify->start->
// call path end to end instead - as far as that's possible without a
// live cluster to actually apply/observe/roll back against.
type stubDeploymentPlugin struct {
	capabilities   []string
	calls          []string
	lastParams     map[string]json.RawMessage
	applyResult    api.DeploymentApplyResult
	observeResult  api.DeploymentObserveResult
	rollbackResult api.DeploymentRollbackResult
	err            error
}

func (s *stubDeploymentPlugin) Hello() plugin.HelloResult {
	return plugin.HelloResult{Name: "kubernetes", Capabilities: s.capabilities}
}

func (s *stubDeploymentPlugin) HasCapability(capability string) bool {
	for _, declared := range s.capabilities {
		if declared == capability {
			return true
		}
	}
	return false
}

func (s *stubDeploymentPlugin) Close() error { return nil }

func (s *stubDeploymentPlugin) recordParams(method string, params any) {
	if s.lastParams == nil {
		s.lastParams = map[string]json.RawMessage{}
	}
	data, _ := json.Marshal(params)
	s.lastParams[method] = data
}

func (s *stubDeploymentPlugin) Call(_ context.Context, method string, params, result any) error {
	s.calls = append(s.calls, "call:"+method)
	s.recordParams(method, params)
	if s.err != nil {
		return s.err
	}
	if result == nil {
		return nil
	}
	switch method {
	case "v1." + api.CapabilityDeploymentObserve:
		data, _ := json.Marshal(s.observeResult)
		return json.Unmarshal(data, result)
	}
	return nil
}

func (s *stubDeploymentPlugin) CallWithIdempotency(_ context.Context, operationID core.OperationID, method string, params, result any) error {
	s.calls = append(s.calls, "idempotent:"+method+":"+string(operationID))
	s.recordParams(method, params)
	if s.err != nil {
		return s.err
	}
	if result == nil {
		return nil
	}
	switch method {
	case "v1." + api.CapabilityDeploymentApply:
		data, _ := json.Marshal(s.applyResult)
		return json.Unmarshal(data, result)
	case "v1." + api.CapabilityDeploymentRollback:
		data, _ := json.Marshal(s.rollbackResult)
		return json.Unmarshal(data, result)
	}
	return nil
}

func allDeploymentCapabilities() []string {
	return []string{api.CapabilityDeploymentApply, api.CapabilityDeploymentObserve, api.CapabilityDeploymentRollback}
}
