import { zodResolver } from "@hookform/resolvers/zod";
import { Plus } from "lucide-react";
import { useCallback, useEffect, useId, useMemo, useReducer, useRef, useState } from "react";
import { useForm, useWatch } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import {
  errorMessage,
  isProjectMissingError,
  type ApiService,
  type TaskDependencyCreateIntent,
  type TaskMutationInput,
  type WorkspaceCatalogRow,
} from "@/api";
import {
  useConnectionSnapshot,
  projectWorkspaceQueryOptions,
  queryKeys,
  useTextFieldSubmitShortcut,
  workspaceCatalogInfiniteQueryOptions,
} from "@/app-facade";
import { useAppServices } from "@/app-facade";
import { useStatusController } from "@/app-facade";
import {
  LabelChooser,
  ProjectLabelsProvider,
  orderedAssignedLabels,
  useProjectLabelCatalog,
} from "@/shared/labels";
import { NativeDialogWindow } from "@/shared/native-dialog";
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
  Dialog,
  FieldShell,
  InfiniteListBoundary,
  SelectField,
  TextArea,
  TextInput,
  type SelectFieldPaging,
} from "@/ui";
import { cx } from "@/ui";

const newTaskContentMaxWidth = "560px";

const newTaskSchema = z.object({
  title: z.string().trim().min(1),
  body: z.string(),
  sourceWorkspaceID: z.string().trim().min(1).optional(),
});

type NewTaskFormValues = z.output<typeof newTaskSchema>;

type NewTaskRelationship = Readonly<{
  originTaskID: string;
  newTaskRole: TaskDependencyCreateIntent["newTaskRole"];
}>;

type NewTaskWorkflowScope =
  | Readonly<{
      workflowID: string;
      pendingRelationship?: NewTaskRelationship | undefined;
    }>
  | Readonly<{
      workflowID?: undefined;
      pendingRelationship?: undefined;
    }>;

type NewTaskFormProps = Readonly<{
  boardQueryWorkflowID: string | undefined;
  className?: string;
  onSubmitted: (taskID: string) => void;
  onPendingChange?: ((pending: boolean) => void) | undefined;
  onProjectMissing?: (() => void) | undefined;
  projectID: string;
  initialSourceWorkspaceID?: string | undefined;
}> &
  NewTaskWorkflowScope;

export type NewTaskFallbackDialogProps = Readonly<{
  boardQueryWorkflowID: string | undefined;
  projectID: string;
  workflowID: string;
  onClose: () => void;
}>;

export function NewTaskFallbackDialog({
  boardQueryWorkflowID,
  projectID,
  workflowID,
  onClose,
}: NewTaskFallbackDialogProps) {
  const { t } = useTranslation();

  return (
    <Dialog
      className="w-[min(calc(560px+var(--space-4)*2),calc(100vw-32px))]"
      closeLabel={t("app.close")}
      onClose={onClose}
      open
      title={t("task.newTitle")}
    >
      <NewTaskForm
        boardQueryWorkflowID={boardQueryWorkflowID}
        className="mx-auto w-full max-w-[560px]"
        onSubmitted={onClose}
        projectID={projectID}
        workflowID={workflowID}
      />
    </Dialog>
  );
}

export function NewTaskWindowRoute({
  projectID,
  workflowID,
}: Readonly<{
  projectID: string;
  workflowID: string;
}>) {
  const { t } = useTranslation();
  const { nativeBridge } = useAppServices();

  return (
    <NativeDialogWindow
      contentMaxWidth={newTaskContentMaxWidth}
      fitToContent={false}
      title={t("task.newTitle")}
    >
      <NewTaskForm
        boardQueryWorkflowID={workflowID}
        className="w-full"
        onSubmitted={() => {
          void nativeBridge.window.closeCurrent();
        }}
        projectID={projectID}
        workflowID={workflowID}
      />
    </NativeDialogWindow>
  );
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
  onSubmitted,
  onPendingChange,
  onProjectMissing,
  initialSourceWorkspaceID,
  pendingRelationship,
  projectID,
  workflowID,
}: Readonly<{
  boardQueryWorkflowID: string | undefined;
  className?: string;
  onSubmitted: (taskID: string) => void;
  onPendingChange?: ((pending: boolean) => void) | undefined;
  onProjectMissing?: (() => void) | undefined;
  projectID: string;
  workflowID?: string | undefined;
  initialSourceWorkspaceID?: string | undefined;
  pendingRelationship?: NewTaskRelationship | undefined;
}>) {
  const { t } = useTranslation();
  const { api } = useAppServices();
  const connection = useConnectionSnapshot();
  const workspaceCatalog = useNewTaskWorkspaceCatalog(api, projectID, initialSourceWorkspaceID, t);
  const catalog = useProjectLabelCatalog();
  const createTask = useCreateTask(projectID, boardQueryWorkflowID, workflowID);
  useEffect(() => onPendingChange?.(createTask.isPending), [createTask.isPending, onPendingChange]);
  const projectMissing = [
    workspaceCatalog.workspaces.error,
    workspaceCatalog.initiatingWorkspace.error,
    createTask.error,
  ].some(isProjectMissingError);
  useEffect(() => {
    if (projectMissing) onProjectMissing?.();
  }, [onProjectMissing, projectMissing]);
  const [selectedLabelIDs, setSelectedLabelIDs] = useState<readonly string[]>([]);
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
    defaultValues: {
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
  const canSubmit =
    connection.phase === "connected" &&
    !createTask.isPending &&
    !labelCreatePending &&
    selectedWorkspace !== undefined;
  async function submit(values: NewTaskFormValues): Promise<void> {
    if (!canSubmit) {
      return;
    }
    const sourceWorkspaceID = values.sourceWorkspaceID;
    if (sourceWorkspaceID === undefined) {
      throw new Error("New Task submission requires a source Workspace.");
    }
    const availableLabelIDs = new Set(catalog.data?.labels.map((label) => label.id) ?? []);
    const fields = {
      projectID,
      title: values.title,
      body: values.body,
      sourceWorkspaceID,
      labelIDs: effectiveSelectedLabelIDs.filter((labelID) => availableLabelIDs.has(labelID)),
    };
    const dependencyIntent =
      pendingRelationship === undefined
        ? undefined
        : {
            relatedTaskID: pendingRelationship.originTaskID,
            newTaskRole: pendingRelationship.newTaskRole,
          };
    const input =
      workflowID === undefined
        ? projectScopedTaskMutation(fields, pendingRelationship)
        : {
            ...fields,
            workflowID,
            ...(dependencyIntent === undefined ? {} : { dependencyIntent }),
          };
    try {
      const taskID = await createTask.mutateAsync(input);
      onSubmitted(taskID);
    } catch {
      // The mutation state renders the persistent failure without clearing form input.
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
      {createTask.error !== null ? (
        <p className="m-0 text-[var(--color-error)]">{errorMessage(createTask.error)}</p>
      ) : null}
      <Button className="mx-auto w-full max-w-[400px]" disabled={!canSubmit} type="submit" variant="primary">
        {t("task.create")}
      </Button>
    </form>
  );
}

function projectScopedTaskMutation(
  fields: Omit<TaskMutationInput, "workflowID" | "dependencyIntent">,
  pendingRelationship: NewTaskRelationship | undefined,
): TaskMutationInput {
  if (pendingRelationship !== undefined) {
    throw new Error("Related Task creation requires an explicit Workflow.");
  }
  return fields;
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
