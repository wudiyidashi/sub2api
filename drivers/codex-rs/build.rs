//! 从版本锁和宿主合同生成驱动来源信息与 Rust 账号执行 RPC

use std::{env, fs, path::PathBuf, process::Command};

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let root = PathBuf::from(env::var("CARGO_MANIFEST_DIR")?);
    println!("cargo:rerun-if-changed=upstream-lock.json");
    let lock_text = fs::read_to_string(root.join("upstream-lock.json"))
        .map_err(|_| "CPR upstream lock is unreadable")?;
    let lock: serde_json::Value =
        serde_json::from_str(&lock_text).map_err(|_| "CPR upstream lock is invalid JSON")?;
    let commit = lock
        .get("commit")
        .and_then(serde_json::Value::as_str)
        .filter(|value| value.len() == 40 && value.bytes().all(|byte| byte.is_ascii_hexdigit()))
        .ok_or("CPR upstream commit must be a full 40-character hexadecimal SHA")?
        .to_ascii_lowercase();
    let upstream = root.join("upstream");
    if upstream.join(".git").exists() {
        println!("cargo:rerun-if-changed=upstream/.git/HEAD");
        let result = Command::new("git")
            .arg("-C")
            .arg(upstream)
            .args(["rev-parse", "--verify", "HEAD"])
            .output()
            .map_err(|_| "CPR checkout revision could not be verified")?;
        if !result.status.success() || String::from_utf8_lossy(&result.stdout).trim() != commit {
            return Err("CPR checkout revision does not match the upstream lock".into());
        }
    }
    println!("cargo:rustc-env=CPR_UPSTREAM_COMMIT={commit}");
    tonic_build::configure()
        .boxed(".sub2api.accountdriver.v1.ExecuteCommand.frame.start")
        .boxed(".sub2api.accountdriver.v1.WebSocketCommand.frame.start")
        .compile_protos(
            &["../../backend/pkg/accountdriverapi/v1/account_driver.proto"],
            &["../../backend/pkg/accountdriverapi/v1"],
        )?;
    Ok(())
}
