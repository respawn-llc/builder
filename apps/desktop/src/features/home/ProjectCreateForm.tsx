import { type ChangeEvent } from "react";
import { useForm, type FieldErrors, type RegisterOptions, type UseFormRegisterReturn } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import { errorMessage } from "../../api/errors";
import { useAppNavigation } from "../../app/navigation";
import { useAppServices } from "../../app/useAppServices";
import { useStatusController } from "../../app/useStatusController";
import { Button, Dialog, NativeDialogWindow, TextInput } from "../../ui";
import { cx } from "../../ui/classes";
import { useProjectCreation } from "./useHomeData";

const LOCAL_UNBOUND_PLAN_KIND = "local_unbound";
const errorMessageSchema = z.string();

export type ProjectDraft = Readonly<{
  name: string;
  key: string;
  workspaceRoot: string;
}>;

export type ProjectCreateDialogProps = Readonly<{
  creationError: Error | null;
  draft: ProjectDraft;
  isCreating: boolean;
  onClose: () => void;
  onSubmitDraft: (values: ProjectDraft) => void;
}>;

export function ProjectCreateDialog({
  creationError,
  draft,
  isCreating,
  onClose,
  onSubmitDraft,
}: ProjectCreateDialogProps) {
  const { t } = useTranslation();

  return (
    <Dialog
      className="w-[min(560px,calc(100vw-32px))]"
      closeLabel={t("app.close")}
      onClose={onClose}
      open
      title={t("home.createProject")}
    >
      <ProjectCreateForm
        creationError={creationError}
        draft={draft}
        isCreating={isCreating}
        onSubmitDraft={onSubmitDraft}
      />
    </Dialog>
  );
}

export function ProjectCreateWindowRoute({ draft }: Readonly<{ draft: ProjectDraft }>) {
  const { t } = useTranslation();
  const creation = useProjectCreation();
  const { api, nativeBridge } = useAppServices();
  const navigation = useAppNavigation();
  const { push } = useStatusController();

  async function submitDraft(values: ProjectDraft): Promise<void> {
    try {
      const plan = await api.planWorkspace(values.workspaceRoot);
      if (plan.binding !== null) {
        await nativeBridge.projectCreation.notifyCreated({ projectID: plan.binding.projectID });
        await nativeBridge.window.closeCurrent();
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
      const binding = await creation.mutateAsync({
        name: values.name.trim(),
        key: values.key.trim().toUpperCase(),
        workspaceRoot: values.workspaceRoot,
      });
      await nativeBridge.projectCreation.notifyCreated({ projectID: binding.projectID });
      void navigation.openProject(binding.projectID);
      await nativeBridge.window.closeCurrent();
    } catch (error) {
      push({
        id: "project-create-submit-error",
        tone: "danger",
        title: t("home.workspacePlanError"),
        body: errorMessage(error),
      });
    }
  }

  return (
    <NativeDialogWindow title={t("home.createProject")}>
      <ProjectCreateForm
        className="w-[520px]"
        creationError={creation.error}
        draft={draft}
        isCreating={creation.isPending}
        onSubmitDraft={(values) => void submitDraft(values)}
      />
    </NativeDialogWindow>
  );
}

function ProjectCreateForm({
  className,
  creationError,
  draft,
  isCreating,
  onSubmitDraft,
}: Readonly<{
  className?: string;
  creationError: Error | null;
  draft: ProjectDraft;
  isCreating: boolean;
  onSubmitDraft: (values: ProjectDraft) => void;
}>) {
  const { t } = useTranslation();
  const form = useForm<ProjectDraft>({
    criteriaMode: "all",
    defaultValues: draft,
    mode: "onChange",
  });
  const nameField = form.register("name", projectNameRules(t));
  const keyField = form.register("key", projectKeyRules(t));

  return (
    <form
      className={cx("grid gap-[var(--space-3)]", className)}
      onSubmit={(event) => void form.handleSubmit(onSubmitDraft)(event)}
    >
      <TextInput
        error={fieldErrorMessages(form.formState.errors, "name")}
        label={t("home.projectName")}
        {...nameField}
      />
      <TextInput
        error={fieldErrorMessages(form.formState.errors, "key")}
        label={t("home.projectKey")}
        {...keyField}
        onChange={(event) => {
          handleUppercaseProjectKeyChange(event, keyField);
        }}
      />
      <TextInput label={t("home.workspaceRoot")} readOnly {...form.register("workspaceRoot")} />
      {creationError !== null ? (
        <p className="m-0 text-[var(--color-error)]">{projectCreateErrorMessage(creationError, draft)}</p>
      ) : null}
      <Button disabled={isCreating} type="submit" variant="primary">
        {t("home.createProject")}
      </Button>
    </form>
  );
}

function projectCreateErrorMessage(error: unknown, draft: ProjectDraft): string {
  const message = errorMessage(error);
  return `${message} (workspace: ${draft.workspaceRoot}; key: ${draft.key})`;
}

function projectNameRules(t: ReturnType<typeof useTranslation>["t"]): RegisterOptions<ProjectDraft, "name"> {
  return {
    validate: {
      length: (value) => {
        const length = value.trim().length;
        return length >= 1 && length <= 80 ? true : t("form.projectNameLength");
      },
      whitespace: (value) => (value === value.trim() ? true : t("form.noEdgeWhitespace")),
      singleLine: (value) => (hasLineBreak(value) ? t("form.singleLine") : true),
    },
  };
}

function projectKeyRules(t: ReturnType<typeof useTranslation>["t"]): RegisterOptions<ProjectDraft, "key"> {
  return {
    validate: {
      length: (value) => (value.length >= 2 && value.length <= 8 ? true : t("form.projectKeyLength")),
      whitespace: (value) => (hasWhitespace(value) ? t("form.noWhitespace") : true),
      firstSymbol: (value) =>
        isAsciiUppercaseLetter(value.at(0) ?? "") ? true : t("form.projectKeyStartsWithLetter"),
      permittedSymbols: (value) =>
        hasOnlyAsciiUppercaseLettersAndDigits(value) ? true : t("form.projectKeySymbols"),
    },
  };
}

function handleUppercaseProjectKeyChange(
  event: ChangeEvent<HTMLInputElement>,
  keyField: UseFormRegisterReturn<"key">,
): void {
  event.target.value = event.target.value.toUpperCase();
  void keyField.onChange(event);
}

function fieldErrorMessages(
  fieldErrors: FieldErrors<ProjectDraft>,
  field: keyof ProjectDraft,
): readonly string[] | undefined {
  const error = fieldErrors[field];
  if (error === undefined) {
    return undefined;
  }
  const typedMessages = error.types === undefined ? [] : Object.values(error.types).flatMap(errorMessageItem);
  if (typedMessages.length > 0) {
    return typedMessages;
  }
  return error.message === undefined ? undefined : [error.message];
}

function hasLineBreak(value: string): boolean {
  for (const char of value) {
    if (char === "\n" || char === "\r") {
      return true;
    }
  }
  return false;
}

function hasWhitespace(value: string): boolean {
  for (const char of value) {
    if (char.trim().length === 0) {
      return true;
    }
  }
  return false;
}

function hasOnlyAsciiUppercaseLettersAndDigits(value: string): boolean {
  for (const char of value) {
    if (!isAsciiUppercaseLetter(char) && !isAsciiDigit(char)) {
      return false;
    }
  }
  return true;
}

function isAsciiUppercaseLetter(value: string): boolean {
  if (value.length !== 1) {
    return false;
  }
  const code = value.charCodeAt(0);
  return code >= 65 && code <= 90;
}

function isAsciiDigit(value: string): boolean {
  if (value.length !== 1) {
    return false;
  }
  const code = value.charCodeAt(0);
  return code >= 48 && code <= 57;
}

function errorMessageItem(value: unknown): readonly string[] {
  const parsed = errorMessageSchema.safeParse(value);
  return parsed.success && parsed.data.length > 0 ? [parsed.data] : [];
}
