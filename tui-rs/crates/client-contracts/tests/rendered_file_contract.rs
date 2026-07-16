#![forbid(unsafe_code)]

use client_contracts::clientui::RenderedFile;
use serde_json::json;

#[test]
fn rendered_file_preserves_whole_file_deletion_count_presence() {
    let file: RenderedFile = serde_json::from_value(json!({
        "AbsPath": "/workspace/empty.txt",
        "RelPath": "./empty.txt",
        "Added": 0,
        "Removed": 0,
        "Diff": ["*** Delete File: empty.txt"],
        "WholeFileDeletions": [{
            "ID": {"HunkOrdinal": 3},
            "CountKnown": true
        }]
    }))
    .expect("server rendered file must decode");

    assert_eq!(file.whole_file_deletions.len(), 1);
    let deletion = &file.whole_file_deletions[0];
    assert_eq!(deletion.id.hunk_ordinal, 3);
    assert!(deletion.count_known);

    let encoded = serde_json::to_value(file).expect("rendered file must encode");
    assert_eq!(encoded["WholeFileDeletions"][0]["CountKnown"], true);
}

#[test]
fn rendered_file_defaults_legacy_missing_deletion_metadata_to_empty() {
    let file: RenderedFile = serde_json::from_value(json!({
        "AbsPath": "/workspace/file.txt",
        "RelPath": "./file.txt",
        "Added": 1,
        "Removed": 0,
        "Diff": ["+content"]
    }))
    .expect("legacy rendered file must decode");

    assert!(file.whole_file_deletions.is_empty());
}
