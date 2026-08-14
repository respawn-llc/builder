import { zodResolver } from "@hookform/resolvers/zod";
import { Plus } from "lucide-react";
import { useCallback, useEffect, useId, useMemo, useReducer, useRef, useState } from "react";
import { useForm, useWatch } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import {
  decodeWorkflowTaskDependencyError,
  errorMessage,
  isProjectMissingError,
  type ApiService,
  type WorkspaceCatalogRow,
} from "@/api";
import {
  projectWorkspaceQueryOptions,
  queryKeys,
  useAppServices,
  useConnectionSnapshot,
  useStatusController,
  useTextFieldSubmitShortcut,
  workspaceCatalogInfiniteQueryOptions,
  type NewTaskPreparedDependency,
  type SidebarPageNavigator,
} from "@/app-facade";
import {
  LabelChooser,
  ProjectLabelsProvider,
  orderedAssignedLabels,
  useProjectLabelCatalog,
} from "@/shared/labels";
import {
  DependenciesArea,
  insertPreparedTaskDependency,
  preparedTaskDependenciesProjection,
  removePreparedTaskDependency,
  type PreparedTaskDependency,
} from "@/shared/task-dependencies";
import { useCreateTask } from "@/shared/task-mutations";
import {
  projectWorkspaceSelectorProjection,
  updateWorkspaceSelection,
  type WorkspaceSelectionState,
} from "@/shared/workspaces";
import { useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Badge,
  Button,
  FieldShell,
  InfiniteListBoundary,
  SelectField,
  TextArea,
  TextInput,
  type SelectFieldPaging,
} from "@/ui";
import { cx } from "@/ui";

const newTaskSchema = z.object({
  title: z.string().trim().min(1),
  body: z.string(),
  sourceWorkspaceID: z.string().trim().min(1).optional(),
});

type NewTaskFormValues = z.output<typeof newTaskSchema>;

const retainedStatusSchema = z.object({
  kind: z.enum([
    "done",
    "waiting_question",
    "waiting_approval",
    "interrupted",
    "running",
    "queued",
    "backlog",
    "active",
  ]),
  nativeState: z.string(),
  nodeIDs: z.array(z.string()),
  attentionTypes: z.array(z.string()),
});

const preparedDependencySchema = z.object({
  direction: z.enum(["blocked-by", "blocks"]),
  taskID: z.string().min(1),
  shortID: z.string().min(1),
  title: z.string(),
  workflowID: z.string().min(1),
  status: retainedStatusSchema,
});

const newTaskRetainedStateSchema = z.object({
  formValues: z.object({
    title: z.string(),
    body: z.string(),
    sourceWorkspaceID: z.string().trim().min(1).optional(),
  }),
  selectedLabelIDs: z.array(z.string().min(1)),
  preparedDependencies: z.array(preparedDependencySchema),
});

type Translate = ReturnType<typeof useTranslation>["t"];
type Logger = ReturnType<typeof useAppServices>["logger"];

function newTaskCreateErrorBody(error: unknown, t: Translate, logger: Logger): string {
  const dependencyError = decodeWorkflowTaskDependencyError(error);
  if (dependencyError === null) return errorMessage(error);
  const reason = dependencyError.reason;
  void logger.append("warn", "Dependency rejected.", { error: errorMessage(error), reason });
  return t("task.dependenciesRejected");
}

export type NewTaskRetainedState = Readonly<{
  formValues: NewTaskFormValues;
  selectedLabelIDs: readonly string[];
  preparedDependencies: readonly PreparedTaskDependency[];
}>;

type NewTaskFormProps = Readonly<{
  boardQueryWorkflowID: string | undefined;
  className?: string | undefined;
  initialPreparedDependency?: NewTaskPreparedDependency | undefined;
  initialSourceWorkspaceID?: string | undefined;
  navigator: SidebarPageNavigator;
  onCreated?: ((taskID: string) => void | Promise<void>) | undefined;
  onPendingChange?: ((pending: boolean) => void) | undefined;
  parentReturnDirection?: "blocked-by" | "blocks" | undefined;
  projectID: string;
  retainedState?: unknown;
  workflowID?: string | undefined;
}>;

export function decodeNewTaskRetainedState(value: unknown): NewTaskRetainedState | undefined {
  const parsed = newTaskRetainedStateSchema.safeParse(value);
  return parsed.success ? parsed.data : undefined;
}

export function NewTaskForm(props: NewTaskFormProps) {
  const { projectID } = props;
  const { t } = useTranslation();
  const { push } = useStatusController();
  const reportLabelError = useCallback(
    (error: unknown) => {
      push({
        body: errorMessage(error),
        durationMs: Infinity,
        id: "new-task-label-load-error",
        title: t("labels.loadFailed"),
        tone: "danger",
      });
    },
    [push, t],
  );
  return (
    <ProjectLabelsProvider onBackgroundError={reportLabelError} projectID={projectID}>
      <NewTaskFormContent {...props} />
    </ProjectLabelsProvider>
  );
}

function NewTaskFormContent({
  boardQueryWorkflowID,
  className,
  initialPreparedDependency,
  initialSourceWorkspaceID,
  navigator,
  onCreated,
  onPendingChange,
  parentReturnDirection,
  projectID,
  retainedState,
  workflowID,
}: NewTaskFormProps) {
  const { t } = useTranslation();
  const { api, logger } = useAppServices();
  const { dismiss, push } = useStatusController();
  const connection = useConnectionSnapshot();
  const restored = useMemo(() => decodeNewTaskRetainedState(retainedState), [retainedState]);
  const authoredSourceWorkspaceID = restored?.formValues.sourceWorkspaceID ?? initialSourceWorkspaceID;
  const workspaceCatalog = useNewTaskWorkspaceCatalog(api, projectID, authoredSourceWorkspaceID, t);
  const catalog = useProjectLabelCatalog();
  const createTask = useCreateTask(projectID, boardQueryWorkflowID, workflowID);
  useEffect(() => onPendingChange?.(createTask.isPending), [createTask.isPending, onPendingChange]);
  const workspaceProjectMissing = [
    workspaceCatalog.workspaces.error,
    workspaceCatalog.initiatingWorkspace.error,
  ].some(isProjectMissingError);
  useEffect(() => {
    if (workspaceProjectMissing) navigator.back();
  }, [navigator, workspaceProjectMissing]);
  const [selectedLabelIDs, setSelectedLabelIDs] = useState<readonly string[]>(
    () => restored?.selectedLabelIDs ?? [],
  );
  const [preparedDependencies, setPreparedDependencies] = useState<readonly PreparedTaskDependency[]>(
    () =>
      restored?.preparedDependencies ??
      (initialPreparedDependency === undefined ? [] : [initialPreparedDependency]),
  );
  const [labelCreatePending, setLabelCreatePending] = useState(false);
  const effectiveSelectedLabelIDs = useMemo(() => {
    if (catalog.data === undefined) {
      return selectedLabelIDs;
    }
    const availableLabelIDs = new Set(catalog.data.labels.map((label) => label.id));
    return selectedLabelIDs.filter((labelID) => availableLabelIDs.has(labelID));
  }, [catalog.data, selectedLabelIDs]);
  const { selectedWorkspace, workspaceItems, workspaceProjection, workspaceSelection } = workspaceCatalog;
  const form = useForm<NewTaskFormValues>({
    resolver: zodResolver(newTaskSchema),
    defaultValues: restored?.formValues ?? {
      title: "",
      body: "",
      sourceWorkspaceID: undefined,
    },
  });
  const selectedWorkspaceID = useWatch({ control: form.control, name: "sourceWorkspaceID" });
  useEffect(() => {
    if (selectedWorkspace !== undefined && selectedWorkspaceID !== selectedWorkspace.id) {
      form.setValue("sourceWorkspaceID", selectedWorkspace.id, {
        shouldDirty: false,
        shouldTouch: false,
        shouldValidate: true,
      });
    }
  }, [form, selectedWorkspace, selectedWorkspaceID]);
  useEffect(
    () =>
      navigator.registerCapture((): NewTaskRetainedState => ({
        formValues: form.getValues(),
        preparedDependencies,
        selectedLabelIDs: effectiveSelectedLabelIDs,
      })),
    [effectiveSelectedLabelIDs, form, navigator, preparedDependencies],
  );
  const canSubmit = [
    connection.phase === "connected",
    !createTask.isPending,
    !labelCreatePending,
    selectedWorkspace !== undefined,
  ].every(Boolean);
  async function submit(values: NewTaskFormValues): Promise<void> {
    if (!canSubmit) {
      return;
    }
    const sourceWorkspaceID = values.sourceWorkspaceID;
    if (sourceWorkspaceID === undefined) {
      throw new Error("New Task submission requires a source Workspace.");
    }
    const availableLabelIDs = new Set(catalog.data?.labels.map((label) => label.id) ?? []);
    dismiss("new-task-create-error");
    try {
      const createdTask = await createTask.mutateAsync({
        projectID,
        ...(workflowID === undefined ? {} : { workflowID }),
        title: values.title,
        body: values.body,
        sourceWorkspaceID,
        labelIDs: effectiveSelectedLabelIDs.filter((labelID) => availableLabelIDs.has(labelID)),
        dependencyIntents: preparedDependencies.map((dependency) => ({
          relatedTaskID: dependency.taskID,
          newTaskRole: dependency.direction === "blocked-by" ? "blocked" : "blocker",
        })),
      });
      const navigation = navigator.back(
        parentReturnDirection === undefined
          ? undefined
          : {
              kind: "newTaskCreated",
              direction: parentReturnDirection,
              task: {
                ...createdTask,
                status: {
                  kind: "backlog",
                  nativeState: "active",
                  nodeIDs: [],
                  attentionTypes: [],
                },
              },
            },
      );
      if (navigation === "accepted") void onCreated?.(createdTask.id);
    } catch (error) {
      push({
        body: newTaskCreateErrorBody(error, t, logger),
        durationMs: Infinity,
        id: "new-task-create-error",
        title: t("task.createFailed"),
        tone: "danger",
      });
    }
  }

  const workspaceOptions = useMemo(
    () => workspaceItems.map((workspace) => ({ label: workspace.name, value: workspace.id })),
    [workspaceItems],
  );
  const workspacePaging = workspaceCatalog.workspacePaging;
  const formShortcut = useTextFieldSubmitShortcut({
    available: canSubmit,
    kind: "form",
  });

  return (
    <form
      className={cx("grid gap-[var(--space-3)]", className)}
      onKeyDown={formShortcut}
      onSubmit={(event) => void form.handleSubmit(submit)(event)}
    >
      <TextInput
        error={form.formState.errors.title !== undefined ? t("form.required") : undefined}
        label={t("task.name")}
        {...form.register("title")}
      />
      <TextArea
        label={t("task.body")}
        placeholder={t("task.bodyPlaceholder")}
        rows={6}
        {...form.register("body")}
      />
      <NewTaskLabels
        disabled={connection.phase !== "connected"}
        onCreatePendingChange={setLabelCreatePending}
        onSelectionChange={(labelID, selected) => {
          setSelectedLabelIDs((current) => {
            if (selected) {
              return current.includes(labelID) ? current : [...current, labelID];
            }
            return current.filter((candidate) => candidate !== labelID);
          });
        }}
        selectedLabelIDs={effectiveSelectedLabelIDs}
      />
      <DependenciesArea
        dependencies={preparedTaskDependenciesProjection(preparedDependencies)}
        disabled={connection.phase !== "connected"}
        excludedTaskIDs={(direction) =>
          new Set(
            preparedDependencies
              .filter((dependency) => dependency.direction === direction)
              .map((dependency) => dependency.taskID),
          )
        }
        navigationDisabled={connection.phase !== "connected"}
        onAdd={(direction) => {
          const destination = {
            ...(selectedWorkspace === undefined ? {} : { initialSourceWorkspaceID: selectedWorkspace.id }),
            kind: "newTask" as const,
            parentReturnDirection: direction,
            projectID,
          };
          navigator.push(
            workflowID === undefined
              ? { ...destination, boardQueryWorkflowID: undefined }
              : { ...destination, boardQueryWorkflowID, workflowID },
          );
        }}
        onRemove={(direction, item) => {
          setPreparedDependencies((current) => removePreparedTaskDependency(current, direction, item.taskID));
        }}
        onSelectCandidate={async (direction, result) => {
          setPreparedDependencies((current) =>
            insertPreparedTaskDependency(current, {
              direction,
              taskID: result.group.taskID,
              shortID: result.group.shortID,
              title: result.group.title,
              workflowID: result.group.workflowID,
              status: result.group.status,
            }),
          );
        }}
        onSelectTask={(taskID) => {
          navigator.push({ kind: "taskDetail", taskID });
        }}
        previewProgress
        projectID={projectID}
      />
      {workspaceProjection.selectionDisabled ? (
        <>
          <input type="hidden" {...form.register("sourceWorkspaceID")} />
          <SelectField
            disabled
            label={t("task.sourceWorkspace")}
            onValueChange={() => undefined}
            options={workspaceOptions}
            paging={workspacePaging}
            value={selectedWorkspaceID}
          />
        </>
      ) : (
        <SelectField
          disabled={workspaceItems.length === 0}
          label={t("task.sourceWorkspace")}
          name="sourceWorkspaceID"
          onValueChange={(value) => {
            const row = workspaceItems.find((workspace) => workspace.id === value);
            if (row === undefined) {
              throw new Error(`Selected Workspace ${value} is absent from the selector projection.`);
            }
            workspaceCatalog.selectWorkspace(row);
            form.setValue("sourceWorkspaceID", value, {
              shouldDirty: true,
              shouldTouch: true,
              shouldValidate: true,
            });
          }}
          options={workspaceOptions}
          paging={workspacePaging}
          value={selectedWorkspaceID}
        />
      )}
      {workspaceSelection.initiating?.state === "failed" ? (
        <InfiniteListBoundary
          direction="initial"
          state={{
            state: "error",
            message: errorMessage(workspaceSelection.initiating.error),
            retryLabel: t("app.retry"),
            onRetry: () => {
              workspaceCatalog.retryInitiatingWorkspace();
            },
          }}
        />
      ) : null}
      {workspaceItems.length > 0 && workspacePaging.initialBoundary !== undefined ? (
        <InfiniteListBoundary direction="initial" state={workspacePaging.initialBoundary} />
      ) : null}
      <Button className="mx-auto w-full max-w-[400px]" disabled={!canSubmit} type="submit" variant="primary">
        {t("task.create")}
      </Button>
    </form>
  );
}

function useNewTaskWorkspaceCatalog(
  api: ApiService,
  projectID: string,
  initialSourceWorkspaceID: string | undefined,
  t: ReturnType<typeof useTranslation>["t"],
) {
  const workspaces = useNewTaskWorkspaceQuery(api, projectID);
  const queryClient = useQueryClient();
  const initiatingWorkspace = useQuery(
    projectWorkspaceQueryOptions(api, projectID, initialSourceWorkspaceID),
  );
  const [workspaceSelection, dispatchWorkspaceSelection] = useReducer(
    updateWorkspaceSelection,
    initialSourceWorkspaceID,
    (workspaceID): WorkspaceSelectionState => ({
      catalog: { state: "pending" },
      initiating: workspaceID === undefined ? undefined : { state: "pending" },
      selection: { state: "uncommitted" },
    }),
  );
  const catalogRestartedRef = useRef(false);
  const restartedProjectRef = useRef<string | undefined>(undefined);
  const firstRetainedOffset = workspaces.data?.pages[0]?.offset;
  useEffect(() => {
    if (restartedProjectRef.current !== projectID) {
      restartedProjectRef.current = projectID;
      catalogRestartedRef.current = false;
    }
    if (firstRetainedOffset !== undefined && firstRetainedOffset > 0 && !catalogRestartedRef.current) {
      catalogRestartedRef.current = true;
      void queryClient.resetQueries({
        exact: true,
        queryKey: queryKeys.projectWorkspaceCatalog(projectID),
      });
    }
  }, [firstRetainedOffset, projectID, queryClient]);
  const defaultWorkspace = workspaces.data?.pages[0]?.workspaces.find((workspace) => workspace.isDefault);
  useEffect(() => {
    if (defaultWorkspace !== undefined) {
      dispatchWorkspaceSelection({ type: "catalog-loaded", defaultWorkspace });
    }
  }, [defaultWorkspace]);
  useEffect(() => {
    if (initialSourceWorkspaceID === undefined) return;
    if (initiatingWorkspace.data?.kind === "attached") {
      dispatchWorkspaceSelection({
        type: "initiating-attached",
        row: initiatingWorkspace.data.workspace,
      });
      return;
    }
    if (initiatingWorkspace.data?.kind === "not_attached") {
      dispatchWorkspaceSelection({ type: "initiating-not-attached" });
      return;
    }
    if (initiatingWorkspace.isError) {
      dispatchWorkspaceSelection({ type: "initiating-failed", error: initiatingWorkspace.error });
    }
  }, [
    initialSourceWorkspaceID,
    initiatingWorkspace.data,
    initiatingWorkspace.error,
    initiatingWorkspace.isError,
  ]);
  const selectedWorkspace =
    workspaceSelection.selection.state === "committed" ? workspaceSelection.selection.row : undefined;
  const workspaceProjection = useMemo(
    () =>
      projectWorkspaceSelectorProjection({
        catalogPages: workspaces.data?.pages ?? [],
        initiatingRow:
          workspaceSelection.initiating?.state === "attached" ? workspaceSelection.initiating.row : undefined,
        selectedSnapshot: selectedWorkspace,
        catalogExhausted: workspaces.data !== undefined && !workspaces.hasNextPage,
      }),
    [selectedWorkspace, workspaceSelection.initiating, workspaces.data, workspaces.hasNextPage],
  );
  const workspacePaging = workspacePagingState(workspaces, t);
  return {
    initiatingWorkspace,
    retryInitiatingWorkspace() {
      dispatchWorkspaceSelection({ type: "initiating-retry" });
      void initiatingWorkspace.refetch();
    },
    selectedWorkspace,
    selectWorkspace(row: WorkspaceCatalogRow) {
      dispatchWorkspaceSelection({ type: "user-selected", row });
    },
    workspaceItems: workspaceProjection.rows,
    workspacePaging,
    workspaceProjection,
    workspaceSelection,
    workspaces,
  };
}

function workspacePagingState(
  workspaces: ReturnType<typeof useNewTaskWorkspaceQuery>,
  t: ReturnType<typeof useTranslation>["t"],
): SelectFieldPaging {
  return {
    hasNextPage: workspaces.hasNextPage,
    initialBoundary: workspaces.isPending
      ? { state: "loading", label: t("states.loading") }
      : workspaces.isError && workspaces.data === undefined
        ? {
            state: "error",
            message: errorMessage(workspaces.error),
            retryLabel: t("app.retry"),
            onRetry: () => {
              void workspaces.refetch();
            },
          }
        : undefined,
    loadKey: workspaces.data?.pages.at(-1)?.nextOffset?.toString(),
    nextBoundary: workspaces.isFetchingNextPage
      ? { state: "loading", label: t("app.loadingMore") }
      : workspaces.isFetchNextPageError
        ? {
            state: "error",
            message: errorMessage(workspaces.error),
            retryLabel: t("app.retry"),
            onRetry: () => {
              void workspaces.fetchNextPage();
            },
          }
        : undefined,
    onLoadNext: () => {
      void workspaces.fetchNextPage();
    },
  };
}

function useNewTaskWorkspaceQuery(api: ApiService, projectID: string) {
  return useInfiniteQuery(workspaceCatalogInfiniteQueryOptions(api, projectID));
}

function NewTaskLabels({
  disabled,
  onCreatePendingChange,
  onSelectionChange,
  selectedLabelIDs,
}: Readonly<{
  disabled: boolean;
  onCreatePendingChange(pending: boolean): void;
  onSelectionChange(labelID: string, selected: boolean): void;
  selectedLabelIDs: readonly string[];
}>) {
  const { t } = useTranslation();
  const catalog = useProjectLabelCatalog();
  const inputID = useId();
  const labels = catalog.data === undefined ? [] : orderedAssignedLabels(catalog.data, selectedLabelIDs);
  return (
    <FieldShell
      errorId={`${inputID}-error`}
      hintId={`${inputID}-hint`}
      inputId={inputID}
      label={t("labels.filter")}
    >
      <LabelChooser
        invocation={{
          kind: "assignment",
          onCreatePendingChange,
          selectedLabelIDs,
          onSelectionChange,
        }}
        trigger={
          <Button
            aria-label={t("labels.editAssignments")}
            className="min-h-11 h-auto w-full min-w-0 justify-start text-left"
            disabled={disabled}
            id={inputID}
            variant="secondary"
          >
            <span className="flex min-w-0 flex-wrap items-center gap-[var(--space-1)]">
              {labels.length === 0 ? (
                <span className="inline-flex items-center gap-[var(--space-1)] text-[var(--color-muted)]">
                  <Plus aria-hidden="true" size={14} />
                  {t("labels.add")}
                </span>
              ) : null}
              {labels.map((label) => (
                <Badge key={label.id} tone="neutral">
                  {label.name}
                </Badge>
              ))}
            </span>
          </Button>
        }
      />
    </FieldShell>
  );
}
