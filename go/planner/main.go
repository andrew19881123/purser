// Command planner is a placeholder for the Purser planner.
//
// Its only job today is to prove that the generated Purser v1 protobuf types
// are importable and usable from Go, end to end.
package main

import (
	"fmt"

	purserv1 "github.com/purser/purser/go/gen/purser/v1"
)

func main() {
	// Construct a DeploymentPlan out of generated types to exercise codegen.
	plan := &purserv1.DeploymentPlan{
		PlanId:        "plan-placeholder",
		ModelId:       "placeholder/model",
		Quantization:  "q4",
		PipelineOrder: []string{"node-a", "node-b"},
		Assignments: []*purserv1.Assignment{
			{
				NodeId:     "node-a",
				Role:       purserv1.Role_ROLE_HOST,
				LayerStart: 0,
				LayerEnd:   15,
			},
			{
				NodeId:     "node-b",
				Role:       purserv1.Role_ROLE_WORKER,
				LayerStart: 16,
				LayerEnd:   31,
			},
		},
		Estimated: &purserv1.PerfEstimate{DecodeTokSMin: 10, DecodeTokSMax: 40},
	}

	fmt.Printf("purser-planner placeholder: plan=%q model=%q assignments=%d\n",
		plan.GetPlanId(), plan.GetModelId(), len(plan.GetAssignments()))
}
