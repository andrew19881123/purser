package reconciler

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/purser/purser/go/controlplane/orchestrator"
	"github.com/purser/purser/go/controlplane/registry"
	purserv1 "github.com/purser/purser/go/gen/purser/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// OrchestratorActuator adapts the real Orchestrator to the Actuator interface
// the reconciler drives.
type OrchestratorActuator struct {
	orch *orchestrator.Orchestrator
	reg  registry.Registry
}

var _ Actuator = (*OrchestratorActuator)(nil)

// NewOrchestratorActuator builds the adapter.
func NewOrchestratorActuator(orch *orchestrator.Orchestrator, reg registry.Registry) *OrchestratorActuator {
	return &OrchestratorActuator{orch: orch, reg: reg}
}

func (a *OrchestratorActuator) RestartEngine(ctx context.Context, deploymentID, nodeID string) error {
	return a.orch.RestartEngine(ctx, deploymentID, nodeID)
}

// Failover applies the deployment's alternate plan (if one is referenced in the
// deployment detail) and tears down the failed deployment. Without an alternate
// plan it returns an error so the reconciler surfaces it for operator handling.
func (a *OrchestratorActuator) Failover(ctx context.Context, deploymentID string) error {
	dep, err := a.reg.GetDeployment(ctx, deploymentID)
	if err != nil {
		return fmt.Errorf("failover: load deployment: %w", err)
	}
	var detail orchestrator.DeploymentDetail
	if len(dep.Detail) > 0 {
		_ = json.Unmarshal(dep.Detail, &detail)
	}
	if detail.FailoverPlanID == "" {
		return fmt.Errorf("failover: deployment %q has no alternate plan", deploymentID)
	}
	planRow, err := a.reg.GetPlan(ctx, detail.FailoverPlanID)
	if err != nil {
		return fmt.Errorf("failover: load alternate plan %q: %w", detail.FailoverPlanID, err)
	}
	plan := &purserv1.DeploymentPlan{}
	if err := protojson.Unmarshal(planRow.Plan, plan); err != nil {
		return fmt.Errorf("failover: decode alternate plan: %w", err)
	}
	if _, err := a.orch.Apply(ctx, plan); err != nil {
		return fmt.Errorf("failover: apply alternate plan: %w", err)
	}
	// Best-effort teardown of the failed deployment.
	_ = a.orch.Teardown(ctx, deploymentID)
	return nil
}

func (a *OrchestratorActuator) Cleanup(ctx context.Context, deploymentID string) error {
	return a.orch.Teardown(ctx, deploymentID)
}
