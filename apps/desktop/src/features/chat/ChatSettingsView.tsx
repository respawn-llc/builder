import { Check, Settings, Zap } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { ChatSettingsMutation } from "@/api";
import {
  InteractiveChip,
  Popover,
  PopoverContent,
  PopoverTrigger,
  SegmentedControl,
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/ui";

import { settingsDisabledReason, settingsPresentation } from "./chatSettingsPresentation";
import { ThinkingSelector } from "./ThinkingSelector";
import { ChatSettingsSummary } from "./ChatSettingsSummary";
import { CustomThinkingEditor } from "./CustomThinkingEditor";
import { SettingsRow, SettingsSwitch } from "./ChatSettingsRows";
import type { ReadyChatSettings } from "./useChatSettings";

export function ChatSettingsView({ feature }: Readonly<{ feature: ReadyChatSettings }>) {
  const { t } = useTranslation();
  const settings = settingsPresentation(feature);
  const disconnected =
    feature.kind === "ready-session" && feature.serverMutationAvailability === "disconnected";
  const reason = (
    editability: Parameters<typeof settingsDisabledReason>[1],
    policy?: Parameters<typeof settingsDisabledReason>[3],
  ) => settingsDisabledReason(t, editability, disconnected, policy);
  function activate(operation: ChatSettingsMutation) {
    if (!("activate" in feature)) return;
    if (feature.kind === "ready-new-chat") {
      feature.activate(operation);
      return;
    }
    void feature.activate(operation).catch(() => {
      // The controller has already rolled back and reported the operation failure.
    });
  }
  const supervisor = settings.supervisor;
  const summary = {
    role: settings.selectedAgent.role,
    model: settings.selectedAgent.model,
    thinking: settings.thinking.kind === "unsupported" ? null : settings.thinking.value,
    fast: settings.fast.kind === "supported" && settings.fast.value,
  };
  return (
    <Popover>
      <TooltipProvider>
        <Tooltip>
          <TooltipTrigger asChild>
            <PopoverTrigger asChild>
              <InteractiveChip aria-label={t("chatSettings.open")} className="min-w-0">
                <Settings className="shrink-0" size={14} />
                <ChatSettingsSummary {...summary} />
              </InteractiveChip>
            </PopoverTrigger>
          </TooltipTrigger>
          <TooltipContent>
            {summary.role}: {summary.model}
            {summary.thinking === null ? null : ` ${summary.thinking}`}
            {summary.fast ? (
              <Zap className="ml-[var(--space-1)] inline text-[var(--color-secondary)]" size={14} />
            ) : null}
          </TooltipContent>
        </Tooltip>
      </TooltipProvider>
      <PopoverContent
        align="start"
        className="w-96 max-w-[var(--radix-popover-content-available-width)] max-h-[var(--radix-popover-content-available-height)] grid-rows-[minmax(0,1fr)] gap-0 overflow-hidden p-[var(--space-1)]"
      >
        <div className="min-h-0 overflow-y-auto text-sm">
          <SettingsRow reason={reason(settings.agentEditability)}>
            <div className="grid min-w-0 flex-1">
              <span className="flex min-w-0 items-center gap-[var(--space-2)] font-bold">
                <span className="truncate">{settings.selectedAgent.role}</span>
                <Check className="shrink-0" size={14} />
              </span>
              <span className="truncate font-mono text-xs text-[var(--color-muted)]">
                {settings.selectedAgent.model}{" "}
                {settings.thinking.kind === "unsupported" ? null : settings.thinking.value}
              </span>
            </div>
          </SettingsRow>
          <SettingsRow
            onActivate={() => {
              activate({
                kind: "supervisor",
                value:
                  supervisor.value === "off"
                    ? supervisor.baseline === "off"
                      ? "edits"
                      : supervisor.baseline
                    : "off",
              });
            }}
            reason={reason(supervisor.editability)}
          >
            <span className="min-w-0 flex-1 truncate">{t("chatSettings.supervisor")}</span>
            <div
              onClick={(event) => {
                event.stopPropagation();
              }}
            >
              <SegmentedControl
                ariaLabel={t("chatSettings.supervisor")}
                disabled={reason(supervisor.editability) !== undefined}
                onValueChange={(value) => {
                  activate({ kind: "supervisor", value });
                }}
                options={[
                  { value: "edits", label: t("chatSettings.supervisorEdits") },
                  { value: "all", label: t("chatSettings.supervisorAlways") },
                  { value: "off", label: t("chatSettings.supervisorOff") },
                ]}
                value={supervisor.value}
              />
            </div>
          </SettingsRow>
          {settings.thinking.kind === "enumerated" ? (
            <SettingsRow reason={reason(settings.thinking.editability)}>
              <div className="min-w-0 flex-1">
                <ThinkingSelector
                  disabled={disconnected}
                  onCommit={(value) => {
                    activate({ kind: "thinking", value });
                  }}
                  thinking={settings.thinking}
                />
              </div>
            </SettingsRow>
          ) : null}
          {settings.thinking.kind === "custom" ? (
            <CustomThinkingEditor
              reason={reason(settings.thinking.editability)}
              feature={feature}
              key={settings.selectedAgent.role}
              thinking={settings.thinking}
            />
          ) : null}
          {settings.fast.kind === "supported" ? (
            <SettingsSwitch
              checked={settings.fast.value}
              icon={<Zap className="shrink-0 text-[var(--color-secondary)]" size={14} />}
              label={t("chatSettings.fast")}
              onChange={(enabled) => {
                activate({ kind: "fast", enabled });
              }}
              reason={reason(settings.fast.editability)}
            />
          ) : null}
          <SettingsSwitch
            checked={settings.questions.enabled}
            label={t("chatSettings.questions")}
            onChange={(enabled) => {
              activate({ kind: "questions", enabled });
            }}
            reason={reason(settings.questions.editability)}
          />
          <SettingsSwitch
            checked={settings.autoCompaction.effective}
            label={t("chatSettings.autoCompaction")}
            onChange={(enabled) => {
              activate({ kind: "auto_compaction", enabled });
            }}
            reason={reason(settings.autoCompaction.editability, settings.autoCompaction.policy)}
          />
        </div>
      </PopoverContent>
    </Popover>
  );
}
