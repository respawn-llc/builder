import { useTranslation } from "react-i18next";

import { showStatusToast } from "@/ui";

export type CopyText = (value: string) => Promise<void> | void;

export function WorkflowNodeInfoTooltipContent({
  nodeID,
  nodeKey,
  onCopyText,
}: Readonly<{ nodeID: string; nodeKey: string; onCopyText: CopyText }>) {
  const { t } = useTranslation();
  const keyLabel = t("workflowEditor.key");
  const idLabel = t("workflowEditor.id");
  return (
    <>
      <CopyableNodeValue
        copyLabel={t("workflowEditor.copyNodeMetadata", { label: keyLabel, value: nodeKey })}
        label={keyLabel}
        onCopyText={onCopyText}
        successMessage={t("workflowEditor.nodeMetadataCopied", { label: keyLabel })}
        failureMessage={t("workflowEditor.nodeMetadataCopyFailed", { label: keyLabel })}
        value={nodeKey}
      />
      <CopyableNodeValue
        copyLabel={t("workflowEditor.copyNodeMetadata", { label: idLabel, value: nodeID })}
        label={idLabel}
        onCopyText={onCopyText}
        successMessage={t("workflowEditor.nodeMetadataCopied", { label: idLabel })}
        failureMessage={t("workflowEditor.nodeMetadataCopyFailed", { label: idLabel })}
        value={nodeID}
      />
    </>
  );
}

function CopyableNodeValue({
  copyLabel,
  failureMessage,
  label,
  onCopyText,
  successMessage,
  value,
}: Readonly<{
  copyLabel: string;
  failureMessage: string;
  label: string;
  onCopyText: CopyText;
  successMessage: string;
  value: string;
}>) {
  return (
    <button
      aria-label={copyLabel}
      className="grid min-w-0 grid-cols-[auto_minmax(0,1fr)] items-baseline gap-2 rounded-sm bg-transparent px-1.5 py-0.5 text-left outline-none hover:bg-[var(--color-island-2)] focus-visible:bg-[var(--color-island-2)] focus-visible:outline-none"
      onClick={(event) => {
        event.stopPropagation();
        copyNodeText({ failureMessage, onCopyText, successMessage, value });
      }}
      type="button"
    >
      <span className="text-sm font-bold opacity-70">{label}</span>
      <span className="min-w-0 break-all font-mono text-sm">{value}</span>
    </button>
  );
}

function copyNodeText({
  failureMessage,
  onCopyText,
  successMessage,
  value,
}: Readonly<{ failureMessage: string; onCopyText: CopyText; successMessage: string; value: string }>): void {
  try {
    void Promise.resolve(onCopyText(value))
      .then(() => {
        showStatusToast({
          id: `workflow-node-metadata-copy-${value}`,
          title: successMessage,
          tone: "success",
        });
      })
      .catch(() => {
        showStatusToast({
          id: `workflow-node-metadata-copy-failed-${value}`,
          title: failureMessage,
          tone: "danger",
        });
      });
  } catch {
    showStatusToast({
      id: `workflow-node-metadata-copy-failed-${value}`,
      title: failureMessage,
      tone: "danger",
    });
  }
}
