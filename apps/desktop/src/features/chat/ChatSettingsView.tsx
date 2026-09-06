import { Zap } from "lucide-react";
import { useId, useState } from "react";
import { flushSync } from "react-dom";
import { useTranslation } from "react-i18next";

import { errorMessage, type ChatSettingsMutation } from "@/api";
import { runViewTransition, useAppServices } from "@/app-facade";
import { Popover, PopoverContent, SegmentedControl } from "@/ui";

import { settingsDisabledReason, settingsPresentation } from "./chatSettingsPresentation";
import { ThinkingSelector } from "./ThinkingSelector";
import { ChatSettingsChip } from "./ChatSettingsChip";
import { CustomThinkingEditor } from "./CustomThinkingEditor";
import { SettingsRow, SettingsSwitch } from "./ChatSettingsRows";
import { ChatSettingsAgentRow } from "./ChatSettingsAgentRow";
import type { ChatSettingsNavigation, ReadyChatSettings } from "./useChatSettings";
import { ChatSettingsSessionFacts } from "./ChatSettingsSessionFacts";
import "./chatSettings.css";

export type ChatSettingsViewProps =
  | Readonly<{ feature: Extract<ReadyChatSettings, { kind: "ready-new-chat" }> }>
  | Readonly<{
      feature: Extract<ReadyChatSettings, { kind: "ready-session" }>;
      navigation: ChatSettingsNavigation;
    }>;

export function ChatSettingsView(props: ChatSettingsViewProps) {
  const { feature } = props;
  const { t } = useTranslation();
  const { logger } = useAppServices();
  const [view, setView] = useState<"closed" | "overview" | "agents">("closed");
  const transitionName = CSS.escape(`chat-settings-agent-${useId()}`);
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
  const agentReason = reason(settings.agentEditability);
  const selectedAgent = {
    ...settings.selectedAgent,
    thinking:
      settings.thinking.kind === "unsupported" ? settings.selectedAgent.thinking : settings.thinking.value,
  };
  const agents =
    feature.kind === "ready-new-chat"
      ? feature.catalog.choices.map((choice) => choice.agent)
      : feature.settings.agentChoices;
  function showAgents() {
    if (agentReason !== undefined) return;
    transition(() => {
      setView((current) => (current === "closed" ? current : "agents"));
    });
  }
  function selectAgent(role: string) {
    if (agentReason !== undefined) return;
    transition(() => {
      activate({ kind: "agent", role });
      setView((current) => (current === "closed" ? current : "overview"));
    });
  }
  function transition(update: () => void) {
    void runViewTransition({
      scope: "chat-settings",
      update: () => {
        flushSync(update);
      },
    })
      .then(async (result) => result.updateCallbackDone)
      .catch((error: unknown) => {
        void logger.append("warn", "Chat Settings view transition failed.", { error: errorMessage(error) });
      });
  }
  const summary = {
    role: settings.selectedAgent.role,
    model: settings.selectedAgent.model,
    thinking: settings.thinking.kind === "unsupported" ? null : settings.thinking.value,
    fast: settings.fast.kind === "supported" && settings.fast.value,
  };
  return (
    <Popover
      open={view !== "closed"}
      onOpenChange={(open) => {
        setView(open ? "overview" : "closed");
      }}
    >
      <ChatSettingsChip {...summary} />
      <PopoverContent
        align="start"
        className="w-96 max-w-[var(--radix-popover-content-available-width)] max-h-[var(--radix-popover-content-available-height)] grid-rows-[minmax(0,1fr)] gap-0 overflow-hidden p-[var(--space-1)]"
      >
        <div className="min-h-0 overflow-y-auto text-sm">
          {view === "agents"
            ? agents.map((agent) => (
                <ChatSettingsAgentRow
                  agent={agent.role === selectedAgent.role ? selectedAgent : agent}
                  key={agent.role}
                  onActivate={() => {
                    selectAgent(agent.role);
                  }}
                  reason={agentReason}
                  selected={agent.role === selectedAgent.role}
                  transitionName={transitionName}
                />
              ))
            : null}
          {/* Retain uncommitted input when returning through the already-selected Agent. */}
          <div hidden={view === "agents"}>
            <ChatSettingsAgentRow
              agent={selectedAgent}
              onActivate={showAgents}
              reason={agentReason}
              selected
              transitionName={transitionName}
            />
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
            {"navigation" in props ? (
              <ChatSettingsSessionFacts
                facts={props.feature.session}
                navigation={props.navigation}
                closeAndNavigate={(action) => {
                  flushSync(() => {
                    setView("closed");
                  });
                  action();
                }}
              />
            ) : null}
          </div>
        </div>
      </PopoverContent>
    </Popover>
  );
}
