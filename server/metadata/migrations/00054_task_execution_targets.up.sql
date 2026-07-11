-- +goose Up

CREATE TABLE task_execution_targets (
    task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
    policy TEXT NOT NULL CHECK (policy IN ('none', 'head', 'default_branch', 'custom_ref')),
    requested_custom_ref TEXT
        CHECK (requested_custom_ref IS NULL OR length(trim(requested_custom_ref)) > 0),
    resolved_source_kind TEXT
        CHECK (resolved_source_kind IS NULL OR resolved_source_kind IN ('named_ref', 'detached_commit')),
    resolved_source_ref TEXT
        CHECK (resolved_source_ref IS NULL OR length(trim(resolved_source_ref)) > 0),
    resolved_commit TEXT
        CHECK (resolved_commit IS NULL OR length(trim(resolved_commit)) > 0),
    state TEXT NOT NULL
        CHECK (state IN ('initial_provisioning', 'locked', 'locked_reprovisioning')),
    provisioning_generation TEXT
        CHECK (provisioning_generation IS NULL OR length(trim(provisioning_generation)) > 0),
    setup_provisioning_generation TEXT
        CHECK (setup_provisioning_generation IS NULL OR length(trim(setup_provisioning_generation)) > 0),
    setup_state TEXT NOT NULL
        CHECK (setup_state IN ('not_applicable', 'pending', 'running', 'succeeded', 'failed')),
    active_claim_generation TEXT
        CHECK (active_claim_generation IS NULL OR length(trim(active_claim_generation)) > 0),
    active_claim_phase TEXT
        CHECK (active_claim_phase IS NULL OR active_claim_phase IN ('materializing', 'recovery_queued', 'recovering')),
    recovery_disposition TEXT NOT NULL
        CHECK (recovery_disposition IN ('available', 'manual_recovery')),
    recovery_cause TEXT
        CHECK (recovery_cause IS NULL OR length(trim(recovery_cause)) > 0),
    exact_branch_observation TEXT
        CHECK (exact_branch_observation IS NULL OR length(trim(exact_branch_observation)) > 0),
    linked_worktree_common_dir TEXT
        CHECK (linked_worktree_common_dir IS NULL OR length(trim(linked_worktree_common_dir)) > 0),
    linked_worktree_admin_entry TEXT
        CHECK (linked_worktree_admin_entry IS NULL OR length(trim(linked_worktree_admin_entry)) > 0),
    linked_worktree_gitdir TEXT
        CHECK (linked_worktree_gitdir IS NULL OR length(trim(linked_worktree_gitdir)) > 0),
    linked_worktree_head_ref TEXT
        CHECK (linked_worktree_head_ref IS NULL OR length(trim(linked_worktree_head_ref)) > 0),
    expected_detachment_commit TEXT
        CHECK (expected_detachment_commit IS NULL OR length(trim(expected_detachment_commit)) > 0),
    CHECK (
        (policy = 'custom_ref' AND requested_custom_ref IS NOT NULL)
        OR (policy != 'custom_ref' AND requested_custom_ref IS NULL)
    ),
    CHECK (
        (active_claim_generation IS NULL AND active_claim_phase IS NULL)
        OR (active_claim_generation IS NOT NULL AND active_claim_phase IS NOT NULL)
    ),
    CHECK (
        (recovery_disposition = 'available' AND recovery_cause IS NULL)
        OR (recovery_disposition = 'manual_recovery' AND recovery_cause IS NOT NULL)
    ),
    CHECK (
        (
            linked_worktree_common_dir IS NULL
            AND linked_worktree_admin_entry IS NULL
            AND linked_worktree_gitdir IS NULL
            AND linked_worktree_head_ref IS NULL
        )
        OR (
            linked_worktree_common_dir IS NOT NULL
            AND linked_worktree_admin_entry IS NOT NULL
            AND linked_worktree_gitdir IS NOT NULL
            AND linked_worktree_head_ref IS NOT NULL
        )
    ),
    CHECK (
        expected_detachment_commit IS NULL
        OR expected_detachment_commit = exact_branch_observation
    ),
    CHECK (
        (
            policy = 'none'
            AND resolved_source_kind IS NULL
            AND resolved_source_ref IS NULL
            AND resolved_commit IS NULL
            AND state = 'locked'
            AND provisioning_generation IS NULL
            AND setup_provisioning_generation IS NULL
            AND setup_state = 'not_applicable'
            AND active_claim_generation IS NULL
            AND active_claim_phase IS NULL
            AND recovery_disposition = 'available'
            AND recovery_cause IS NULL
            AND exact_branch_observation IS NULL
            AND linked_worktree_common_dir IS NULL
            AND linked_worktree_admin_entry IS NULL
            AND linked_worktree_gitdir IS NULL
            AND linked_worktree_head_ref IS NULL
            AND expected_detachment_commit IS NULL
        )
        OR (
            policy != 'none'
            AND resolved_source_kind IS NOT NULL
            AND resolved_commit IS NOT NULL
            AND provisioning_generation IS NOT NULL
            AND setup_provisioning_generation = provisioning_generation
            AND setup_state != 'not_applicable'
            AND (
                (resolved_source_kind = 'named_ref' AND resolved_source_ref IS NOT NULL)
                OR (resolved_source_kind = 'detached_commit' AND resolved_source_ref IS NULL)
            )
        )
    )
);
