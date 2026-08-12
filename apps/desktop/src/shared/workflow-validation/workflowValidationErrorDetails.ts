import type { TFunction } from "i18next";

import type { WorkflowValidationError } from "@/api";

export function workflowValidationErrorDetails(
  error: WorkflowValidationError,
  t: TFunction,
): readonly string[] {
  return [
    detail(t("workflowEditor.validationDetailInput"), error.details.inputName),
    detail(t("workflowEditor.validationDetailField"), error.details.fieldName),
    detail(t("workflowEditor.validationDetailPlaceholder"), error.details.placeholder),
    detail(t("workflowEditor.validationDetailProviderEdge"), error.details.providerEdgeID),
  ].filter((item): item is string => item !== null);
}

function detail(label: string, value: string | null): string | null {
  return value === null || value.length === 0 ? null : `${label}: ${value}`;
}
