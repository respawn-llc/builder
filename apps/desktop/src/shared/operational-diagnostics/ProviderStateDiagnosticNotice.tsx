import { useId } from "react";
import { useTranslation } from "react-i18next";

import type { ProviderStateOperationalDiagnostic } from "@/api";
import { Button } from "@/ui";
import { providerStateDiagnosticPresentation } from "./providerStateDiagnosticPresentation";

export type ProviderStateDiagnosticNoticeProps = Readonly<{
  diagnostic: ProviderStateOperationalDiagnostic;
  onRetry: () => void;
}>;

export function ProviderStateDiagnosticNotice({ diagnostic, onRetry }: ProviderStateDiagnosticNoticeProps) {
  const { t } = useTranslation();
  const titleID = useId();
  const presentation = providerStateDiagnosticPresentation(diagnostic, t);
  return (
    <section
      aria-labelledby={titleID}
      className="grid gap-[var(--space-2)] border-l-2 border-[var(--color-warning)] pl-[var(--space-3)]"
      data-tone={presentation.tone}
      role="status"
    >
      <h2 className="m-0 text-sm font-semibold text-[var(--color-warning)]" id={titleID}>
        {presentation.title}
      </h2>
      <p className="m-0 text-sm text-[var(--color-muted)]">{presentation.body}</p>
      <Button className="justify-self-start" onClick={onRetry} variant={presentation.tone}>
        {presentation.actionLabel}
      </Button>
    </section>
  );
}
