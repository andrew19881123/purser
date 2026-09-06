package policy_test

import (
	"context"
	"testing"

	"github.com/purser/purser/go/controlplane/policy"
)

func TestEngine_AllowWhenNoPolicies(t *testing.T) {
	ctx := context.Background()
	e, err := policy.LoadPolicies(ctx, nil)
	if err != nil {
		t.Fatalf("LoadPolicies: %v", err)
	}
	res, err := e.Allow(ctx, policy.EvalRequest{Action: "deploy", ModelID: "any-model"})
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if !res.Allowed {
		t.Errorf("expected allow with no policies, got denied: %s", res.Reason)
	}
}

func TestEngine_AllowWithPermissivePolicy(t *testing.T) {
	ctx := context.Background()
	const permissiveRego = `
package purser

default allow = false
allow = true
`
	e, err := policy.LoadPolicies(ctx, []policy.PolicySource{
		{Name: "allow-all", Rego: permissiveRego},
	})
	if err != nil {
		t.Fatalf("LoadPolicies: %v", err)
	}
	res, err := e.Allow(ctx, policy.EvalRequest{Action: "deploy", ModelID: "any-model"})
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if !res.Allowed {
		t.Errorf("expected allow, got denied: %s", res.Reason)
	}
}

func TestEngine_DenyWithRestrictivePolicy(t *testing.T) {
	ctx := context.Background()
	const restrictiveRego = `
package purser

default allow = false

allow if {
    input.model_id == "allowed-model"
}
`
	e, err := policy.LoadPolicies(ctx, []policy.PolicySource{
		{Name: "model-allowlist", Rego: restrictiveRego},
	})
	if err != nil {
		t.Fatalf("LoadPolicies: %v", err)
	}

	// Allowed model.
	res, err := e.Allow(ctx, policy.EvalRequest{Action: "deploy", ModelID: "allowed-model"})
	if err != nil {
		t.Fatalf("Allow(allowed): %v", err)
	}
	if !res.Allowed {
		t.Errorf("expected allow for 'allowed-model', got denied: %s", res.Reason)
	}

	// Different model — should be denied.
	res, err = e.Allow(ctx, policy.EvalRequest{Action: "deploy", ModelID: "forbidden-model"})
	if err != nil {
		t.Fatalf("Allow(forbidden): %v", err)
	}
	if res.Allowed {
		t.Error("expected deny for 'forbidden-model', got allowed")
	}
}

func TestEngine_MultiplePolices_AllMustAllow(t *testing.T) {
	ctx := context.Background()

	// Policy 1: only "deploy" action is allowed.
	const actionPolicy = `
package purser

default allow = false

allow if {
    input.action == "deploy"
}
`
	// Policy 2: only "llama3" model is allowed.
	const modelPolicy = `
package purser

default allow = false

allow if {
    input.model_id == "llama3"
}
`
	e, err := policy.LoadPolicies(ctx, []policy.PolicySource{
		{Name: "action-gate", Rego: actionPolicy},
		{Name: "model-gate", Rego: modelPolicy},
	})
	if err != nil {
		t.Fatalf("LoadPolicies: %v", err)
	}

	// Both conditions satisfied.
	res, err := e.Allow(ctx, policy.EvalRequest{Action: "deploy", ModelID: "llama3"})
	if err != nil {
		t.Fatalf("Allow(both ok): %v", err)
	}
	if !res.Allowed {
		t.Errorf("expected allow when both policies pass, got denied: %s", res.Reason)
	}

	// Action ok, model wrong — should be denied.
	res, err = e.Allow(ctx, policy.EvalRequest{Action: "deploy", ModelID: "other"})
	if err != nil {
		t.Fatalf("Allow(model fail): %v", err)
	}
	if res.Allowed {
		t.Error("expected deny when model policy fails, got allowed")
	}

	// Action wrong, model ok — should be denied.
	res, err = e.Allow(ctx, policy.EvalRequest{Action: "infer", ModelID: "llama3"})
	if err != nil {
		t.Fatalf("Allow(action fail): %v", err)
	}
	if res.Allowed {
		t.Error("expected deny when action policy fails, got allowed")
	}
}

func TestEngine_InvalidRego_LoadError(t *testing.T) {
	ctx := context.Background()
	const badRego = `this is not valid rego at all !!!`
	_, err := policy.LoadPolicies(ctx, []policy.PolicySource{
		{Name: "bad", Rego: badRego},
	})
	if err == nil {
		t.Error("expected error loading invalid Rego, got nil")
	}
}
