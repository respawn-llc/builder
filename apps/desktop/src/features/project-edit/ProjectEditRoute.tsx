import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactElement,
  type ReactNode,
} from "react";
import { useTranslation } from "react-i18next";
import { Plus, Save } from "lucide-react";

import type { ProjectEdit, WorkspaceCatalogRow } from "@/api";
import { errorMessage, isProjectMissingError } from "@/api";
import type { SidebarPageNavigator } from "@/app-facade";
import { workspaceCatalogInfiniteQueryOptions } from "@/app-facade";
import { useAppServices } from "@/app-facade";
import { useConnectionSnapshot } from "@/app-facade";
import { useNativeDialogFallback } from "@/app-facade";
import { usePublishSidebarHeaderAction } from "@/app-facade";
import { useSidebarBackWhen } from "@/app-facade";
import { useSidebarHeaderOffset } from "@/app-facade";
import { useStatusController } from "@/app-facade";
import { useTextFieldSubmitShortcut } from "@/app-facade";
import { useWindowChromeTitle } from "@/app-facade";
import {
  Button,
  ErrorState,
  HelpHint,
  LoadingState,
  VirtualizedInfiniteList,
  type VirtualizedInfiniteListBoundaryState,
} from "@/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import {
  ProjectKeyField,
  ProjectNameField,
  WorkspaceRow,
  WorkspaceUnlinkFallbackDialog,
  type WorkspaceUnlinkTarget,
  workspaceUnlinkDialogWidth,
} from "./ProjectEditParts";
import { projectKeyErrors, projectNameErrors } from "./ProjectEditUtils";
import {
  useProjectDefaultWorkspaceSave,
  useProjectWorkspaceChangedEvents,
  useProjectEdit,
  useProjectSave,
  useProjectWorkspaceAttach,
  useProjectWorkspaceUnlinkRequests,
  useProjectWorkspaceUnlink,
} from "./useProjectEditData";

const projectEditContentMaxWidthClassName = "max-w-[1200px]";

export function ProjectEditRoute({
  headerAccessory,
  navigator,
  projectId,
}: Readonly<{
  headerAccessory?: ReactNode;
  navigator?: SidebarPageNavigator;
  projectId: string;
}>): ReactElement | null {
  const { t } = useTranslation();
  const { api } = useAppServices();
  const query = useProjectEdit(projectId);
  const catalog = useInfiniteQuery(workspaceCatalogInfiniteQueryOptions(api, projectId));
  const workspaceOccurrences = useMemo(
    () =>
      catalog.data?.pages.flatMap((page) =>
        page.workspaces.map((workspace, index) => ({
          occurrenceKey: `${page.offset.toString()}:${index.toString()}`,
          workspace,
        })),
      ) ?? [],
    [catalog.data?.pages],
  );
  const projectMissing = [query.error, catalog.error].some(isProjectMissingError);
  useSidebarBackWhen(projectMissing, navigator);
  useWindowChromeTitle(query.data?.displayName ?? null);
  if (projectMissing && navigator !== undefined) return null;

  return (
    <ProjectEditContent
      catalogBoundary={projectCatalogBoundary(catalog, t)}
      catalogPending={catalog.isPending}
      headerAccessory={headerAccessory}
      hasNextPage={catalog.hasNextPage}
      hasPreviousPage={catalog.hasPreviousPage}
      isFetchingNextPage={catalog.isFetchingNextPage}
      isFetchingPreviousPage={catalog.isFetchingPreviousPage}
      key={query.data?.projectID ?? projectId}
      metadata={
        query.isPending
          ? { state: "pending" }
          : query.isError
            ? {
                state: "error",
                error: query.error,
                onRetry: () => void query.refetch(),
              }
            : { state: "loaded", project: query.data }
      }
      onLoadMore={() => void catalog.fetchNextPage()}
      onLoadPrevious={() => void catalog.fetchPreviousPage()}
      previousBoundary={projectCatalogPreviousBoundary(catalog, t)}
      projectID={projectId}
      workspaceOccurrences={workspaceOccurrences}
    />
  );
}

function ProjectEditContent({
  catalogBoundary,
  catalogPending,
  headerAccessory,
  hasNextPage,
  hasPreviousPage,
  isFetchingNextPage,
  isFetchingPreviousPage,
  metadata,
  onLoadMore,
  onLoadPrevious,
  previousBoundary,
  projectID,
  workspaceOccurrences,
}: Readonly<{
  catalogBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  catalogPending: boolean;
  headerAccessory?: ReactNode;
  hasNextPage: boolean;
  hasPreviousPage: boolean;
  isFetchingNextPage: boolean;
  isFetchingPreviousPage: boolean;
  metadata: ProjectEditMetadataState;
  onLoadMore: () => void;
  onLoadPrevious: () => void;
  previousBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  projectID: string;
  workspaceOccurrences: readonly ProjectWorkspaceOccurrence[];
}>) {
  const { t } = useTranslation();
  const { nativeBridge } = useAppServices();
  const { push } = useStatusController();
  const connection = useConnectionSnapshot();
  const save = useProjectSave(projectID);
  const defaultSave = useProjectDefaultWorkspaceSave(projectID);
  const attach = useProjectWorkspaceAttach(projectID);
  const unlink = useProjectWorkspaceUnlink(projectID);
  const project = metadata.state === "loaded" ? metadata.project : undefined;
  const { keyDraft, nameDraft, setKeyDraft, setNameDraft } = useProjectDrafts(project);
  const disabled = connection.phase !== "connected";
  const mutating =
    disabled || save.isPending || defaultSave.isPending || attach.isPending || unlink.isPending;
  const nameErrors = projectNameErrors(nameDraft, t);
  const keyErrors = projectKeyErrors(keyDraft, t);
  const nameChanged = project !== undefined && nameDraft !== project.displayName;
  const keyChanged = project !== undefined && keyDraft !== project.projectKey;
  const dirty = nameChanged || keyChanged;
  const pushToast = useCallback(
    (id: string, tone: "info" | "success" | "danger", body: string, title = t("projectEdit.title")) => {
      push({ id, tone, title, body });
    },
    [push, t],
  );
  const confirmUnlink = useConfirmWorkspaceUnlink(unlink, pushToast, t);
  const unlinkDialog = useNativeDialogFallback<WorkspaceUnlinkTarget>({
    errorNoticeID: "workspace-unlink-window-error",
    errorTitle: t("projectEdit.unlinkWindowError"),
    nativeAvailable: nativeBridge.capabilities.dialogWindows,
    openNative: async (target) => {
      await nativeBridge.dialogs.openWindow(
        workspaceUnlinkWindowOptions(target, t("projectEdit.unlinkTitle")),
      );
    },
    renderFallback: (target, close) => (
      <WorkspaceUnlinkFallbackDialog
        disabled={mutating}
        onClose={close}
        onConfirm={(nextTarget) => void confirmUnlink(nextTarget, close)}
        target={target}
      />
    ),
  });
  const handleWorkspaceUnlinkRequest = useCallback(
    (target: WorkspaceUnlinkTarget) => {
      if (target.projectID === projectID) {
        void confirmUnlink(target);
      }
    },
    [confirmUnlink, projectID],
  );

  useProjectWorkspaceUnlinkRequests(nativeBridge, handleWorkspaceUnlinkRequest);
  useProjectWorkspaceChangedEvents(nativeBridge, projectID);

  const chooseWorkspace = useChooseWorkspace(nativeBridge, attach, pushToast, t);

  const saveProject = useCallback(async (): Promise<void> => {
    try {
      await save.mutateAsync({ displayName: nameDraft, projectKey: keyChanged ? keyDraft : "" });
      pushToast("project-edit-saved", "success", t("projectEdit.projectSaved"));
    } catch (error) {
      pushToast("project-edit-save-error", "danger", errorMessage(error));
    }
  }, [keyChanged, keyDraft, nameDraft, save, pushToast, t]);

  const saveDefaultWorkspace = useSaveDefaultWorkspace(defaultSave, pushToast, t);

  // Publish the save control into the shared sidebar header (left of delete). It only appears when a
  // draft (name or key) differs from the saved value, and is disabled while invalid or disconnected.
  const canSave = dirty && nameErrors.length === 0 && keyErrors.length === 0 && !mutating;
  const projectSaveShortcut = useTextFieldSubmitShortcut({
    action: () => {
      void saveProject();
    },
    available: canSave,
    kind: "direct",
  });
  const headerSaveAction = useMemo<ReactNode>(() => {
    if (!dirty) {
      return null;
    }
    return (
      <Button
        aria-label={t("projectEdit.saveName")}
        disabled={!canSave}
        onClick={() => void saveProject()}
        size="icon"
        title={t("projectEdit.saveName")}
        variant="primary"
      >
        <Save aria-hidden="true" size={18} strokeWidth={1.5} />
      </Button>
    );
  }, [canSave, dirty, saveProject, t]);
  const headerActions = useMemo(
    () => (
      <>
        {headerSaveAction}
        {headerAccessory}
      </>
    ),
    [headerAccessory, headerSaveAction],
  );
  usePublishSidebarHeaderAction(headerActions);

  const header = (
    <ProjectEditListHeader
      disabled={mutating}
      keyDraft={keyDraft}
      keyErrors={keyErrors}
      metadata={metadata}
      nameDraft={nameDraft}
      nameErrors={nameErrors}
      onAttach={() => void chooseWorkspace()}
      onKeyDown={projectSaveShortcut}
      onKeyChange={setKeyDraft}
      onNameChange={setNameDraft}
    />
  );

  return (
    <section
      aria-labelledby="workspaces-title"
      className="h-full min-h-0 overflow-hidden"
      data-testid="project-edit-route"
    >
      {unlinkDialog.fallback}
      <ProjectWorkspaceList
        disabled={mutating}
        hasNextPage={hasNextPage}
        hasPreviousPage={hasPreviousPage}
        header={header}
        isFetchingNextPage={isFetchingNextPage}
        isFetchingPreviousPage={isFetchingPreviousPage}
        nextBoundary={catalogBoundary}
        onLoadMore={onLoadMore}
        onLoadPrevious={onLoadPrevious}
        previousBoundary={previousBoundary}
        onMakeDefault={(workspace) => void saveDefaultWorkspace(workspace)}
        onUnlink={(workspace) => {
          void unlinkDialog.open({
            projectID,
            rootPath: workspace.rootPath,
            workspaceID: workspace.id,
          });
        }}
        catalogPending={catalogPending}
        workspaceOccurrences={workspaceOccurrences}
      />
    </section>
  );
}

function useProjectDrafts(project: ProjectEdit | undefined) {
  const [nameDraft, setNameDraft] = useState(project?.displayName ?? "");
  const [keyDraft, setKeyDraft] = useState(project?.projectKey ?? "");
  const draftsHydrated = useRef(project !== undefined);
  useEffect(() => {
    if (project === undefined || draftsHydrated.current) return;
    setNameDraft(project.displayName);
    setKeyDraft(project.projectKey);
    draftsHydrated.current = true;
  }, [project]);
  return { keyDraft, nameDraft, setKeyDraft, setNameDraft };
}

type ProjectEditMutation = ReturnType<typeof useProjectWorkspaceUnlink>;
type ProjectAttachMutation = ReturnType<typeof useProjectWorkspaceAttach>;
type ProjectDefaultMutation = ReturnType<typeof useProjectDefaultWorkspaceSave>;
type ProjectEditTranslator = ReturnType<typeof useTranslation>["t"];
type PushToast = (
  id: string,
  tone: "info" | "success" | "danger",
  body: string,
  title?: string,
) => void;

function useConfirmWorkspaceUnlink(
  unlink: ProjectEditMutation,
  pushToast: PushToast,
  t: ProjectEditTranslator,
) {
  return useCallback(
    async (target: WorkspaceUnlinkTarget, close?: () => void): Promise<void> => {
      try {
        const response = await unlink.mutateAsync(target.workspaceID);
        if (response.blockers.length === 0) {
          close?.();
          pushToast("project-edit-workspace-unlinked", "success", t("projectEdit.workspaceUnlinked"));
          return;
        }
        pushToast(
          "project-edit-workspace-unlink-blocked",
          "danger",
          response.blockers.map((blocker) => blocker.message).join("\n") ||
            t("projectEdit.workspaceUnlinkBlocked"),
          t("projectEdit.workspaceUnlinkBlocked"),
        );
      } catch (error) {
        pushToast("project-edit-workspace-unlink-error", "danger", errorMessage(error));
      }
    },
    [pushToast, t, unlink],
  );
}

function useChooseWorkspace(
  nativeBridge: ReturnType<typeof useAppServices>["nativeBridge"],
  attach: ProjectAttachMutation,
  pushToast: PushToast,
  t: ProjectEditTranslator,
) {
  return useCallback(async (): Promise<void> => {
    try {
      const selected = await nativeBridge.directories.selectDirectory({
        title: t("projectEdit.chooseWorkspace"),
      });
      if (selected === null) return;
      const response = await attach.mutateAsync(selected.path);
      pushToast(
        "project-edit-workspace-attached",
        "success",
        response.outcome === "already_attached"
          ? t("projectEdit.workspaceAlreadyLinked")
          : t("projectEdit.workspaceAttached"),
      );
    } catch (error) {
      pushToast("project-edit-workspace-attach-error", "danger", errorMessage(error));
    }
  }, [attach, nativeBridge.directories, pushToast, t]);
}

function useSaveDefaultWorkspace(
  defaultSave: ProjectDefaultMutation,
  pushToast: PushToast,
  t: ProjectEditTranslator,
) {
  return useCallback(
    async (workspace: WorkspaceCatalogRow): Promise<void> => {
      if (workspace.isDefault) return;
      try {
        await defaultSave.mutateAsync(workspace.id);
        pushToast("project-edit-default-saved", "success", t("projectEdit.defaultWorkspaceSaved"));
      } catch (error) {
        pushToast("project-edit-default-save-error", "danger", errorMessage(error));
      }
    },
    [defaultSave, pushToast, t],
  );
}

function ProjectEditListHeader({
  disabled,
  keyDraft,
  keyErrors,
  metadata,
  nameDraft,
  nameErrors,
  onAttach,
  onKeyDown,
  onKeyChange,
  onNameChange,
}: Readonly<{
  disabled: boolean;
  keyDraft: string;
  keyErrors: readonly string[];
  metadata: ProjectEditMetadataState;
  nameDraft: string;
  nameErrors: readonly string[];
  onAttach: () => void;
  onKeyDown: React.KeyboardEventHandler<HTMLInputElement>;
  onKeyChange: (value: string) => void;
  onNameChange: (value: string) => void;
}>) {
  const { t } = useTranslation();
  return (
    <div className={`mx-auto grid w-full ${projectEditContentMaxWidthClassName} gap-[var(--space-3)]`}>
      <div className="grid min-w-0 gap-[var(--space-3)]">
        {metadata.state === "pending" ? (
          <LoadingState body={t("states.loading")} reveal={false} title={t("projectEdit.loadingTitle")} />
        ) : null}
        {metadata.state === "error" ? (
          <ErrorState
            body={errorMessage(metadata.error)}
            onRetry={metadata.onRetry}
            reveal={false}
            retryLabel={t("app.retry")}
            title={t("states.error")}
          />
        ) : null}
        {metadata.state === "loaded" ? (
          <>
            <ProjectNameField
              disabled={disabled}
              nameDraft={nameDraft}
              nameErrors={nameErrors}
              onKeyDown={onKeyDown}
              onNameChange={onNameChange}
            />
            <ProjectKeyField
              disabled={disabled}
              keyDraft={keyDraft}
              keyErrors={keyErrors}
              onKeyDown={onKeyDown}
              onKeyChange={onKeyChange}
            />
          </>
        ) : null}
      </div>
      <div className="flex min-w-0 items-center justify-between gap-[var(--space-3)]">
        <span className="inline-flex min-w-0 items-center gap-[var(--space-1)]">
          <h1 className="m-0 truncate text-[1.15rem] font-bold" id="workspaces-title">
            {t("projectEdit.workspaces")}
          </h1>
          <HelpHint className="shrink-0" label={t("projectEdit.workspacesHelp")} side="bottom" />
        </span>
        <button
          aria-label={t("projectEdit.attachWorkspace")}
          className="grid h-9 w-9 place-items-center rounded-full border border-[var(--color-outline)] bg-[var(--color-island-1)] text-[var(--color-on-island)] disabled:cursor-not-allowed disabled:opacity-55"
          disabled={disabled}
          onClick={onAttach}
          type="button"
        >
          <Plus aria-hidden="true" size={20} strokeWidth={1.5} />
        </button>
      </div>
    </div>
  );
}

function ProjectWorkspaceList({
  catalogPending,
  disabled,
  hasNextPage,
  hasPreviousPage,
  header,
  isFetchingNextPage,
  isFetchingPreviousPage,
  nextBoundary,
  onLoadMore,
  onLoadPrevious,
  onMakeDefault,
  onUnlink,
  workspaceOccurrences,
  previousBoundary,
}: Readonly<{
  catalogPending: boolean;
  disabled: boolean;
  hasNextPage: boolean;
  hasPreviousPage: boolean;
  header: ReactNode;
  isFetchingNextPage: boolean;
  isFetchingPreviousPage: boolean;
  nextBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  onLoadMore: () => void;
  onLoadPrevious: () => void;
  onMakeDefault: (workspace: WorkspaceCatalogRow) => void;
  onUnlink: (workspace: WorkspaceCatalogRow) => void;
  workspaceOccurrences: readonly ProjectWorkspaceOccurrence[];
  previousBoundary: VirtualizedInfiniteListBoundaryState | undefined;
}>) {
  const { t } = useTranslation();
  const headerOffset = useSidebarHeaderOffset();
  return (
    <VirtualizedInfiniteList
      className="h-full min-h-0 overflow-auto px-[var(--space-4)] hide-scrollbar contain-strict [-webkit-overflow-scrolling:touch]"
      empty={
        catalogPending ? (
          <LoadingState body={t("states.loading")} reveal={false} title={t("projectEdit.workspaces")} />
        ) : nextBoundary?.state === "error" ? (
          <ErrorState
            body={nextBoundary.message}
            onRetry={nextBoundary.onRetry}
            reveal={false}
            retryLabel={nextBoundary.retryLabel}
            title={t("projectEdit.workspaces")}
          />
        ) : (
          <p className="m-0 text-[var(--color-muted)]">{t("projectEdit.noWorkspaces")}</p>
        )
      }
      estimateSize={() => 72}
      getItemKey={(occurrence) => occurrence.occurrenceKey}
      hasNextPage={hasNextPage && nextBoundary?.state !== "error"}
      hasPreviousPage={hasPreviousPage && previousBoundary?.state !== "error"}
      header={header}
      isFetchingNextPage={isFetchingNextPage}
      isFetchingPreviousPage={isFetchingPreviousPage}
      items={workspaceOccurrences}
      loadingLabel={t("app.loadingMore")}
      nextBoundary={workspaceOccurrences.length === 0 ? undefined : nextBoundary}
      onLoadMore={onLoadMore}
      onLoadPrevious={onLoadPrevious}
      previousBoundary={previousBoundary}
      previousLoadItemKey={workspaceOccurrences[0]?.occurrenceKey}
      paddingEnd={16}
      paddingStart={16 + headerOffset}
      renderItem={({ workspace }) => (
        <div className={`mx-auto w-full ${projectEditContentMaxWidthClassName}`}>
          <WorkspaceRow
            disabled={disabled}
            onMakeDefault={() => {
              onMakeDefault(workspace);
            }}
            onUnlink={() => {
              onUnlink(workspace);
            }}
            workspace={workspace}
          />
        </div>
      )}
    />
  );
}

type ProjectEditMetadataState =
  | Readonly<{ state: "pending" }>
  | Readonly<{ state: "error"; error: unknown; onRetry: () => void }>
  | Readonly<{ state: "loaded"; project: ProjectEdit }>;

type ProjectWorkspaceOccurrence = Readonly<{
  occurrenceKey: string;
  workspace: WorkspaceCatalogRow;
}>;

function projectCatalogBoundary(
  catalog: ReturnType<typeof useInfiniteQuery>,
  t: ReturnType<typeof useTranslation>["t"],
): VirtualizedInfiniteListBoundaryState | undefined {
  if (catalog.isFetchingNextPage) {
    return { state: "loading", label: t("app.loadingMore") };
  }
  if (catalog.isFetchNextPageError || (catalog.isError && catalog.data === undefined)) {
    return {
      state: "error",
      message: errorMessage(catalog.error),
      retryLabel: t("app.retry"),
      onRetry: () => {
        if (catalog.data === undefined) {
          void catalog.refetch();
        } else {
          void catalog.fetchNextPage();
        }
      },
    };
  }
  return undefined;
}

function projectCatalogPreviousBoundary(
  catalog: ReturnType<typeof useInfiniteQuery>,
  t: ReturnType<typeof useTranslation>["t"],
): VirtualizedInfiniteListBoundaryState | undefined {
  if (catalog.isFetchingPreviousPage) {
    return { state: "loading", label: t("app.loadingMore") };
  }
  if (catalog.isFetchPreviousPageError) {
    return {
      state: "error",
      message: errorMessage(catalog.error),
      retryLabel: t("app.retry"),
      onRetry: () => {
        void catalog.fetchPreviousPage();
      },
    };
  }
  return undefined;
}

function workspaceUnlinkWindowOptions(target: WorkspaceUnlinkTarget, title: string) {
  return {
    initialHeight: 320,
    initialWidth: workspaceUnlinkDialogWidth,
    label: `workspace-unlink-${target.projectID}-${target.workspaceID}`,
    params: {
      projectID: target.projectID,
      rootPath: target.rootPath,
      workspaceID: target.workspaceID,
    },
    route: "/native-dialog/workspace-unlink",
    title,
  };
}
