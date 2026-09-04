//! Generated Purser v1 protobuf + gRPC types.
//!
//! The types are produced at build time by `build.rs` (tonic-build with a
//! vendored protoc) and included here under a module path matching the proto
//! package `purser.v1`.

pub mod purser {
    pub mod v1 {
        tonic::include_proto!("purser.v1");
    }
}

/// Convenience re-export: `purser_proto::v1::HardwareProfile`, etc.
pub use purser::v1;
