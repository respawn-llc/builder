import type { ProviderStateOperationalDiagnostic } from "@/api";
import type { StatusNotice } from "@/ui";

export type ProviderStateDiagnosticPresentation = Readonly<
  Required<Pick<StatusNotice, "tone" | "title" | "body" | "actionLabel">> & {
    tone: "warning";
  }
>;

type ProviderStateDiagnosticTranslationKeys = Readonly<{
  title: ProviderStateDiagnosticTranslationKey;
  body: ProviderStateDiagnosticTranslationKey;
}>;

export type ProviderStateDiagnosticTranslationKey =
  | "operationalDiagnostic.providerTurnStateInvalid.title"
  | "operationalDiagnostic.providerTurnStateInvalid.body"
  | "operationalDiagnostic.providerTurnStateConflict.title"
  | "operationalDiagnostic.providerTurnStateConflict.body"
  | "operationalDiagnostic.retryAction";

export type ProviderStateDiagnosticTranslator = (key: ProviderStateDiagnosticTranslationKey) => string;

const translationKeysByCode: Readonly<
  Record<ProviderStateOperationalDiagnostic["code"], ProviderStateDiagnosticTranslationKeys>
> = {
  provider_turn_state_invalid: {
    title: "operationalDiagnostic.providerTurnStateInvalid.title",
    body: "operationalDiagnostic.providerTurnStateInvalid.body",
  },
  provider_turn_state_conflict: {
    title: "operationalDiagnostic.providerTurnStateConflict.title",
    body: "operationalDiagnostic.providerTurnStateConflict.body",
  },
};

export function providerStateDiagnosticPresentation(
  diagnostic: ProviderStateOperationalDiagnostic,
  t: ProviderStateDiagnosticTranslator,
): ProviderStateDiagnosticPresentation {
  const keys = translationKeysByCode[diagnostic.code];
  return {
    tone: "warning",
    title: t(keys.title),
    body: t(keys.body),
    actionLabel: t("operationalDiagnostic.retryAction"),
  };
}
