package license_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/purser/purser/enterprise/license"
)

// TestKnownFeaturesMatchesEnforcedSet pins the canonical list to the seven
// strings the control plane actually enforces. The declaration sites are named
// in features.go; go/controlplane/server/license_features_test.go asserts the
// agreement mechanically. This test guards the *count and content* here so that
// an accidental edit to the canonical slice fails in this module too.
func TestKnownFeaturesMatchesEnforcedSet(t *testing.T) {
	want := []string{
		"audit",
		"policy_engine",
		"billing",
		"deployment_approvals",
		"inference_audit",
		"gdpr",
		"ai_act_compliance",
	}
	got := license.KnownFeatures()
	if len(got) != len(want) {
		t.Fatalf("KnownFeatures() has %d entries, want %d: %v", len(got), len(want), got)
	}
	for _, w := range want {
		if !license.IsKnownFeature(w) {
			t.Errorf("IsKnownFeature(%q) = false, want true", w)
		}
	}
}

// TestKnownFeaturesReturnsCopy ensures a caller cannot mutate the canonical
// list through the accessor — it is trust-relevant state.
func TestKnownFeaturesReturnsCopy(t *testing.T) {
	first := license.KnownFeatures()
	if len(first) == 0 {
		t.Fatal("KnownFeatures() is empty")
	}
	first[0] = "clobbered"
	if license.KnownFeatures()[0] == "clobbered" {
		t.Error("KnownFeatures() exposes the backing array; callers can mutate the canonical list")
	}
	if !license.IsKnownFeature("audit") {
		t.Error("mutating the returned slice corrupted the lookup set")
	}
}

// TestValidateFeaturesAcceptsEveryKnownFeature is the happy path: each of the
// seven enforced strings validates.
func TestValidateFeaturesAcceptsEveryKnownFeature(t *testing.T) {
	for _, f := range license.KnownFeatures() {
		t.Run(f, func(t *testing.T) {
			if err := license.ValidateFeatures([]string{f}); err != nil {
				t.Errorf("ValidateFeatures([%q]) = %v, want nil", f, err)
			}
		})
	}
	// And all seven together, which is what a maximal key looks like.
	if err := license.ValidateFeatures(license.KnownFeatures()); err != nil {
		t.Errorf("ValidateFeatures(all known) = %v, want nil", err)
	}
	// An empty feature set is not this function's business to reject.
	if err := license.ValidateFeatures(nil); err != nil {
		t.Errorf("ValidateFeatures(nil) = %v, want nil", err)
	}
}

// TestValidateFeaturesRejectsUnknown asserts the error names the offending
// value — an operator must be able to see which flag they got wrong.
func TestValidateFeaturesRejectsUnknown(t *testing.T) {
	err := license.ValidateFeatures([]string{"audit", "totally_made_up"})
	if err == nil {
		t.Fatal("ValidateFeatures with an unknown string = nil, want error")
	}
	if !errors.Is(err, license.ErrUnknownFeature) {
		t.Errorf("error does not match ErrUnknownFeature: %v", err)
	}
	if !strings.Contains(err.Error(), "totally_made_up") {
		t.Errorf("error must name the offending value, got: %v", err)
	}
	// The valid sibling must not be blamed.
	if strings.Contains(err.Error(), "audit") {
		t.Errorf("error should not mention the valid feature 'audit', got: %v", err)
	}
}

// TestSuggestFeatureProductNameConfusions covers the two confusions that
// actually shipped in the customer-facing docs: the product name was used
// where the flag string was required.
func TestSuggestFeatureProductNameConfusions(t *testing.T) {
	cases := map[string]string{
		"chargeback":   "billing",
		"opa_policies": "policy_engine",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			got, ok := license.SuggestFeature(in)
			if !ok {
				t.Fatalf("SuggestFeature(%q) returned no suggestion, want %q", in, want)
			}
			if got != want {
				t.Errorf("SuggestFeature(%q) = %q, want %q", in, got, want)
			}

			// The validation error must carry the suggestion through, naming
			// both the bad value and the right one.
			err := license.ValidateFeatures([]string{in})
			if err == nil {
				t.Fatalf("ValidateFeatures([%q]) = nil, want error", in)
			}
			if !strings.Contains(err.Error(), in) {
				t.Errorf("error must name the offending value %q, got: %v", in, err)
			}
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error must name the correct flag %q, got: %v", in, err)
			}
		})
	}
}

// TestSuggestFeatureTypos exercises the nearest-match path for genuine typos
// (as opposed to the explicit product-name aliases).
func TestSuggestFeatureTypos(t *testing.T) {
	cases := map[string]string{
		"inference_audi": "inference_audit", // dropped char
		"gdrp":           "gdpr",            // transposition
		"polcy_engine":   "policy_engine",   // dropped char mid-word
		"billling":       "billing",         // doubled char
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			got, ok := license.SuggestFeature(in)
			if !ok {
				t.Fatalf("SuggestFeature(%q) returned no suggestion, want %q", in, want)
			}
			if got != want {
				t.Errorf("SuggestFeature(%q) = %q, want %q", in, got, want)
			}
		})
	}
}

// TestSuggestFeatureCaseAndSeparator covers strings that differ from a real
// gate only by case or by - vs _. HasFeature is byte-exact, so these must still
// be REJECTED, but the suggestion should point at the exact spelling.
func TestSuggestFeatureCaseAndSeparator(t *testing.T) {
	cases := map[string]string{
		"Billing":              "billing",
		"POLICY_ENGINE":        "policy_engine",
		"policy-engine":        "policy_engine",
		"deployment-approvals": "deployment_approvals",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			if license.IsKnownFeature(in) {
				t.Errorf("IsKnownFeature(%q) = true, but entitlement matching is byte-exact", in)
			}
			got, ok := license.SuggestFeature(in)
			if !ok {
				t.Fatalf("SuggestFeature(%q) returned no suggestion, want %q", in, want)
			}
			if got != want {
				t.Errorf("SuggestFeature(%q) = %q, want %q", in, got, want)
			}
		})
	}
}

// TestSuggestFeatureUngatedCapabilities covers strings that are not typos and
// have no correct replacement: they were documented as feature flags but no
// gate checks them. Suggesting a nearest match here would be actively
// misleading, so the explanation must say the capability is not gated rather
// than offering a substitute flag.
func TestSuggestFeatureUngatedCapabilities(t *testing.T) {
	for _, in := range []string{"ha", "rbac", "fleet-scale"} {
		t.Run(in, func(t *testing.T) {
			if license.IsKnownFeature(in) {
				t.Fatalf("IsKnownFeature(%q) = true, want false — no gate checks it", in)
			}
			err := license.ValidateFeatures([]string{in})
			if err == nil {
				t.Fatalf("ValidateFeatures([%q]) = nil, want error", in)
			}
			msg := err.Error()
			if !strings.Contains(msg, in) {
				t.Errorf("error must name %q, got: %v", in, err)
			}
			// Must not pretend there is an equivalent flag to use instead.
			if strings.Contains(msg, "did you mean") {
				t.Errorf("%q is not a typo; error should explain it is ungated, not guess: %v", in, err)
			}
		})
	}
}

// TestSuggestFeatureNoSuggestionForGibberish ensures we do not attach an absurd
// nearest match to a string that resembles nothing.
func TestSuggestFeatureNoSuggestionForGibberish(t *testing.T) {
	for _, in := range []string{"zzzzzzzzzzzz", "q", ""} {
		if got, ok := license.SuggestFeature(in); ok {
			t.Errorf("SuggestFeature(%q) = %q, want no suggestion", in, got)
		}
	}
}

// TestUnknownFeaturesReportsAllOffenders is the helper the verify path uses to
// diagnose an already-issued key: it must list every unknown string, in order,
// and nothing else.
func TestUnknownFeaturesReportsAllOffenders(t *testing.T) {
	got := license.UnknownFeatures([]string{"audit", "chargeback", "gdpr", "ha"})
	want := []string{"chargeback", "ha"}
	if len(got) != len(want) {
		t.Fatalf("UnknownFeatures() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("UnknownFeatures() = %v, want %v", got, want)
		}
	}
	if len(license.UnknownFeatures(license.KnownFeatures())) != 0 {
		t.Error("UnknownFeatures(all known) should be empty")
	}
}
