import { taskDetailSavePending, taskDetailSidebarState, taskDetailSnapshot } from "./taskDetailSidebarState";

describe("task detail sidebar state", () => {
  it("blocks state-retaining navigation while any approved save is pending", () => {
    expect(
      taskDetailSavePending({
        task: true,
        addComment: false,
        editComment: false,
      }),
    ).toBe(true);
    expect(
      taskDetailSavePending({
        task: false,
        addComment: true,
        editComment: false,
      }),
    ).toBe(true);
    expect(
      taskDetailSavePending({
        task: false,
        addComment: false,
        editComment: true,
      }),
    ).toBe(true);
    expect(
      taskDetailSavePending({
        task: false,
        addComment: false,
        editComment: false,
      }),
    ).toBe(false);
  });

  it("retains only approved presentation and unsent draft state", () => {
    expect(
      taskDetailSnapshot({
        scrollTop: 123,
        descriptionExpanded: true,
        selectedTab: "activity",
        titleBodyDraft: { title: "Draft title", body: "Draft body" },
        newCommentDraft: "Unsaved comment",
        editedCommentDraft: { commentID: "comment-1", body: "Edited comment" },
      }),
    ).toEqual({
      kind: "taskDetail",
      scrollTop: 123,
      descriptionExpanded: true,
      selectedTab: "activity",
      titleBodyDraft: { title: "Draft title", body: "Draft body" },
      newCommentDraft: "Unsaved comment",
      editedCommentDraft: { commentID: "comment-1", body: "Edited comment" },
    });
  });

  it("normalizes sidebar restoration presence around the activation ID", () => {
    const snapshot = taskDetailSnapshot({
      scrollTop: 32,
      descriptionExpanded: false,
      selectedTab: "comments",
    });

    expect(taskDetailSidebarState(undefined, snapshot)).toBeUndefined();
    expect(taskDetailSidebarState(null, snapshot)).toBeUndefined();
    expect(taskDetailSidebarState("activation-1", undefined)).toEqual({
      activationID: "activation-1",
    });
    expect(taskDetailSidebarState("activation-1", snapshot)).toEqual({
      activationID: "activation-1",
      snapshot,
    });
  });
});
