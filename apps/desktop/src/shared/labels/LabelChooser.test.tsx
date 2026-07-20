import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";

import type { LabelFilterState } from "./labelFilterState";
import { reduceLabelFilterState } from "./labelFilterState";
import { LabelChooser, ProjectLabelsProvider, useProjectCatalogAuthority } from "./index";
import { RpcError } from "@/api";
import { TestAppProviders, createTestServices } from "@/test-support/app-services";
import { installTestStorage } from "@/test-support/storage";

const priorityID = "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";
const reviewID = "942495c2-5958-4959-8445-94046ad74fbd";

describe("LabelChooser", () => {
  beforeEach(() => {
    installTestStorage("localStorage");
  });

  it("searches the loaded catalog and selects a result for a filter invocation", async () => {
    const user = userEvent.setup();
    const services = createTestServices([
      {
        method: "workflow.project.label.list",
        result: {
          catalog: {
            project_id: "project-1",
            labels: [
              { id: priorityID, name: "Priority" },
              { id: reviewID, name: "Review" },
            ],
          },
        },
      },
    ]);

    function Harness() {
      const [state, setState] = useState<LabelFilterState>({
        filter: { kind: "none" },
        namedMode: "any",
      });
      return (
        <TestAppProviders services={services}>
          <ProjectLabelsProvider projectID="project-1">
            <LabelChooser
              invocation={{
                kind: "filter",
                state,
                onAction: (action) => {
                  setState((current) => reduceLabelFilterState(current, action));
                },
              }}
              trigger={<button type="button">Open labels</button>}
            />
          </ProjectLabelsProvider>
        </TestAppProviders>
      );
    }

    render(<Harness />);
    await user.click(screen.getByRole("button", { name: "Open labels" }));

    const search = await screen.findByRole("textbox");
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Priority" })).toBeVisible();
    });
    await user.type(search, "rev");

    expect(screen.queryByRole("button", { name: "Priority" })).not.toBeInTheDocument();
    const review = screen.getByRole("button", { name: "Review" });
    await user.click(review);

    expect(review).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("textbox")).toBeVisible();
  });

  it("exposes named match mode and mutually exclusive unlabeled selection only for filtering", async () => {
    const user = userEvent.setup();
    const services = createTestServices([
      {
        method: "workflow.project.label.list",
        result: {
          catalog: {
            project_id: "project-1",
            labels: [{ id: priorityID, name: "Priority" }],
          },
        },
      },
    ]);

    function Harness() {
      const [state, setState] = useState<LabelFilterState>({
        filter: { kind: "none" },
        namedMode: "any",
      });
      return (
        <TestAppProviders services={services}>
          <ProjectLabelsProvider projectID="project-1">
            <LabelChooser
              invocation={{
                kind: "filter",
                state,
                onAction: (action) => {
                  setState((current) => reduceLabelFilterState(current, action));
                },
              }}
              trigger={<button type="button">Open labels</button>}
            />
          </ProjectLabelsProvider>
        </TestAppProviders>
      );
    }

    render(<Harness />);
    await user.click(screen.getByRole("button", { name: "Open labels" }));
    const [anyMode, allMode] = await screen.findAllByRole("radio");
    if (anyMode === undefined || allMode === undefined) {
      throw new Error("expected both label match modes");
    }
    expect(anyMode).toBeChecked();
    await user.click(allMode);
    expect(allMode).toBeChecked();

    const unlabeled = filterOnlyPressedButton();
    await user.click(unlabeled);
    expect(unlabeled).toHaveAttribute("aria-pressed", "true");
    expect(anyMode).toBeDisabled();
    expect(allMode).toBeDisabled();
  });

  it("creates a non-matching label and immediately selects it for assignment", async () => {
    const user = userEvent.setup();
    const alphaID = "38bf0da7-a3f7-4c15-bc5f-c8fca538e667";
    const staleRead = deferred<unknown>();
    const currentRead = deferred<unknown>();
    const services = createTestServices([
      {
        method: "workflow.project.label.list",
        handler: async (_params, callIndex) =>
          callIndex === 0
            ? {
                catalog: {
                  project_id: "project-1",
                  labels: [],
                },
              }
            : callIndex === 1
              ? staleRead.promise
              : currentRead.promise,
      },
      {
        method: "workflow.project.label.create",
        result: {
          label: { id: alphaID, name: "Alpha" },
        },
      },
    ]);

    function Harness() {
      const [selectedLabelIDs, setSelectedLabelIDs] = useState<readonly string[]>([]);
      return (
        <TestAppProviders services={services}>
          <ProjectLabelsProvider projectID="project-1">
            <RefreshCatalogButton />
            <LabelChooser
              invocation={{
                kind: "assignment",
                selectedLabelIDs,
                onSelectionChange: (labelID, selected) => {
                  setSelectedLabelIDs((current) =>
                    selected ? [...current, labelID] : current.filter((candidate) => candidate !== labelID),
                  );
                },
              }}
              trigger={<button type="button">Open labels</button>}
            />
          </ProjectLabelsProvider>
        </TestAppProviders>
      );
    }

    render(<Harness />);
    await waitFor(() => {
      expect(
        services.transport.calls.filter((call) => call.method === "workflow.project.label.list"),
      ).toHaveLength(1);
    });
    await user.click(screen.getByRole("button", { name: "Refresh catalog" }));
    await waitFor(() => {
      expect(
        services.transport.calls.filter((call) => call.method === "workflow.project.label.list"),
      ).toHaveLength(2);
    });
    await user.click(screen.getByRole("button", { name: "Open labels" }));
    const search = await screen.findByRole("textbox");
    await user.type(search, "alpha");
    await user.keyboard("{Enter}");

    const alpha = await screen.findByRole("button", { name: "Alpha" });
    expect(alpha).toHaveAttribute("aria-pressed", "true");
    expect(screen.queryByRole("radio")).not.toBeInTheDocument();
    expect(filterOnlyPressedButtonOrNull()).toBeNull();
    expect(screen.getByRole("textbox")).toBeVisible();

    staleRead.resolve({
      catalog: {
        project_id: "project-1",
        labels: [],
      },
    });
    await waitFor(() => {
      expect(
        services.transport.calls.filter((call) => call.method === "workflow.project.label.list"),
      ).toHaveLength(3);
    });
    expect(screen.getByRole("button", { name: "Alpha" })).toHaveAttribute("aria-pressed", "true");

    currentRead.resolve({
      catalog: {
        project_id: "project-1",
        labels: [{ id: alphaID, name: "Alpha" }],
      },
    });
  });

  it("keeps search and filter controls outside the bounded label-row scroll area", async () => {
    const user = userEvent.setup();
    const labels = Array.from({ length: 12 }, (_, index) => ({
      id: `00000000-0000-4000-8000-${(index + 1).toString().padStart(12, "0")}`,
      name: `Label ${String(index + 1).padStart(2, "0")}`,
    }));
    const services = createTestServices([
      {
        method: "workflow.project.label.list",
        result: {
          catalog: {
            project_id: "project-1",
            labels,
          },
        },
      },
    ]);

    render(
      <TestAppProviders services={services}>
        <ProjectLabelsProvider projectID="project-1">
          <LabelChooser
            invocation={{
              kind: "filter",
              state: { filter: { kind: "none" }, namedMode: "any" },
              onAction: () => undefined,
            }}
            trigger={<button type="button">Open labels</button>}
          />
        </ProjectLabelsProvider>
      </TestAppProviders>,
    );
    await user.click(screen.getByRole("button", { name: "Open labels" }));

    const results = await screen.findByRole("list");
    expect(within(results).getAllByRole("listitem")).toHaveLength(12);
    expect(within(results).queryByRole("textbox")).not.toBeInTheDocument();
    expect(within(results).queryByRole("radio")).not.toBeInTheDocument();
    expect(screen.getByRole("textbox")).toBeVisible();
    expect(screen.getAllByRole("radio")).toHaveLength(2);
  });

  it("commits an inline rename from the row and keeps the popup open", async () => {
    const user = userEvent.setup();
    const reconciliation = deferred<unknown>();
    const services = createTestServices([
      {
        method: "workflow.project.label.list",
        handler: async (_params, callIndex) =>
          callIndex === 0
            ? {
                catalog: {
                  project_id: "project-1",
                  labels: [{ id: priorityID, name: "Priority" }],
                },
              }
            : reconciliation.promise,
      },
      {
        method: "workflow.project.label.rename",
        result: {
          label: { id: priorityID, name: "Urgent" },
        },
      },
    ]);

    render(
      <TestAppProviders services={services}>
        <ProjectLabelsProvider projectID="project-1">
          <LabelChooser
            invocation={{
              kind: "assignment",
              selectedLabelIDs: [],
              onSelectionChange: () => undefined,
            }}
            trigger={<button type="button">Open labels</button>}
          />
        </ProjectLabelsProvider>
      </TestAppProviders>,
    );
    await user.click(screen.getByRole("button", { name: "Open labels" }));
    await screen.findByRole("button", { name: "Priority" });
    await user.click(labelActionButtons("Priority").rename);
    const rename = renameTextbox();
    await user.clear(rename);
    await user.type(rename, "Urgent{Enter}");

    expect(await screen.findByRole("button", { name: "Urgent" })).toBeVisible();
    expect(searchTextbox()).toBeVisible();

    reconciliation.resolve({
      catalog: {
        project_id: "project-1",
        labels: [{ id: priorityID, name: "Urgent" }],
      },
    });
  });

  it("cancels an inline rename with Escape without closing the popup", async () => {
    const user = userEvent.setup();
    const services = createTestServices([
      {
        method: "workflow.project.label.list",
        result: {
          catalog: {
            project_id: "project-1",
            labels: [{ id: priorityID, name: "Priority" }],
          },
        },
      },
    ]);

    render(
      <TestAppProviders services={services}>
        <ProjectLabelsProvider projectID="project-1">
          <LabelChooser
            invocation={{
              kind: "assignment",
              selectedLabelIDs: [],
              onSelectionChange: () => undefined,
            }}
            trigger={<button type="button">Open labels</button>}
          />
        </ProjectLabelsProvider>
      </TestAppProviders>,
    );
    await user.click(screen.getByRole("button", { name: "Open labels" }));
    await screen.findByRole("button", { name: "Priority" });
    await user.click(labelActionButtons("Priority").rename);
    const rename = renameTextbox();
    await user.clear(rename);
    await user.type(rename, "Discarded");
    await user.keyboard("{Escape}");

    expect(within(labelList()).queryByRole("textbox")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Priority" })).toBeVisible();
    expect(searchTextbox()).toBeVisible();
    expect(
      services.transport.calls.filter((call) => call.method === "workflow.project.label.rename"),
    ).toHaveLength(0);
  });

  it("keeps an invalid rename editable and explains a typed conflict inline", async () => {
    const user = userEvent.setup();
    const services = createTestServices([
      {
        method: "workflow.project.label.list",
        result: {
          catalog: {
            project_id: "project-1",
            labels: [{ id: priorityID, name: "Priority" }],
          },
        },
      },
      {
        method: "workflow.project.label.rename",
        error: new RpcError({
          code: -32000,
          message: "server wording must not drive client behavior",
          method: "workflow.project.label.rename",
          data: {
            type: "workflow_label_error",
            reason: "name_conflict",
            project_id: "project-1",
          },
        }),
      },
    ]);

    render(
      <TestAppProviders services={services}>
        <ProjectLabelsProvider projectID="project-1">
          <LabelChooser
            invocation={{
              kind: "assignment",
              selectedLabelIDs: [],
              onSelectionChange: () => undefined,
            }}
            trigger={<button type="button">Open labels</button>}
          />
        </ProjectLabelsProvider>
      </TestAppProviders>,
    );
    await user.click(screen.getByRole("button", { name: "Open labels" }));
    await screen.findByRole("button", { name: "Priority" });
    await user.click(labelActionButtons("Priority").rename);
    const rename = renameTextbox();
    await user.clear(rename);
    await user.type(rename, "Review{Enter}");

    const alert = await screen.findByRole("alert");
    expect(alert).not.toBeEmptyDOMElement();
    expect(alert).not.toHaveTextContent("server wording must not drive client behavior");
    expect(rename).toBeEnabled();
    expect(rename).toHaveValue("Review");
  });

  it("confirms deletion and removes the row and assignment before reconciliation", async () => {
    const user = userEvent.setup();
    const staleRead = deferred<unknown>();
    const currentRead = deferred<unknown>();
    const services = createTestServices([
      {
        method: "workflow.project.label.list",
        handler: async (_params, callIndex) =>
          callIndex === 0
            ? {
                catalog: {
                  project_id: "project-1",
                  labels: [{ id: priorityID, name: "Priority" }],
                },
              }
            : callIndex === 1
              ? staleRead.promise
              : currentRead.promise,
      },
      {
        method: "workflow.project.label.delete",
        result: { label_id: priorityID },
      },
    ]);

    function Harness() {
      const [selectedLabelIDs, setSelectedLabelIDs] = useState<readonly string[]>([priorityID]);
      return (
        <TestAppProviders services={services}>
          <ProjectLabelsProvider projectID="project-1">
            <RefreshCatalogButton />
            <LabelChooser
              invocation={{
                kind: "assignment",
                selectedLabelIDs,
                onSelectionChange: (labelID, selected) => {
                  setSelectedLabelIDs((current) =>
                    selected ? [...current, labelID] : current.filter((candidate) => candidate !== labelID),
                  );
                },
              }}
              trigger={<button type="button">Open labels</button>}
            />
            <output aria-label="Selected labels">{selectedLabelIDs.join(",")}</output>
          </ProjectLabelsProvider>
        </TestAppProviders>
      );
    }

    render(<Harness />);
    await waitFor(() => {
      expect(
        services.transport.calls.filter((call) => call.method === "workflow.project.label.list"),
      ).toHaveLength(1);
    });
    await user.click(screen.getByRole("button", { name: "Refresh catalog" }));
    await waitFor(() => {
      expect(
        services.transport.calls.filter((call) => call.method === "workflow.project.label.list"),
      ).toHaveLength(2);
    });
    await user.click(screen.getByRole("button", { name: "Open labels" }));
    await screen.findByRole("button", { name: "Priority" });
    await user.click(labelActionButtons("Priority").delete);
    const confirmation = screen.getByRole("group");
    await user.click(confirmationButtons(confirmation).confirm);

    await waitFor(() => {
      expect(screen.queryByRole("button", { name: "Priority" })).not.toBeInTheDocument();
    });
    expect(screen.getByRole("status", { name: "Selected labels" })).toHaveTextContent("");
    expect(searchTextbox()).toBeVisible();

    staleRead.resolve({
      catalog: {
        project_id: "project-1",
        labels: [{ id: priorityID, name: "Priority" }],
      },
    });
    await waitFor(() => {
      expect(
        services.transport.calls.filter((call) => call.method === "workflow.project.label.list"),
      ).toHaveLength(3);
    });
    expect(screen.queryByRole("button", { name: "Priority" })).not.toBeInTheDocument();

    currentRead.resolve({
      catalog: {
        project_id: "project-1",
        labels: [],
      },
    });
  });

  it("keeps search and selection available while disabling creation at 100 labels", async () => {
    const user = userEvent.setup();
    const labels = Array.from({ length: 100 }, (_, index) => ({
      id: `00000000-0000-4000-8000-${(index + 1).toString().padStart(12, "0")}`,
      name: `Label ${String(index + 1).padStart(3, "0")}`,
    }));
    const services = createTestServices([
      {
        method: "workflow.project.label.list",
        result: {
          catalog: {
            project_id: "project-1",
            labels,
          },
        },
      },
    ]);

    function Harness() {
      const [selectedLabelIDs, setSelectedLabelIDs] = useState<readonly string[]>([]);
      return (
        <TestAppProviders services={services}>
          <ProjectLabelsProvider projectID="project-1">
            <LabelChooser
              invocation={{
                kind: "assignment",
                selectedLabelIDs,
                onSelectionChange: (labelID, selected) => {
                  setSelectedLabelIDs(selected ? [labelID] : []);
                },
              }}
              trigger={<button type="button">Open labels</button>}
            />
          </ProjectLabelsProvider>
        </TestAppProviders>
      );
    }

    render(<Harness />);
    await user.click(screen.getByRole("button", { name: "Open labels" }));
    const search = await screen.findByRole("textbox");
    fireEvent.change(search, { target: { value: "New label" } });

    const create = createChoiceButton();
    expect(create).toBeDisabled();
    expect(create).toHaveAccessibleDescription();

    fireEvent.change(search, { target: { value: "Label 001" } });
    const existing = screen.getByRole("button", { name: "Label 001" });
    await user.click(existing);
    expect(existing).toHaveAttribute("aria-pressed", "true");
    expect(
      services.transport.calls.filter((call) => call.method === "workflow.project.label.create"),
    ).toHaveLength(0);
  });

  it("closes on click-away or Escape and discards an uncommitted rename", async () => {
    const user = userEvent.setup();
    const services = createTestServices([
      {
        method: "workflow.project.label.list",
        result: {
          catalog: {
            project_id: "project-1",
            labels: [{ id: priorityID, name: "Priority" }],
          },
        },
      },
    ]);

    render(
      <TestAppProviders services={services}>
        <ProjectLabelsProvider projectID="project-1">
          <LabelChooser
            invocation={{
              kind: "assignment",
              selectedLabelIDs: [],
              onSelectionChange: () => undefined,
            }}
            trigger={<button type="button">Open labels</button>}
          />
        </ProjectLabelsProvider>
      </TestAppProviders>,
    );
    const trigger = screen.getByRole("button", { name: "Open labels" });
    await user.click(trigger);
    await screen.findByRole("button", { name: "Priority" });
    await user.click(labelActionButtons("Priority").rename);
    const rename = renameTextbox();
    await user.clear(rename);
    await user.type(rename, "Discarded");
    await user.click(document.body);

    expect(trigger).toHaveAttribute("aria-expanded", "false");
    await user.click(trigger);
    expect(await screen.findByRole("button", { name: "Priority" })).toBeVisible();
    expect(within(labelList()).queryByRole("textbox")).not.toBeInTheDocument();

    await user.keyboard("{Escape}");
    expect(trigger).toHaveAttribute("aria-expanded", "false");
  });

  it("keeps catalog load failures actionable through Retry", async () => {
    const user = userEvent.setup();
    const services = createTestServices([
      {
        method: "workflow.project.label.list",
        handler: (_params, callIndex) => {
          if (callIndex === 0) {
            throw new Error("offline");
          }
          return {
            catalog: {
              project_id: "project-1",
              labels: [{ id: priorityID, name: "Priority" }],
            },
          };
        },
      },
    ]);

    render(
      <TestAppProviders services={services}>
        <ProjectLabelsProvider projectID="project-1">
          <LabelChooser
            invocation={{
              kind: "assignment",
              selectedLabelIDs: [],
              onSelectionChange: () => undefined,
            }}
            trigger={<button type="button">Open labels</button>}
          />
        </ProjectLabelsProvider>
      </TestAppProviders>,
    );
    await user.click(screen.getByRole("button", { name: "Open labels" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button"));

    expect(await screen.findByRole("button", { name: "Priority" })).toBeVisible();
  });

  it("uses prepared Unicode text for exact-match search instead of offering a duplicate create", async () => {
    const user = userEvent.setup();
    const services = createTestServices([
      {
        method: "workflow.project.label.list",
        result: {
          catalog: {
            project_id: "project-1",
            labels: [
              { id: priorityID, name: "Priority" },
              { id: reviewID, name: "Straße" },
            ],
          },
        },
      },
    ]);

    render(
      <TestAppProviders services={services}>
        <ProjectLabelsProvider projectID="project-1">
          <LabelChooser
            invocation={{
              kind: "assignment",
              selectedLabelIDs: [],
              onSelectionChange: () => undefined,
            }}
            trigger={<button type="button">Open labels</button>}
          />
        </ProjectLabelsProvider>
      </TestAppProviders>,
    );
    await user.click(screen.getByRole("button", { name: "Open labels" }));
    const search = await screen.findByRole("textbox");
    await user.type(search, " priority ");
    expect(screen.getByRole("button", { name: "Priority" })).toBeVisible();
    expect(screen.queryByText(/Create/)).not.toBeInTheDocument();

    await user.clear(search);
    await user.type(search, "STRASSE");
    expect(screen.getByRole("button", { name: "Straße" })).toBeVisible();
    expect(screen.queryByText(/Create/)).not.toBeInTheDocument();
  });

  it("keeps the newest returned rename when an older catalog read settles later", async () => {
    const user = userEvent.setup();
    const staleRead = deferred<unknown>();
    const currentRead = deferred<unknown>();
    const services = createTestServices([
      {
        method: "workflow.project.label.list",
        handler: async (_params, callIndex) =>
          callIndex === 0
            ? {
                catalog: {
                  project_id: "project-1",
                  labels: [{ id: priorityID, name: "Priority" }],
                },
              }
            : callIndex === 1
              ? staleRead.promise
              : currentRead.promise,
      },
      {
        method: "workflow.project.label.rename",
        handler: (_params, callIndex) => ({
          label: {
            id: priorityID,
            name: callIndex === 0 ? "Urgent" : "Critical",
          },
        }),
      },
    ]);

    render(
      <TestAppProviders services={services}>
        <ProjectLabelsProvider projectID="project-1">
          <RefreshCatalogButton />
          <LabelChooser
            invocation={{
              kind: "assignment",
              selectedLabelIDs: [],
              onSelectionChange: () => undefined,
            }}
            trigger={<button type="button">Open labels</button>}
          />
        </ProjectLabelsProvider>
      </TestAppProviders>,
    );
    await waitFor(() => {
      expect(
        services.transport.calls.filter((call) => call.method === "workflow.project.label.list"),
      ).toHaveLength(1);
    });
    await user.click(screen.getByRole("button", { name: "Refresh catalog" }));
    await waitFor(() => {
      expect(
        services.transport.calls.filter((call) => call.method === "workflow.project.label.list"),
      ).toHaveLength(2);
    });
    await user.click(screen.getByRole("button", { name: "Open labels" }));

    await renameVisibleLabel(user, "Priority", "Urgent");
    expect(await screen.findByRole("button", { name: "Urgent" })).toBeVisible();
    await renameVisibleLabel(user, "Urgent", "Critical");
    expect(await screen.findByRole("button", { name: "Critical" })).toBeVisible();

    staleRead.resolve({
      catalog: {
        project_id: "project-1",
        labels: [{ id: priorityID, name: "Priority" }],
      },
    });
    await waitFor(() => {
      expect(
        services.transport.calls.filter((call) => call.method === "workflow.project.label.list"),
      ).toHaveLength(3);
    });
    expect(screen.getByRole("button", { name: "Critical" })).toBeVisible();

    currentRead.resolve({
      catalog: {
        project_id: "project-1",
        labels: [{ id: priorityID, name: "Critical" }],
      },
    });
  });

  it("keeps row selection separate from keyboard-reachable rename and delete actions", async () => {
    const user = userEvent.setup();
    const onSelectionChange = vi.fn();
    const services = createTestServices([
      {
        method: "workflow.project.label.list",
        result: {
          catalog: {
            project_id: "project-1",
            labels: [{ id: priorityID, name: "Priority" }],
          },
        },
      },
    ]);

    render(
      <TestAppProviders services={services}>
        <ProjectLabelsProvider projectID="project-1">
          <LabelChooser
            invocation={{
              kind: "assignment",
              selectedLabelIDs: [],
              onSelectionChange,
            }}
            trigger={<button type="button">Open labels</button>}
          />
        </ProjectLabelsProvider>
      </TestAppProviders>,
    );
    await user.click(screen.getByRole("button", { name: "Open labels" }));
    await screen.findByRole("button", { name: "Priority" });
    const renameAction = labelActionButtons("Priority").rename;
    act(() => {
      renameAction.focus();
    });
    await user.keyboard("{Enter}");
    expect(renameTextbox()).toBeVisible();
    expect(onSelectionChange).not.toHaveBeenCalled();

    const renameItem = within(labelList()).getByRole("listitem");
    const renameButtons = within(renameItem).getAllByRole("button");
    const cancelRename = renameButtons.at(-1);
    if (cancelRename === undefined) {
      throw new Error("rename editor is missing its cancel action");
    }
    await user.click(cancelRename);
    const deleteAction = labelActionButtons("Priority").delete;
    act(() => {
      deleteAction.focus();
    });
    await user.keyboard("{Enter}");
    expect(screen.getByRole("group")).toBeVisible();
    expect(onSelectionChange).not.toHaveBeenCalled();
  });

  it("keeps a failed delete confirmation open with an actionable retry", async () => {
    const user = userEvent.setup();
    const services = createTestServices([
      {
        method: "workflow.project.label.list",
        result: {
          catalog: {
            project_id: "project-1",
            labels: [{ id: priorityID, name: "Priority" }],
          },
        },
      },
      {
        method: "workflow.project.label.delete",
        error: new RpcError({
          code: -32000,
          message: "server wording must not drive client behavior",
          method: "workflow.project.label.delete",
          data: {
            type: "workflow_label_error",
            reason: "label_not_found",
            label_id: priorityID,
          },
        }),
      },
    ]);

    render(
      <TestAppProviders services={services}>
        <ProjectLabelsProvider projectID="project-1">
          <LabelChooser
            invocation={{
              kind: "assignment",
              selectedLabelIDs: [],
              onSelectionChange: () => undefined,
            }}
            trigger={<button type="button">Open labels</button>}
          />
        </ProjectLabelsProvider>
      </TestAppProviders>,
    );
    await user.click(screen.getByRole("button", { name: "Open labels" }));
    await screen.findByRole("button", { name: "Priority" });
    await user.click(labelActionButtons("Priority").delete);
    const confirmation = screen.getByRole("group");
    await user.click(confirmationButtons(confirmation).confirm);

    const alert = await within(confirmation).findByRole("alert");
    expect(alert).not.toBeEmptyDOMElement();
    expect(alert).not.toHaveTextContent("server wording must not drive client behavior");
    expect(confirmationButtons(confirmation).confirm).toBeEnabled();
  });
});

function deferred<T>(): Readonly<{
  promise: Promise<T>;
  resolve(value: T): void;
}> {
  let resolve: ((value: T) => void) | null = null;
  const promise = new Promise<T>((nextResolve) => {
    resolve = nextResolve;
  });
  return {
    promise,
    resolve(value: T): void {
      resolve?.(value);
    },
  };
}

function RefreshCatalogButton() {
  const authority = useProjectCatalogAuthority();
  return (
    <button
      onClick={() => {
        authority.requestRefresh();
      }}
      type="button"
    >
      Refresh catalog
    </button>
  );
}

async function renameVisibleLabel(
  user: ReturnType<typeof userEvent.setup>,
  currentName: string,
  nextName: string,
): Promise<void> {
  await user.click(labelActionButtons(currentName).rename);
  const rename = renameTextbox();
  await user.clear(rename);
  await user.type(rename, `${nextName}{Enter}`);
}

function labelList(): HTMLElement {
  return screen.getByRole("list");
}

function searchTextbox(): HTMLElement {
  const list = labelList();
  const textbox = screen.getAllByRole("textbox").find((candidate) => !list.contains(candidate));
  if (textbox === undefined) {
    throw new Error("label chooser search field is missing");
  }
  return textbox;
}

function renameTextbox(): HTMLElement {
  return within(labelList()).getByRole("textbox");
}

function labelActionButtons(labelName: string): Readonly<{
  rename: HTMLButtonElement;
  delete: HTMLButtonElement;
}> {
  const selection = screen.getByRole("button", { name: labelName });
  const item = labelRowItem(labelName);
  const actions = within(item)
    .getAllByRole("button")
    .filter((button) => button !== selection);
  const rename = actions[0];
  const deleteAction = actions[1];
  if (!(rename instanceof HTMLButtonElement) || !(deleteAction instanceof HTMLButtonElement)) {
    throw new Error(`label row "${labelName}" is missing rename or delete actions`);
  }
  return { rename, delete: deleteAction };
}

function createChoiceButton(): HTMLButtonElement {
  const items = within(labelList()).getAllByRole("listitem");
  const createItem = items.at(-1);
  if (createItem === undefined) {
    throw new Error("label chooser create row is missing");
  }
  const button = within(createItem).getByRole("button");
  if (!(button instanceof HTMLButtonElement)) {
    throw new Error("label chooser create control is not a button");
  }
  return button;
}

function filterOnlyPressedButton(): HTMLButtonElement {
  const button = filterOnlyPressedButtonOrNull();
  if (button === null) {
    throw new Error("filter-only unlabeled choice is missing");
  }
  return button;
}

function filterOnlyPressedButtonOrNull(): HTMLButtonElement | null {
  const dialog = screen.getByRole("dialog");
  const list = screen.queryByRole("list");
  const dialogButtons = [
    ...within(dialog).queryAllByRole("button", { pressed: false }),
    ...within(dialog).queryAllByRole("button", { pressed: true }),
  ];
  const listButtons =
    list === null
      ? []
      : [
          ...within(list).queryAllByRole("button", { pressed: false }),
          ...within(list).queryAllByRole("button", { pressed: true }),
        ];
  const buttons = dialogButtons.filter(
    (button): button is HTMLButtonElement =>
      button instanceof HTMLButtonElement && !listButtons.includes(button),
  );
  if (buttons.length > 1) {
    throw new Error("label chooser rendered multiple filter-only pressed controls");
  }
  return buttons[0] ?? null;
}

function labelRowItem(labelName: string): HTMLElement {
  const item = within(labelList())
    .getAllByRole("listitem")
    .find((candidate) => within(candidate).queryByRole("button", { name: labelName }) !== null);
  if (item === undefined) {
    throw new Error(`label row "${labelName}" is missing its list item`);
  }
  return item;
}

function confirmationButtons(group: HTMLElement): Readonly<{
  cancel: HTMLButtonElement;
  confirm: HTMLButtonElement;
}> {
  const buttons = within(group).getAllByRole("button");
  const cancel = buttons[0];
  const confirm = buttons[1];
  if (!(cancel instanceof HTMLButtonElement) || !(confirm instanceof HTMLButtonElement)) {
    throw new Error("label deletion confirmation is missing cancel or confirm actions");
  }
  return { cancel, confirm };
}
