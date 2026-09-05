import { useCallback, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import type { AttentionItem } from "@/api";
import { errorMessage } from "@/api";
import { basename, projectKeyFromName } from "@/app-facade";
import { useAppNavigation } from "@/app-facade";
import { queryKeys } from "@/app-facade";
import { SidebarRootOwner, useOwnedSidebarRoots, type SidebarMode } from "@/app-facade";
import { useAppServices } from "@/app-facade";
import { useNativeDialogFallback } from "@/app-facade";
import { useStatusController } from "@/app-facade";
import { useConnectionSnapshot } from "@/app-facade";
import { desktopChatEnabled } from "@/shared/feature-flags";
import { ErrorState, homeListCardListMaxWidthClassName, LoadingState, VirtualizedInfiniteList } from "@/ui";
import { HomeSidebar, type HomeSidebarCategory } from "./HomeSidebar";
import { HomeProjectContent } from "./HomeProjectContent";
import { OverlappingCrossfade } from "./OverlappingCrossfade";
import { ProjectCreateDialog, type ProjectDraft } from "./ProjectCreateForm";
import { useHomeSidebarMode } from "./useHomeSidebarMode";
import {
  useGlobalAttentionPages,
  useProjectCreation,
  useProjectCreationEvents,
  useProjectPages,
} from "./useHomeData";
import { AttentionRow } from "./AttentionRow";

const LOCAL_UNBOUND_PLAN_KIND = "local_unbound";
export function HomeRoute({ selectedProjectID }: Readonly<{ selectedProjectID: string | null }>) {
  return (
    <SidebarRootOwner>
      <HomeRouteContent selectedProjectID={selectedProjectID} />
    </SidebarRootOwner>
  );
}

function HomeRouteContent({ selectedProjectID }: Readonly<{ selectedProjectID: string | null }>) {
  const { t } = useTranslation();
  const { api, nativeBridge } = useAppServices();
  const { push } = useStatusController();
  const connection = useConnectionSnapshot();
  const { mainPaneRef, sidebarMode } = useHomeSidebarMode();
  const navigation = useAppNavigation();
  const { open } = useOwnedSidebarRoots();
  const queryClient = useQueryClient();
  const creation = useProjectCreation();
  const projects = useProjectPages();
  const attention = useGlobalAttentionPages();
  const [category, setCategory] = useState<HomeSidebarCategory>("projects");
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

  const selectedCategory = selectedProjectID === null ? category : "projects";
  const detailKey = selectedProjectID === null ? "inbox" : `project:${selectedProjectID}`;
  return (
    <div className="h-full min-h-0" data-testid="home-route-root">
      {projectCreationDialog.fallback}
      <div className="grid h-full min-h-0 grid-cols-[350px_minmax(0,1fr)]" data-testid="home-pane-grid">
        <HomeSidebar
          disabled={disabled}
          onChooseWorkspace={() => void chooseWorkspace()}
          onCreateWorkflow={() => {
            open({ kind: "workflowCreate", mode: sidebarMode });
          }}
          onProjectSelect={(projectID) => {
            if (selectedProjectID === projectID) {
              void navigation.selectHomeProject(null);
              return;
            }
            void navigation.selectHomeProject(projectID);
          }}
          onCategorySelect={(nextCategory) => {
            if (category === nextCategory && selectedProjectID === null) {
              return;
            }
            setCategory(nextCategory);
            if (selectedProjectID !== null) {
              void navigation.selectHomeProject(null);
            }
          }}
          selectedCategory={selectedCategory}
          projectItems={projectItems}
          projectsQuery={projects}
          sidebarMode={sidebarMode}
          selectedProjectID={selectedProjectID}
        />
        <section
          className="island-glass my-[var(--space-2)] mr-[var(--space-2)] min-h-0 overflow-hidden rounded-[var(--radius-xl)]"
          data-sidebar-protected-main
          ref={mainPaneRef}
        >
          <OverlappingCrossfade contentKey={detailKey}>
            {selectedProjectID !== null ? (
              <HomeProjectContent
                key={selectedProjectID}
                projectID={selectedProjectID}
                sessionsVisible={desktopChatEnabled}
                sidebarMode={sidebarMode}
              />
            ) : (
              <AttentionList items={attentionItems} query={attention} sidebarMode={sidebarMode} />
            )}
          </OverlappingCrossfade>
        </section>
      </div>
    </div>
  );
}

type AttentionListProps = Readonly<{
  items: readonly AttentionItem[];
  query: ReturnType<typeof useGlobalAttentionPages>;
  sidebarMode: SidebarMode;
}>;

function AttentionList({ items, query, sidebarMode }: AttentionListProps) {
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
      renderItem={(item) => <AttentionRow item={item} openSidebar={open} sidebarMode={sidebarMode} />}
    />
  );
}

function HomeInlineEmptyState({ body }: Readonly<{ body: string }>) {
  return (
    <div className="rounded-[var(--radius-l)] border border-dashed border-[var(--color-outline)] p-[var(--space-4)] text-[var(--color-muted)]">
      <p>{body}</p>
    </div>
  );
}
