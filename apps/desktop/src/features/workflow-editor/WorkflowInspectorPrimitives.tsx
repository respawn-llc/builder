import { useId, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import type { WorkflowValidationError } from "@/api";
import { WorkflowValidationErrorDetailsLine } from "@/shared/workflow-validation";
import { ErrorState, HelpHint, IslandSurface } from "@/ui";

export function DetailSection({
  children,
  hideTitle = false,
  leading,
  title,
  titleHelp,
}: Readonly<{
  children: ReactNode;
  hideTitle?: boolean | undefined;
  leading?: ReactNode | undefined;
  title?: string | undefined;
  titleHelp?: string | undefined;
}>) {
  const titleID = useId();
  return (
    <IslandSurface
      aria-labelledby={title === undefined ? undefined : titleID}
      as="section"
      className="grid gap-[var(--space-2)] rounded-[var(--radius-l)] p-[var(--space-3)]"
      level={1}
    >
      {leading}
      {title === undefined ? null : hideTitle ? (
        <h3 className="sr-only" id={titleID}>
          {title}
        </h3>
      ) : (
        <div className="inline-flex items-center gap-[var(--space-1)]">
          <h3 className="m-0 text-sm font-bold" id={titleID}>
            {title}
          </h3>
          {titleHelp === undefined ? null : <HelpHint className="shrink-0" label={titleHelp} side="right" />}
        </div>
      )}
      {children}
    </IslandSurface>
  );
}

export function DetailRow({
  help,
  label,
  mono = false,
  value,
}: Readonly<{ help?: string | undefined; label: string; mono?: boolean; value: string }>) {
  return (
    <div className="grid gap-[2px]">
      <span className="inline-flex items-center gap-[var(--space-1)] text-sm font-bold text-[var(--color-on-island)] opacity-70">
        {label}
        {help === undefined ? null : <HelpHint className="shrink-0" label={help} side="right" />}
      </span>
      <span className={mono ? "break-all font-mono text-sm" : "text-sm"}>{value}</span>
    </div>
  );
}

export function InspectorStack({ children }: Readonly<{ children: ReactNode }>) {
  return <div className="grid gap-[var(--space-3)]">{children}</div>;
}

export function ValidationDetails({
  errors,
  title,
}: Readonly<{ errors: readonly WorkflowValidationError[]; title?: string }>) {
  const { t } = useTranslation();
  if (errors.length === 0) {
    return null;
  }
  return (
    <DetailSection title={title ?? t("workflowEditor.validationErrors")}>
      <ul className="m-0 grid list-disc gap-[var(--space-1)] pl-[1.1rem] text-sm leading-snug">
        {errors.map((error, index) => (
          <li className="pl-[2px] marker:text-[var(--color-error)]" key={validationErrorKey(error, index)}>
            <span>{error.message}</span>
            <WorkflowValidationErrorDetailsLine error={error} />
          </li>
        ))}
      </ul>
    </DetailSection>
  );
}

function validationErrorKey(error: WorkflowValidationError, index: number): string {
  const entityID = error.edgeID || error.nodeID || error.transitionGroupID || error.workflowID;
  if (entityID.length > 0) {
    return `${error.code}:${entityID}:${error.message}`;
  }
  return `${error.code}:${error.message}:${index.toString()}`;
}

export function MissingEntity({ entityID }: Readonly<{ entityID: string }>) {
  const { t } = useTranslation();
  return (
    <ErrorState
      body={t("workflowEditor.entityMissing")}
      details={entityID}
      fullPage={false}
      reveal={false}
      title={t("workflowEditor.inspectorUnavailable")}
    />
  );
}
