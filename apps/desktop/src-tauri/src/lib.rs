use std::env;
use std::path::Path;
use std::process::Command;

#[tauri::command]
fn get_auth_token() -> Result<String, String> {
    if let Ok(home) = env::var("HOME") {
        let token_path = Path::new(&home).join(".prot").join("auth.token");
        if let Ok(data) = std::fs::read_to_string(token_path) {
            return Ok(data.trim().to_string());
        }
    }
    Err("token not found".to_string())
}

#[tauri::command]
fn get_admin_url() -> String {
    if let Ok(home) = env::var("HOME") {
        let state_path = Path::new(&home).join(".prot").join("daemon.json");
        if let Ok(data) = std::fs::read_to_string(state_path) {
            if let Ok(parsed) = serde_json::from_str::<serde_json::Value>(&data) {
                if let Some(addr) = parsed.get("admin_addr").and_then(|v| v.as_str()) {
                    return format!("http://{}", addr);
                }
            }
        }
    }
    "http://127.0.0.1:4400".to_string()
}

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
        .invoke_handler(tauri::generate_handler![get_auth_token, get_admin_url])
        .run(tauri::generate_context!())
        .expect("error while running prot desktop application");
}
