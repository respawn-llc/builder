import { zodResolver } from "@hookform/resolvers/zod";
import { Plus } from "lucide-react";
import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import { useForm, useWatch } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import { errorMessage, type TaskDependencyCreateIntent } from "@/api";
import { useConnectionSnapshot, useTextFieldSubmitShortcut } from "@/app-facade";
import { useAppServices } from "@/app-facade";
import { useStatusController } from "@/app-facade";
import { LabelChooser, ProjectLabelsProvider, useProjectLabelCatalog } from "@/shared/labels";
import { NativeDialogWindow } from "@/shared/native-dialog";
import { useCreateTask, useWorkspaces } from "@/shared/task-mutations";
import { Badge, Button, Dialog, FieldShell, SelectField, TextArea, TextInput } from "@/ui";
import { cx } from "@/ui";

const newTaskContentMaxWidth = "560px";

const newTaskSchema = z.object({
  title: z.string().trim().min(1),
  body: z.string(),
  sourceWorkspaceID: z.string(),
});

type NewTaskFormValues = z.output<typeof newTaskSchema>;

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

export function NewTaskForm({
  projectID,
  initialSourceWorkspaceID,
  pendingRelationship,
  ...props
}: Readonly<{
  boardQueryWorkflowID: string | undefined;
  className?: string;
  onSubmitted: (taskID?: string) => void;
  onSubmissionStateChange?: ((pending: boolean) => void) | undefined;
  projectID: string;
  workflowID: string;
  initialSourceWorkspaceID?: string | undefined;
  pendingRelationship?:
    | Readonly<{
        originTaskID: string;
        newTaskRole: TaskDependencyCreateIntent["newTaskRole"];
      }>
    | undefined;
}>) {
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
      <NewTaskFormContent
        initialSourceWorkspaceID={initialSourceWorkspaceID}
        pendingRelationship={pendingRelationship}
        projectID={projectID}
        {...props}
      />
    </ProjectLabelsProvider>
  );
}

function NewTaskFormContent({
  boardQueryWorkflowID,
  className,
  onSubmitted,
  onSubmissionStateChange,
  initialSourceWorkspaceID,
  pendingRelationship,
  projectID,
  workflowID,
}: Readonly<{
  boardQueryWorkflowID: string | undefined;
  className?: string;
  onSubmitted: (taskID?: string) => void;
  onSubmissionStateChange?: ((pending: boolean) => void) | undefined;
  projectID: string;
  workflowID: string;
  initialSourceWorkspaceID?: string | undefined;
  pendingRelationship?:
    | Readonly<{
        originTaskID: string;
        newTaskRole: TaskDependencyCreateIntent["newTaskRole"];
      }>
    | undefined;
}>) {
  const { t } = useTranslation();
  const connection = useConnectionSnapshot();
  const workspaces = useWorkspaces(projectID);
  const catalog = useProjectLabelCatalog();
  const createTask = useCreateTask(projectID, boardQueryWorkflowID, workflowID);
  const [selectedLabelIDs, setSelectedLabelIDs] = useState<readonly string[]>([]);
  const effectiveSelectedLabelIDs = useMemo(() => {
    if (catalog.data === undefined) {
      return selectedLabelIDs;
    }
    const availableLabelIDs = new Set(catalog.data.labels.map((label) => label.id));
    return selectedLabelIDs.filter((labelID) => availableLabelIDs.has(labelID));
  }, [catalog.data, selectedLabelIDs]);
  const defaultWorkspaceID = workspaces.data?.defaultWorkspaceID;
  const workspaceItems = useMemo(() => workspaces.data?.workspaces ?? [], [workspaces.data?.workspaces]);
  const initialWorkspaceID = resolveInitialSourceWorkspaceID(
    initialSourceWorkspaceID,
    defaultWorkspaceID,
    workspaceItems,
  );
  const initializedRef = useRef(false);
  const form = useForm<NewTaskFormValues>({
    resolver: zodResolver(newTaskSchema),
    defaultValues: {
      title: "",
      body: "",
      sourceWorkspaceID: initialWorkspaceID ?? "",
    },
  });
  const canSubmit =
    connection.phase === "connected" && !createTask.isPending && initialWorkspaceID !== undefined;

  useEffect(() => {
    onSubmissionStateChange?.(createTask.isPending);
  }, [createTask.isPending, onSubmissionStateChange]);

  useEffect(() => {
    if (!initializedRef.current && initialWorkspaceID !== undefined) {
      form.reset({ title: "", body: "", sourceWorkspaceID: initialWorkspaceID });
      initializedRef.current = true;
    }
  }, [form, initialWorkspaceID]);
  async function submit(values: NewTaskFormValues): Promise<void> {
    if (!canSubmit) {
      return;
    }
    const sourceWorkspaceID = values.sourceWorkspaceID.trim() || initialWorkspaceID;
    const availableLabelIDs = new Set(catalog.data?.labels.map((label) => label.id) ?? []);
    try {
      const createdTaskID = await createTask.mutateAsync({
        projectID,
        workflowID,
        title: values.title,
        body: values.body,
        sourceWorkspaceID,
        labelIDs: effectiveSelectedLabelIDs.filter((labelID) => availableLabelIDs.has(labelID)),
        dependencyIntent:
          pendingRelationship === undefined
            ? undefined
            : {
                relatedTaskID: pendingRelationship.originTaskID,
                newTaskRole: pendingRelationship.newTaskRole,
              },
      });
      onSubmitted(createdTaskID);
    } catch {
      // The mutation state renders the persistent failure without clearing form input.
    }
  }

  const workspaceOptions = useMemo(
    () => workspaceItems.map((workspace) => ({ label: workspace.name, value: workspace.id })),
    [workspaceItems],
  );
  const selectedWorkspaceID = useWatch({ control: form.control, name: "sourceWorkspaceID" });
  const displayedWorkspaceID =
    selectedWorkspaceID.trim().length > 0 ? selectedWorkspaceID : (initialWorkspaceID ?? "");
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
      {workspaceItems.length === 1 ? (
        <>
          <input type="hidden" {...form.register("sourceWorkspaceID")} />
          <SelectField
            disabled
            disabledReason={t("task.onlyOneWorkspaceLinked")}
            label={t("task.sourceWorkspace")}
            onValueChange={() => undefined}
            options={workspaceOptions}
            value={displayedWorkspaceID}
          />
        </>
      ) : (
        <SelectField
          disabled={workspaceItems.length <= 1}
          label={t("task.sourceWorkspace")}
          name="sourceWorkspaceID"
          onValueChange={(value) => {
            form.setValue("sourceWorkspaceID", value, {
              shouldDirty: true,
              shouldTouch: true,
              shouldValidate: true,
            });
          }}
          options={workspaceOptions}
          value={selectedWorkspaceID}
        />
      )}
      {createTask.error !== null ? (
        <p className="m-0 text-[var(--color-error)]">{errorMessage(createTask.error)}</p>
      ) : null}
      <Button className="mx-auto w-full max-w-[400px]" disabled={!canSubmit} type="submit" variant="primary">
        {t("task.create")}
      </Button>
    </form>
  );
}

function NewTaskLabels({
  disabled,
  onSelectionChange,
  selectedLabelIDs,
}: Readonly<{
  disabled: boolean;
  onSelectionChange(labelID: string, selected: boolean): void;
  selectedLabelIDs: readonly string[];
}>) {
  const { t } = useTranslation();
  const catalog = useProjectLabelCatalog();
  const inputID = useId();
  const selected = new Set(selectedLabelIDs);
  const labels = catalog.data?.labels.filter((label) => selected.has(label.id)) ?? [];
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

function resolveInitialSourceWorkspaceID(
  requestedWorkspaceID: string | undefined,
  defaultWorkspaceID: string | undefined,
  workspaceItems: readonly { id: string }[],
): string | undefined {
  if (
    requestedWorkspaceID !== undefined &&
    workspaceItems.some((workspace) => workspace.id === requestedWorkspaceID)
  ) {
    return requestedWorkspaceID;
  }
  if (defaultWorkspaceID !== undefined) {
    return defaultWorkspaceID;
  }
  return workspaceItems[0]?.id;
}
