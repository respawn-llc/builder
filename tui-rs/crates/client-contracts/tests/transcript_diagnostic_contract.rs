use client_contracts::clientui::{
    DeletionFactMismatchDeveloperDiagnostic, DeletionFactMismatchKind, DeveloperDiagnostic,
    TranscriptDiagnostic, TranscriptNoticeRow, WholeFileDeletionOperationId,
};
use serde_json::json;

#[test]
fn typed_deletion_diagnostic_decodes_server_payload_without_legacy_strings() {
    let notice: TranscriptNoticeRow = serde_json::from_value(json!({
        "Reason": "runtime_diagnostic",
        "Severity": "error",
        "Diagnostic": {
            "Code": "",
            "Detail": "",
            "Developer": {
                "deletion_fact_mismatch": {
                    "call_id": "call-1",
                    "operation_id": {
                        "HunkOrdinal": 2
                    },
                    "mismatch_kind": "missing"
                }
            }
        }
    }))
    .expect("server-emitted typed diagnostic notice must decode");

    let diagnostic = notice
        .diagnostic
        .expect("typed transcript diagnostic must be present");
    assert!(diagnostic.code.is_empty());
    assert!(diagnostic.detail.is_empty());
    let mismatch = diagnostic
        .developer
        .expect("typed developer diagnostic must be present")
        .deletion_fact_mismatch
        .expect("deletion mismatch context must be present");
    assert_eq!(mismatch.call_id, "call-1");
    assert_eq!(mismatch.operation_id.hunk_ordinal, 2);
    assert_eq!(mismatch.mismatch_kind, DeletionFactMismatchKind::Missing);
}

#[test]
fn legacy_diagnostic_decodes_without_developer_variant() {
    let diagnostic: TranscriptDiagnostic = serde_json::from_value(json!({
        "Code": "legacy_code",
        "Detail": "legacy detail"
    }))
    .expect("legacy diagnostic must decode without a developer variant");

    assert_eq!(diagnostic.code, "legacy_code");
    assert_eq!(diagnostic.detail, "legacy detail");
    assert!(diagnostic.developer.is_none());
}

#[test]
fn blank_present_legacy_diagnostic_fields_are_rejected() {
    for payload in [
        json!({"Code": " ", "Detail": ""}),
        json!({"Code": "", "Detail": "\t"}),
    ] {
        let result = serde_json::from_value::<TranscriptDiagnostic>(payload);
        assert!(
            result.is_err(),
            "blank present legacy field must not decode"
        );
    }

    let invalid = TranscriptDiagnostic {
        code: " ".to_owned(),
        detail: String::new(),
        developer: None,
    };
    assert!(
        serde_json::to_value(invalid).is_err(),
        "blank present legacy field must not encode"
    );
}

fn valid_developer_diagnostic() -> DeveloperDiagnostic {
    DeveloperDiagnostic {
        deletion_fact_mismatch: Some(DeletionFactMismatchDeveloperDiagnostic {
            call_id: "call-1".to_owned(),
            operation_id: WholeFileDeletionOperationId { hunk_ordinal: 2 },
            mismatch_kind: DeletionFactMismatchKind::Missing,
        }),
    }
}

#[test]
fn invalid_diagnostic_variants_are_rejected_on_decode() {
    let valid_developer = json!({
        "deletion_fact_mismatch": {
            "call_id": "call-1",
            "operation_id": {"HunkOrdinal": 2},
            "mismatch_kind": "missing"
        }
    });
    for payload in [
        json!({"Code": "", "Detail": "", "Developer": null}),
        json!({"Code": "legacy", "Detail": "detail", "Developer": valid_developer}),
        json!({"Code": "", "Detail": "", "Developer": {}}),
        json!({
            "Code": "",
            "Detail": "",
            "Developer": {
                "deletion_fact_mismatch": {
                    "call_id": " ",
                    "operation_id": {"HunkOrdinal": 2},
                    "mismatch_kind": "missing"
                }
            }
        }),
        json!({
            "Code": "",
            "Detail": "",
            "Developer": {
                "deletion_fact_mismatch": {
                    "call_id": "call-1",
                    "operation_id": {"HunkOrdinal": -1},
                    "mismatch_kind": "missing"
                }
            }
        }),
    ] {
        let result = serde_json::from_value::<TranscriptDiagnostic>(payload);
        assert!(result.is_err(), "invalid diagnostic must not decode");
    }

    assert!(
        serde_json::from_value::<DeveloperDiagnostic>(json!({})).is_err(),
        "developer diagnostic without a known variant must not decode"
    );
}

#[test]
fn invalid_diagnostic_variants_are_rejected_on_encode() {
    let valid_developer = valid_developer_diagnostic();
    let invalid_diagnostics = [
        TranscriptDiagnostic {
            code: String::new(),
            detail: String::new(),
            developer: None,
        },
        TranscriptDiagnostic {
            code: "legacy".to_owned(),
            detail: "detail".to_owned(),
            developer: Some(valid_developer),
        },
        TranscriptDiagnostic {
            code: String::new(),
            detail: String::new(),
            developer: Some(DeveloperDiagnostic {
                deletion_fact_mismatch: None,
            }),
        },
        TranscriptDiagnostic {
            code: String::new(),
            detail: String::new(),
            developer: Some(DeveloperDiagnostic {
                deletion_fact_mismatch: Some(DeletionFactMismatchDeveloperDiagnostic {
                    call_id: " ".to_owned(),
                    operation_id: WholeFileDeletionOperationId { hunk_ordinal: 2 },
                    mismatch_kind: DeletionFactMismatchKind::Missing,
                }),
            }),
        },
        TranscriptDiagnostic {
            code: String::new(),
            detail: String::new(),
            developer: Some(DeveloperDiagnostic {
                deletion_fact_mismatch: Some(DeletionFactMismatchDeveloperDiagnostic {
                    call_id: "call-1".to_owned(),
                    operation_id: WholeFileDeletionOperationId { hunk_ordinal: -1 },
                    mismatch_kind: DeletionFactMismatchKind::Missing,
                }),
            }),
        },
    ];
    for diagnostic in invalid_diagnostics {
        assert!(
            serde_json::to_value(diagnostic).is_err(),
            "invalid diagnostic must not encode"
        );
    }

    assert!(
        serde_json::to_value(DeveloperDiagnostic {
            deletion_fact_mismatch: None
        })
        .is_err(),
        "developer diagnostic without a known variant must not encode"
    );
}
