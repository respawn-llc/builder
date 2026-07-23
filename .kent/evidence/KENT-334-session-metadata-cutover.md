# KENT-334 — legacy Session workflow metadata cutover

- [x] Read the approved migration/session cutover requirements and the workflow domain terminology.
- [x] Identify the direct ownership authority: `sessions.task_id`, `task_current_nodes.session_id`, and `session_workflow_node_associations`.
- [x] RED/GREEN: prove migration strips a stale `workflow_session` object while retaining direct Session Task/Node ownership.
- [x] Identify that a retained Session/Node association is historical provenance, not permission to bind that Session to an arbitrary current Node.
- [x] RED/GREEN: prove `ResolveCurrentSessionStartContext` reconstructs a migrated agent Session through direct Task/current-Node/association ownership despite a stale legacy Run ID.
- [x] GREEN: remove durable `session.Meta.WorkflowSession`, central metadata serialization/decoding, planner transport, and starter writes.
- [x] GREEN: resolve persisted workflow inspection from direct Session/current-node facts without consuming legacy Run ID.
- [x] RED/GREEN: prove legacy JSON is stripped on migration and ordinary Session import plus workspace retarget writes remove it.
- [x] Update obsolete Session-metadata tests and regenerate SQLite bindings.
- [x] Focused checks: migration resolver, session/metadata/launch/runtime-control/runprompt/session-launch focused tests; workflowrunner/core/sessionview/runtimeview compile checks; query generation; `git diff --check`.
- [x] RED/GREEN: `TestBuildPersistedWorkflowInspectionUsesDirectCurrentSessionOwnership` reconstructs inspection from direct Task/current-Node/association ownership with stale persisted `workflow_session` JSON; its legacy Run bridge is fixture-only until the later runner lifecycle cutover.
