#![forbid(unsafe_code)]

use client_contracts::clientui::{
    DeletionFactMismatchDeveloperDiagnostic, DeletionFactMismatchKind, DeveloperDiagnostic,
    TranscriptDiagnostic, TranscriptNoticeRow, WholeFileDeletionOperationId,
};
use serde_json::json;

fn valid_mismatch() -> DeletionFactMismatchDeveloperDiagnostic {
    DeletionFactMismatchDeveloperDiagnostic {
        call_id: "call-1".to_owned(),
        operation_id: WholeFileDeletionOperationId { hunk_ordinal: 2 },
        mismatch_kind: DeletionFactMismatchKind::Missing,
    }
}

fn valid_developer_diagnostic() -> DeveloperDiagnostic {
    DeveloperDiagnostic::DeletionFactMismatch(valid_mismatch())
}

#[test]
fn typed_deletion_diagnostic_decodes_nullable_server_payload() {
    let notice: TranscriptNoticeRow = serde_json::from_value(json!({
        "Reason": "runtime_diagnostic",
        "Severity": "error",
        "Diagnostic": {
            "Code": null,
            "Detail": null,
            "Developer": {
                "deletion_fact_mismatch": {
                    "call_id": "call-1",
                    "operation_id": {"HunkOrdinal": 2},
                    "mismatch_kind": "missing"
                }
            }
        }
    }))
    .expect("server-emitted typed diagnostic notice must decode");

    let diagnostic = notice
        .diagnostic
        .expect("typed transcript diagnostic must be present");
    let TranscriptDiagnostic::Developer(DeveloperDiagnostic::DeletionFactMismatch(mismatch)) =
        diagnostic
    else {
        panic!("expected typed deletion mismatch diagnostic");
    };
    assert_eq!(mismatch.call_id, "call-1");
    assert_eq!(mismatch.operation_id.hunk_ordinal, 2);
    assert_eq!(mismatch.mismatch_kind, DeletionFactMismatchKind::Missing);
}

#[test]
fn diagnostic_variants_encode_with_nullable_inactive_wire_fields() {
    let legacy = serde_json::to_value(TranscriptDiagnostic::Legacy {
        code: "legacy_code".to_owned(),
        detail: "legacy detail".to_owned(),
    })
    .expect("legacy diagnostic must encode");
    assert_eq!(
        legacy,
        json!({
            "Code": "legacy_code",
            "Detail": "legacy detail",
            "Developer": null
        })
    );

    let developer =
        serde_json::to_value(TranscriptDiagnostic::Developer(valid_developer_diagnostic()))
            .expect("developer diagnostic must encode");
    assert_eq!(developer["Code"], json!(null));
    assert_eq!(developer["Detail"], json!(null));
    assert!(developer["Developer"]["deletion_fact_mismatch"].is_object());
}

#[test]
fn legacy_diagnostic_decodes_without_developer_variant() {
    let diagnostic: TranscriptDiagnostic = serde_json::from_value(json!({
        "Code": "legacy_code",
        "Detail": "legacy detail",
        "Developer": null
    }))
    .expect("legacy diagnostic must decode");

    assert_eq!(
        diagnostic,
        TranscriptDiagnostic::Legacy {
            code: "legacy_code".to_owned(),
            detail: "legacy detail".to_owned(),
        }
    );
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
        json!({"Code": null, "Detail": null, "Developer": null}),
        json!({"Code": "legacy", "Detail": null, "Developer": null}),
        json!({"Code": null, "Detail": "detail", "Developer": null}),
        json!({"Code": "", "Detail": "", "Developer": null}),
        json!({"Code": "legacy", "Detail": "detail", "Developer": valid_developer}),
        json!({"Code": null, "Detail": null, "Developer": {}}),
        json!({
            "Code": null,
            "Detail": null,
            "Developer": {
                "deletion_fact_mismatch": {
                    "call_id": " ",
                    "operation_id": {"HunkOrdinal": 2},
                    "mismatch_kind": "missing"
                }
            }
        }),
        json!({
            "Code": null,
            "Detail": null,
            "Developer": {
                "deletion_fact_mismatch": {
                    "call_id": "call-1",
                    "operation_id": {"HunkOrdinal": -1},
                    "mismatch_kind": "missing"
                }
            }
        }),
    ] {
        assert!(
            serde_json::from_value::<TranscriptDiagnostic>(payload).is_err(),
            "invalid diagnostic must not decode"
        );
    }
}

#[test]
fn invalid_diagnostic_variants_are_rejected_on_encode() {
    for diagnostic in [
        TranscriptDiagnostic::Legacy {
            code: " ".to_owned(),
            detail: "detail".to_owned(),
        },
        TranscriptDiagnostic::Legacy {
            code: "legacy".to_owned(),
            detail: "\t".to_owned(),
        },
        TranscriptDiagnostic::Developer(DeveloperDiagnostic::DeletionFactMismatch(
            DeletionFactMismatchDeveloperDiagnostic {
                call_id: " ".to_owned(),
                ..valid_mismatch()
            },
        )),
        TranscriptDiagnostic::Developer(DeveloperDiagnostic::DeletionFactMismatch(
            DeletionFactMismatchDeveloperDiagnostic {
                operation_id: WholeFileDeletionOperationId { hunk_ordinal: -1 },
                ..valid_mismatch()
            },
        )),
    ] {
        assert!(
            serde_json::to_value(diagnostic).is_err(),
            "invalid diagnostic must not encode"
        );
    }
}
