# Architecture Overview

Purser is a heterogeneous-fleet inference scheduler. It observes the available nodes and their hardware profiles, then decides how to split a model across the fleet for optimal throughput.

## Planner

The planner takes a fleet snapshot (nodes + network links) and a model specification and produces a `DeploymentPlan`: which layers run on which node, in which pipeline order, and at which quantization.

Performance estimates shown in the UI are derived from each node's memory bandwidth, VRAM, and — when reported by the engine — optional hardware features. Specifically, **prefix caching** (KV-cache hit fraction) and **KV-SSD offload** (cold KV blocks spilled to SSD) are factored into placement estimates when the engine reports them, allowing the planner to prefer nodes where these capabilities increase effective throughput or usable memory.

## Agents

Each fleet node runs a Purser agent that reports hardware capabilities (GPU VRAM, memory bandwidth, disk space, FP4 support, engine version) and measures network link quality to its peers. The control plane aggregates these reports and feeds them to the planner.

## Pipeline Execution

The planner produces a pipeline-parallel deployment: each node holds a contiguous shard of the model's layers and passes activations to the next stage over the measured network links. The host node (pipeline head) faces client requests and owns the first shard.
