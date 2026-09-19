use std::{env, process::Command};

// Rust has no runtime API for its compiler's version: ask the rustc cargo builds with.
fn main() {
    let out = Command::new(env::var("RUSTC").unwrap()).arg("--version").output().unwrap();
    println!("cargo::rustc-env=RUSTC_VERSION={}", String::from_utf8(out.stdout).unwrap().trim());
}
