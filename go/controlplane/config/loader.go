package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// LoadFile reads and validates a purser.yaml file from the given path.
func LoadFile(path string) (*ClusterConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return Load(data)
}

// Load parses and validates a purser.yaml from raw YAML bytes.
func Load(data []byte) (*ClusterConfig, error) {
	var cfg ClusterConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing YAML: %w", err)
	}
	if err := Validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
