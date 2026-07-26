import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";

import { createLabelFilterState, type LabelFilterAction } from "./labelFilterState";
import { LabelChooser } from "./LabelChooser";

const createdLabelID = "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";

const hooks = vi.hoisted(() => ({
  create: vi.fn(async () => ({ id: createdLabelID, name: "New label" })),
  reset: vi.fn(),
}));

vi.mock("./projectLabelHooks", () => ({
  useProjectLabelCatalog: () => ({
    data: { labels: [] },
    isError: false,
    isPending: false,
    refetch: vi.fn(),
  }),
  useProjectLabelCatalogMutations: () => ({
    create: {
      error: null,
      isError: false,
      isPending: false,
      mutateAsync: hooks.create,
      reset: hooks.reset,
    },
    delete: {
      error: null,
      isError: false,
      isPending: false,
      mutateAsync: vi.fn(),
      reset: hooks.reset,
    },
    rename: {
      error: null,
      isError: false,
      isPending: false,
      mutateAsync: vi.fn(),
      reset: hooks.reset,
    },
  }),
}));

describe("LabelChooser", () => {
  it("creates a filter label through exactly one included-condition cycle", async () => {
    const user = userEvent.setup();
    const actions: LabelFilterAction[] = [];
    render(
      <LabelChooser
        invocation={{
          kind: "filter",
          onAction(action) {
            actions.push(action);
          },
          state: createLabelFilterState(),
        }}
        trigger={<button type="button">Open label chooser</button>}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Open label chooser" }));
    await user.type(screen.getByRole("textbox"), "New label");
    await user.keyboard("{Enter}");

    await waitFor(() => {
      expect(actions).toEqual([{ type: "named.cycle", labelID: createdLabelID }]);
    });
  });
});
