import type { AttentionItem } from "@/api";
import { attentionChatTarget } from "./attentionChatTarget";

const base = {
  id: "attention-1",
  projectID: "project-1",
  taskID: "task-1",
  taskShortID: "T-1",
  taskTitle: "Resolve blocker",
  occurredAt: 1,
  workflowID: "workflow-1",
} as const;

const question = {
  ...base,
  currentNode: {
    effectiveAssignee: null,
    effectiveThinking: null,
    nodeID: "node-1",
    sessionID: "session-1",
    transitionBranchKey: null,
  },
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

const interruptedWithSession = {
  ...base,
  currentNode: {
    effectiveAssignee: null,
    effectiveThinking: null,
    nodeID: "node-1",
    sessionID: "session-2",
    transitionBranchKey: null,
  },
  detailJSON: null,
  kind: "interrupted_current_node",
  message: null,
  sessionID: "session-2",
} satisfies AttentionItem;

const interruptedWithoutSession = {
  ...interruptedWithSession,
  sessionID: null,
} satisfies AttentionItem;

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
} satisfies AttentionItem;

it.each([
  [question, { projectID: "project-1", sessionID: "session-1" }],
  [interruptedWithSession, { projectID: "project-1", sessionID: "session-2" }],
  [interruptedWithoutSession, null],
  [approval, null],
] as const)("projects the typed attention Chat target", (item, expected) => {
  expect(attentionChatTarget(item)).toEqual(expected);
});
