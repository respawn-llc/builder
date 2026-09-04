import type { SessionChatTarget } from "@/app-facade";

export type TaskDetailSessionChatEntry = (target: SessionChatTarget) => Promise<void>;
