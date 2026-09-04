import { fireEvent, render, screen, within } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";

import type { ApprovalAttentionItem, AttentionItem, InterruptedCurrentNodeAttentionItem } from "@/api";
import { appI18n, initializeI18n } from "@/i18n";
import { AttentionRow } from "./AttentionRow";

const fixture = vi.hoisted(() => ({
  featureFlags: { desktopChatEnabled: true },
  openSessionChat: vi.fn(async () => undefined),
}));

vi.mock("@/shared/feature-flags", () => fixture.featureFlags);

vi.mock("@/app-facade", async (importOriginal) => ({
  ...(await importOriginal()),
  useAppNavigation: () => ({ openSessionChat: fixture.openSessionChat }),
}));

beforeAll(async () => initializeI18n());
beforeEach(() => {
  fixture.featureFlags.desktopChatEnabled = true;
  fixture.openSessionChat.mockClear();
});

function renderAttention(item: AttentionItem) {
  const openSidebar = vi.fn();
  const view = render(
    <I18nextProvider i18n={appI18n}>
      <AttentionRow item={item} openSidebar={openSidebar} sidebarMode="shift" />
    </I18nextProvider>,
  );
  return { openSidebar, view };
}

it("splits a Session-bearing attention card between Chat header and Task Detail body", () => {
  const { openSidebar } = renderAttention(questionAttention);
  const row = screen.getByTestId("attention-row");

  expect(within(row).getAllByRole("button")).toHaveLength(2);
  fireEvent.click(screen.getByTestId("attention-chat-header"));
  fireEvent.click(screen.getByTestId("attention-task-detail-body"));

  expect(fixture.openSessionChat).toHaveBeenCalledWith({
    projectID: "project-1",
    sessionID: "session-1",
  });
  expect(openSidebar).toHaveBeenCalledOnce();
});

const base = {
  id: "attention-1",
  occurredAt: 1,
  projectID: "project-1",
  taskID: "task-1",
  taskShortID: "T-1",
  taskTitle: "Resolve blocker",
  workflowID: "workflow-1",
} as const;

const currentNode = {
  effectiveAssignee: null,
  effectiveThinking: null,
  nodeID: "node-1",
  sessionID: "session-2",
  transitionBranchKey: null,
} as const;

const interruptedWithSession = {
  ...base,
  currentNode,
  detailJSON: null,
  kind: "interrupted_current_node",
  message: null,
  sessionID: "session-2",
} satisfies InterruptedCurrentNodeAttentionItem;

const interruptedWithoutSession = {
  ...interruptedWithSession,
  sessionID: null,
} satisfies InterruptedCurrentNodeAttentionItem;

const approval = {
  ...base,
  approvalID: "approval-1",
  approvalSnapshot: {
    commentary: "",
    outputValues: {},
    sourceNodeName: "Implement",
    targets: [],
    version: 1,
  },
  kind: "approval",
  message: null,
} satisfies ApprovalAttentionItem;

const questionAttention = {
  ...base,
  currentNode: { ...currentNode, sessionID: null },
  kind: "question",
  message: "Choose",
  question: {
    kind: "ordinary",
    promptID: "prompt-1",
    recommendedOptionIndex: null,
    sessionID: "session-1",
    stepID: "step-1",
    suggestions: ["One"],
  },
  sessionName: null,
} satisfies AttentionItem;

it.each([
  ["interrupted Session", interruptedWithSession, true],
  ["interrupted attention without Session", interruptedWithoutSession, false],
  ["Task Approval", approval, false],
] as const)("keeps the typed Chat affordance for %s", (_name, item, hasChat) => {
  const { openSidebar } = renderAttention(item);
  const chatHeader = screen.queryByTestId("attention-chat-header");

  expect(chatHeader !== null).toBe(hasChat);
  if (hasChat) {
    if (chatHeader === null) throw new Error("Expected Chat header.");
    fireEvent.click(chatHeader);
    expect(fixture.openSessionChat).toHaveBeenCalledOnce();
  } else {
    const row = screen.getByTestId("attention-row");
    expect(screen.getAllByRole("button")).toHaveLength(1);
    fireEvent.click(row);
    expect(openSidebar).toHaveBeenCalledOnce();
  }
});

it("keeps production attention rows as one Task Detail interaction", () => {
  fixture.featureFlags.desktopChatEnabled = false;
  const { openSidebar } = renderAttention(questionAttention);
  const row = screen.getByTestId("attention-row");

  expect(within(row).queryByTestId("attention-chat-header")).not.toBeInTheDocument();
  expect(screen.getAllByRole("button")).toHaveLength(1);
  fireEvent.click(row);
  expect(openSidebar).toHaveBeenCalledOnce();
});
