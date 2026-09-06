// Package config implements the purser.yaml declarative configuration loader.
// It defines the schema for the cluster desired state and provides functions
// to load, validate, and diff configurations against the live registry.
//
// Usage:
//
//	cfg, err := config.LoadFile("purser.yaml")
//	if err != nil { ... }
//	diff, err := config.Diff(ctx, cfg, registry)
package config
