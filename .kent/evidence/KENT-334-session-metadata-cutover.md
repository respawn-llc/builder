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

## Completed boundary at `791217047`

- [x] Replace the inspection tracer's legacy Run/Placement bridge: persisted workflow inspection resolves and builds from direct Task/current-Node/association state only.
- [x] Add one atomic live Session-to-Current-Node binding operation and use it from `Starter.StartCurrentNode`; preserve `AssociateTaskSession` as historical provenance only.
- [x] Distinguish durable Task origin from a currently Session-bound Node in dump-model-request inspection: Task-owned retained non-current Sessions use the ordinary Session plan.
- [x] User approved retaining the exact entering Transition Branch as current Task state. The field is required to reconstruct the latest branch-owned prompt/context policy without retained movement history.
- [x] RED/GREEN: `TestBindSessionToCurrentNodeEstablishesLiveBindingAndProvenance` verifies the aggregate's direct Session Task ownership, exact Current-Node binding, and retained node association without a Run bridge.

## Dependent server lifecycle boundary

- [ ] Close and evidence the live Start response/protocol path. The response
  returns Current Nodes, but this remains part of the runner and server
  lifecycle API checkpoint until its exact end-to-end command and coherent
  commit are recorded.
