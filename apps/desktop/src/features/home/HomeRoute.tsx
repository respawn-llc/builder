import {
  memo,
  type ReactNode,
  useCallback,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { useInfiniteQuery, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Folder, Plus, Workflow } from "lucide-react";
import { useTranslation } from "react-i18next";

import type {
  AttentionItem,
  ProjectSummary,
  SessionCatalogSummary,
} from "@/api";
import { errorMessage } from "@/api";
import {
  basename,
  formatRelativeTime,
  mainSessionCatalogInfiniteQueryOptions,
  projectKeyFromName,
  subagentSessionCatalogInfiniteQueryOptions,
} from "@/app-facade";
import { useAppNavigation } from "@/app-facade";
import { queryKeys } from "@/app-facade";
import { SidebarRootOwner, useOwnedSidebarRoots, type SidebarRootController } from "@/app-facade";
import { taskDetailInitialFocusFromAttentionItem } from "@/app-facade";
import { useAppServices } from "@/app-facade";
import { useNativeDialogFallback } from "@/app-facade";
import { useStatusController } from "@/app-facade";
import { useConnectionSnapshot } from "@/app-facade";
import { desktopChatEnabled } from "@/shared/feature-flags";
import { WorkflowCard, useWorkflowPages } from "@/shared/workflow-library";
import {
  EmptyState,
  ErrorState,
  homeListCardListMaxWidthClassName,
  HomeListCard,
  IslandTabs,
  islandSurfaceClassName,
  LoadingState,
  VirtualizedInfiniteList,
} from "@/ui";
import { cx } from "@/ui";
import { HomePrimaryPane, type HomePrimaryTab } from "./HomePrimaryPane";
import { ProjectCreateDialog, type ProjectDraft } from "./ProjectCreateForm";
import { ProjectRow } from "./ProjectRow";
import {
  useGlobalAttentionPages,
  useGlobalAttentionEvents,
  useProjectCreation,
  useProjectCreationEvents,
  useProjectPages,
} from "./useHomeData";

const LOCAL_UNBOUND_PLAN_KIND = "local_unbound";
export function HomeRoute() {
  return <SidebarRootOwner><HomeRouteContent /></SidebarRootOwner>;
}

function HomeRouteContent() {
  const { t } = useTranslation();
  const { api, nativeBridge } = useAppServices();
  const { push } = useStatusController();
  const connection = useConnectionSnapshot();
  const navigation = useAppNavigation();
  const { open } = useOwnedSidebarRoots();
  const queryClient = useQueryClient();
  const creation = useProjectCreation();
  const projects = useProjectPages();
  const attentionSubscriptionReady = useGlobalAttentionEvents();
  const attention = useGlobalAttentionPages(attentionSubscriptionReady);
  const [primaryTab, setPrimaryTab] = useState<HomePrimaryTab>("projects");
  const [selectedProjectID, setSelectedProjectID] = useState<string | null>(null);
  const [workflowsSelected, setWorkflowsSelected] = useState(false);
  const projectItems = projects.data?.pages.flatMap((page) => page.projects) ?? [];
  const attentionItems = attention.data?.pages.flatMap((page) => page.items) ?? [];
  const disabled = connection.phase !== "connected";
  const projectCreationDialog = useNativeDialogFallback<ProjectDraft>({
    errorNoticeID: "project-create-window-error",
    errorTitle: t("home.projectCreateWindowError"),
    nativeAvailable: nativeBridge.capabilities.projectCreationWindow,
    openNative: async (nextDraft) => {
      await nativeBridge.projectCreation.openWindow(nextDraft);
    },
    renderFallback: (nextDraft, close) => (
      <ProjectCreateDialog
        creationError={creation.error}
        draft={nextDraft}
        isCreating={creation.isPending}
        onClose={close}
        onSubmitDraft={(values) => void submitDraft(values, close)}
      />
    ),
  });

  async function chooseWorkspace(): Promise<void> {
    try {
      const selected = await nativeBridge.directories.selectDirectory({ title: t("home.chooseWorkspace") });
      if (selected === null) {
        return;
      }
      await openProjectCreationDestination(selected.path);
    } catch (error) {
      push({
        id: "project-create-picker-error",
        tone: "danger",
        title: t("home.workspacePickerError"),
        body: errorMessage(error),
      });
    }
  }

  async function openProjectCreationDestination(workspacePath: string): Promise<void> {
    try {
      const plan = await api.planWorkspace(workspacePath);
      if (plan.binding !== null) {
        void navigation.openProject(plan.binding.projectID);
        return;
      }
      if (plan.kind !== LOCAL_UNBOUND_PLAN_KIND) {
        push({
          id: "project-create-selection-required",
          tone: "info",
          title: t("home.workspaceSelectionRequired"),
          body: t("home.workspaceSelectionRequiredBody"),
        });
        return;
      }
      const name = basename(plan.canonicalRoot);
      const nextDraft = { name, key: projectKeyFromName(name), workspaceRoot: plan.canonicalRoot };
      await projectCreationDialog.open(nextDraft);
    } catch (error) {
      push({
        id: "project-create-plan-error",
        tone: "danger",
        title: t("home.workspacePlanError"),
        body: errorMessage(error),
      });
    }
  }

  async function submitDraft(values: ProjectDraft, close: () => void): Promise<void> {
    try {
      const plan = await api.planWorkspace(values.workspaceRoot);
      if (plan.binding !== null) {
        close();
        void navigation.openProject(plan.binding.projectID);
        return;
      }
      if (plan.kind !== LOCAL_UNBOUND_PLAN_KIND) {
        close();
        push({
          id: "project-create-selection-required",
          tone: "info",
          title: t("home.workspaceSelectionRequired"),
          body: t("home.workspaceSelectionRequiredBody"),
        });
        return;
      }
      const binding = await creation.mutateAsync({
        name: values.name.trim(),
        key: values.key.trim().toUpperCase(),
        workspaceRoot: values.workspaceRoot,
      });
      close();
      void navigation.openProject(binding.projectID);
    } catch (error) {
      push({
        id: "project-create-submit-error",
        tone: "danger",
        title: t("home.workspacePlanError"),
        body: errorMessage(error),
      });
    }
  }

  const handleNativeProjectCreated = useCallback(
    (binding: Readonly<{ projectID: string }>) => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.projects });
      void navigation.openProject(binding.projectID);
    },
    [navigation, queryClient],
  );

  useProjectCreationEvents(handleNativeProjectCreated);

  if (desktopChatEnabled) {
    const selectedProject =
      selectedProjectID === null
        ? null
        : (projectItems.find((project) => project.id === selectedProjectID) ?? null);
    const detailKey =
      selectedProject !== null ? `project:${selectedProject.id}` : workflowsSelected ? "workflows" : "inbox";
    return (
      <div className="h-full min-h-0" data-testid="home-route-root">
        {projectCreationDialog.fallback}
        <div
          className="grid h-full min-h-0 grid-cols-[350px_minmax(0,1fr)]"
          data-testid="home-pane-grid"
        >
          <HomePrototypeSidebar
            disabled={disabled}
            onChooseWorkspace={() => void chooseWorkspace()}
            onCreateWorkflow={() => {
              open({ kind: "workflowCreate", mode: "overlay" });
            }}
            onProjectSelect={(projectID) => {
              setWorkflowsSelected(false);
              setSelectedProjectID((current) => (current === projectID ? null : projectID));
            }}
            onWorkflowsSelect={() => {
              setSelectedProjectID(null);
              setWorkflowsSelected((current) => !current);
            }}
            projectItems={projectItems}
            projectsQuery={projects}
            selectedProjectID={selectedProjectID}
            workflowsSelected={workflowsSelected}
          />
          <section className="island-glass my-[var(--space-2)] mr-[var(--space-2)] min-h-0 overflow-hidden rounded-[var(--radius-xl)]">
            <DetailPaneCrossfade contentKey={detailKey}>
              {selectedProject !== null ? (
                <ProjectPrototypeDetail
                  key={selectedProject.id}
                  disabled={disabled}
                  onLinkWorkflow={() => {
                    open({ kind: "linkWorkflow", mode: "overlay", projectID: selectedProject.id });
                  }}
                  project={selectedProject}
                />
              ) : workflowsSelected ? (
                <GlobalWorkflowsPrototype />
              ) : (
                <AttentionList items={attentionItems} query={attention} />
              )}
            </DetailPaneCrossfade>
          </section>
        </div>
      </div>
    );
  }

  return (
    <div className="h-full min-h-0" data-testid="home-route-root">
      {projectCreationDialog.fallback}
      <div
        className="grid h-full min-h-0 grid-cols-[repeat(auto-fit,minmax(min(100%,360px),1fr))] gap-[var(--space-3)]"
        data-testid="home-pane-grid"
      >
        <section
          aria-labelledby="home-primary-pane-title"
          className="island-glass min-h-0 overflow-hidden rounded-[var(--radius-xl)]"
        >
          <HomePrimaryPane
            activeTab={primaryTab}
            disabled={disabled}
            onChooseWorkspace={() => void chooseWorkspace()}
            onCreateWorkflow={() => {
              open({ kind: "workflowCreate", mode: "overlay" });
            }}
            onTabChange={setPrimaryTab}
            projectItems={projectItems}
            projectsQuery={projects}
          />
        </section>
        <section
          aria-labelledby="attention-title"
          className="island-glass min-h-0 overflow-hidden rounded-[var(--radius-xl)]"
        >
          <AttentionList items={attentionItems} query={attention} />
        </section>
      </div>
    </div>
  );
}

function DetailPaneCrossfade({
  children,
  contentKey,
}: Readonly<{ children: ReactNode; contentKey: string }>) {
  const previous = useRef({ children, contentKey });
  const [outgoing, setOutgoing] = useState<Readonly<{
    children: ReactNode;
    contentKey: string;
  }> | null>(null);

  useLayoutEffect(() => {
    if (previous.current.contentKey !== contentKey) {
      setOutgoing(previous.current);
    }
  }, [contentKey]);

  useLayoutEffect(() => {
    previous.current = { children, contentKey };
  });

  return (
    <div className="relative h-full min-h-0">
      {outgoing === null ? null : (
        <div
          className="pointer-events-none absolute inset-0 z-0 animate-[detail-pane-crossfade-out_var(--motion-normal)_both]"
          key={`outgoing:${outgoing.contentKey}`}
          onAnimationEnd={(event) => {
            if (event.target === event.currentTarget) setOutgoing(null);
          }}
        >
          {outgoing.children}
        </div>
      )}
      <div
        className={cx(
          "absolute inset-0 z-10",
          outgoing !== null && "animate-[detail-pane-crossfade-in_var(--motion-normal)_both]",
        )}
        key={contentKey}
      >
        {children}
      </div>
    </div>
  );
}

function HomePrototypeSidebar({
  disabled,
  onChooseWorkspace,
  onCreateWorkflow,
  onProjectSelect,
  onWorkflowsSelect,
  projectItems,
  projectsQuery,
  selectedProjectID,
  workflowsSelected,
}: Readonly<{
  disabled: boolean;
  onChooseWorkspace: () => void;
  onCreateWorkflow: () => void;
  onProjectSelect: (projectID: string) => void;
  onWorkflowsSelect: () => void;
  projectItems: readonly ProjectSummary[];
  projectsQuery: ReturnType<typeof useProjectPages>;
  selectedProjectID: string | null;
  workflowsSelected: boolean;
}>) {
  const { t } = useTranslation();
  const sidebarItems = [
    { kind: "projects" as const, id: "projects" },
    { kind: "workflows" as const, id: "workflows" },
    ...projectItems.map((project) => ({ kind: "project" as const, id: project.id, project })),
  ];
  if (projectsQuery.isPending) {
    return <LoadingState appearanceDelayMs={0} fullPage={false} title={t("states.loading")} />;
  }
  if (projectsQuery.isError) {
    return <ErrorState body={errorMessage(projectsQuery.error)} fullPage={false} title={t("states.error")} />;
  }
  return (
    <VirtualizedInfiniteList
      className="home-prototype-sidebar-scroll h-full min-h-0 overflow-auto px-[calc(var(--space-3)/2)] hide-scrollbar contain-strict [-webkit-overflow-scrolling:touch]"
      estimateSize={() => 54}
      getItemKey={(item) => item.id}
      hasNextPage={projectsQuery.hasNextPage}
      items={sidebarItems}
      isFetchingNextPage={projectsQuery.isFetchingNextPage}
      loadingLabel={t("app.loadingMore")}
      onLoadMore={() => void projectsQuery.fetchNextPage()}
      paddingEnd={12}
      paddingStart={12}
      rowSpacing="compact"
      renderItem={(item) => {
        if (item.kind === "projects") {
          return (
          <div className="relative flex min-w-0 items-center gap-[var(--space-2)] px-[calc(var(--space-3)/2)] py-[var(--space-1)] pr-[calc(40px+var(--space-3)/2)]">
            <Folder aria-hidden="true" className="shrink-0" size={18} strokeWidth={1.5} />
            <strong className="min-w-0 flex-1 truncate">{t("home.projectsPane")}</strong>
            <button
              aria-label={t("home.newProject")}
              className="absolute right-[calc(var(--space-3)/2)] top-1/2 grid h-10 w-10 -translate-y-1/2 place-items-center justify-items-end rounded-full text-[var(--color-on-island)] disabled:opacity-55"
              disabled={disabled}
              onClick={onChooseWorkspace}
              type="button"
            >
              <Plus aria-hidden="true" size={14} strokeWidth={1.5} />
            </button>
          </div>
          );
        }
        if (item.kind === "workflows") {
          return (
          <div
            className={cx(
              "relative flex min-w-0 items-center gap-[var(--space-2)] rounded-[var(--radius-m)] px-[calc(var(--space-3)/2)] py-[var(--space-1)] pr-[calc(40px+var(--space-3)/2)] transition-colors",
              workflowsSelected
                ? "bg-[color-mix(in_srgb,var(--color-on-island)_12%,transparent)]"
                : "hover:bg-[color-mix(in_srgb,var(--color-on-island)_4%,transparent)]",
            )}
          >
            <button
              className="flex min-w-0 flex-1 items-center gap-[var(--space-2)] text-left"
              onClick={onWorkflowsSelect}
              type="button"
            >
              <span className="relative h-[18px] w-[18px] shrink-0">
                <Workflow
                  aria-hidden="true"
                  className={cx(
                    "absolute inset-0 transition-opacity",
                    workflowsSelected ? "opacity-0" : "opacity-100",
                  )}
                  size={18}
                  strokeWidth={1.5}
                />
                <Check
                  aria-hidden="true"
                  className={cx(
                    "absolute left-0.5 top-0.5 transition-opacity",
                    workflowsSelected ? "opacity-100" : "opacity-0",
                  )}
                  size={14}
                  strokeWidth={2}
                />
              </span>
              <strong className="min-w-0 truncate">{t("workflowLibrary.homeIslandTitle")}</strong>
            </button>
            <button
              aria-label={t("workflowLibrary.createWorkflow")}
              className="absolute right-[calc(var(--space-3)/2)] top-1/2 grid h-10 w-10 -translate-y-1/2 place-items-center justify-items-end rounded-full text-[var(--color-on-island)] disabled:opacity-55"
              disabled={disabled}
              onClick={onCreateWorkflow}
              type="button"
            >
              <Plus aria-hidden="true" size={14} strokeWidth={1.5} />
            </button>
          </div>
          );
        }
        return (
        <ProjectRow
          onSelect={() => {
            onProjectSelect(item.project.id);
          }}
          project={item.project}
          selected={item.project.id === selectedProjectID}
        />
        );
      }}
    />
  );
}

function GlobalWorkflowsPrototype() {
  const { t } = useTranslation();
  const navigation = useAppNavigation();
  const workflowsQuery = useWorkflowPages();
  const workflows = useMemo(
    () => workflowsQuery.data?.pages.flatMap((page) => page.workflows) ?? [],
    [workflowsQuery.data],
  );
  if (workflowsQuery.isPending) {
    return <LoadingState appearanceDelayMs={0} fullPage={false} title={t("states.loading")} />;
  }
  if (workflowsQuery.isError) {
    return (
      <ErrorState
        body={errorMessage(workflowsQuery.error)}
        fullPage={false}
        onRetry={() => void workflowsQuery.refetch()}
        retryLabel={t("app.retry")}
        title={t("states.error")}
      />
    );
  }
  return (
    <VirtualizedInfiniteList
      className={`h-full min-h-0 overflow-auto px-[var(--space-4)] hide-scrollbar contain-strict [&>*]:mx-auto [&>*]:w-full ${homeListCardListMaxWidthClassName}`}
      empty={<EmptyState body={t("workflowLibrary.emptyBody")} fullPage={false} title={t("workflowLibrary.emptyTitle")} />}
      estimateSize={() => 96}
      getItemKey={(workflow) => workflow.id}
      hasNextPage={workflowsQuery.hasNextPage}
      isFetchingNextPage={workflowsQuery.isFetchingNextPage}
      items={workflows}
      loadingLabel={t("app.loadingMore")}
      onLoadMore={() => void workflowsQuery.fetchNextPage()}
      paddingEnd={16}
      paddingStart={16}
      renderItem={(workflow) => (
        <WorkflowCard
          onOpen={() => void navigation.openWorkflowEditor({ workflowID: workflow.id })}
          workflow={workflow}
        />
      )}
    />
  );
}

type ProjectPrototypeTab = "boards" | "sessions" | "subagents";

function ProjectPrototypeDetail({
  disabled,
  onLinkWorkflow,
  project,
}: Readonly<{
  disabled: boolean;
  onLinkWorkflow: () => void;
  project: ProjectSummary;
}>) {
  const { t } = useTranslation();
  const { api } = useAppServices();
  const navigation = useAppNavigation();
  const [tab, setTab] = useState<ProjectPrototypeTab>("boards");
  const linksQuery = useQuery({
    queryKey: queryKeys.projectWorkflowLinks(project.id),
    queryFn: async () => api.listProjectWorkflowLinks(project.id),
  });
  const linkedWorkflowQueries = useQueries({
    queries: (linksQuery.data ?? []).map((link) => ({
      queryKey: queryKeys.workflowDefinition(link.workflowID),
      queryFn: async () => api.getWorkflow(link.workflowID),
    })),
  });
  const mainSessionsQuery = useInfiniteQuery({
    ...mainSessionCatalogInfiniteQueryOptions(api, project.id),
    enabled: tab === "sessions",
  });
  const subagentSessionsQuery = useInfiniteQuery({
    ...subagentSessionCatalogInfiniteQueryOptions(api, project.id),
    enabled: tab === "subagents",
  });
  const linkedWorkflows = linkedWorkflowQueries.flatMap((query) =>
    query.data === undefined ? [] : [query.data.workflow],
  );
  const linkedWorkflowError = linkedWorkflowQueries.find((query) => query.isError)?.error ?? null;
  const linkedWorkflowsPending =
    linksQuery.data !== undefined && linkedWorkflowQueries.some((query) => query.isPending);

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="px-[var(--space-4)] pt-[var(--space-4)]">
        <IslandTabs
          ariaLabel={t("home.prototype.projectContent")}
          className="grid-cols-3"
          items={[
            {
              action: {
                ariaLabel: t("workflowLibrary.linkWorkflow"),
                children: <Plus aria-hidden="true" size={18} strokeWidth={1.5} />,
                disabled,
                onClick: onLinkWorkflow,
              },
              label: t("home.prototype.boards"),
              value: "boards",
            },
            {
              action: {
                ariaLabel: t("home.prototype.newChatUnavailable"),
                children: <Plus aria-hidden="true" size={18} strokeWidth={1.5} />,
                disabled: true,
                onClick: () => undefined,
              },
              label: t("home.prototype.sessions"),
              value: "sessions",
            },
            { label: t("home.prototype.subagents"), value: "subagents" },
          ]}
          onValueChange={setTab}
          value={tab}
        />
      </div>
      <div className="min-h-0 flex-1">
        {tab === "boards" ? (
          linksQuery.isPending || linkedWorkflowsPending ? (
            <LoadingState appearanceDelayMs={0} fullPage={false} title={t("states.loading")} />
          ) : linksQuery.isError || linkedWorkflowError !== null ? (
            <ErrorState
              body={errorMessage(linksQuery.error ?? linkedWorkflowError)}
              fullPage={false}
              onRetry={() => {
                if (linksQuery.isError) {
                  void linksQuery.refetch();
                  return;
                }
                for (const query of linkedWorkflowQueries) {
                  if (query.isError) void query.refetch();
                }
              }}
              retryLabel={t("app.retry")}
              title={t("states.error")}
            />
          ) : (
            <VirtualizedInfiniteList
              className={`h-full min-h-0 overflow-auto px-[var(--space-4)] hide-scrollbar contain-strict [&>*]:mx-auto [&>*]:w-full ${homeListCardListMaxWidthClassName}`}
              empty={
                <EmptyState
                  body={t("home.prototype.noBoardsBody")}
                  fullPage={false}
                  title={t("home.prototype.noBoardsTitle")}
                />
              }
              estimateSize={() => 96}
              getItemKey={(workflow) => workflow.id}
              hasNextPage={false}
              isFetchingNextPage={false}
              items={linkedWorkflows}
              loadingLabel={t("app.loadingMore")}
              onLoadMore={() => undefined}
              paddingEnd={16}
              paddingStart={16}
              renderItem={(workflow) => (
                <WorkflowCard
                  onOpen={() => void navigation.openProject(project.id, workflow.id)}
                  workflow={workflow}
                />
              )}
            />
          )
        ) : (
          <SessionPrototypeList
            query={tab === "sessions" ? mainSessionsQuery : subagentSessionsQuery}
          />
        )}
      </div>
    </div>
  );
}

function SessionPrototypeList({
  query,
}: Readonly<{
  query: Readonly<{
    data:
      | Readonly<{
          pages: readonly Readonly<{ sessions: readonly SessionCatalogSummary[] }>[];
        }>
      | undefined;
    error: Error | null;
    fetchNextPage: () => Promise<unknown>;
    hasNextPage: boolean;
    isError: boolean;
    isFetchingNextPage: boolean;
    isPending: boolean;
    refetch: () => Promise<unknown>;
  }>;
}>) {
  const { t } = useTranslation();
  const sessions = query.data?.pages.flatMap((page) => page.sessions) ?? [];
  if (query.isPending) {
    return <LoadingState appearanceDelayMs={0} fullPage={false} title={t("states.loading")} />;
  }
  if (query.isError) {
    return (
      <ErrorState
        body={errorMessage(query.error)}
        fullPage={false}
        onRetry={() => void query.refetch()}
        retryLabel={t("app.retry")}
        title={t("states.error")}
      />
    );
  }
  return (
    <VirtualizedInfiniteList
      className={`h-full min-h-0 overflow-auto px-[var(--space-4)] hide-scrollbar contain-strict [&>*]:mx-auto [&>*]:w-full ${homeListCardListMaxWidthClassName}`}
      empty={
        <EmptyState
          body={t("home.prototype.noSessionsBody")}
          fullPage={false}
          title={t("home.prototype.noSessionsTitle")}
        />
      }
      estimateSize={() => 96}
      getItemKey={(session) => session.id}
      hasNextPage={query.hasNextPage}
      isFetchingNextPage={query.isFetchingNextPage}
      items={sessions}
      loadingLabel={t("app.loadingMore")}
      onLoadMore={() => void query.fetchNextPage()}
      paddingEnd={16}
      paddingStart={16}
      renderItem={(session) => (
        <HomeListCard
          ariaLabel={session.name ?? session.firstPromptPreview ?? session.id}
          onClick={() => undefined}
        >
          <span className="truncate text-sm text-[var(--color-muted)]">
            {formatRelativeTime(session.updatedAt)}
          </span>
          <strong className="truncate">
            {session.name ?? session.firstPromptPreview ?? session.id}
          </strong>
          <span className="truncate text-sm text-[var(--color-muted)]">
            {session.firstPromptPreview ?? t("home.prototype.noPromptPreview")}
          </span>
        </HomeListCard>
      )}
    />
  );
}

type AttentionListProps = Readonly<{
  items: readonly AttentionItem[];
  query: ReturnType<typeof useGlobalAttentionPages>;
}>;

function AttentionList({ items, query }: AttentionListProps) {
  const { t } = useTranslation();
  const { open } = useOwnedSidebarRoots();
  if (query.isPending) {
    return <LoadingState appearanceDelayMs={0} fullPage={false} reveal={false} title={t("states.loading")} />;
  }
  if (query.isError) {
    return <ErrorState body={errorMessage(query.error)} reveal={false} title={t("states.error")} />;
  }
  return (
    <VirtualizedInfiniteList
      className={`h-full min-h-0 overflow-auto px-[var(--space-4)] hide-scrollbar contain-strict [-webkit-overflow-scrolling:touch] [&>*]:mx-auto [&>*]:w-full ${homeListCardListMaxWidthClassName}`}
      empty={<HomeInlineEmptyState body={t("home.noAttentionBody")} />}
      estimateSize={() => 144}
      getItemKey={(item) => item.id}
      hasNextPage={query.hasNextPage}
      header={
        // Mirror the projects-tab pill's internal top inset (1px border + p-1 + button py-2) and label
        // typography so the "Inbox" upper edge aligns precisely with the tab labels in the adjacent pane.
        <h2
          className="m-0 mt-[calc(1px+var(--space-1)+var(--space-2))] text-base font-bold"
          id="attention-title"
        >
          {t("home.attentionPane")}
        </h2>
      }
      isFetchingNextPage={query.isFetchingNextPage}
      items={items}
      loadingLabel={t("app.loadingMore")}
      onLoadMore={() => void query.fetchNextPage()}
      paddingEnd={16}
      paddingStart={16}
      renderItem={(item) => <AttentionRow item={item} openSidebar={open} />}
    />
  );
}

const AttentionRow = memo(function AttentionRow({
  item,
  openSidebar,
}: Readonly<{
  item: AttentionItem;
  openSidebar: SidebarRootController["open"];
}>) {
  const { t } = useTranslation();
  const message =
    item.message ??
    (item.kind === "approval"
      ? t("app.attention.approvalFallback")
      : t("app.attention.interruptedCurrentNodeFallback"));
  return (
    <button
      className={cx(
        "grid w-full min-w-0 gap-[var(--space-2)] rounded-[var(--radius-l)] p-[var(--space-3)] text-left text-[var(--color-on-island)]",
        islandSurfaceClassName(1),
      )}
      data-testid="attention-row"
      onClick={() => {
        openSidebar({
          kind: "taskDetail",
          initialFocus: taskDetailInitialFocusFromAttentionItem(item),
          inboxNav: true,
          mode: "overlay",
          onMutated: undefined,
          taskID: item.taskID,
        });
      }}
      type="button"
    >
      <div
        className="flex min-w-0 flex-wrap items-center gap-[var(--space-2)]"
        data-testid="attention-row-meta"
      >
        {item.taskShortID.length > 0 ? (
          <span className="min-w-0 truncate font-mono text-sm text-[var(--color-muted)]">
            {item.taskShortID}
          </span>
        ) : null}
      </div>
      {item.taskTitle.length > 0 ? <strong className="min-w-0 truncate">{item.taskTitle}</strong> : null}
      <span className="min-w-0 text-sm break-words">{message}</span>
      <span className="text-sm text-[var(--color-muted)]">{formatRelativeTime(item.occurredAt)}</span>
    </button>
  );
}, attentionRowPropsEqual);

function attentionRowPropsEqual(
  previous: Readonly<{
    item: AttentionItem;
    openSidebar: SidebarRootController["open"];
  }>,
  next: Readonly<{
    item: AttentionItem;
    openSidebar: SidebarRootController["open"];
  }>,
): boolean {
  return previous.openSidebar === next.openSidebar && attentionItemsEqual(previous.item, next.item);
}

function attentionItemsEqual(previous: AttentionItem, next: AttentionItem): boolean {
  return (
    previous.id === next.id &&
    previous.kind === next.kind &&
    previous.taskID === next.taskID &&
    previous.taskShortID === next.taskShortID &&
    previous.taskTitle === next.taskTitle &&
    previous.message === next.message &&
    previous.occurredAt === next.occurredAt
  );
}

function HomeInlineEmptyState({ body }: Readonly<{ body: string }>) {
  return (
    <div className="rounded-[var(--radius-l)] border border-dashed border-[var(--color-outline)] p-[var(--space-4)] text-[var(--color-muted)]">
      <p>{body}</p>
    </div>
  );
}
