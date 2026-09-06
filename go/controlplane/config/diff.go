package config

import "context"

// DiffResult describes the changes needed to converge the live state to the
// desired state expressed in a ClusterConfig.
type DiffResult struct {
	ModelsToAdd         []ModelSpec
	ModelsToRemove      []string // model IDs present in the live registry but absent from desired state
	DeploymentsToAdd    []DeploySpec
	DeploymentsToRemove []string // deployment model IDs present live but absent from desired state
	QuotasToUpsert      []QuotaSpec

	// Org/team hierarchy changes.
	OrgsToAdd    []OrgSpec
	OrgsToUpdate []OrgSpec

	// Node pool changes.
	NodePoolsToAdd    []NodePoolSpec
	NodePoolsToUpdate []NodePoolSpec
	NodePoolsToRemove []string // pool IDs present live but absent from desired state
}

// Lister is the minimal read interface needed for diffing.
// Keeping it small avoids importing the full registry package and makes
// the config package standalone and easily mockable in tests.
type Lister interface {
	// CurrentModelIDs returns the model IDs currently registered.
	CurrentModelIDs(ctx context.Context) ([]string, error)
	// CurrentDeploymentModelIDs returns the model IDs referenced by active deployments.
	CurrentDeploymentModelIDs(ctx context.Context) ([]string, error)
	// CurrentOrgIDs returns the org IDs currently registered.
	// Implementations that do not track orgs may return (nil, nil).
	CurrentOrgIDs(ctx context.Context) ([]string, error)
	// CurrentNodePoolIDs returns the node pool IDs currently registered.
	// Implementations that do not track pools may return (nil, nil).
	CurrentNodePoolIDs(ctx context.Context) ([]string, error)
}

// Diff computes what changes are needed to bring the live cluster state in line
// with the desired state in cfg.
//
// When lister is nil the result is a full "add everything" diff — useful for
// dry-run previews or first-time apply when no live state is available.
func Diff(ctx context.Context, cfg *ClusterConfig, lister Lister) (*DiffResult, error) {
	result := &DiffResult{}

	if lister == nil {
		result.ModelsToAdd = append(result.ModelsToAdd, cfg.Models...)
		result.DeploymentsToAdd = append(result.DeploymentsToAdd, cfg.Deployments...)
		result.QuotasToUpsert = cfg.Quotas
		result.OrgsToAdd = append(result.OrgsToAdd, cfg.Orgs...)
		result.NodePoolsToAdd = append(result.NodePoolsToAdd, cfg.NodePools...)
		return result, nil
	}

	// --- Models diff ---
	currentModelIDs, err := lister.CurrentModelIDs(ctx)
	if err != nil {
		return nil, err
	}
	currentModelSet := make(map[string]bool, len(currentModelIDs))
	for _, id := range currentModelIDs {
		currentModelSet[id] = true
	}

	desiredModelSet := make(map[string]bool, len(cfg.Models))
	for _, m := range cfg.Models {
		desiredModelSet[m.ID] = true
		if !currentModelSet[m.ID] {
			result.ModelsToAdd = append(result.ModelsToAdd, m)
		}
	}
	for _, id := range currentModelIDs {
		if !desiredModelSet[id] {
			result.ModelsToRemove = append(result.ModelsToRemove, id)
		}
	}

	// --- Deployments diff ---
	currentDeployModelIDs, err := lister.CurrentDeploymentModelIDs(ctx)
	if err != nil {
		return nil, err
	}
	currentDeploySet := make(map[string]bool, len(currentDeployModelIDs))
	for _, id := range currentDeployModelIDs {
		currentDeploySet[id] = true
	}

	desiredDeploySet := make(map[string]bool, len(cfg.Deployments))
	for _, d := range cfg.Deployments {
		desiredDeploySet[d.Model] = true
		if !currentDeploySet[d.Model] {
			result.DeploymentsToAdd = append(result.DeploymentsToAdd, d)
		}
	}
	for _, id := range currentDeployModelIDs {
		if !desiredDeploySet[id] {
			result.DeploymentsToRemove = append(result.DeploymentsToRemove, id)
		}
	}

	// Quotas are always upserted (no removal semantics for quotas in Wave 1).
	result.QuotasToUpsert = cfg.Quotas

	// --- Orgs diff ---
	currentOrgIDs, err := lister.CurrentOrgIDs(ctx)
	if err != nil {
		return nil, err
	}
	currentOrgSet := make(map[string]bool, len(currentOrgIDs))
	for _, id := range currentOrgIDs {
		currentOrgSet[id] = true
	}
	for _, org := range cfg.Orgs {
		if !currentOrgSet[org.ID] {
			result.OrgsToAdd = append(result.OrgsToAdd, org)
		} else {
			result.OrgsToUpdate = append(result.OrgsToUpdate, org)
		}
	}

	// --- Node pools diff ---
	currentPoolIDs, err := lister.CurrentNodePoolIDs(ctx)
	if err != nil {
		return nil, err
	}
	currentPoolSet := make(map[string]bool, len(currentPoolIDs))
	for _, id := range currentPoolIDs {
		currentPoolSet[id] = true
	}
	desiredPoolSet := make(map[string]bool, len(cfg.NodePools))
	for _, p := range cfg.NodePools {
		desiredPoolSet[p.ID] = true
		if !currentPoolSet[p.ID] {
			result.NodePoolsToAdd = append(result.NodePoolsToAdd, p)
		} else {
			result.NodePoolsToUpdate = append(result.NodePoolsToUpdate, p)
		}
	}
	for _, id := range currentPoolIDs {
		if !desiredPoolSet[id] {
			result.NodePoolsToRemove = append(result.NodePoolsToRemove, id)
		}
	}

	return result, nil
}
