package config

// ClusterConfig is the top-level purser.yaml schema.
// Example:
//
//	apiVersion: purser/v1
//	kind: ClusterConfig
//	metadata:
//	  name: my-cluster
//	cluster:
//	  id: prod-cluster
//	models:
//	  - id: qwen3-moe-235b
//	    source:
//	      type: huggingface
//	      repo: Qwen/Qwen3-MoE-235B-A22B
//	    quantizations: [Q4_K_M]
//	deployments:
//	  - model: qwen3-moe-235b
//	    quantization: Q4_K_M
//	quotas:
//	  - team: eng
//	    monthly_requests: 100000
type ClusterConfig struct {
	APIVersion  string       `yaml:"apiVersion"`
	Kind        string       `yaml:"kind"`
	Metadata    Metadata     `yaml:"metadata"`
	Cluster     ClusterSpec  `yaml:"cluster"`
	Models      []ModelSpec  `yaml:"models"`
	Deployments []DeploySpec `yaml:"deployments"`
	Quotas      []QuotaSpec  `yaml:"quotas"`
	Gateway     GatewaySpec  `yaml:"gateway"`
}

// Metadata holds identification and labelling fields for the cluster config.
type Metadata struct {
	Name        string            `yaml:"name"`
	Labels      map[string]string `yaml:"labels"`
	Annotations map[string]string `yaml:"annotations"`
}

// ClusterSpec describes the cluster-level identity.
type ClusterSpec struct {
	ID string `yaml:"id"`
}

// ModelSpec defines a model catalog entry in the desired state.
type ModelSpec struct {
	ID            string     `yaml:"id"`
	Source        SourceSpec `yaml:"source"`
	Quantizations []string   `yaml:"quantizations"`
	Description   string     `yaml:"description"`
	MaxContextLen int64      `yaml:"max_context_len"`
}

// SourceSpec describes where to obtain a model's weights.
type SourceSpec struct {
	// Type is one of: "huggingface", "s3", "local", "vertexai", "sagemaker", "azureml".
	Type      string `yaml:"type"`
	Repo      string `yaml:"repo"`       // huggingface: "Qwen/Qwen3-MoE-235B-A22B"
	BucketURL string `yaml:"bucket_url"` // s3: "s3://mybucket/path"
	Path      string `yaml:"path"`       // local: "/data/models/..."
}

// DeploySpec describes a desired deployment of a model.
type DeploySpec struct {
	Model        string `yaml:"model"`
	Quantization string `yaml:"quantization"`
	MinNodes     int    `yaml:"min_nodes"`
	MaxNodes     int    `yaml:"max_nodes"`
	// Approved is the approval gate used in Wave 3 for gated rollouts.
	Approved bool `yaml:"approved"`
}

// QuotaSpec sets usage limits for a team.
type QuotaSpec struct {
	Team            string `yaml:"team"`
	MonthlyRequests int64  `yaml:"monthly_requests"` // 0 = unlimited
	MonthlyTokens   int64  `yaml:"monthly_tokens"`   // 0 = unlimited
}

// GatewaySpec tunes the API gateway.
type GatewaySpec struct {
	Port        int `yaml:"port"`
	BodyLimitMB int `yaml:"body_limit_mb"`
}
