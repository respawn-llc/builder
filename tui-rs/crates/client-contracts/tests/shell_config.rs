use client_contracts::config::ShellSettings;
use serde_json::{Value, json};

#[test]
fn shell_postprocess_hook_decodes_null_as_absence() {
    let settings: ShellSettings = serde_json::from_value(json!({
        "PostprocessingMode": "builtin",
        "PostprocessHook": null
    }))
    .expect("nullable shell postprocess hook must decode");

    let encoded = serde_json::to_value(settings).expect("shell settings must encode");
    assert_eq!(encoded["PostprocessHook"], Value::Null);
}
