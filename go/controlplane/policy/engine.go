// Package policy embeds Open Policy Agent (OPA) to evaluate Rego governance
// rules against control-plane operations. The engine is stateless: callers
// supply compiled policies at construction time and the engine evaluates them
// on every request. Reloading is achieved by constructing a new Engine and
// atomically swapping it into the Server.
package policy

import (
	"context"
	"fmt"

	"github.com/open-policy-agent/opa/v1/rego"
)

// Engine evaluates Rego policies against input data.
// An Engine with no policies allows all requests (open-by-default).
type Engine struct {
	policies []compiledPolicy
}

type compiledPolicy struct {
	name  string
	query rego.PreparedEvalQuery
}

// EvalRequest is the input document passed to every policy evaluation.
// All fields are JSON-serialised as the `input` document in OPA.
type EvalRequest struct {
	Action   string            `json:"action"`    // "deploy", "infer", "read_audit"
	ModelID  string            `json:"model_id"`  // model being acted on
	TenantID string            `json:"tenant_id"` // caller's tenant
	KeyHash  string            `json:"key_hash"`  // SHA-256 hex of the API key
	Claims   map[string]string `json:"claims"`    // additional OIDC claims
}

// Result is the output from policy evaluation.
type Result struct {
	Allowed bool
	Reason  string
}

// LoadPolicies compiles a set of Rego source strings into the engine.
// Each policy must evaluate data.purser.allow to a boolean. Call this at
// startup and whenever policies change; swap the returned *Engine atomically.
func LoadPolicies(ctx context.Context, sources []PolicySource) (*Engine, error) {
	e := &Engine{}
	for _, src := range sources {
		q, err := rego.New(
			rego.Query("data.purser.allow"),
			rego.Module(src.Name+".rego", src.Rego),
		).PrepareForEval(ctx)
		if err != nil {
			return nil, fmt.Errorf("policy: compiling %q: %w", src.Name, err)
		}
		e.policies = append(e.policies, compiledPolicy{name: src.Name, query: q})
	}
	return e, nil
}

// PolicySource pairs a human-readable name with its Rego source.
type PolicySource struct {
	Name string
	Rego string
}

// Allow returns true only if ALL policies in the engine allow the request.
// When the engine has no policies it allows everything (open-by-default
// semantics: removing the last policy must not lock out the operator).
func (e *Engine) Allow(ctx context.Context, req EvalRequest) (Result, error) {
	if len(e.policies) == 0 {
		return Result{Allowed: true}, nil
	}
	input := map[string]interface{}{
		"action":    req.Action,
		"model_id":  req.ModelID,
		"tenant_id": req.TenantID,
		"key_hash":  req.KeyHash,
		"claims":    req.Claims,
	}
	for _, p := range e.policies {
		results, err := p.query.Eval(ctx, rego.EvalInput(input))
		if err != nil {
			return Result{}, fmt.Errorf("policy: evaluating %q: %w", p.name, err)
		}
		allowed := len(results) > 0 &&
			len(results[0].Expressions) > 0 &&
			results[0].Expressions[0].Value == true
		if !allowed {
			return Result{Allowed: false, Reason: fmt.Sprintf("denied by policy %q", p.name)}, nil
		}
	}
	return Result{Allowed: true}, nil
}
