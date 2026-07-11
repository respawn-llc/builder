import { fireEvent, render, screen } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";
import { beforeAll, describe, expect, it, vi } from "vitest";

import { appI18n, initializeI18n } from "../../i18n/setup";
import { ExecutionTargetSelectionDialog } from "./ExecutionTargetSelectionDialog";

describe("ExecutionTargetSelectionDialog", () => {
  beforeAll(async () => {
    await initializeI18n();
  });

  it("submits a server-supported custom ref selection and leaves dismiss side-effect free", () => {
    const onClose = vi.fn();
    const onSubmit = vi.fn();
    render(
      <I18nextProvider i18n={appI18n}>
        <ExecutionTargetSelectionDialog
          onClose={onClose}
          onSubmit={onSubmit}
          requirement={{
            configuredPolicy: { customRef: null, mode: "ask" },
            generation: "generation-1",
            recoveryCause: null,
            source: { commit: "abc123", kind: "named_ref", namedRef: "refs/heads/main" },
            sourceWorkspaceID: "workspace-1",
            supportedSelections: ["none", "custom_ref"],
            taskID: "task-1",
          }}
        />
      </I18nextProvider>,
    );

    fireEvent.pointerDown(screen.getByRole("button", { name: "Execution target" }));
    fireEvent.click(screen.getByRole("menuitemradio", { name: "Custom ref" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Custom ref" }), {
      target: { value: "refs/tags/v1.2.3" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Continue" }));

    expect(onSubmit).toHaveBeenCalledWith({ customRef: "refs/tags/v1.2.3", mode: "custom_ref" });
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
