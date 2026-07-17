import { useId } from "react";
import { useTranslation } from "react-i18next";

import type { WorkflowDefinition, WorkflowEdge, WorkflowNode } from "@/api";
import { Checkbox, HelpHint, IslandSurface, MarkdownText } from "@/ui";
import { cx } from "@/ui";
import { DetailRow, DetailSection } from "./WorkflowInspectorPrimitives";
import { derivedNodeWiring, providerEdgeLabel } from "./workflowInspectorWiring";

export function ApprovalToggle({
  checked,
  disabled = false,
  label,
  labelHelp,
  onCheckedChange,
}: Readonly<{
  checked: boolean;
  disabled?: boolean | undefined;
  label: string;
  labelHelp?: string | undefined;
  onCheckedChange: (checked: boolean) => void;
}>) {
  const checkboxID = useId();
  const labelID = `${checkboxID}-label`;
  return (
    <div className="flex min-h-9 min-w-0 items-center gap-[var(--space-2)] rounded-[var(--radius-m)] text-sm font-semibold text-[var(--color-on-island)]">
      <Checkbox
        aria-labelledby={labelID}
        checked={checked}
        disabled={disabled}
        id={checkboxID}
        onCheckedChange={(value) => {
          if (disabled) {
            return;
          }
          onCheckedChange(value === true);
        }}
      />
      <label
        className={cx("min-w-0 select-none", disabled ? "cursor-not-allowed opacity-55" : "cursor-pointer")}
        htmlFor={checkboxID}
        id={labelID}
      >
        {label}
      </label>
      {labelHelp === undefined ? null : <HelpHint className="shrink-0" label={labelHelp} side="right" />}
    </div>
  );
}

export function FieldSummary({
  fields,
  title,
  titleHelp,
}: Readonly<{
  fields: readonly { name: string; description: string }[];
  title: string;
  titleHelp?: string | undefined;
}>) {
  const { t } = useTranslation();
  return (
    <DetailSection title={title} titleHelp={titleHelp}>
      {fields.length === 0 ? (
        <p className="m-0 text-sm text-[var(--color-muted)]">{t("workflowEditor.none")}</p>
      ) : (
        <ul className="m-0 grid gap-[var(--space-2)] p-0">
          {fields.map((field, index) => (
            <li className="list-none" key={`${field.name}:${index.toString()}`}>
              <span className="font-mono text-sm">{field.name}</span>
              {field.description.length > 0 ? (
                <p className="m-0 text-sm text-[var(--color-muted)]">{field.description}</p>
              ) : null}
            </li>
          ))}
        </ul>
      )}
    </DetailSection>
  );
}

export function JoinProviders({
  definition,
  node,
}: Readonly<{ definition: WorkflowDefinition; node: WorkflowNode }>) {
  const { t } = useTranslation();
  const fields = derivedNodeWiring(definition, node.id).joinOutputFields;
  const providerByInput = new Map(node.joinInputProviders.map((provider) => [provider.inputName, provider]));
  if (fields.length === 0) {
    return null;
  }
  return (
    <DetailSection title={t("workflowEditor.joinProviders")}>
      <ul className="m-0 grid gap-[var(--space-2)] p-0">
        {fields.map((field) => {
          const providerEdgeID = providerByInput.get(field.name)?.providerEdgeID ?? "";
          const provider = providerEdgeLabel(definition, providerEdgeID);
          return (
            <li className="list-none text-sm" key={field.name}>
              <span className="font-mono">{field.name}</span>
              <span className="text-[var(--color-muted)]"> = {provider || t("workflowEditor.none")}</span>
            </li>
          );
        })}
      </ul>
    </DetailSection>
  );
}

export function Bindings({ bindings }: Readonly<{ bindings: WorkflowEdge["inputBindings"] }>) {
  const { t } = useTranslation();
  return (
    <DetailSection title={t("workflowEditor.derivedInputBindings")}>
      {bindings.length === 0 ? (
        <p className="m-0 text-sm text-[var(--color-muted)]">{t("workflowEditor.none")}</p>
      ) : (
        <ul className="m-0 grid gap-[var(--space-1)] p-0">
          {bindings.map((binding) => (
            <li className="list-none text-sm" key={`${binding.name}:${binding.source}:${binding.field}`}>
              <span className="font-mono">{binding.name}</span> = {binding.source}.{binding.field}
            </li>
          ))}
        </ul>
      )}
    </DetailSection>
  );
}

export function PromptPreview({ help, prompt }: Readonly<{ help?: string | undefined; prompt: string }>) {
  const { t } = useTranslation();
  if (prompt.length === 0) {
    return <DetailRow help={help} label={t("workflowEditor.prompt")} value={t("workflowEditor.none")} />;
  }
  return (
    <div className="grid gap-[var(--space-1)]">
      <span className="inline-flex items-center gap-[var(--space-1)] text-sm font-bold text-[var(--color-on-island)] opacity-70">
        {t("workflowEditor.prompt")}
        {help === undefined ? null : <HelpHint className="shrink-0" label={help} side="right" />}
      </span>
      <IslandSurface as="div" className="rounded-[var(--radius-m)] p-[var(--space-2)] text-sm" level={1}>
        <MarkdownText value={prompt} />
      </IslandSurface>
    </div>
  );
}
