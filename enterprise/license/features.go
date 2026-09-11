package license

import (
	"errors"
	"fmt"
	"strings"
)

// This file is the single source of truth for which feature flag strings mean
// something. It exists because entitlement matching is byte-exact (see
// [License.HasFeature]) while the strings themselves were, until now, typed by
// hand into a signing command with nothing checking them. A key signed with a
// string no gate looks for verifies successfully, appears in the licensee's
// /api/v1/enterprise/status output, and unlocks nothing — and because the
// features array is inside the signed payload, it cannot be corrected in place.
// The key has to be reissued.
//
// knownFeatures is therefore deliberately placed in this package rather than in
// cmd/purser-license: the control plane already depends on this module, so it
// can import the list and assert that the gates it enforces and the flags the
// signer will grant are the same set. That check lives in
// go/controlplane/server/license_features_test.go and derives the control
// plane's half from the AST, so it cannot silently drift the way a hand-written
// mirror would.
//
// Each entry below is enforced at the cited declaration site. Verified against
// release/v0.6 by grepping licenseAllows and HasFeature across every Go module.
var knownFeatures = []string{
	"audit",                // go/controlplane/server/server.go    (featureAudit)
	"policy_engine",        // go/controlplane/server/server.go    (featurePolicyEngine)
	"billing",              // go/controlplane/server/billing.go   (featureBilling)
	"deployment_approvals", // go/controlplane/server/approvals.go (featureDeploymentApprovals)
	"inference_audit",      // go/controlplane/server/inference_audit.go (featureInferenceAudit)
	"gdpr",                 // go/controlplane/server/gdpr.go      (featureGDPR)
	"ai_act_compliance",    // go/controlplane/server/compliance.go (featureAIActCompliance)
}

var knownFeatureSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(knownFeatures))
	for _, f := range knownFeatures {
		m[f] = struct{}{}
	}
	return m
}()

// ErrUnknownFeature is the sentinel behind every rejection from
// [ValidateFeatures]. Match it with errors.Is; use errors.As with
// [*UnknownFeatureError] to recover the offending value and the suggestion.
var ErrUnknownFeature = errors.New("license: unknown feature flag")

// KnownFeatures returns the canonical feature flag strings the control plane
// enforces, in declaration order. The returned slice is a copy: the canonical
// list is trust-relevant and must not be mutable through this accessor.
func KnownFeatures() []string {
	out := make([]string, len(knownFeatures))
	copy(out, knownFeatures)
	return out
}

// IsKnownFeature reports whether name is a feature flag that some gate actually
// checks. The comparison is byte-exact, matching [License.HasFeature] — "Billing"
// and "policy-engine" are not known features, because no gate would match them
// either.
func IsKnownFeature(name string) bool {
	_, ok := knownFeatureSet[name]
	return ok
}

// UnknownFeatures returns the entries of names that no gate checks, preserving
// input order. It is the diagnostic used when inspecting an already-issued key:
// a non-empty result means the key grants strings that unlock nothing.
func UnknownFeatures(names []string) []string {
	var out []string
	for _, n := range names {
		if !IsKnownFeature(n) {
			out = append(out, n)
		}
	}
	return out
}

// featureAlias records a string that operators reasonably type instead of the
// real flag, together with the explanation to show them. why is phrased for
// direct display to a human running purser-license.
type featureAlias struct {
	flag string
	why  string
}

// featureAliases maps known confusions to the flag that actually works. A
// capability's product name and its flag string were chosen independently, and
// for two capabilities they differ — which is exactly the mistake that shipped
// in the customer-facing docs. Keys are matched after normalisation (lowercased,
// "-" folded to "_").
var featureAliases = map[string]featureAlias{
	"chargeback":        {"billing", `"chargeback" is the product name; the licence flag for that capability is "billing"`},
	"finops":            {"billing", `the FinOps / chargeback capability is gated on "billing"`},
	"usage_accounting":  {"billing", `usage accounting is part of the chargeback capability, gated on "billing"`},
	"opa_policies":      {"policy_engine", `"opa_policies" is not a flag; the Policy-as-Code capability is gated on "policy_engine"`},
	"opa":               {"policy_engine", `the OPA/Rego policy capability is gated on "policy_engine"`},
	"rego":              {"policy_engine", `the OPA/Rego policy capability is gated on "policy_engine"`},
	"policy_as_code":    {"policy_engine", `"Policy-as-Code" is the product name; the licence flag is "policy_engine"`},
	"policies":          {"policy_engine", `the policy capability is gated on "policy_engine"`},
	"audit_log":         {"audit", `the tamper-evident control-plane audit log is gated on "audit"`},
	"ai_act":            {"ai_act_compliance", `the AI Act technical-documentation capability is gated on "ai_act_compliance"`},
	"approvals":         {"deployment_approvals", `the approval-gate capability is gated on "deployment_approvals"`},
	"right_to_erasure":  {"gdpr", `the GDPR right-to-erasure capability is gated on "gdpr"`},
	"inference_logging": {"inference_audit", `per-request inference records are gated on "inference_audit"`},
}

// ungatedCapabilities maps strings that were once documented as feature flags —
// or that operators assume are flags — to why no key can grant them. These are
// NOT typos: there is no substitute flag, so offering a nearest match would be
// actively misleading. Keys are matched after normalisation.
//
// The five strings from the docs audit are all here: ha, rbac and fleet_scale
// gate nothing, while chargeback and opa_policies are real capabilities under
// different flag names and so live in featureAliases instead.
var ungatedCapabilities = map[string]string{
	"ha":           `the Raft HA control plane is shipped and ungated — no licence flag is checked for it, so a key does not need one`,
	"rbac":         `RBAC is always enforced in every edition and is never a licence feature`,
	"sso":          `SSO / OIDC / SAML sign-in is always available and is never a licence feature`,
	"oidc":         `OIDC sign-in is always available and is never a licence feature`,
	"saml":         `SAML sign-in is always available and is never a licence feature`,
	"ldap":         `LDAP / Active Directory integration is always available and is never a licence feature`,
	"fleet_scale":  `"fleet-scale" covered several capabilities at once and gates nothing; Ansible enrollment and the internal CA are shipped and ungated, while MDM, golden-image enrollment, signed air-gap bundles and multi-cluster management are roadmap`,
	"slo":          `SLO contracts are shipped and ungated — GET /api/v1/slo/compliance answers without a key`,
	"what_if":      `the what-if planner is shipped and ungated — POST /api/v1/planner/what-if answers without a key`,
	"planner":      `the planner is part of the MIT core and is never a licence feature`,
	"pki":          `the internal CA / PKI is shipped and ungated`,
	"multicluster": `multi-cluster fleet management has no implementation in this release and gates nothing`,
}

// normaliseFeature folds the differences that do not change an operator's
// intent — surrounding space, letter case, and "-" versus "_" — so that
// suggestion lookups catch "Billing" and "policy-engine". It is used ONLY for
// producing suggestions, never for deciding entitlement: matching stays
// byte-exact, because that is what the control plane does.
func normaliseFeature(name string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), "-", "_")
}

// SuggestFeature returns the flag string name was probably meant to be, and
// whether a suggestion was found. It resolves, in order: an exact match after
// normalisation (a case or separator slip), an explicit product-name alias, then
// a nearest match by edit distance for typos.
//
// It deliberately returns no suggestion for capabilities that simply have no
// gate — see [ungatedCapabilities]. For those, [ValidateFeatures] explains the
// situation instead of guessing.
func SuggestFeature(name string) (string, bool) {
	norm := normaliseFeature(name)
	if norm == "" {
		return "", false
	}

	// A capability with no gate has no correct flag to suggest.
	if _, ungated := ungatedCapabilities[norm]; ungated {
		return "", false
	}

	// Case or separator slip: the normalised form is itself a real flag. Only a
	// suggestion if it differs from what was typed, otherwise there is nothing
	// to correct.
	if IsKnownFeature(norm) && norm != name {
		return norm, true
	}

	if alias, ok := featureAliases[norm]; ok {
		return alias.flag, true
	}

	// Typo: nearest known flag by edit distance. The distance must be small in
	// absolute terms AND small relative to the candidate, so that a short flag
	// like "gdpr" is not matched by an unrelated four-character string.
	best, bestDist := "", -1
	for _, known := range knownFeatures {
		d := levenshtein(norm, known)
		if d > 3 || d*2 > len(known) {
			continue
		}
		if bestDist == -1 || d < bestDist {
			best, bestDist = known, d
		}
	}
	if bestDist >= 0 {
		return best, true
	}
	return "", false
}

// UnknownFeatureError reports a feature flag string that no gate checks. It
// carries the offending value and, when one could be determined, the flag the
// operator probably meant.
type UnknownFeatureError struct {
	// Feature is the offending string, exactly as supplied.
	Feature string
	// Suggestion is the flag that was probably meant, or "" if none was found.
	Suggestion string
	// Reason explains why the string is not a flag, for cases where no
	// substitute exists (a capability that is shipped but ungated). Empty when
	// the problem is a typo or a product-name confusion.
	Reason string
}

// Hint returns just the explanatory clause — the product-name correction, the
// "did you mean", or the reason no flag exists — with no surrounding boilerplate.
// It is empty when nothing could be determined. Callers that render their own
// layout (the verify report lists several offenders at once) use this so that the
// explanations stay identical to the ones sign prints.
func (e *UnknownFeatureError) Hint() string {
	switch {
	case e.Reason != "":
		return e.Reason
	case e.Suggestion != "":
		return fmt.Sprintf("did you mean %q?", e.Suggestion)
	}
	return ""
}

func (e *UnknownFeatureError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%q is not a feature flag the control plane enforces", e.Feature)
	if hint := e.Hint(); hint != "" {
		fmt.Fprintf(&b, ": %s", hint)
	}
	b.WriteString("\n  A key granting it would verify but unlock nothing, and the features array is\n" +
		"  inside the signed payload — such a key cannot be corrected, only reissued.\n" +
		"  Authoritative flag list: website/docs/enterprise/license.md (Feature gate reference).")
	return b.String()
}

// FeatureHint returns the human explanation for why name is not a usable feature
// flag: the flag to use instead, or why the capability has no flag at all. It
// returns "" when name is a valid feature or nothing useful could be determined.
func FeatureHint(name string) string {
	var ufe *UnknownFeatureError
	if errors.As(ValidateFeature(name), &ufe) {
		return ufe.Hint()
	}
	return ""
}

// Unwrap ties the error to [ErrUnknownFeature] so callers can match the class
// with errors.Is while still recovering the detail with errors.As.
func (e *UnknownFeatureError) Unwrap() error { return ErrUnknownFeature }

// ValidateFeature checks a single flag string, returning an
// [*UnknownFeatureError] if no gate would ever match it.
func ValidateFeature(name string) error {
	if IsKnownFeature(name) {
		return nil
	}
	e := &UnknownFeatureError{Feature: name}
	if reason, ok := ungatedCapabilities[normaliseFeature(name)]; ok {
		e.Reason = reason
	} else if alias, ok := featureAliases[normaliseFeature(name)]; ok {
		e.Suggestion = alias.flag
		e.Reason = alias.why
	} else if s, ok := SuggestFeature(name); ok {
		e.Suggestion = s
	}
	return e
}

// ValidateFeatures checks every entry of names, reporting all offenders rather
// than stopping at the first. A nil or empty slice is valid — whether a licence
// ought to grant something is the caller's policy, not this function's.
//
// The returned error matches [ErrUnknownFeature] via errors.Is.
func ValidateFeatures(names []string) error {
	var errs []error
	for _, n := range names {
		if err := ValidateFeature(n); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// levenshtein returns the edit distance between a and b, operating on runes so
// that a multi-byte character counts as one edit. Two rolling rows keep it
// O(len(b)) in space; the inputs here are short flag strings.
//
// Hand-rolled on purpose: this module has no third-party dependencies, and a
// signing tool is the wrong place to add one for twenty lines of arithmetic.
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}

	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}
