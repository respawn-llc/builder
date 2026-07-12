# Workflow Editor Spec

## Product Direction

- Workflow editor/library UX is about how operators use top-level reusable workflows, not only graph-editing mechanics.
- Daily work remains project-first. Users open a project, then choose one linked reusable workflow.
- Workflows are top-level reusable definitions in the data model, presented inside projects as linked process lenses rather than owned children.
- Projects stay first-class daily-work destinations because they provide task namespace, workspaces, source workspace defaults, and project-workflow-link context.
- Home keeps Projects as the primary picker and global Inbox as the secondary pane.
- A global Workflow Library entry point exists for creating/editing reusable workflow definitions across projects without entering a project first.
- Workflow Library is definition management only. It lists/opens/creates/deletes workflows and manages project links. It does not create tasks or act as an aggregate task board.
- Task creation remains project-originated because tasks require project, workspace, and project-workflow-link context.
- Project-workflow pairing is visible in UI. A task derives project/workflow execution context through `project_workflow_link_id -> project_workflow_links`.
- Workflow editor UI uses transition language for graph connections. Edge terminology is internal to graph/persistence adapters.
- Do not use transfer-to-project semantics unless explicitly reintroduced. Workflows are reusable definitions and project association is link/unlink.

## Editor Scope

- The editor is a workflow definition editor with a graph canvas, route-local draft state, sidebar edit forms, validation, conflict handling, and atomic Save/Discard.
- The editor route accepts project context when opened from a board and also supports global workflow-definition context for Workflow Library usage.
- A project-scoped editor route is project-link gated. Direct project-context route with an unlinked workflow shows a blocker rather than displaying the workflow.
- Runtime/task state remains in Kanban and task detail. The editor edits workflow definitions, not live task execution.
- The canvas renders start/backlog, agent, script, join, terminal/done, disconnected nodes, and node groups.
- Join nodes are inspectable internal merge plumbing. Board/Kanban read models still omit join columns.
- Board/Kanban column order is derived from workflow graph structure. Persisted node list order is not a board-order source; structurally ambiguous non-terminal sibling branches are ordered by node key, and reachable terminal columns sort after reachable non-terminal columns when graph precedence does not decide their order. Visible cycles from backward/rework transitions are ordered from their structural entry and graph-discovery order, so downstream review or gate nodes do not move before the upstream family that leads to them.
- GUI-authored node groups are execution-shaped parallel groups. A saved node group contains branch nodes and one join; its fan-out is represented by one fan-out transition.
- One-node node groups may exist only as unsaved invalid drafts while the operator is building the parallel group.
- Agent nodes show name plus assignee role. Script nodes show their configured path when present. Non-agent/no-role nodes have blank role line.
- Node kind color communicates kind: start primary/blue, agent neutral/gray, script code/subroutine styling, join secondary/orange, terminal success/green.
- Each visible transition branch color communicates its context-preservation mode: `new_session` primary/blue, `continue_session` neutral/gray, and `compact_and_continue_session` secondary/orange.
- Validation-error red is reserved for invalid graph entities and overrides normal semantic colors.
- Transition labels show the transition label or key. Fan-out branch labels show the branch key.
- Node groups render visually as labeled branch islands. The owned Join renders outside the island to the right, vertically centered with the island, while remaining owned by the node group. Branch-to-Join routes are normalized into root canvas coordinates before rendering. Empty node groups are not a saved workflow concept.
- Canvas layout is deterministic client-side ELK layout from graph structure. Coordinates are not persisted.
- Layout orientation is left-to-right.
- Canvas layering keeps group backgrounds below transition paths and labels, while workflow node cards and handles stay above transition paths and labels.
- Initial viewport fits the whole graph on first open. Live refetch preserves pan/zoom, clears stale selection through React Flow state, and shows workflow-updated feedback.
- Canvas controls are inspect workflow, zoom in/out, fit-to-view, and reset zoom in a top-left floating island. There is no minimap.
- The canvas legend starts collapsed in the bottom-left corner and uses a help/question-mark affordance when collapsed.
- The canvas add-node affordance uses a plain plus icon. Zoom controls use zoom-specific icons or a visually separate zoom control so `+` consistently means add.
- Keyboard shortcuts are zoom in/out, reset, fit-to-view, and delete selected editable graph entities.
- Expected scale is optimized for 5-50 nodes and 5-100 transitions, with graceful behavior up to roughly 200 nodes.

## Draft Editing

- Editing uses GUI route-local draft state. Server mutations apply only on Save.
- Draft state owns workflow metadata, node identity fields, transition invocation fields, join aggregate diagnostics, dirty state, remote conflicts, and draft version counters.
- Workflow metadata editing includes workflow name, description, and execution target policy through the workflow inspector/settings sidebar.
- Execution target policy exposes exactly five choices: no managed worktree, source `HEAD`, repository default branch, custom Git ref, and ask when execution first starts.
- The policy uses an existing UI-kit single-choice control with concise explanations. Prefer an explained vertical choice group when shared components support it cleanly; a shared dropdown plus adjacent explanation is acceptable. Custom Git ref reveals its value field directly with the selected policy.
- Selecting a policy other than custom Git ref clears the custom-ref value. A missing custom-ref value is a draft validation issue rather than a hard Save rejection.
- The editor does not attempt repository-dependent Git resolution. The selected revision resolves against each task's source workspace when execution first begins.
- Agent node editing includes display name, key, and assignee role. Script node editing includes display name, key, and script path. Agent prompts and parameters are edited on transitions.
- Start/backlog and terminal node editing includes display name and key. Their kind and execution config stay fixed by domain validation.
- The assignee dropdown is sourced from server readiness `subagent_roles`.
- If a workflow references a legacy role no longer configured, the legacy role remains visible/selectable instead of forcing the placeholder state.
- Node inspectors are identity-focused. Transition configuration is edited by selecting transition visuals.
- Transition inspectors edit label, key, model-facing description, prompt/context when the target is an agent, parameters when the source is an executable node, approval, routing, and validation issues.
- Transition display labels are separate from transition keys and model-facing descriptions, and derive from keys until manually edited. The transition inspector labels the human display text as `Label`, the model-facing `transition_id` as `Key`, and the prompt-visible description as `Model-facing description`; it does not expose a separate `Transition ID` label.
- Selecting a normal transition opens its transition inspector. Selecting a fan-out branch opens the branch invocation editor and includes compact fan-out parent metadata. The fan-out parent owns source-choice label/key and approval; branches own target prompt, parameters, context, and routing.
- Normal transitions hide the generated branch key. Fan-out branch inspectors expose `Branch key` for the concrete edge key; fan-out branch keys are generated from target node keys, editable in the branch editor, must use workflow model-key format, and are unique within the parent fan-out transition.
- Parameter fields contain a stable key and `Model-facing description`. Parameters are string-only and required when declared.
- Parameter keys cannot be `transition` or `commentary`.
- Fan-out branch parameters are unioned into one source result contract. Matching branch parameter keys share one produced value only when their trimmed descriptions are identical; the merged parameter uses that trimmed description. Different descriptions for the same key are validation errors.
- Prompt editing for transitions shows invocation-parameter placeholder chips below the prompt field. Clicking a first-order parameter chip inserts `.Params.<parameter_key>` at the prompt cursor, or at the end when the prompt field is not focused. The informational `{{.Params.<transition_key>.<parameter>}}` chip opens helper text for previous-transition parameter references and does not insert template text.
- Transition prompt built-in field placeholders are exactly `.TaskId`, `.TaskShortId`, `.TaskTitle`, `.TaskBody`, `.NodeId`, `.NodeKey`, and `.NodeDisplayName`. The runtime-owned `.Params.commentary` placeholder renders the source transition commentary, or an empty string when no commentary exists. Unsupported top-level prompt field references are validation errors.
- Prompts can reference previous transition parameters with `.Params.<transition_key>.<parameter_key>`, for example `{{.Params.planning.plan_file_location}}`. Previous-transition references validate only against guaranteed-prior transitions. A transition is guaranteed-prior when every path from Start to the prompt-owning branch source passes through the referenced transition, so branching outputs are usable only when the producing transition output is guaranteed to exist. Inside a parallel batch, previous-transition lookup is scoped to the same batch.
- `.Nodes.<node_key>.<field>` prompt references are not a user-authored workflow editor concept.
- Transition prompts into agent nodes are required for task start/execution. Drafts may be saved with empty agent-target prompts. Transitions into non-agent nodes cannot have prompts.
- Start transitions can have prompts for their first agent target and cannot declare parameters. Start transition prompts can use built-in prompt fields but show no parameter chips.
- Join inspectors show the read-only aggregated parameter set and same-key collision errors. Join outgoing transitions do not declare parameters. Join-to-agent prompts can reference aggregate parameters through `.Params.<parameter_key>`. A same-key parameter shared by branches of one fan-out transition de-duplicates at the join because it has one producing transition result; same-key parameters from different producing transitions collide.
- Inspector validation sections keep their section header and render errors as plain bullet lists without card containers or code chips.
- Context mode and context source are visible only for transitions into agent nodes. The context-source selector remains openable for agent targets. Unavailable context-source options stay visible, are disabled, and show `N/A for current configuration`.
- `Previous run of this target` is enabled for continuation modes only when the target is an agent node that dominates the transition source, meaning every path from Start to the source passes through the target. Runtime resolves the latest completed run of that target before the transition event, scoped to the same parallel batch when applicable, and fails without fallback when no matching run exists.
- `Previous run of this target, or new session` is enabled for continuation modes into agent targets. Runtime resolves the latest completed run of the target before the transition event when one exists; otherwise it starts the target with an effective `new_session`.
- Selected-node context-source options list all agent nodes for agent-target transitions. A selected-node option is enabled only for continuation modes when the selected node is not the target and is guaranteed before the transition source. Invalid preserved selected-node values remain visible as disabled options.
- Script, join, and terminal targets do not start agent sessions.
- Transition targets are assigned through canvas connections instead of inspector dropdowns.
- Editable non-terminal nodes show one always-visible `+` creation handle in a reserved right-side interaction rail. Routed transition endpoints use layout ports that do not occupy the creation-handle slot.
- Reconnect handles appear on transition hover. Operators reconnect by dragging a transition endpoint onto a node body or side; the editor does not show node-side target connection handles.
- Reconnecting preserves transition prompt, parameters, context, approval, and key. Invalid preserved configuration remains in the draft with validation errors unless the topology itself is impossible.
- Source-tail reconnect is unavailable for fan-out branches because branch source changes alter fan-out membership.
- Unsupported graph entities use read-only sidebar inspection with clear unavailable-editing behavior.
- Topology editing includes adding and deleting agent/script/terminal nodes, node groups, and transitions, drag-connecting transitions on the canvas, reconnecting transition endpoints, editing transition route/config facts, and creating/removing node group membership.
- Add node is a canvas action, not a right-sidebar form. It creates unconnected agent, script, or terminal nodes; validation explains unreachable or incomplete graph states until the operator wires them.
- Drag-connecting from a source node to a target node creates a new normal transition by default.
- When the target is an agent branch inside a node group and the source already has one unambiguous fan-out transition into sibling branches of that group, drag-connect reuses that fan-out transition so the group remains execution-shaped.
- Other fan-out edits that add branches to an existing fan-out transition are explicit fan-out/group actions, not the default drag-connect behavior.
- Node deletion cascades incident transition deletion and removes transitions that have no remaining branches.
- Deletion is available through keyboard delete/backspace, context menu actions, and inspector trash actions where the selected entity is editable.
- Deleting a transition with prompt text confirms immediately. Deleting only parameters does not confirm. Cascading node/group deletion confirms immediately when prompt-bearing transitions would be removed.
- Direct join creation is not exposed as generic node creation. Joins are created through node group/parallelism editing.

## Save, Validation, And Conflicts

- Route-level Save/Discard appears in one bottom-right workflow-editor status island.
- The status island owns unsaved state, validation issues, save blockers, remote conflict state, and save errors.
- Draft validation and execution validation are shown separately. Draft validation blocks graph-dirty saves when it has blocking errors.
- Execution validation errors remain visible but do not block metadata-only saves.
- Draft validation blocks prompts into non-agent targets, duplicate transition keys, invalid or duplicate fan-out branch keys, invalid parameter keys/descriptions, invalid previous-parameter references, and join aggregate key collisions. Task start/execution validation blocks empty prompts into agent targets.
- Definitions do not fall back from legacy node-owned prompt/contract fields. Legacy-authored contracts must be reauthored as transition prompts and parameters.
- Legacy node-owned contract fields are round-tripped as inert metadata. Validation and runtime ignore them for execution. Active-work edit blockers continue to apply; blocked legacy definitions cannot become runnable through fallback behavior.
- Metadata-only and no-op saves bypass graph edit policy and active-work blockers.
- Actual graph changes run save preview before save.
- Save preview returns draft validation, execution validation, active-task blockers, destructive/removal impact, and confirmation requirement.
- Destructive graph saves are confirmed inside the bottom-right workflow-editor status island. The editor does not open a modal or sidebar for graph-save confirmation.
- Save recomputes validation and impact transactionally, rejects stale workflow versions, rejects active blockers, rejects unconfirmed or changed destructive impact, applies metadata and graph changes atomically, increments workflow version once, publishes linked-project events, and returns the saved definition plus validations.
- Desktop and server protocol versions gate workflow graph contract compatibility before the editor can communicate with the service.
- Workflow definitions use one monotonic `version` over persisted definition changes. Metadata-only changes and graph changes each increment it once; combined metadata+graph saves also increment it once; no-op saves increment neither.
- If a subscription event changes the same workflow while the local draft is dirty, keep the local draft and show a conflict banner.
- Conflict banner actions are Reload remote and Keep editing.
- Save uses the expected workflow version and stale save rejects clearly.
- Workflow-scoped subscriptions are required so global Workflow Library editor mode gets the same reactive conflict behavior as project-linked editing.

## Workflow Library And Linking

- Project workflow management uses `Link workflow` language.
- Link workflow opens a global right-side sidebar picker listing all reusable workflows and a `New workflow` action.
- `New workflow` from project-originated Link workflow creates a global workflow definition, auto-links it to the originating project, and opens the editor.
- Project-originated `New workflow` uses default policy `if_project_has_none`: it becomes the project default only when the project has no default workflow yet. It does not replace an existing default workflow.
- `New workflow` from global Workflow Library creates a global workflow definition without implicit project linkage unless the user explicitly links it later.
- Workflow editor/library routes may be global workflow-definition routes, but project-originated task/board routes remain project-scoped.
- The editor may own selected-workflow settings/delete actions. Workflow Library/sidebar owns create/copy/link entry points.
- Workflow settings include name/description and actions to link/unlink workflow to projects.
- Project selection for workflow settings/linking is paginated and minimal, hosted inside the sidebar rather than a native blocking window.
- The workflow editor toolbar Add node control opens its node-kind popup on hover or focus. Clicking the toolbar button itself does not create a node or toggle the popup.

## Global Sidebar

- Workflow intermediary, picker, settings, and entity-edit flows use a global right-side sidebar island.
- Do not use Tauri native blocking windows for workflow UX.
- Sidebar stretches from the right side of the screen and is reusable from board/editor/other pages.
- Sidebar supports typed destinations, local sidebar navigation, and returning result/cancel to opener screen.
- Opening another main destination or navigating back/forward closes the sidebar and rejects/returns canceled for pending opener promises.
- Sidebar visual treatment: fixed right overlay, glass island, full-height below titlebar, left rounded corners, adaptive width around 420-560px and max `calc(100vw - margins)`.
- Sidebar destinations should be terminal enough to avoid stacked blocking surfaces. Child picker destinations may return results to previous sidebar screen.

## Editing Constraints

- GUI remains a remote-control surface. Server remains authoritative for definitions, validation, persistence, project links, events, task-impact analysis, workflow version, and destructive cleanup.
- Editing a linked workflow is allowed while tasks exist on the board.
- Save validation blocks graph edits that would detach a task from the graph entity that anchors its current visible workflow state.
- Active means any task whose active/waiting placement is not start/backlog or terminal/done and is not solely interrupted, any pending approval, any non-completed/non-interrupted run needing runtime ownership, or any other non-terminal automation state.
- Backlog/start deletion is out of scope. Blocked graph deletes surface as toast feedback. Hide `start` from add/kind-change controls. Existing Backlog can be renamed where safe, but kind stays fixed.
- Start node outgoing transitions may be edited in drafts, but execution validation requires exactly one start transition with exactly one branch targeting an executable node.
- Start/Backlog cannot be the fan-out source for a node group. Use a split agent after Start/Backlog, fan out from that agent into the grouped branches, then join the branches.
- Done/terminal deletion is allowed only when at least one other terminal node remains; otherwise block with toast.
- Saved node groups must be execution-shaped parallel groups. A node group without enough branch nodes or without exactly one owned join blocks save validation.
- Dragging a node in the workflow editor changes node group membership, not persisted canvas position. The real node remains in its derived layout position and a drag ghost follows the pointer. Canvas layout remains derived from the graph.
- Node group drag/drop is validated as a membership operation. If the editor cannot safely infer the source node or fan-out transition needed for fan-out wiring, the membership is preserved and validation explains the incomplete wiring before save.
- Destructive delete impact is evaluated on Save, not at draft edit time.
- Save runs server-side impact check for pending graph diff.
- If a graph diff would remove a node, transition, or edge currently anchoring an active task, pending approval, or unresolved parallel branch, Save is blocked.
- If a graph diff would change the kind of a node referenced by any task history, Save is blocked. Delete-and-confirm detaches historical node references; kind changes keep the node id and would reinterpret existing placements.
- Transition invocation metadata may be edited while tasks, pending approvals, or runs exist when the change only affects future work. This includes transition group display name/description and edge approval, context mode/source, and valid prompt template settings.
- Transition contract changes are blocked while unresolved work references the affected transition branch or can still emit it from the source node. This includes transition source/key changes and edge branch key, target node, parameters, input bindings, and output requirements.
- Moving an existing edge to a different transition group is blocked when any task history references that edge, because group membership is part of historical branch interpretation.
- If only backlog/done tasks would lose graph references due to removed nodes or transitions, show confirmation with affected reference counts before applying.
- Manual task moves are blocked for selected prior-node, `Previous run of this target`, and `Previous run of this target, or new session` continuation context sources.
- Requested destructive wording pattern: `XXX task references will be detached from the removed graph entity. Proceed?`
- Workflow graph saves never delete or move tasks; whole-workflow deletion is the task-deleting path.

## Q/A Decisions Preserved

- Q: Should runtime/task overlay appear in editor? A: No; runtime is handled by Kanban/task detail.
- Q: Should workflow editor UX use native blocking windows? A: No; use the global right-side sidebar.
- Q: Should workflow library create tasks? A: No; it is definition management only.
- Q: Is task-link normalization compatible? A: No compatibility shim; hard cutover.
- Q: Should project-originated `New workflow` replace an existing project default? A: No; link it and open the editor, but set it as default only when the project has no default.
- Q: Where do workflow-level management actions belong? A: Editor may own selected-workflow settings/delete, while create/copy/link remain in Workflow Library/sidebar.
- Q: What does drag-connecting a transition from a node with existing outgoing transitions do? A: It creates a new normal transition by default. Fan-out into an existing fan-out transition is explicit.
- Q: Should add-node create an incoming transition automatically? A: No. New nodes are unconnected until the operator wires them.
- Q: Which node kinds does generic add-node expose? A: Agent, script, and terminal. Start is fixed, and join is created through node group/parallelism editing.
- Q: Where is destructive graph-save confirmation shown? A: In the workflow-editor status island.
- Q: What does dragging a node mean in the workflow editor? A: Node group membership DnD, not canvas repositioning.
