use client_contracts::clientui::SessionExecutionTarget;
use client_contracts::routes;
use client_contracts::worktree::{
    WorktreeCreateResponse, WorktreeCreateTargetResolution, WorktreeCreateTargetResolutionKind,
    WorktreeCreateTargetResolveResponse, WorktreeDeleteResponse, WorktreeView,
};
use serde_json::json;

#[test]
fn worktree_list_response_deserializes_go_shape_and_ignores_future_fields() {
    let response: client_contracts::worktree::WorktreeListResponse = serde_json::from_value(json!({
        "target": {
            "WorkspaceID": "workspace-1",
            "WorkspaceName": "Builder Core",
            "WorkspaceRoot": "/repo",
            "WorkspaceAvailability": "available",
            "WorktreeID": "worktree-main",
            "WorktreeName": "main",
            "WorktreeRoot": "/repo",
            "WorktreeAvailability": "available",
            "CwdRelpath": "crates/tui",
            "EffectiveWorkdir": "/repo/crates/tui"
        },
        "worktrees": [
            {
                "worktree_id": "worktree-main",
                "display_name": "main",
                "canonical_root": "/repo",
                "availability": "available",
                "branch_ref": "refs/heads/main",
                "branch_name": "main",
                "is_main": true,
                "is_current": true,
                "builder_managed": true,
                "future_go_field": "ignored"
            },
            {
                "worktree_id": "worktree-detached",
                "display_name": "detached",
                "canonical_root": "/repo-detached",
                "availability": "blocked",
                "detached": true,
                "locked_reason": "in use",
                "prunable_reason": "gone",
                "dirty_file_count": 2,
                "origin_session_id": "session-2"
            }
        ],
        "future_response_field": true
    }))
    .unwrap();

    assert_eq!(
        response.target,
        SessionExecutionTarget {
            workspace_id: "workspace-1".to_owned(),
            workspace_name: "Builder Core".to_owned(),
            workspace_root: "/repo".to_owned(),
            workspace_availability: "available".to_owned(),
            worktree_id: "worktree-main".to_owned(),
            worktree_name: "main".to_owned(),
            worktree_root: "/repo".to_owned(),
            worktree_availability: "available".to_owned(),
            cwd_relpath: "crates/tui".to_owned(),
            effective_workdir: "/repo/crates/tui".to_owned(),
        }
    );
    assert_eq!(
        response.worktrees,
        vec![
            WorktreeView {
                worktree_id: "worktree-main".to_owned(),
                display_name: "main".to_owned(),
                canonical_root: "/repo".to_owned(),
                availability: "available".to_owned(),
                branch_ref: "refs/heads/main".to_owned(),
                branch_name: "main".to_owned(),
                detached: false,
                locked_reason: String::new(),
                prunable_reason: String::new(),
                dirty_file_count: 0,
                is_main: true,
                is_current: true,
                builder_managed: true,
                created_branch: false,
                origin_session_id: String::new(),
            },
            WorktreeView {
                worktree_id: "worktree-detached".to_owned(),
                display_name: "detached".to_owned(),
                canonical_root: "/repo-detached".to_owned(),
                availability: "blocked".to_owned(),
                branch_ref: String::new(),
                branch_name: String::new(),
                detached: true,
                locked_reason: "in use".to_owned(),
                prunable_reason: "gone".to_owned(),
                dirty_file_count: 2,
                is_main: false,
                is_current: false,
                builder_managed: false,
                created_branch: false,
                origin_session_id: "session-2".to_owned(),
            },
        ]
    );
}

#[test]
fn worktree_list_route_metadata_matches_go_control_route() {
    let route = routes::routes()
        .iter()
        .find(|route| route.method == "worktree.list")
        .unwrap();

    assert_eq!(route.kind, "unary");
    assert_eq!(route.auth, "server_auth");
    assert_eq!(route.rpc_scope, "session_active_project");
    assert_eq!(route.connection, "control");
    assert_eq!(route.dependency, "worktree");
}

#[test]
fn worktree_switch_response_deserializes_go_shape_and_ignores_future_fields() {
    let response: client_contracts::worktree::WorktreeSwitchResponse =
        serde_json::from_value(json!({
            "target": {
                "WorkspaceID": "workspace-1",
                "WorkspaceName": "Builder Core",
                "WorkspaceRoot": "/repo",
                "WorkspaceAvailability": "available",
                "WorktreeID": "worktree-rust",
                "WorktreeName": "rust-tui",
                "WorktreeRoot": "/repo-rust",
                "WorktreeAvailability": "available",
                "CwdRelpath": "crates/tui-rs",
                "EffectiveWorkdir": "/repo-rust/crates/tui-rs"
            },
            "worktree": {
                "worktree_id": "worktree-rust",
                "display_name": "rust-tui",
                "canonical_root": "/repo-rust",
                "availability": "available",
                "branch_ref": "refs/heads/rust-tui",
                "branch_name": "rust-tui",
                "is_current": true,
                "builder_managed": true,
                "future_go_field": "ignored"
            },
            "future_response_field": true
        }))
        .unwrap();

    assert_eq!(
        response.target,
        SessionExecutionTarget {
            workspace_id: "workspace-1".to_owned(),
            workspace_name: "Builder Core".to_owned(),
            workspace_root: "/repo".to_owned(),
            workspace_availability: "available".to_owned(),
            worktree_id: "worktree-rust".to_owned(),
            worktree_name: "rust-tui".to_owned(),
            worktree_root: "/repo-rust".to_owned(),
            worktree_availability: "available".to_owned(),
            cwd_relpath: "crates/tui-rs".to_owned(),
            effective_workdir: "/repo-rust/crates/tui-rs".to_owned(),
        }
    );
    assert_eq!(
        response.worktree,
        WorktreeView {
            worktree_id: "worktree-rust".to_owned(),
            display_name: "rust-tui".to_owned(),
            canonical_root: "/repo-rust".to_owned(),
            availability: "available".to_owned(),
            branch_ref: "refs/heads/rust-tui".to_owned(),
            branch_name: "rust-tui".to_owned(),
            detached: false,
            locked_reason: String::new(),
            prunable_reason: String::new(),
            dirty_file_count: 0,
            is_main: false,
            is_current: true,
            builder_managed: true,
            created_branch: false,
            origin_session_id: String::new(),
        }
    );
}

#[test]
fn worktree_switch_route_metadata_matches_go_control_route() {
    let route = routes::routes()
        .iter()
        .find(|route| route.method == "worktree.switch")
        .unwrap();

    assert_eq!(route.kind, "unary");
    assert_eq!(route.auth, "server_auth");
    assert_eq!(route.rpc_scope, "session_active_project");
    assert_eq!(route.connection, "control");
    assert_eq!(route.dependency, "worktree");
}

#[test]
fn worktree_create_target_resolve_response_deserializes_go_shape_and_ignores_future_fields() {
    let detached: WorktreeCreateTargetResolveResponse = serde_json::from_value(json!({
        "resolution": {
            "input": "HEAD~1",
            "kind": "detached_ref",
            "resolved_ref": "abc123",
            "future_resolution_field": true
        },
        "future_response_field": true
    }))
    .unwrap();
    let existing: WorktreeCreateTargetResolveResponse = serde_json::from_value(json!({
        "resolution": {
            "input": "main",
            "kind": "existing_branch",
            "resolved_ref": "main"
        }
    }))
    .unwrap();
    let new_branch: WorktreeCreateTargetResolveResponse = serde_json::from_value(json!({
        "resolution": {
            "input": "feature/new",
            "kind": "new_branch"
        }
    }))
    .unwrap();

    assert_eq!(
        detached.resolution,
        WorktreeCreateTargetResolution {
            input: "HEAD~1".to_owned(),
            kind: WorktreeCreateTargetResolutionKind::DetachedRef,
            resolved_ref: "abc123".to_owned(),
        }
    );
    assert_eq!(
        existing.resolution,
        WorktreeCreateTargetResolution {
            input: "main".to_owned(),
            kind: WorktreeCreateTargetResolutionKind::ExistingBranch,
            resolved_ref: "main".to_owned(),
        }
    );
    assert_eq!(
        new_branch.resolution,
        WorktreeCreateTargetResolution {
            input: "feature/new".to_owned(),
            kind: WorktreeCreateTargetResolutionKind::NewBranch,
            resolved_ref: String::new(),
        }
    );
}

#[test]
fn worktree_create_response_deserializes_go_shape_and_ignores_future_fields() {
    let response: WorktreeCreateResponse = serde_json::from_value(json!({
        "target": {
            "WorkspaceID": "workspace-1",
            "WorkspaceName": "Builder Core",
            "WorkspaceRoot": "/repo",
            "WorkspaceAvailability": "available",
            "WorktreeID": "worktree-new",
            "WorktreeName": "new-feature",
            "WorktreeRoot": "/wt/new-feature",
            "WorktreeAvailability": "available",
            "CwdRelpath": "crates/tui-rs",
            "EffectiveWorkdir": "/wt/new-feature/crates/tui-rs"
        },
        "worktree": {
            "worktree_id": "worktree-new",
            "display_name": "new-feature",
            "canonical_root": "/wt/new-feature",
            "availability": "available",
            "branch_ref": "refs/heads/feature/new",
            "branch_name": "feature/new",
            "is_current": true,
            "builder_managed": true,
            "created_branch": true,
            "future_go_field": "ignored"
        },
        "created_branch": true,
        "setup_scheduled": true,
        "future_response_field": true
    }))
    .unwrap();

    assert_eq!(
        response.target,
        SessionExecutionTarget {
            workspace_id: "workspace-1".to_owned(),
            workspace_name: "Builder Core".to_owned(),
            workspace_root: "/repo".to_owned(),
            workspace_availability: "available".to_owned(),
            worktree_id: "worktree-new".to_owned(),
            worktree_name: "new-feature".to_owned(),
            worktree_root: "/wt/new-feature".to_owned(),
            worktree_availability: "available".to_owned(),
            cwd_relpath: "crates/tui-rs".to_owned(),
            effective_workdir: "/wt/new-feature/crates/tui-rs".to_owned(),
        }
    );
    assert_eq!(response.worktree.worktree_id, "worktree-new");
    assert_eq!(response.worktree.display_name, "new-feature");
    assert!(response.worktree.created_branch);
    assert!(response.created_branch);
    assert!(response.setup_scheduled);
}

#[test]
fn worktree_delete_response_deserializes_go_shape_and_ignores_future_fields() {
    let response: WorktreeDeleteResponse = serde_json::from_value(json!({
        "target": {
            "WorkspaceID": "workspace-1",
            "WorkspaceName": "Builder Core",
            "WorkspaceRoot": "/repo",
            "WorkspaceAvailability": "available",
            "WorktreeID": "worktree-main",
            "WorktreeName": "main",
            "WorktreeRoot": "/repo",
            "WorktreeAvailability": "available",
            "CwdRelpath": "",
            "EffectiveWorkdir": "/repo"
        },
        "worktree": {
            "worktree_id": "worktree-rust",
            "display_name": "rust-tui",
            "canonical_root": "/repo-rust",
            "availability": "available",
            "branch_name": "rust-tui",
            "builder_managed": true,
            "future_go_field": "ignored"
        },
        "branch_deleted": true,
        "branch_cleanup_message": "Deleted branch rust-tui",
        "future_response_field": true
    }))
    .unwrap();

    assert_eq!(
        response.target,
        SessionExecutionTarget {
            workspace_id: "workspace-1".to_owned(),
            workspace_name: "Builder Core".to_owned(),
            workspace_root: "/repo".to_owned(),
            workspace_availability: "available".to_owned(),
            worktree_id: "worktree-main".to_owned(),
            worktree_name: "main".to_owned(),
            worktree_root: "/repo".to_owned(),
            worktree_availability: "available".to_owned(),
            cwd_relpath: String::new(),
            effective_workdir: "/repo".to_owned(),
        }
    );
    assert_eq!(response.worktree.worktree_id, "worktree-rust");
    assert_eq!(response.worktree.display_name, "rust-tui");
    assert!(response.branch_deleted);
    assert_eq!(response.branch_cleanup_message, "Deleted branch rust-tui");
}

#[test]
fn worktree_delete_route_metadata_matches_go_control_route() {
    let route = routes::routes()
        .iter()
        .find(|route| route.method == "worktree.delete")
        .unwrap();

    assert_eq!(route.kind, "unary");
    assert_eq!(route.auth, "server_auth");
    assert_eq!(route.rpc_scope, "session_active_project");
    assert_eq!(route.connection, "control");
    assert_eq!(route.dependency, "worktree");
}
