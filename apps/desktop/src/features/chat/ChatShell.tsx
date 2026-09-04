import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { useWindowChromeTitle, type SessionChatTarget } from "@/app-facade";
import { ErrorState } from "@/ui";

export type SelectedSession = Pick<SessionChatTarget, "projectID" | "sessionID">;

export type ChatShellState =
  | Readonly<{ kind: "ready" }>
  | Readonly<{
      kind: "error";
      diagnostic?: ReactNode;
      onRetry: () => void;
    }>;

export type ChatShellProps = Readonly<{
  composer: (session: SelectedSession) => ReactNode;
  content: (session: SelectedSession) => ReactNode;
  selectedSession: SelectedSession;
  sessionName: string | null;
  state: ChatShellState;
}>;

export function ChatShell({ composer, content, selectedSession, sessionName, state }: ChatShellProps) {
  const { t } = useTranslation();
  useWindowChromeTitle(sessionName);

  if (state.kind === "error") {
    return (
      <div className="flex h-full min-h-0 flex-col" data-testid="chat-shell">
        <div className="min-h-0 flex-1">
          <ErrorState
            body={state.diagnostic}
            onRetry={state.onRetry}
            retryLabel={t("app.retry")}
            title={t("states.error")}
          />
        </div>
      </div>
    );
  }

  return (
    <div className="flex h-full min-h-0 flex-col" data-testid="chat-shell">
      <div className="min-h-0 flex-1">{content(selectedSession)}</div>
      <div className="shrink-0">{composer(selectedSession)}</div>
    </div>
  );
}
