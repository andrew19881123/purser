use std::env;

// Compile the Purser v1 protos with tonic-build, using a fully project-local,
// *vendored* protoc (crate `protoc-bin-vendored`). No system protoc is needed,
// which keeps the build offline-friendly and reproducible.
fn main() {
    let protoc = protoc_bin_vendored::protoc_bin_path().expect("vendored protoc binary");
    // tonic-build / prost-build honor the PROTOC env var.
    env::set_var("PROTOC", &protoc);

    // The vendored package also ships the well-known-type .proto includes
    // (google/protobuf/timestamp.proto, ...), so imports resolve without a
    // system include path.
    let wkt_include = protoc_bin_vendored::include_path().expect("vendored protoc includes");

    // Repo layout: this crate lives at rust/crates/purser-proto/, and the
    // proto tree is at <repo>/proto/, hence ../../../proto.
    let proto_root = "../../../proto";
    let protos = [
        "../../../proto/purser/v1/common.proto",
        "../../../proto/purser/v1/engine.proto",
        "../../../proto/purser/v1/agent.proto",
        "../../../proto/purser/v1/registration.proto",
    ];

    // tonic 0.14 moved prost message codegen into `tonic-prost-build`; the
    // service/codec entry point is `tonic_prost_build::configure()`.
    tonic_prost_build::configure()
        .build_server(true)
        .build_client(true)
        .compile_protos(&protos, &[proto_root, wkt_include.to_str().unwrap()])
        .expect("failed to compile purser v1 protos");

    println!("cargo:rerun-if-changed={proto_root}");
}
