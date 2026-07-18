import { workflowDeleteConfirmationTextKeys } from "./workflowDeleteConfirmationModel";

describe("workflowDeleteConfirmationModel", () => {
  it("uses branch copy for branch-only deletes", () => {
    expect(
      workflowDeleteConfirmationTextKeys(
        {
          edgeCount: 1,
          nodeCount: 0,
          promptCount: 1,
          transitionGroupCount: 1,
        },
        "delete",
      ),
    ).toEqual({
      bodyKey: "workflowEditor.deleteBranchCascadeBody",
      confirmKey: "workflowEditor.deleteBranchCascadeConfirm",
      titleKey: "workflowEditor.deleteBranchCascadeTitle",
    });
  });

  it("keeps node and extraction copy for non-branch-only confirmations", () => {
    expect(
      workflowDeleteConfirmationTextKeys(
        {
          edgeCount: 1,
          nodeCount: 1,
          promptCount: 1,
          transitionGroupCount: 1,
        },
        "delete",
      ),
    ).toEqual({
      bodyKey: "workflowEditor.deleteCascadeBody",
      confirmKey: "workflowEditor.deleteCascadeConfirm",
      titleKey: "workflowEditor.deleteCascadeTitle",
    });
    expect(
      workflowDeleteConfirmationTextKeys(
        {
          edgeCount: 1,
          nodeCount: 0,
          promptCount: 0,
          transitionGroupCount: 0,
        },
        "extract",
      ),
    ).toEqual({
      bodyKey: "workflowEditor.extractNodeCascadeBody",
      confirmKey: "workflowEditor.extractNodeCascadeConfirm",
      titleKey: "workflowEditor.extractNodeCascadeTitle",
    });
  });
});
