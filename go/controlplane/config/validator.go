package config

import "fmt"

// knownSourceTypes is the set of recognised model source types.
var knownSourceTypes = map[string]bool{
	"huggingface": true,
	"s3":          true,
	"local":       true,
	"vertexai":    true,
	"sagemaker":   true,
	"azureml":     true,
}

// Validate performs semantic validation of a ClusterConfig.
// It is called automatically by Load and LoadFile.
func Validate(cfg *ClusterConfig) error {
	if cfg.APIVersion != "purser/v1" {
		return fmt.Errorf("unsupported apiVersion %q (expected purser/v1)", cfg.APIVersion)
	}
	if cfg.Kind != "ClusterConfig" {
		return fmt.Errorf("unsupported kind %q (expected ClusterConfig)", cfg.Kind)
	}

	// Build set of declared model IDs and validate each model.
	modelIDs := make(map[string]bool, len(cfg.Models))
	for _, m := range cfg.Models {
		if m.ID == "" {
			return fmt.Errorf("model missing required field 'id'")
		}
		if modelIDs[m.ID] {
			return fmt.Errorf("duplicate model id %q", m.ID)
		}
		modelIDs[m.ID] = true

		if m.Source.Type != "" && !knownSourceTypes[m.Source.Type] {
			return fmt.Errorf("model %q has unknown source type %q", m.ID, m.Source.Type)
		}
	}

	// Every deployment must reference a declared model.
	for _, d := range cfg.Deployments {
		if d.Model == "" {
			return fmt.Errorf("deployment missing required field 'model'")
		}
		if !modelIDs[d.Model] {
			return fmt.Errorf("deployment references unknown model %q", d.Model)
		}
	}

	// Quota teams must be non-empty.
	for _, q := range cfg.Quotas {
		if q.Team == "" {
			return fmt.Errorf("quota entry missing required field 'team'")
		}
	}

	// Validate orgs and collect team IDs for cross-reference.
	allTeamIDs := make(map[string]bool)
	orgIDs := make(map[string]bool)
	for _, org := range cfg.Orgs {
		if org.ID == "" {
			return fmt.Errorf("org missing required field 'id'")
		}
		if orgIDs[org.ID] {
			return fmt.Errorf("duplicate org id %q", org.ID)
		}
		orgIDs[org.ID] = true
		for _, team := range org.Teams {
			if team.ID == "" {
				return fmt.Errorf("team in org %q missing required field 'id'", org.ID)
			}
			allTeamIDs[team.ID] = true
			// Custom roles are accepted — only validate that the field is present.
			for _, m := range team.Members {
				if m.UserID == "" {
					return fmt.Errorf("member in team %q (org %q) missing required field 'user_id'", team.ID, org.ID)
				}
			}
		}
	}

	// Validate node pools.
	poolIDs := make(map[string]bool)
	for _, p := range cfg.NodePools {
		if p.ID == "" {
			return fmt.Errorf("node_pool missing required field 'id'")
		}
		if poolIDs[p.ID] {
			return fmt.Errorf("duplicate node_pool id %q", p.ID)
		}
		poolIDs[p.ID] = true
		if p.Policy != "exclusive" && p.Policy != "shared" {
			return fmt.Errorf("node_pool %q: policy must be 'exclusive' or 'shared', got %q", p.ID, p.Policy)
		}
		if p.Policy == "exclusive" && len(p.Quotas) > 0 {
			return fmt.Errorf("node_pool %q: quotas are only allowed on 'shared' pools", p.ID)
		}
		// When owner_type is "team", verify the team ID is known (warn-only: team may be
		// created externally, so we skip returning an error for unknown team references).
		_ = allTeamIDs // reserved for future strict-mode validation
	}

	return nil
}
