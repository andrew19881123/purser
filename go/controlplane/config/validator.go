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

	return nil
}
