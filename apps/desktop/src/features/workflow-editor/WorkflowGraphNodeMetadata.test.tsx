import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { initializeI18n } from "@/i18n";
import { showStatusToast } from "@/ui";
import { WorkflowNodeInfoTooltipContent } from "./WorkflowGraphNodeMetadata";
import type * as AppUI from "@/ui";

vi.mock("@/ui", async (importOriginal) => {
  const actual = await importOriginal<typeof AppUI>();
  return {
    ...actual,
    showStatusToast: vi.fn(),
  };
});

void initializeI18n();

describe("WorkflowNodeInfoTooltipContent", () => {
  beforeEach(() => {
    vi.mocked(showStatusToast).mockClear();
  });

  it("shows a Sonner status toast after copying node metadata", async () => {
    const copyText = vi.fn();
    render(<WorkflowNodeInfoTooltipContent nodeID="node-terminal" nodeKey="done" onCopyText={copyText} />);

    fireEvent.click(screen.getByRole("button", { name: "Copy Key done" }));
    fireEvent.click(screen.getByRole("button", { name: "Copy ID node-terminal" }));

    expect(copyText).toHaveBeenNthCalledWith(1, "done");
    expect(copyText).toHaveBeenNthCalledWith(2, "node-terminal");
    await waitFor(() => {
      expect(showStatusToast).toHaveBeenNthCalledWith(1, {
        id: "workflow-node-metadata-copy-done",
        title: "Copied Key to clipboard",
        tone: "success",
      });
    });
    expect(showStatusToast).toHaveBeenNthCalledWith(2, {
      id: "workflow-node-metadata-copy-node-terminal",
      title: "Copied ID to clipboard",
      tone: "success",
    });
  });

  it("shows a danger status toast when node metadata copy fails", async () => {
    render(
      <WorkflowNodeInfoTooltipContent
        nodeID="node-terminal"
        nodeKey="done"
        onCopyText={async () => Promise.reject(new Error("clipboard unavailable"))}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Copy ID node-terminal" }));

    await waitFor(() => {
      expect(showStatusToast).toHaveBeenCalledWith({
        id: "workflow-node-metadata-copy-failed-node-terminal",
        title: "ID copy failed",
        tone: "danger",
      });
    });
  });
});
