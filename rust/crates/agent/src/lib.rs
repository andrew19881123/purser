//! Purser fleet agent library.
//!
//! A lightweight, reliable daemon that runs on every node in the fleet. It is
//! the sole contact between the control plane and the physical machine: it
//! executes, observes and reports — it does not make orchestration decisions.
//!
//! It serves [`AgentService`](purser_proto::v1::agent_service_server) over gRPC
//! toward the control plane. The daemon binary ([`bin/purser-agent`](../main.rs))
//! is a thin wrapper that wires these subsystems together:
//!
//! * [`probe`] — hardware description (Linux/CPU baseline; optional NVML GPU),
//! * [`supervisor`] — engine process lifecycle over `EngineBackend`, with
//!   exponential-backoff restart and crash-loop detection,
//! * [`linkbench`] — real RTT / bandwidth measurement between nodes,
//! * [`discovery`] — enrollment (`Join`/`Heartbeat`), mDNS + seed discovery, and
//!   a membership view,
//! * [`modelcache`] — checksum-verified, LRU-evicting model weight cache,
//! * [`healing`] — heartbeat watchdog, self-diagnosis, signed-update interface,
//! * [`state`] — the explicit node lifecycle state machine,
//! * [`secrets`] — secret redaction + storage interface.

pub mod config;
pub mod discovery;
#[cfg(feature = "http-fetch")]
pub mod http_client;
pub mod healing;
pub mod linkbench;
pub mod mock_inference;
pub mod modelcache;
pub mod probe;
pub mod secrets;
pub mod service;
pub mod state;
pub mod supervisor;
pub mod swim;
