# Workflow Editor

## Purpose and scope

- A Workflow is a top-level reusable definition. Projects use Workflows through Project Workflow Links; they do not own copied Workflow definitions.
- Daily work is project-first: operators open a Project and choose one of its linked Workflows. Projects remain the source of a Task's namespace, workspaces, source-workspace defaults, and Project Workflow Link context.
- Home presents Projects as the primary destination and the global Inbox as a secondary destination.
- Workflow Library manages reusable Workflow definitions and their Project Workflow Links. It can list, open, create, copy, link, unlink, and delete Workflows, but it neither creates Tasks nor acts as a Task board.
- Tasks are created from a Project because Task creation requires Project, workspace, and Project Workflow Link context.
- Project/Workflow pairing is visible wherever it affects work. A Task's Project Workflow Link is its authoritative Project/Workflow pairing.
- The editor changes Workflow definitions, not live Task execution. Kanban and Task detail show live work and Task state.
- The editor uses **transition** and **transition branch** for user-facing graph connections.
- Workflows are linked and unlinked from Projects; there is no transfer-to-Project behavior.
- An editor opened from a Project is available only for a Workflow linked to that Project. An unlinked Workflow in that context is blocked rather than shown.

## Workflow canvas

- The canvas shows start, agent, script, join, terminal, disconnected Nodes, and Node Groups.
- Join Nodes are inspectable merge plumbing. They are not Kanban columns.
- Kanban column order comes from Workflow structure, never from authored Node-list order. Where structure does not decide between reachable non-terminal sibling branches, Node Key decides. Reachable terminal columns follow reachable non-terminal columns where structure does not decide. Visible rework cycles retain the order of their structural entry and discovery, so a later review or gate Node does not precede the upstream family that reaches it.
- A saved Node Group is an execution-shaped parallel group: it contains its branch Nodes and exactly one owned Join, with one Fan-Out Transition into its branches. A one-Node group may exist only as an unsaved invalid Workflow Draft.
- Agent Nodes show their name and Assignee. Script Nodes show their configured path when present. Nodes without an Assignee have no role line.
- Node appearance distinguishes kind: start is blue, agent is gray, script has a code-like treatment, join is orange, and terminal is green. Invalid graph entities are red and override these treatments.
- Transition branches visibly distinguish their Context-Preservation Mode: a new Session is blue, a continued Session is gray, and a compacted-and-continued Session is orange.
- A transition displays its Transition Label or Transition Key. A Fan-Out Transition branch displays its Transition Branch Key.
- Node Groups appear as labeled branch islands. Their owned Join appears to the right, vertically centered on the group, while remaining part of that group. Empty Node Groups are not saved.
- Layout is deterministic, derived from Workflow structure, and left-to-right. Canvas positions are not Workflow data.
- Group backgrounds remain behind transitions and labels; Node cards and their interaction points remain above them.
- Opening a Workflow fits the complete graph. Refreshing a changed Workflow preserves pan and zoom, clears stale selection, and reports that the Workflow changed.
- Canvas controls provide inspection, zoom in/out, fit-to-view, and zoom reset from the top-left. There is no minimap. The legend begins collapsed in the bottom-left and can be opened from the canvas.
- The add-Node control uses a plain plus sign. Zoom controls use zoom-specific or visually separate controls so plus always means add.
- Keyboard shortcuts support zooming, reset, fit-to-view, and deletion of selected editable graph entities.
- The expected working scale is 5–50 Nodes and 5–100 transitions; the editor remains usable through roughly 200 Nodes.

## Drafts and editing

- Edits stay in a Workflow Draft until Save. Discard abandons them. Saving changes the Workflow definition as one operation.
- The saved Workflow definition is authoritative for validation, Project Workflow Links, change notices, Task impact, Workflow Version, and deletion.
- Workflow details include name, description, and Execution Target Policy.
- The available Execution Target Policies are: no managed worktree, source `HEAD`, repository default branch, custom Git ref, and asking when executable work first starts.
- The policy selector explains each choice. Selecting custom Git ref exposes its value. Selecting any other policy clears the custom-ref value.
- A missing custom Git ref is a draft validation issue, not an immediate Save rejection. The selected revision is resolved in each Task's source workspace when execution starts; the editor does not resolve repository state.
- Agent Nodes can edit display name, Node Key, and Assignee. Script Nodes can edit display name, Node Key, and script path. Start and terminal Nodes can edit display name and Node Key, but their kind and execution configuration are fixed.
- Available Assignees come from configured subagent roles. A referenced role absent from configuration remains visible and selectable.
- The Agent Node inspector keeps the ordinary Assignee picker and requires a concrete fallback Assignee.
- Each eligible serial Transition Branch inspector exposes an Assignee picker item labeled **Let the previous node choose**. Selecting it enables Assignee selection only for that Transition.
- An eligible Transition without that override uses the target Agent Node's configured Assignee, so incoming Transitions may mix override-enabled and fallback-only behavior.
- A checkbox labeled **Let the previous node select thinking level** enables thinking selection only for the inspected eligible Transition.
- Assignee selection is unavailable with `N/A for current configuration` when no role is explicitly configured with `agent_callable = true`.
- With one explicitly agent-callable role, Assignee selection remains available but no model-facing Assignee Parameter appears because Kent applies that role automatically.
- Thinking selection is unavailable with `N/A for current configuration` when no applicable target model supports thinking.
- The thinking checkbox may remain enabled with one finite supported level, but no model-facing thinking Parameter appears because Kent applies that level automatically.
- Node inspection is for identity. Transition configuration is edited by selecting a transition or transition branch.
- A normal transition inspector edits its label, key, model-facing description, applicable prompt and context, Parameters, approval, routing, and validation issues.
- A Fan-Out Transition branch inspector edits branch invocation details and shows its parent transition's source-choice label, key, and approval. The parent owns those source-choice details; each branch owns its target prompt, Parameters, context, and routing.
- A Transition Label is distinct from its Transition Key and model-facing description. The label begins from the key until the operator changes it. The editor names these fields **Label**, **Key**, and **Model-facing description**.
- Normal transitions do not expose their generated branch key. Fan-Out Transition branches expose a **Branch key** derived from the target Node Key; operators may edit it. It must meet Workflow Key requirements and be unique within its parent Fan-Out Transition.
- Parameters have a stable key and model-facing description. They are required when declared and their values are strings. `transition` and `commentary` cannot be Parameter Keys.
- Each enabled Assignee or thinking selector shows its Protected Parameter in the owning Transition's ordinary Parameters list at its saved order.
- The editor applies the canonical Protected Parameter edit, delete, persistence, and hidden-state behavior from the terminology specification.
- A blank protected description appears as an empty editor field while Kent derives its default only for Workflow completion instructions.
- When an applicable thinking model has no enumerable catalog contract, a blank protected thinking description is an execution-validation issue rather than a Draft-save blocker.
- Fan-Out Transition branch Parameters form one Parameter Requirements set. Branches using the same Parameter Key share one produced value only when their descriptions match after ignoring leading and trailing whitespace; the shared description omits that whitespace. Different descriptions for the same key are validation errors.
- Prompt editing offers insertable chips for direct Parameters. Selecting one inserts `.Params.<parameter_key>` at the cursor, or at the end if the prompt is not focused. The chip for `{{.Params.<transition_key>.<parameter>}}` explains previous-transition references and does not insert text.
- Built-in Transition Prompt fields are exactly `.TaskId`, `.TaskShortId`, `.TaskTitle`, `.TaskBody`, `.NodeId`, `.NodeKey`, and `.NodeDisplayName`. `.Params.commentary` renders the source Transition Result commentary, or an empty string when none exists. Other top-level fields are validation errors.
- A prompt can reference a previous Transition Parameter as `.Params.<transition_key>.<parameter_key>`, such as `{{.Params.planning.plan_file_location}}`. The referenced Transition must be guaranteed-prior: every path from Start to the prompt-owning transition branch source passes through it. Within parallel work, lookup stays within the same batch.
- `.Nodes.<node_key>.<field>` is not an authorable prompt reference.
- Transitions into agent Nodes require a prompt for Task start or execution, though a Workflow Draft may save with an empty agent prompt. Transitions into non-agent Nodes cannot have prompts.
- Start transitions may prompt their first agent target but cannot declare Parameters. They can use built-in prompt fields and show no Parameter chips.
- A Join shows its read-only aggregate Parameters and same-key collision errors. Its outgoing transitions cannot declare Parameters. A Join-to-agent prompt can use aggregate Parameters as `.Params.<parameter_key>`. One Fan-Out Transition's matching Parameter Key deduplicates because it has one producing Transition Result; matching keys from different producing transitions collide.
- Validation sections retain their heading and show errors as plain lists.
- Context-Preservation Mode and Context Source apply only to transitions into agent Nodes. Unavailable Context Source choices remain visible, disabled, and show `N/A for current configuration`.
- Assignee and thinking selection is applicable only to serial Transitions from Agent or Script Nodes into Agent Nodes.
- Fan-Out Transition branches, Start and Join sources, and non-Agent targets do not expose protected selection Parameters.
- New Session and Compact and Continue Session entries expose enabled protected Assignee Parameters.
- Continue Session exposes a protected Assignee Parameter only when **Previous session from this target, or new session** will create a new Session.
- Retained-Session continuation hides the protected Assignee Parameter and preserves the retained materialized Assignee.
- Eligible serial Transitions expose enabled protected thinking Parameters in every Context-Preservation Mode.
- **Previous session from this target** is available in continuation modes only when the agent target dominates the transition source: every path from Start to that source passes through the target. It uses the latest retained Session for that target Node in the same Transition Branch Key while parallel work is active, and fails if no such Session exists.
- **Previous session from this target, or new session** is available in continuation modes into agent targets. It uses that same retained Session when available and otherwise starts a new Session.
- Selected-Node Context Source choices list all agent Nodes for agent-target transitions. A choice is available in a continuation mode only when it is not the target and is guaranteed before the transition source. Invalid retained selections remain visible and disabled.
- Script, Join, and terminal targets do not start agent Sessions.

## Topology editing

- Transition targets are assigned on the canvas, not from an inspector list.
- Every editable non-terminal Node, including Start, agent, script, and Join, has one persistent creation handle on its right side. Terminal Nodes and canvases without topology editing do not have one. Transition endpoints do not occupy this handle.
- Dragging a creation handle connects to an existing target. Activating it without dragging opens the same agent/script/terminal picker as the canvas add-Node action. Activating it does not begin a drag; completing or cancelling a drag does not open the picker.
- Choosing a kind from a creation handle adds one ungrouped, unconfigured Node and one default normal transition from the source in one Draft change. The editor never retains a partially created result. The new Node receives derived layout without changing the current pan or zoom.
- After handle creation, the new transition is selected and its inspector opens. Keyboard selection focuses the inspector's first editable field; pointer selection retains ordinary pointer focus. Dismissing the picker returns focus to the source handle.
- The shared Node-kind picker stays visible within the viewport, closes after selection, Escape, or outside interaction, and behaves consistently from every entry point.
- Hovering a transition exposes reconnect handles. An endpoint can be dragged onto a Node body or side; Nodes do not show target connection handles.
- Reconnecting preserves the transition's prompt, Parameters, context, approval, and key. Configuration made invalid by the new topology remains in the Draft and is reported by validation unless the topology itself is impossible.
- Reconnecting also preserves dormant protected Parameter settings, which remain hidden until the new target enables an applicable selector.
- A Fan-Out Transition branch cannot reconnect its source because that would change Fan-Out membership.
- Unsupported graph entities remain inspectable but cannot be edited.
- Topology editing includes adding and deleting agent, script, and terminal Nodes; creating and removing Node Group membership; creating and deleting transitions; connecting Nodes; reconnecting transition endpoints; and editing transition routing and configuration.
- Add Node creates an unconnected agent, script, or terminal Node. Unreachable or incomplete states remain in the Draft and are explained by validation. Start is fixed; Join is created through parallel-group editing.
- Connecting a source Node to a target Node creates a normal transition by default. If the target is an agent branch in a Node Group and the source already has one unambiguous Fan-Out Transition into sibling branches of that group, the connection joins that Fan-Out Transition. Other additions to a Fan-Out Transition are explicit parallel-group or Fan-Out actions.
- Deleting a Node deletes its incident transitions and deletes transitions left with no branches.
- Editable entities can be deleted through keyboard deletion, context actions, or inspector deletion actions.
- Deleting a transition with prompt text requires confirmation. Deleting Parameters alone does not. Deleting a Node or Node Group requires confirmation when it would delete prompt-bearing transitions.
- Join cannot be added as a generic Node. It is created through Node Group and parallel-work editing.
- Dragging a Node changes Node Group membership; it never changes a saved canvas position. A drag ghost follows the pointer while the Node remains in derived layout.
- Node Group drag-and-drop is validated as membership editing. If the editor cannot infer safe source or Fan-Out wiring, it preserves membership and explains the incomplete wiring before Save.

## Save, validation, and conflicts

- Save and Discard share one bottom-right editor status area. It also shows unsaved state, validation issues, Save blockers, remote conflicts, and Save errors.
- Draft validation and execution validation remain separate. Blocking draft-validation errors prevent graph-changing saves. Execution-validation errors remain visible but do not prevent a save limited to Workflow details.
- Draft validation blocks prompts into non-agent targets, duplicate Transition Keys, invalid or duplicate Fan-Out Transition Branch Keys, invalid Parameter Keys or descriptions, invalid previous-Parameter references, and Join aggregate key collisions.
- Execution validation blocks starting or executing an agent-target transition without a prompt.
- Historical Node-owned prompt and contract data may be read for inspection, but the editor never writes or round-trips it. A runnable definition must author Transition Prompts and Parameters.
- A save limited to Workflow details, and a no-op save, bypass graph-edit policy.
- Graph-changing saves show a preview with draft validation, execution validation, destructive or removal impact, and any required confirmation.
- Destructive graph-save confirmation appears in the editor status area, not in a separate blocking surface.
- Save recalculates validation and impact together. It rejects a stale Workflow Version, an unconfirmed destructive change, or a changed destructive impact; otherwise it applies every change together, increments Workflow Version once, and reports the saved definition and validations to linked Projects.
- The editor refuses an incompatible Workflow graph format.
- Workflow Version advances once for any definition edit, whether it changes only Workflow details, only the graph, or both. No-op saves do not advance it.
- If the same Workflow changes remotely while its local Workflow Draft is unsaved, the Draft remains and a conflict banner offers **Reload remote** and **Keep editing**. Saving uses the expected Workflow Version and clearly rejects stale saves.
- The same remote-conflict behavior applies whether the editor was opened from Workflow Library or from a linked Project.

## Workflow Library and Project links

- Project Workflow management uses **Link workflow** language.
- Link workflow opens a global side panel listing reusable Workflows and offering **New workflow**.
- Creating a Workflow from a Project's Link workflow flow creates a reusable Workflow, links it to that Project, and opens the editor. It becomes the Project default only when that Project has no default Workflow; it never replaces an existing default.
- Creating a Workflow from Workflow Library creates an unlinked reusable Workflow until an operator explicitly links it.
- Workflow Library and the editor can open a global Workflow definition. Project-originated boards and Task flows remain Project-scoped.
- The editor may provide settings and deletion for its selected Workflow. Workflow Library and the side panel provide creation, copying, and linking.
- Whole-Workflow deletion uses one in-app confirmation dialog from Workflow Library, Link workflow, and editor settings. Deletion confirmation stays in the app.
- Preview, blocker, and failure details stay visible and retryable while that dialog is open. After deletion succeeds, the dialog closes before surrounding views update or navigate. If the surrounding view cannot complete after deletion, it shows a warning and never repeats deletion.
- Workflow settings include name, description, and linking or unlinking Projects.
- Project selection for settings and linking uses bounded Infinite Scroll inside the side panel and never materializes the complete Project collection.
- The editor's toolbar Add Node control opens the Node-kind picker on hover or focus. Clicking the control itself neither creates a Node nor toggles the picker.

## Side panel

- Workflow intermediary, picker, settings, and entity-edit flows use a reusable global right-side side panel.
- The side panel is an in-app, fixed right overlay below the title bar, with a glass treatment, left rounded corners, and an adaptive width of about 420–560 pixels within viewport margins.
- It supports local destinations and navigation, returning a result or cancellation to the flow that opened it. Opening another main destination or navigating back or forward closes it and cancels any unresolved flow that opened it.
- Side-panel destinations remain sufficiently complete to avoid stacked blocking surfaces. Child pickers can return a result to the preceding side-panel destination.

## Editing constraints

- A linked Workflow can be edited while its Project has Tasks.
- A graph-changing save is blocked when it would remove a Node or Transition Branch required by a Task's Current Nodes, live execution, unresolved parallel work, or pending approval.
- A Task is active for these rules when it has a non-terminal Current Node, pending approval, unresolved parallel branch, Exact Execution Scope, or runtime gate.
- Start deletion is unavailable. Start is hidden from add and kind-change controls; an existing Start may be renamed where safe, but its kind remains fixed. A blocked graph delete reports feedback.
- A Start Node's outgoing transitions may be edited in a Draft, but execution validation requires exactly one Start transition with one branch to an executable Node.
- Start cannot be the source of a Fan-Out Transition into a Node Group. Parallel work begins from a later agent Node and rejoins through the group's Join.
- A terminal Node may be deleted only when at least one other terminal Node remains; otherwise deletion is blocked with feedback.
- A saved Node Group must have enough branch Nodes and exactly one owned Join to remain execution-shaped; otherwise Save is blocked.
- Destructive impact is evaluated at Save, not while making a Draft edit.
- Save blocks a graph change that removes a Node, Transition, or graph connection required by a Task's Current Nodes, pending approval, live Exact Execution Scope, or unresolved parallel branch.
- Changing the kind of a current Node is blocked. A Node without current Task references has no completed-work restriction on its kind.
- Transition routing, Parameters, and display details may change while Tasks exist. Pending Approvals and unresolved parallel work keep their captured data. A live Exact Execution Scope keeps its model-visible completion requirements; if an incompatible edit makes its completion invalid, completion fails without Task mutation. Start and Resume use the latest valid requirements.
- Moving a graph connection to a different Transition is blocked only while current Task state depends on it.
- Backlog and terminal Tasks do not require confirmation before otherwise unreferenced Nodes or transitions are removed.
- Manual Task moves are blocked when they would violate a selected prior-Node continuation Context Source. Previous-target continuation uses the context resolved for that transition.
- Saving a Workflow graph never deletes or moves Tasks. Whole-Workflow deletion is the Task-deleting operation.
