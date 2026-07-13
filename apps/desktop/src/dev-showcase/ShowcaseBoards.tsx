import { Maximize2, Minimize2, Workflow } from "lucide-react";
import type { ReactNode } from "react";

import type { WorkspaceSummary } from "../api";
import { BoardHoverMenu } from "../features/board/BoardHoverMenu";
import { KanbanColumn, KanbanGroup } from "../features/board/BoardColumns";
import { toKanbanCardVM, toKanbanColumnVM, toKanbanGroupVM } from "../features/board/BoardColumnViewModel";
import { WorkspaceRow, WorkspaceUnlinkFallbackDialog } from "../features/project-edit/ProjectEditParts";
import {
  Badge,
  Button,
  Dialog,
  EmptyState,
  ErrorState,
  HelpHint,
  LoadingState,
  MarkdownText,
  SelectField,
  Spinner,
  TextArea,
  TextInput,
  VirtualizedInfiniteList,
} from "../ui";
import {
  mockAttentionRows,
  mockBoard,
  mockBoardNodeCards,
  mockProjectRows,
  mockWorkspaces,
} from "./mockData";

export function PrimitiveBoard({
  dialogOpen,
  onDialogOpenChange,
}: Readonly<{ dialogOpen: boolean; onDialogOpenChange: (open: boolean) => void }>) {
  return (
    <div className="grid gap-[var(--space-3)] lg:grid-cols-[minmax(0,1.2fr)_minmax(320px,0.8fr)]">
      <Panel title="Buttons, Badges, Fields">
        <div className="flex flex-wrap gap-[var(--space-2)]">
          <Button variant="primary">Primary</Button>
          <Button variant="secondary">Secondary</Button>
          <Button variant="ghost">Ghost</Button>
          <Button variant="danger">Danger</Button>
          <Button disabled>Disabled</Button>
        </div>
        <div className="flex flex-wrap gap-[var(--space-2)]">
          <Badge>Neutral</Badge>
          <Badge tone="info">Info</Badge>
          <Badge tone="success">Success</Badge>
          <Badge tone="warning">Warning</Badge>
          <Badge tone="danger">Danger</Badge>
        </div>
        <div
          className="flex flex-wrap items-center gap-[var(--space-3)] rounded-[var(--radius-l)] border border-[var(--color-outline)] bg-[var(--color-island-1)] p-[var(--space-3)]"
          data-testid="dev-showcase-help-hint-row"
        >
          <span className="text-sm text-[var(--color-muted)]">Help hint</span>
          <span className="inline-flex items-center gap-[var(--space-1)] text-sm text-[var(--color-on-island)]">
            Worktree
            <HelpHint label={"Isolated git checkout for this task.\nChanges stay sandboxed until merged."} />
          </span>
        </div>
        <div
          className="flex flex-wrap items-center gap-[var(--space-3)] rounded-[var(--radius-l)] border border-[var(--color-outline)] bg-[var(--color-island-1)] p-[var(--space-3)]"
          data-testid="dev-showcase-spinner-row"
        >
          <span className="text-sm text-[var(--color-muted)]">Spinners</span>
          <Spinner size="sm" testID="dev-showcase-spinner-sm" />
          <Spinner testID="dev-showcase-spinner-md" />
          <Spinner className="h-[18px] w-[18px]" strokeWidth={1.5} testID="dev-showcase-spinner-task-card" />
        </div>
        <div className="grid gap-[var(--space-3)] md:grid-cols-2">
          <TextInput
            error={["Required", "Use one line."]}
            label="Project name"
            value="Kent Desktop"
            readOnly
          />
          <SelectField
            disabled
            label="Source workspace"
            onValueChange={() => undefined}
            options={[
              { label: "kent", value: "workspace-api" },
              { label: "docs", value: "workspace-docs" },
            ]}
            value="workspace-api"
          />
          <TextArea
            hint="Markdown accepted."
            label="Task body"
            readOnly
            rows={4}
            value={"Review board spacing and hover menu behavior.\n\n- Compact\n- Expanded"}
          />
        </div>
      </Panel>
      <Panel title="States, Markdown, Dialog">
        <LoadingState body="Checking readiness and authentication state." fullPage={false} title="Loading" />
        <EmptyState
          actions={<Button variant="primary">Create task</Button>}
          body="Create or attach a workspace to start tracking workflow tasks."
          fullPage={false}
          title="Empty"
        />
        <ErrorState
          body="Workflow graph is invalid for task creation."
          fullPage={false}
          retryLabel="Try again"
          title="Error"
        />
        <article className="rounded-[var(--radius-l)] border border-[var(--color-outline)] bg-[var(--color-island-1)] p-[var(--space-3)]">
          <MarkdownText
            value={
              "**Markdown** body with `inline code`, [safe link](https://example.com), and\n\n> quote preview"
            }
          />
        </article>
        <Button
          onClick={() => {
            onDialogOpenChange(true);
          }}
          variant="primary"
        >
          Open dialog state
        </Button>
        <Dialog
          closeLabel="Close"
          onClose={() => {
            onDialogOpenChange(false);
          }}
          open={dialogOpen}
          title="Create Backlog task"
        >
          <div className="grid gap-[var(--space-3)]">
            <TextInput label="Title" value="Document UI showcase" readOnly />
            <TextArea
              label="Details"
              rows={4}
              value="This dialog is intentionally open on first load."
              readOnly
            />
            <Button variant="primary">Create task</Button>
          </div>
        </Dialog>
      </Panel>
    </div>
  );
}

export function HomeProjectBoard({
  unlinkWorkspace,
  onUnlinkWorkspaceChange,
}: Readonly<{
  unlinkWorkspace: WorkspaceSummary | null;
  onUnlinkWorkspaceChange: (workspace: WorkspaceSummary | null) => void;
}>) {
  return (
    <div className="grid gap-[var(--space-3)] xl:grid-cols-[minmax(340px,0.9fr)_minmax(0,1.1fr)]">
      <Panel title="Home Lists">
        <div className="grid gap-[var(--space-3)] md:grid-cols-2">
          <MockVirtualList title="Projects" items={mockProjectRows} renderItem={ProjectPreviewRow} />
          <MockVirtualList title="Inbox" items={mockAttentionRows} renderItem={AttentionPreviewRow} />
        </div>
      </Panel>
      <Panel title="Project Edit">
        {mockWorkspaces.map((workspace) => (
          <WorkspaceRow
            defaultWorkspaceID="workspace-api"
            disabled={false}
            key={workspace.id}
            onMakeDefault={() => {
              return;
            }}
            onUnlink={() => {
              onUnlinkWorkspaceChange(workspace);
            }}
            workspace={workspace}
          />
        ))}
        {unlinkWorkspace !== null ? (
          <WorkspaceUnlinkFallbackDialog
            disabled={false}
            onClose={() => {
              onUnlinkWorkspaceChange(null);
            }}
            onConfirm={() => {
              onUnlinkWorkspaceChange(null);
            }}
            target={{
              projectID: "project-api",
              rootPath: unlinkWorkspace.rootPath,
              workspaceID: unlinkWorkspace.id,
            }}
          />
        ) : null}
      </Panel>
    </div>
  );
}

export function KanbanBoard() {
  return (
    <div className="island-glass h-[760px] overflow-auto rounded-[var(--radius-xl)] p-[var(--space-3)] hide-scrollbar">
      <div className="grid h-full w-max min-w-full grid-flow-col gap-[var(--space-4)]">
        {mockBoard.groups.map((group) => {
          const columns = mockBoard.columns.filter((column) => column.groupID === group.id);
          return (
            <KanbanGroup group={toKanbanGroupVM(group)} key={group.id}>
              {columns.map((column) => (
                <KanbanColumn
                  actionsDisabled={false}
                  cards={(mockBoardNodeCards[column.id] ?? []).map((card) =>
                    toKanbanCardVM(card, {
                      attachedWorkspaceCount: mockBoard.attachedWorkspaceCount,
                      defaultWorkspaceID: mockBoard.defaultWorkspaceID,
                    }),
                  )}
                  column={toKanbanColumnVM(column)}
                  dropState="idle"
                  hasMoreCards={group.id === "group-delivery"}
                  isFirstActive={column.id === "node-design"}
                  isLoadingMoreCards={group.id === "group-delivery"}
                  key={column.id}
                  onCardClick={() => undefined}
                  onCardDragEnd={() => undefined}
                  onCardDragStart={() => undefined}
                  onDeleteTask={() => undefined}
                  onDropTask={() => undefined}
                  onInterruptTask={() => undefined}
                  onLoadMoreCards={() => undefined}
                  onResumeTask={() => undefined}
                />
              ))}
            </KanbanGroup>
          );
        })}
      </div>
    </div>
  );
}

export function HoverMenuBoard() {
  return (
    <div className="relative min-h-[560px] overflow-hidden rounded-[var(--radius-xl)] border border-dashed border-[var(--color-outline)] bg-[var(--color-island-1)] p-[var(--space-4)]">
      <div className="grid max-w-[540px] gap-[var(--space-3)]">
        <Badge tone="info">Hover bottom-left menu</Badge>
        <p className="m-0 text-[var(--color-muted)]">
          This canvas leaves space for reviewing collapsed, hovered, focused, and workflow-picker menu states.
        </p>
        <div className="grid gap-[var(--space-2)] sm:grid-cols-2">
          <StateChip icon={<Minimize2 aria-hidden="true" size={16} />}>Collapsed rail</StateChip>
          <StateChip icon={<Maximize2 aria-hidden="true" size={16} />}>Expanded workflow picker</StateChip>
          <StateChip icon={<Workflow aria-hidden="true" size={16} />}>New task disabled/enabled</StateChip>
        </div>
      </div>
      <BoardHoverMenu
        board={mockBoard}
        canCreateTask
        onNewTask={() => undefined}
        onWorkflowEdit={() => undefined}
        onWorkflowLink={() => undefined}
        onWorkflowSelect={() => undefined}
      />
    </div>
  );
}

function Panel({ children, title }: Readonly<{ children: ReactNode; title: string }>) {
  return (
    <section className="island-glass grid gap-[var(--space-3)] rounded-[var(--radius-xl)] p-[var(--space-4)]">
      <h3 className="m-0">{title}</h3>
      {children}
    </section>
  );
}

function MockVirtualList<TItem>({
  items,
  renderItem,
  title,
}: Readonly<{ items: readonly TItem[]; renderItem: (item: TItem) => ReactNode; title: string }>) {
  return (
    <VirtualizedInfiniteList
      className="h-[420px] min-h-0 overflow-auto rounded-[var(--radius-l)] border border-[var(--color-outline)] bg-[var(--color-island-2)] px-[var(--space-3)] hide-scrollbar contain-strict"
      estimateSize={() => 128}
      getItemKey={(item) => JSON.stringify(item)}
      hasNextPage
      header={<h4 className="m-0 pb-[var(--space-2)]">{title}</h4>}
      isFetchingNextPage
      items={items}
      loadingLabel="Loading more"
      onLoadMore={() => undefined}
      paddingEnd={12}
      paddingStart={12}
      renderItem={renderItem}
    />
  );
}

function ProjectPreviewRow(item: (typeof mockProjectRows)[number]) {
  return (
    <article className="relative rounded-[var(--radius-l)] border border-[var(--color-outline)] bg-[var(--color-island-1)] p-[var(--space-3)]">
      <span className="font-mono text-[0.78rem] text-[var(--color-muted)]">{item.key}</span>
      <strong className="block">{item.name}</strong>
      <span className="block truncate font-mono text-sm text-[var(--color-muted)]">{item.path}</span>
      <div className="mt-[var(--space-2)] flex flex-wrap gap-[var(--space-2)]">
        <Badge tone={item.valid ? "success" : "warning"}>{item.workflow}</Badge>
        <Badge>{item.tasks} tasks</Badge>
        {item.attention > 0 ? <Badge tone="warning">{item.attention} inbox</Badge> : null}
      </div>
    </article>
  );
}

function AttentionPreviewRow(item: (typeof mockAttentionRows)[number]) {
  return (
    <button
      className="grid w-full gap-[var(--space-2)] rounded-[var(--radius-l)] border border-[var(--color-outline)] bg-[var(--color-island-1)] p-[var(--space-3)] text-left text-[var(--color-on-island)]"
      type="button"
    >
      <div className="flex flex-wrap gap-[var(--space-2)]">
        <Badge tone="warning">{item.kind}</Badge>
        <span className="font-mono text-sm text-[var(--color-muted)]">{item.shortId}</span>
      </div>
      <strong>{item.title}</strong>
      <span className="text-sm">{item.message}</span>
    </button>
  );
}

function StateChip({ children, icon }: Readonly<{ children: ReactNode; icon: ReactNode }>) {
  return (
    <span className="inline-flex items-center gap-[var(--space-2)] rounded-[var(--radius-m)] border border-[var(--color-outline)] bg-[var(--color-island-2)] px-[var(--space-3)] py-[var(--space-2)] text-sm">
      {icon}
      {children}
    </span>
  );
}
