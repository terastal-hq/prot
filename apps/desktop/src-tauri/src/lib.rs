use std::env;
use std::path::Path;
use std::process::Command;

fn ensure_daemon_running() {
    // 1. Check if ~/.prot/daemon.json exists
    if let Ok(home) = env::var("HOME") {
        let state_path = Path::new(&home).join(".prot").join("daemon.json");
        if state_path.exists() {
            return;
        }
    }

    // 2. Try starting user service if configured
    let _ = Command::new("systemctl")
        .args(["--user", "start", "protd"])
        .spawn();
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .setup(|_app| {
            ensure_daemon_running();
            Ok(())
        })
        .run(tauri::generate_context!())
        .expect("error while running prot desktop application");
}
