package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/purser/purser/enterprise/license"
)

// This file keeps the canonical feature list in enterprise/license honest about
// what THIS package actually enforces.
//
// Why it lives here and not in enterprise/license: the dependency runs one way
// only — go/controlplane requires enterprise/license (see the replace directive
// in go/controlplane/go.mod). The license module cannot import the control plane
// to check up on it, but the control plane can import the license module. So the
// agreement assertion belongs on this side of the edge, where both halves are in
// scope.
//
// The audit that prompted this: five of seven flag strings in the customer-facing
// docs were wrong, a signed key carrying a wrong string verified successfully and
// unlocked nothing, and nothing in the build noticed. A hand-written mirror of
// the list would have exactly the same failure mode, so these tests derive the
// control plane's side of the comparison from the source instead.

// enforcedFeatureConsts scans the non-test sources of this package for
// package-level `feature… = "…"` string constants and returns value -> const
// name. Deriving this from the AST rather than listing it by hand is the whole
// point: a gate added in a future file is discovered automatically, so the
// agreement test below fails until the canonical list is updated too.
func enforcedFeatureConsts(t *testing.T) map[string]string {
	t.Helper()

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi interface{ Name() string }) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package sources: %v", err)
	}

	found := make(map[string]string)
	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			for _, decl := range file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.CONST {
					continue
				}
				for _, spec := range gen.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, name := range vs.Names {
						if !strings.HasPrefix(name.Name, "feature") || i >= len(vs.Values) {
							continue
						}
						lit, ok := vs.Values[i].(*ast.BasicLit)
						if !ok || lit.Kind != token.STRING {
							continue
						}
						val, err := strconv.Unquote(lit.Value)
						if err != nil {
							t.Fatalf("%s: unquote %s = %s: %v", path, name.Name, lit.Value, err)
						}
						found[val] = name.Name
					}
				}
			}
		}
	}
	if len(found) == 0 {
		t.Fatal("found no feature* string constants — the AST scan is broken, not the code")
	}
	return found
}

// TestEnforcedFeaturesAreCanonical asserts that every feature string this
// package gates on is present in license.KnownFeatures(). A gate the signer does
// not know about cannot be sold: purser-license sign would reject the flag that
// unlocks it.
func TestEnforcedFeaturesAreCanonical(t *testing.T) {
	for value, constName := range enforcedFeatureConsts(t) {
		if !license.IsKnownFeature(value) {
			t.Errorf("%s = %q is enforced here but missing from license.KnownFeatures(); "+
				"add it to enterprise/license/features.go or purser-license sign will refuse to grant it",
				constName, value)
		}
	}
}

// TestCanonicalFeaturesAreEnforced is the other direction: every string the
// signer is willing to sign must correspond to a gate in this package. A
// canonical entry with no gate behind it is the original bug — a key that
// verifies and unlocks nothing.
func TestCanonicalFeaturesAreEnforced(t *testing.T) {
	enforced := enforcedFeatureConsts(t)
	for _, known := range license.KnownFeatures() {
		if _, ok := enforced[known]; !ok {
			t.Errorf("license.KnownFeatures() contains %q but no feature* constant in this package "+
				"declares it; purser-license would sign a flag that gates nothing", known)
		}
	}
}

// TestFeatureSetsAreExactlyEqual states the invariant as a single readable
// diff, so a failure shows both sides at once rather than one mismatch at a time.
func TestFeatureSetsAreExactlyEqual(t *testing.T) {
	var enforced []string
	for value := range enforcedFeatureConsts(t) {
		enforced = append(enforced, value)
	}
	known := license.KnownFeatures()
	sort.Strings(enforced)
	sort.Strings(known)

	if strings.Join(enforced, ",") != strings.Join(known, ",") {
		t.Errorf("feature sets disagree:\n  enforced in go/controlplane/server: %v\n  canonical in enterprise/license:   %v",
			enforced, known)
	}
}

// TestLicenseAllowsUsesCanonicalStrings pins the constants by direct reference
// as well. The AST scan proves the set matches; this proves the identifiers this
// package actually passes to licenseAllows are the ones that were scanned, and
// it fails to compile if a constant is renamed or removed.
func TestLicenseAllowsUsesCanonicalStrings(t *testing.T) {
	for constName, value := range map[string]string{
		"featureAudit":               featureAudit,
		"featurePolicyEngine":        featurePolicyEngine,
		"featureBilling":             featureBilling,
		"featureDeploymentApprovals": featureDeploymentApprovals,
		"featureInferenceAudit":      featureInferenceAudit,
		"featureGDPR":                featureGDPR,
		"featureAIActCompliance":     featureAIActCompliance,
	} {
		if !license.IsKnownFeature(value) {
			t.Errorf("%s = %q is not a canonical feature", constName, value)
		}
	}
}
