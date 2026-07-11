import { Handle, Position, type NodeProps } from "@xyflow/react";
import { Plus } from "lucide-react";
import { memo, type CSSProperties, type MouseEvent, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuTrigger,
  IslandSurface,
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "../../ui";
import { cx } from "../../ui/classes";
import { WorkflowNodeInfoTooltipContent, type CopyText } from "./WorkflowGraphNodeMetadata";
import type { CreatableWorkflowNodeKind } from "./workflowEditorGraphMutationTypes";
import type { WorkflowGraphSelection } from "./workflowGraphSelection";
import { WorkflowNodeKindPicker, type WorkflowNodeKindSelectionModality } from "./WorkflowNodeKindPicker";
import {
  workflowGraphCreationHandleID,
  workflowGraphTargetConnectionHandleID,
  type WorkflowGraphEndpointPort,
} from "./workflowGraphLayoutGeometry";
import type {
  WorkflowGraphGroupNode,
  WorkflowGraphNodeData,
  WorkflowGraphWorkflowNode,
} from "./workflowGraphLayout";

export type { CopyText } from "./WorkflowGraphNodeMetadata";

type WorkflowNodeContextMenuCallbacks = Readonly<{
  onCreateNodeGroup: ((nodeID: string) => void) | undefined;
  onDeleteSelection: ((selection: WorkflowGraphSelection) => void) | undefined;
  onInspectNode: (nodeID: string) => void;
  onRemoveNodeFromGroup: ((nodeID: string) => void) | undefined;
  onSelectContextMenu: (nodeID: string) => void;
}>;

type WorkflowConnectedNodeCallbacks = Readonly<{
  onAddConnectedNode:
    | ((sourceNodeID: string, kind: CreatableWorkflowNodeKind, modality: WorkflowNodeKindSelectionModality) => void)
    | undefined;
  onCreationHandleActivate: (nodeID: string) => boolean;
}>;

function WorkflowNodeContextMenuShell({
  children,
  data,
  onCreateNodeGroup,
  onDeleteSelection,
  onRemoveNodeFromGroup,
  onSelectContextMenu,
  tooltip,
}: Readonly<
  {
    children: ReactNode;
    data: WorkflowGraphNodeData;
    tooltip?: ReactNode | undefined;
  } & WorkflowNodeContextMenuCallbacks
>) {
  const { t } = useTranslation();
  const trigger = (
    <ContextMenuTrigger
      asChild
      onContextMenu={() => {
        onSelectContextMenu(data.entityID);
      }}
    >
      {tooltip === undefined ? children : <TooltipTrigger asChild>{children}</TooltipTrigger>}
    </ContextMenuTrigger>
  );
  return (
    <ContextMenu>
      {tooltip === undefined ? (
        trigger
      ) : (
        <Tooltip>
          {trigger}
          <TooltipContent
            className={NODE_METADATA_TOOLTIP_CLASS}
            data-testid="workflow-node-metadata-tooltip"
            level={3}
            onClick={stopPropagation}
          >
            {tooltip}
          </TooltipContent>
        </Tooltip>
      )}
      <ContextMenuContent level={3}>
        {workflowBranchNodeKind(data.kind) && data.groupID.length === 0 ? (
          <ContextMenuItem
            onSelect={() => {
              onCreateNodeGroup?.(data.entityID);
            }}
          >
            {t("workflowEditor.createNodeGroup")}
          </ContextMenuItem>
        ) : null}
        {workflowBranchNodeKind(data.kind) && data.groupID.length > 0 ? (
          <ContextMenuItem
            onSelect={() => {
              onRemoveNodeFromGroup?.(data.entityID);
            }}
          >
            {t("workflowEditor.ungroupNode")}
          </ContextMenuItem>
        ) : null}
        <ContextMenuItem
          onSelect={() => {
            onDeleteSelection?.({ kind: "node", nodeID: data.entityID });
          }}
        >
          {t("workflowEditor.deleteNode")}
        </ContextMenuItem>
      </ContextMenuContent>
    </ContextMenu>
  );
}

const NODE_METADATA_TOOLTIP_CLASS =
  "pointer-events-auto grid w-[420px] max-w-[calc(100vw-var(--space-4)*2)] items-stretch gap-1.5 p-1.5";

export const WorkflowNode = memo(function WorkflowNode({
  data,
  dragging,
  onCopyText,
  onCreateNodeGroup,
  onDeleteSelection,
  onInspectNode,
  onRemoveNodeFromGroup,
  onSelectContextMenu,
  onAddConnectedNode,
  onCreationHandleActivate,
  selected,
}: NodeProps<WorkflowGraphWorkflowNode> &
  Readonly<
    {
      onCopyText: CopyText;
    } & WorkflowNodeContextMenuCallbacks & WorkflowConnectedNodeCallbacks
  >) {
  const { t } = useTranslation();
  const nodeCard = (
    <IslandSurface
      as="div"
      className={cx(
        "workflow-editor-node nopan relative grid h-full min-w-0 grid-rows-[minmax(0,1fr)_auto] rounded-[var(--radius-l)] p-[var(--space-3)]",
        workflowBranchNodeKind(data.kind) ? "cursor-grab" : undefined,
        dragging ? "cursor-grabbing" : undefined,
        data.hasError ? "workflow-editor-node-error" : undefined,
        selected ? "workflow-editor-node-selected" : undefined,
      )}
      data-kind={data.kind}
      data-testid={`workflow-graph-node-${data.entityID}`}
      level={1}
      style={workflowNodeOutlineStyle(data.kind, data.hasError)}
      title={workflowBranchNodeKind(data.kind) ? t("workflowEditor.dragNodeToGroup") : undefined}
    >
      <WorkflowTargetConnectionHandle data={data} />
      <WorkflowEndpointHandles endpointPorts={data.endpointPorts ?? []} />
      <WorkflowCreationHandle
        data={data}
        onAddConnectedNode={onAddConnectedNode}
        onCreationHandleActivate={onCreationHandleActivate}
      />
      <strong className="line-clamp-2 min-w-0 text-[0.95rem] leading-snug text-[var(--color-on-island)]">
        {data.label}
      </strong>
      <span className="min-w-0 truncate font-mono text-sm text-[var(--color-muted)]">{data.role}</span>
    </IslandSurface>
  );
  const tooltip = data.kind === "join" ? (
    <WorkflowNodeInfoTooltipContent
      nodeID={data.entityID}
      nodeKey={data.key}
      onCopyText={onCopyText}
    />
  ) : undefined;
  return (
    <WorkflowNodeContextMenuShell
      data={data}
      onCreateNodeGroup={onCreateNodeGroup}
      onDeleteSelection={onDeleteSelection}
      onInspectNode={onInspectNode}
      onRemoveNodeFromGroup={onRemoveNodeFromGroup}
      onSelectContextMenu={onSelectContextMenu}
      tooltip={tooltip}
    >
      {nodeCard}
    </WorkflowNodeContextMenuShell>
  );
});

export const WorkflowGroupNode = memo(function WorkflowGroupNode({
  activeDropTarget,
  data,
}: NodeProps<WorkflowGraphGroupNode> &
  Readonly<{ activeDropTarget: boolean }>) {
  const { t } = useTranslation();
  return (
    <IslandSurface
      as="div"
      className={cx(
        "workflow-editor-group nopan h-full rounded-[var(--radius-xl)] p-[var(--space-3)]",
        activeDropTarget ? "workflow-editor-group-drop-active" : undefined,
        data.hasError ? "workflow-editor-node-error" : undefined,
      )}
      data-drop-state={activeDropTarget ? "active" : "idle"}
      data-testid={`workflow-graph-group-${data.entityID}`}
      data-workflow-group-id={data.entityID}
      level={1}
      style={workflowNodeOutlineStyle(data.kind, data.hasError)}
    >
      <div className="font-mono text-sm font-bold text-[var(--color-muted)]">
        {data.label}
      </div>
      {"empty" in data && data.empty ? (
        <div className="grid h-[calc(100%-24px)] place-items-center text-sm text-[var(--color-muted)]">
          {t("workflowEditor.emptyGroup")}
        </div>
      ) : null}
    </IslandSurface>
  );
});

export const WorkflowJoinNode = memo(function WorkflowJoinNode({
  data,
  onCopyText,
  onDeleteSelection,
  onInspectNode,
  onSelectContextMenu,
  onAddConnectedNode,
  onCreationHandleActivate,
  selected,
}: NodeProps<WorkflowGraphWorkflowNode> &
  Readonly<
    {
      onCopyText: CopyText;
    } & Pick<WorkflowNodeContextMenuCallbacks, "onDeleteSelection" | "onInspectNode" | "onSelectContextMenu"> &
      WorkflowConnectedNodeCallbacks
  >) {
  const nodeCard = (
    <div
      className={cx(
        "workflow-editor-join-node nopan relative grid h-full w-full place-items-center",
        data.hasError ? "workflow-editor-node-error" : undefined,
      )}
      style={workflowNodeOutlineStyle(data.kind, data.hasError)}
      title={data.label}
    >
      <WorkflowTargetConnectionHandle data={data} />
      <WorkflowEndpointHandles endpointPorts={data.endpointPorts ?? []} />
      <WorkflowCreationHandle
        data={data}
        onAddConnectedNode={onAddConnectedNode}
        onCreationHandleActivate={onCreationHandleActivate}
      />
      <IslandSurface
        as="div"
        className={cx(
          "workflow-editor-join-diamond relative",
          selected ? "workflow-editor-node-selected" : undefined,
        )}
        data-kind={data.kind}
        data-testid={`workflow-graph-node-${data.entityID}`}
        level={1}
        style={workflowNodeOutlineStyle(data.kind, data.hasError)}
      >
        <span aria-hidden="true" className="absolute inset-0" data-testid="workflow-join-diamond" />
        <span className="sr-only">{data.label}</span>
      </IslandSurface>
    </div>
  );
  const tooltip = (
    <WorkflowNodeInfoTooltipContent
      nodeID={data.entityID}
      nodeKey={data.key}
      onCopyText={onCopyText}
    />
  );
  return (
    <WorkflowNodeContextMenuShell
      data={data}
      onCreateNodeGroup={undefined}
      onDeleteSelection={onDeleteSelection}
      onInspectNode={onInspectNode}
      onRemoveNodeFromGroup={undefined}
      onSelectContextMenu={onSelectContextMenu}
      tooltip={tooltip}
    >
      {nodeCard}
    </WorkflowNodeContextMenuShell>
  );
});

function WorkflowCreationHandle({
  data,
  onAddConnectedNode,
  onCreationHandleActivate,
}: Readonly<{
  data: WorkflowGraphNodeData;
  onAddConnectedNode: WorkflowConnectedNodeCallbacks["onAddConnectedNode"];
  onCreationHandleActivate: WorkflowConnectedNodeCallbacks["onCreationHandleActivate"];
}>) {
  const { t } = useTranslation();
  if (data.kind === "terminal" || onAddConnectedNode === undefined) {
    return null;
  }
  return (
    <>
      <WorkflowNodeKindPicker
        onTriggerActivate={() => onCreationHandleActivate(data.entityID)}
        onSelect={(kind, modality) => {
          onAddConnectedNode(data.entityID, kind, modality);
        }}
        trigger={
          <Handle
            aria-label={t("workflowEditor.createOutgoingTransition")}
            className="workflow-editor-handle workflow-editor-creation-handle"
            data-testid="workflow-node-source-handle"
            id={data.creationHandleID ?? workflowGraphCreationHandleID(data.entityID)}
            position={Position.Right}
            role="button"
            tabIndex={0}
            type="source"
          />
        }
        triggerPolicy="activation"
      />
      <Plus
        aria-hidden="true"
        className="workflow-editor-creation-handle-icon"
        data-testid="workflow-node-source-handle-icon"
        data-workflow-node-id={data.entityID}
        size={14}
        strokeWidth={2}
      />
    </>
  );
}

function WorkflowTargetConnectionHandle({ data }: Readonly<{ data: WorkflowGraphNodeData }>) {
  if (data.kind === "start") {
    return null;
  }
  return (
    <Handle
      aria-hidden="true"
      className="workflow-editor-target-connection-handle"
      data-testid="workflow-node-connection-target-handle"
      id={workflowGraphTargetConnectionHandleID(data.entityID)}
      position={Position.Left}
      type="target"
    />
  );
}

function WorkflowEndpointHandles({
  endpointPorts,
}: Readonly<{ endpointPorts: readonly WorkflowGraphEndpointPort[] }>) {
  return (
    <>
      {endpointPorts.map((port) => (
        <Handle
          aria-hidden="true"
          className="workflow-editor-endpoint-handle"
          data-testid="workflow-node-endpoint-handle"
          id={port.id}
          key={port.id}
          position={port.side === "source" ? Position.Right : Position.Left}
          style={workflowEndpointHandleStyle(port)}
          type={port.side}
        />
      ))}
    </>
  );
}

function workflowEndpointHandleStyle(port: WorkflowGraphEndpointPort): CSSProperties {
  return { top: port.y };
}

function workflowBranchNodeKind(kind: string): boolean {
  return kind === "agent" || kind === "script";
}

type WorkflowNodeOutlineStyle = CSSProperties & Readonly<Record<"--workflow-editor-node-outline-color", string>>;

function workflowNodeOutlineStyle(kind: string, hasError: boolean): WorkflowNodeOutlineStyle {
  if (hasError) {
    return { "--workflow-editor-node-outline-color": "var(--color-error)" };
  }
  if (kind === "start") {
    return { "--workflow-editor-node-outline-color": "var(--color-primary)" };
  }
  if (kind === "terminal") {
    return { "--workflow-editor-node-outline-color": "var(--color-success)" };
  }
  if (kind === "join") {
    return { "--workflow-editor-node-outline-color": "var(--color-secondary)" };
  }
  return { "--workflow-editor-node-outline-color": "var(--color-outline)" };
}

function stopPropagation(event: MouseEvent): void {
  event.stopPropagation();
}
