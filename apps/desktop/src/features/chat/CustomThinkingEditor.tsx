import { Check } from "lucide-react";
import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import type { ChatSettingsThinking } from "@/api";
import { fieldInputClassName, IconTooltipButton } from "@/ui";

import { ThinkingHeading } from "./ThinkingSelector";
import { SettingsRow } from "./ChatSettingsRows";
import type { ReadyChatSettings } from "./useChatSettings";

export function CustomThinkingEditor({
  thinking,
  feature,
  reason,
}: Readonly<{
  thinking: Extract<ChatSettingsThinking, { kind: "custom" }>;
  feature: ReadyChatSettings;
  reason: string | undefined;
}>) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const disabled = reason !== undefined;
  async function commit() {
    if (disabled || !("activate" in feature)) return;
    const entered = draft ?? thinking.value;
    inputRef.current?.focus();
    if (feature.kind === "ready-new-chat") {
      feature.activate({ kind: "thinking", value: entered });
      setDraft(null);
      return;
    }
    setDraft(entered);
    try {
      const response = await feature.activate({ kind: "thinking", value: entered });
      if (inputRef.current === null) return;
      if (response.result.kind === "applied") setDraft(null);
      else inputRef.current.focus();
    } catch {
      // The controller reports the failure; this editor retains the submitted draft.
      inputRef.current?.focus();
    }
  }
  return (
    <SettingsRow reason={reason}>
      <div className="grid min-w-0 flex-1 gap-[var(--space-2)]">
        <ThinkingHeading value={thinking.value} />
        <div
          className="relative"
          onClick={(event) => {
            event.stopPropagation();
          }}
          onKeyDown={(event) => {
            if (event.key !== "Enter" || event.nativeEvent.isComposing) return;
            event.preventDefault();
            event.stopPropagation();
            void commit();
          }}
        >
          <input
            aria-label={t("chatSettings.thinking")}
            className={fieldInputClassName}
            disabled={disabled}
            onChange={(event) => {
              setDraft(event.currentTarget.value);
            }}
            ref={inputRef}
            style={{
              height: "var(--space-6)",
              paddingBlock: "var(--space-0)",
              paddingInlineStart: "var(--space-2)",
              paddingInlineEnd: "calc(var(--space-6) + var(--space-2))",
            }}
            value={draft ?? thinking.value}
          />
          <span className="absolute top-1/2 right-[var(--space-1)] -translate-y-1/2">
            <IconTooltipButton
              disabled={disabled}
              label={t("chatSettings.commitThinking")}
              onClick={() => {
                void commit();
              }}
              size="icon-sm"
              variant="primary-outline"
            >
              <Check size={14} strokeWidth={2} />
            </IconTooltipButton>
          </span>
        </div>
      </div>
    </SettingsRow>
  );
}
