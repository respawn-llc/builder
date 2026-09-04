export type TaskDetailSessionChatEntry = (
  target: Readonly<{
    projectID: string;
    sessionID: string;
  }>,
) => Promise<void>;
