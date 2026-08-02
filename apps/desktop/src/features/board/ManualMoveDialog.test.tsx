import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, describe, expect, it, vi } from "vitest";

import type { TaskMovePreviewResponse } from "@/api";
import { TestAppProviders, createTestServices } from "@/test-support/app-services";
import { appI18n, initializeI18n } from "@/i18n";
import { ManualMoveDialog } from "./ManualMoveDialog";

beforeAll(async () => {
  await initializeI18n();
});

const appServices = createTestServices([], undefined, { platform: "macos" });

function renderDialog(preview: TaskMovePreviewResponse, onSubmit = vi.fn(), onCancel = vi.fn()) {
  render(
    <TestAppProviders services={appServices}>
      <ManualMoveDialog onCancel={onCancel} onSubmit={onSubmit} preview={preview} />
    </TestAppProviders>,
  );
  return { onCancel, onSubmit };
}

function transitionPreview(choiceCount: number): TaskMovePreviewResponse {
  return {
    outcome: "transition",
    transition: {
      choices: Array.from({ length: choiceCount }, (_, index) => ({
        transitionKey: `transition-${String(index)}`,
        label: index === 0 ? "Implement" : "Implement",
        sourceNodeDisplayName: index === 0 ? "Plan" : "Review",
        requiredValues:
          index === 0
            ? [
                {
                  nodeKey: "plan",
                  outputName: "summary",
                  description: "The plan summary.",
                  resolvedValue: null,
                },
              ]
            : [],
      })),
    },
  };
}

describe("ManualMoveDialog", () => {
  it("requires a choice, then a non-blank required value before submitting", async () => {
    const user = userEvent.setup();
    const { onSubmit } = renderDialog(transitionPreview(2));

    const radios = screen.getAllByRole("radio");
    expect(radios).toHaveLength(2);
    const [firstRadio, secondRadio] = radios;
    if (firstRadio === undefined || secondRadio === undefined) {
      throw new Error("expected two transition choices");
    }
    expect(firstRadio).not.toBeChecked();
    expect(secondRadio).not.toBeChecked();
    const continueButton = screen.getByRole("button", { name: appI18n.t("app.continue") });
    expect(continueButton).toBeDisabled();

    await user.click(firstRadio);
    await user.click(continueButton);
    const confirmButton = screen.getByRole("button", { name: appI18n.t("board.manualMoveConfirm") });
    expect(confirmButton).toBeDisabled();

    fireEvent.keyDown(screen.getByLabelText("summary"), { key: "Enter", metaKey: true });
    expect(onSubmit).not.toHaveBeenCalled();
    await user.type(screen.getByLabelText("summary"), "A plan");
    expect(confirmButton).toBeEnabled();
    fireEvent.keyDown(screen.getByLabelText("summary"), { key: "Enter", metaKey: true });

    expect(onSubmit).toHaveBeenCalledWith({
      transitionKey: "transition-0",
      values: { plan: { summary: "A plan" } },
    });
  });

  it("auto-selects a sole choice, preserves prefills, and supports cancellation", async () => {
    const user = userEvent.setup();
    const onCancel = vi.fn();
    const { onSubmit } = renderDialog({
      outcome: "transition",
      transition: {
        choices: [
          {
            transitionKey: "transition-0",
            label: "Implement",
            sourceNodeDisplayName: "Plan",
            requiredValues: [
              {
                nodeKey: "plan",
                outputName: "summary",
                description: "The plan summary.",
                resolvedValue: "Prefilled",
              },
            ],
          },
        ],
      },
    }, undefined, onCancel);

    expect(screen.queryByRole("radio")).not.toBeInTheDocument();
    const confirmButton = screen.getByRole("button", { name: appI18n.t("board.manualMoveConfirm") });
    expect(confirmButton).toBeEnabled();
    await user.click(confirmButton);
    expect(onSubmit).toHaveBeenCalledWith({
      transitionKey: "transition-0",
      values: { plan: { summary: "Prefilled" } },
    });

    await user.click(screen.getByRole("button", { name: appI18n.t("app.cancel") }));
    expect(onCancel).toHaveBeenCalledOnce();
  });

});
