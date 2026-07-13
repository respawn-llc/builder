import type { BoardCard, WorkspaceSummary } from "../../api";
import { toKanbanCardVM } from "./BoardColumnViewModel";

const card: BoardCard = {
  activeNodeIDs: [],
  actions: {
    canCancel: true,
    canInterrupt: false,
    canResume: false,
    canStart: true,
    manualMoveTargetNodeIDs: [],
  },
  preview: { markdown: "Body", truncated: false },
  id: "task-1",
  shortID: "KNT-1",
  sourceWorkspace: {
    availability: "available",
    id: "workspace-other",
    isPrimary: false,
    name: "Other workspace",
    rootPath: "/workspace/other",
    updatedAt: 1,
  },
  status: {
    attentionTypes: [],
    kind: "backlog",
    nativeState: "active",
    nodeIDs: [],
    runIDs: [],
  },
  title: "Task",
  updatedAt: 1,
  workflowID: "workflow-1",
};

describe("toKanbanCardVM", () => {
  it.each<
    readonly [
      {
        name: string;
        attachedWorkspaceCount: number;
        defaultWorkspaceID: string;
        availability: WorkspaceSummary["availability"];
        expectedWorkspaceChipLabel: string | null;
      },
    ]
  >([
    [
      {
        name: "hides the chip for a one-workspace project",
        attachedWorkspaceCount: 1,
        defaultWorkspaceID: "workspace-default",
        availability: "available",
        expectedWorkspaceChipLabel: null,
      },
    ],
    [
      {
        name: "hides the chip for the default workspace",
        attachedWorkspaceCount: 2,
        defaultWorkspaceID: "workspace-other",
        availability: "available",
        expectedWorkspaceChipLabel: null,
      },
    ],
    [
      {
        name: "shows the chip for an attached non-default workspace",
        attachedWorkspaceCount: 2,
        defaultWorkspaceID: "workspace-default",
        availability: "available",
        expectedWorkspaceChipLabel: "Other workspace",
      },
    ],
    [
      {
        name: "shows the chip for a missing attached non-default workspace",
        attachedWorkspaceCount: 2,
        defaultWorkspaceID: "workspace-default",
        availability: "missing",
        expectedWorkspaceChipLabel: "Other workspace",
      },
    ],
    [
      {
        name: "shows the chip for an inaccessible attached non-default workspace",
        attachedWorkspaceCount: 2,
        defaultWorkspaceID: "workspace-default",
        availability: "inaccessible",
        expectedWorkspaceChipLabel: "Other workspace",
      },
    ],
    [
      {
        name: "hides detached historical context with one workspace",
        attachedWorkspaceCount: 1,
        defaultWorkspaceID: "workspace-default",
        availability: "unlinked",
        expectedWorkspaceChipLabel: null,
      },
    ],
    [
      {
        name: "hides detached historical context with multiple remaining workspaces",
        attachedWorkspaceCount: 3,
        defaultWorkspaceID: "workspace-default",
        availability: "unlinked",
        expectedWorkspaceChipLabel: null,
      },
    ],
  ])("$name", ({ attachedWorkspaceCount, defaultWorkspaceID, availability, expectedWorkspaceChipLabel }) => {
    const viewModel = toKanbanCardVM(
      {
        ...card,
        sourceWorkspace: {
          ...card.sourceWorkspace,
          availability,
        },
      },
      { attachedWorkspaceCount, defaultWorkspaceID },
    );

    expect(viewModel.workspaceChipLabel).toBe(expectedWorkspaceChipLabel);
  });

  it.each([
    ["waiting_question", "primary"],
    ["waiting_approval", "secondary"],
    ["backlog", "default"],
  ] as const)("projects %s to the %s border tone", (statusKind, borderTone) => {
    expect(
      toKanbanCardVM(
        {
          ...card,
          status: { ...card.status, kind: statusKind },
        },
        { attachedWorkspaceCount: 2, defaultWorkspaceID: "workspace-default" },
      ).borderTone,
    ).toBe(borderTone);
  });
});
