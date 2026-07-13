import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useEffect, useRef } from "react";
import { vi } from "vitest";

import type {
  TaskStartResponse,
  WorkflowExecutionTargetSelection,
} from "../../api";
import { appI18n, initializeI18n } from "../../i18n/setup";
import { startExecutionTargetAction } from "./executionTargetContinuation";
import {
  ExecutionTargetContinuationDialog,
} from "./ExecutionTargetContinuationDialog";
import { useExecutionTargetContinuation } from "./useExecutionTargetContinuation";

void initializeI18n();

describe("ExecutionTargetContinuationDialog", () => {
  it("shows all concrete choices and closing does not retry the action", async () => {
    const execute = vi.fn(async (): Promise<TaskStartResponse> => ({
      outcome: "selection_required",
      selectionRequired: { reason: "policy_requires_selection" },
    }));
    render(<Harness execute={execute} />);

    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    expect(screen.getAllByRole("radio")).toHaveLength(4);
    expect(
      screen.getByRole("radio", {
        name: new RegExp(appI18n.t("executionTargetContinuation.mode_default_branch"), "u"),
      }),
    ).toBeChecked();
    fireEvent.click(screen.getByRole("button", { name: appI18n.t("app.cancel") }));

    await waitFor(() => {
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });
    expect(execute).toHaveBeenCalledTimes(1);
  });

  it("preserves configured custom input, disables duplicate submission, and keeps failures retryable", async () => {
    let rejectContinuation: ((error: Error) => void) | undefined;
    const continuation = new Promise<TaskStartResponse>((_resolve, reject) => {
      rejectContinuation = reject;
    });
    const execute = vi
      .fn<(_selection?: WorkflowExecutionTargetSelection) => Promise<TaskStartResponse>>()
      .mockResolvedValueOnce({
        outcome: "selection_required",
        selectionRequired: {
          reason: "configured_target_unavailable",
          configuredTarget: { mode: "custom_ref", requestedRef: "release/v2" },
          unavailableCause: "invalid_revision",
        },
      })
      .mockReturnValueOnce(continuation)
      .mockResolvedValueOnce({
        outcome: "applied",
        applied: {
          transitionID: "transition-1",
          placementID: "placement-1",
          runID: "run-1",
        },
      });
    render(<Harness execute={execute} />);

    const customRef = await screen.findByRole("textbox", {
      name: appI18n.t("executionTargetContinuation.customRef"),
    });
    expect(customRef).toHaveValue("release/v2");
    fireEvent.change(customRef, { target: { value: "release/v3" } });
    fireEvent.click(
      screen.getByRole("radio", {
        name: new RegExp(appI18n.t("executionTargetContinuation.mode_head"), "u"),
      }),
    );
    fireEvent.click(
      screen.getByRole("radio", {
        name: new RegExp(appI18n.t("executionTargetContinuation.mode_custom_ref"), "u"),
      }),
    );
    expect(
      screen.getByRole("textbox", {
        name: appI18n.t("executionTargetContinuation.customRef"),
      }),
    ).toHaveValue("release/v3");
    const continueButton = screen.getByRole("button", {
      name: appI18n.t("executionTargetContinuation.continue"),
    });
    fireEvent.click(continueButton);
    fireEvent.click(continueButton);

    expect(execute).toHaveBeenCalledTimes(2);
    expect(continueButton).toBeDisabled();
    expect(screen.getByRole("button", { name: appI18n.t("app.cancel") })).toBeDisabled();
    expect(screen.getByRole("button", { name: appI18n.t("app.close") })).toBeDisabled();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    rejectContinuation?.(new Error("materialization failed"));

    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(
      screen.getByRole("textbox", {
        name: appI18n.t("executionTargetContinuation.customRef"),
      }),
    ).toHaveValue("release/v3");
    fireEvent.click(screen.getByRole("button", { name: appI18n.t("app.retry") }));

    await waitFor(() => {
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });
    expect(execute).toHaveBeenNthCalledWith(
      2,
      expect.objectContaining({ mode: "custom_ref", customRef: "release/v3" }),
    );
    expect(execute).toHaveBeenNthCalledWith(
      3,
      expect.objectContaining({ mode: "custom_ref", customRef: "release/v3" }),
    );
  });

  it("closes after an applied action even when post-apply refresh fails", async () => {
    const onAppliedError = vi.fn();
    const execute = vi
      .fn<(_selection?: WorkflowExecutionTargetSelection) => Promise<TaskStartResponse>>()
      .mockResolvedValueOnce({
        outcome: "selection_required",
        selectionRequired: { reason: "policy_requires_selection" },
      })
      .mockResolvedValueOnce({
        outcome: "applied",
        applied: {
          transitionID: "transition-1",
          placementID: "placement-1",
          runID: "run-1",
        },
      });
    render(
      <Harness
        execute={execute}
        onApplied={async () => {
          throw new Error("refresh failed");
        }}
        onAppliedError={onAppliedError}
      />,
    );

    fireEvent.click(
      await screen.findByRole("button", {
        name: appI18n.t("executionTargetContinuation.continue"),
      }),
    );

    await waitFor(() => {
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });
    expect(onAppliedError).toHaveBeenCalledTimes(1);
    expect(execute).toHaveBeenCalledTimes(2);
  });
});

function Harness({
  execute,
  onApplied,
  onAppliedError,
}: Readonly<{
  execute: (selection?: WorkflowExecutionTargetSelection) => Promise<TaskStartResponse>;
  onApplied?: (() => void | Promise<void>) | undefined;
  onAppliedError?: ((error: unknown) => void) | undefined;
}>) {
  const continuation = useExecutionTargetContinuation({
    execute: async (_action, selection) => ({
      kind: "start",
      action: startExecutionTargetAction("task-1"),
      response: await execute(selection),
    }),
    onApplied: onApplied ?? (() => undefined),
    onAppliedError: onAppliedError ?? (() => undefined),
  });
  const startedRef = useRef(false);
  useEffect(() => {
    if (startedRef.current) {
      return;
    }
    startedRef.current = true;
    void continuation.run(startExecutionTargetAction("task-1"));
  }, [continuation]);
  return <ExecutionTargetContinuationDialog continuation={continuation} />;
}
