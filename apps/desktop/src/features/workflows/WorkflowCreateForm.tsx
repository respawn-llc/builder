import { useState, type SyntheticEvent } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import type { ProjectWorkflowLink, WorkflowRecord } from "@/api";
import { errorMessage } from "@/api";
import { queryKeys } from "@/app-facade";
import { useAppServices } from "@/app-facade";
import { Button, ErrorState, TextArea, TextInput } from "@/ui";

export type WorkflowCreateResult = Readonly<{
  workflow: WorkflowRecord;
  link: ProjectWorkflowLink | null;
}>;

export function WorkflowCreateForm({
  onCreated,
  projectID = "",
}: Readonly<{
  onCreated: (result: WorkflowCreateResult) => void;
  projectID?: string | undefined;
}>) {
  const { t } = useTranslation();
  const { api } = useAppServices();
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const create = useMutation({
    mutationFn: async () => {
      const input = { name: name.trim(), description: description.trim() };
      if (projectID.length === 0) {
        const workflow = await api.createWorkflow(input);
        return { link: null, workflow };
      }
      return api.createAndLinkWorkflowToProject({ ...input, projectID });
    },
    onSuccess: async (result) => {
      await queryClient.invalidateQueries({ queryKey: queryKeys.allWorkflows });
      if (projectID.length > 0) {
        await queryClient.invalidateQueries({ queryKey: queryKeys.allProjectWorkflowLinks });
        await queryClient.invalidateQueries({ queryKey: queryKeys.allBoards });
      }
      onCreated(result);
    },
  });

  function submit(event: SyntheticEvent<HTMLFormElement>): void {
    event.preventDefault();
    if (name.trim().length === 0 || create.isPending) {
      return;
    }
    void create.mutateAsync();
  }

  return (
    <form className="grid gap-[var(--space-4)]" onSubmit={submit}>
      {create.isError ? (
        <ErrorState
          body={errorMessage(create.error)}
          fullPage={false}
          reveal={false}
          title={t("workflowLibrary.createFailed")}
        />
      ) : null}
      <TextInput
        autoFocus
        label={t("workflowLibrary.name")}
        onChange={(event) => {
          setName(event.target.value);
        }}
        placeholder={t("workflowLibrary.namePlaceholder")}
        value={name}
      />
      <TextArea
        label={t("workflowLibrary.description")}
        onChange={(event) => {
          setDescription(event.target.value);
        }}
        placeholder={t("workflowLibrary.descriptionPlaceholder")}
        value={description}
      />
      <div className="flex justify-end gap-[var(--space-2)]">
        <Button disabled={name.trim().length === 0 || create.isPending} type="submit" variant="primary">
          {create.isPending ? t("workflowLibrary.creating") : t("workflowLibrary.createWorkflow")}
        </Button>
      </div>
    </form>
  );
}
