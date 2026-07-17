import { render, screen, within } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";
import { beforeAll, describe, expect, it, vi } from "vitest";

import { appI18n, initializeI18n } from "@/i18n";
import type { PendingMissingInputDrop } from "./BoardDropActions";
import { MissingInputsDialog } from "./BoardDropDialogs";

describe("MissingInputsDialog", () => {
  beforeAll(async () => {
    await initializeI18n();
  });

  it("keeps many required fields in a bounded field list with reachable actions", () => {
    render(
      <I18nextProvider i18n={appI18n}>
        <MissingInputsDialog
          drop={missingInputDrop}
          onClose={vi.fn()}
          onSubmit={vi.fn()}
          onValueChange={vi.fn()}
        />
      </I18nextProvider>,
    );

    const form = screen.getByTestId("missing-inputs-dialog-form");
    const fieldList = screen.getByTestId("missing-inputs-field-list");
    const actions = screen.getByTestId("missing-inputs-dialog-actions");
    const textareas = within(fieldList).getAllByRole("textbox");

    expect(textareas).toHaveLength(missingInputDrop.fields.length);
    expect(textareas.every((textarea) => textarea.getAttribute("rows") === "2")).toBe(true);
    expect(within(fieldList).queryAllByRole("button")).toHaveLength(0);
    expect(within(actions).getAllByRole("button")).toHaveLength(2);
    expect(form).toContainElement(fieldList);
    expect(form).toContainElement(actions);
  });
});

const fields = Array.from({ length: 12 }, (_, index) => {
  const fieldNumber = String(index + 1);
  return {
    description: `Description ${fieldNumber}`,
    name: `field_${fieldNumber}`,
  };
});

const missingInputDrop: PendingMissingInputDrop = {
  fields,
  targetColumn: {
    assigneeRole: "reviewer",
    groupID: "",
    id: "node-review",
    isBacklog: false,
    isDone: false,
    key: "review",
    kind: "agent",
    name: "Review",
    outputFields: [],
    sortOrder: 1,
    taskCount: 0,
    transitionOutputFields: fields,
  },
  taskID: "task-1",
  values: Object.fromEntries(fields.map((field) => [field.name, ""])),
};
