fn main() -> Result<(), Box<dyn std::error::Error>> {
    // Use local proto files with corrected import paths
    let proto_files = &[
        "proto/rdsec/rdsec.proto",
        "proto/rdverb/rdverb.proto",
    ];

    let includes = &["proto"];

    prost_build::compile_protos(proto_files, includes)?;

    Ok(())
}
