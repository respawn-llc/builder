import type { TFunction } from "i18next";

import { decodeWorkflowTaskIntegrityError, errorMessage } from "@/api";

export type WorkflowTaskReadError = Readonly<{
  title: string;
  body: string;
}>;

export function workflowTaskReadError(error: unknown, t: TFunction): WorkflowTaskReadError {
  const integrityError = decodeWorkflowTaskIntegrityError(error);
  return {
    title: t("states.error"),
    body:
      integrityError === null
        ? errorMessage(error)
        : t("states.workflowTaskContractError", { taskID: integrityError.taskID }),
  };
}
