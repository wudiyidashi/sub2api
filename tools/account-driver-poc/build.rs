//! 为隔离概念验证生成 Rust gRPC 合同

fn main() -> Result<(), Box<dyn std::error::Error>> {
    tonic_build::configure().compile_protos(&["proto/driver.proto"], &["proto"])?;
    Ok(())
}
