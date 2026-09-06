import { ChevronRight } from "lucide-react";
import { useTranslation } from "react-i18next";

import { errorMessage, type ChatSettingsSessionFacts as SessionFacts } from "@/api";
import { useAppServices } from "@/app-facade";
import { writeClipboardText } from "@/shared/native-clipboard";
import { showStatusToast, Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/ui";

import { SettingsRow } from "./ChatSettingsRows";
import type { ChatSettingsNavigation } from "./useChatSettings";

export function ChatSettingsSessionFacts({
  facts,
  navigation,
  closeAndNavigate,
}: Readonly<{
  facts: SessionFacts;
  navigation: ChatSettingsNavigation;
  closeAndNavigate(action: () => void): void;
}>) {
  const { t } = useTranslation();
  const { nativeBridge } = useAppServices();
  const { sessionID, previousSessionID, task } = facts;
  async function copySessionID() {
    try {
      await writeClipboardText(sessionID, nativeBridge);
      showStatusToast({
        id: "chat-settings-copy",
        tone: "success",
        title: t("chatSettings.copied"),
        durationMs: 2000,
      });
    } catch (error) {
      showStatusToast({
        id: "chat-settings-copy",
        tone: "danger",
        title: t("chatSettings.copyFailed"),
        body: errorMessage(error),
      });
    }
  }
  return (
    <div className="mt-[var(--space-1)] border-t border-[var(--color-outline)] pt-[var(--space-1)]">
      {previousSessionID === null ? null : (
        <SettingsRow
          as="button"
          onActivate={() => {
            closeAndNavigate(() => {
              navigation.openParentSession(previousSessionID);
            });
          }}
          reason={undefined}
        >
          <span className="min-w-0 flex-1 truncate">{t("chatSettings.toParentChat")}</span>
          <ChevronRight className="shrink-0 text-[var(--color-muted)]" size={14} />
        </SettingsRow>
      )}
      {task === null ? null : (
        <SettingsRow
          as="button"
          onActivate={() => {
            closeAndNavigate(() => {
              navigation.openTask(task.taskID);
            });
          }}
          reason={undefined}
        >
          <span className="min-w-0 flex-1 truncate font-mono">{task.shortID}</span>
          <ChevronRight className="shrink-0 text-[var(--color-muted)]" size={14} />
        </SettingsRow>
      )}
      <TooltipProvider>
        <Tooltip>
          <TooltipTrigger asChild>
            <div>
              <SettingsRow
                as="button"
                onActivate={() => {
                  void copySessionID();
                }}
                reason={undefined}
              >
                <span className="min-w-0 flex-1 truncate font-mono">{sessionID}</span>
              </SettingsRow>
            </div>
          </TooltipTrigger>
          <TooltipContent className="font-mono">{sessionID}</TooltipContent>
        </Tooltip>
      </TooltipProvider>
    </div>
  );
}
