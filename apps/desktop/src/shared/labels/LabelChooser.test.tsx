import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { act } from "react";
import { afterEach, beforeEach, vi } from "vitest";

import { createLabelFilterState, type LabelFilterAction } from "./labelFilterState";
import { LabelChooser } from "./LabelChooser";

const createdLabelID = "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";
const originalScrollIntoViewDescriptor = Object.getOwnPropertyDescriptor(
  HTMLElement.prototype,
  "scrollIntoView",
);

const hooks = vi.hoisted(() => ({
  catalogLabels: new Array<{ id: string; name: string }>(),
  create: vi.fn(async () => ({ id: createdLabelID, name: "New label" })),
  reorder: vi.fn(async (labelIDs: readonly string[]) => ({
    projectID: "project-id",
    labels: labelIDs.map((id) => hooks.catalogLabels.find((label) => label.id === id)).filter((label) => label !== undefined),
  })),
  reset: vi.fn(),
}));

vi.mock("./projectLabelHooks", () => ({
  useProjectLabelCatalog: () => ({
    data: { labels: hooks.catalogLabels },
    isError: false,
    isPending: false,
    refetch: vi.fn(),
  }),
  useProjectCatalogAuthority: () => ({
    reorder: hooks.reorder,
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

beforeEach(() => {
  hooks.catalogLabels = [];
  hooks.reorder.mockClear();
  Object.defineProperty(HTMLElement.prototype, "scrollIntoView", {
    configurable: true,
    value: vi.fn(),
    writable: true,
  });
});

afterEach(() => {
  vi.restoreAllMocks();
  Object.defineProperty(
    HTMLElement.prototype,
    "scrollIntoView",
    originalScrollIntoViewDescriptor ?? { configurable: true, value: undefined, writable: true },
  );
});

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

  it("shows reorder handles only for empty-search filter Label rows", async () => {
    hooks.catalogLabels = [
      { id: "first-label", name: "First" },
      { id: "second-label", name: "Second" },
    ];
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

    expect(reorderHandles()).toHaveLength(2);
    expect(screen.getAllByRole("listitem")).toHaveLength(3);

    await user.type(screen.getByRole("textbox"), "sec");
    expect(reorderHandles()).toHaveLength(0);
    expect(actions).toEqual([]);
  });

  it("does not expose reorder handles in assignment choosers", async () => {
    hooks.catalogLabels = [
      { id: "first-label", name: "First" },
      { id: "second-label", name: "Second" },
    ];
    const user = userEvent.setup();
    render(
      <LabelChooser
        invocation={{
          kind: "assignment",
          onSelectionChange: vi.fn(),
          selectedLabelIDs: [],
        }}
        trigger={<button type="button">Open assignment chooser</button>}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Open assignment chooser" }));

    expect(reorderHandles()).toHaveLength(0);
  });

  it("preserves catalog-relative order when searching matching Labels", async () => {
    hooks.catalogLabels = [
      { id: "first-label", name: "Zulu" },
      { id: "second-label", name: "Beta" },
      { id: "third-label", name: "Alpha" },
    ];
    const user = userEvent.setup();
    render(
      <LabelChooser
        invocation={{
          kind: "assignment",
          onSelectionChange: vi.fn(),
          selectedLabelIDs: [],
        }}
        trigger={<button type="button">Open assignment chooser</button>}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Open assignment chooser" }));
    await user.type(screen.getByRole("textbox"), "a");

    const rows = screen.getAllByRole("listitem");
    expect(rows[0]).toHaveTextContent("Beta");
    expect(rows[1]).toHaveTextContent("Alpha");
  });

  it("reorders from the handle without selecting the Label and projects the complete lifted row", async () => {
    hooks.catalogLabels = [
      { id: "first-label", name: "First" },
      { id: "second-label", name: "Second" },
      { id: "third-label", name: "Third" },
    ];
    const user = userEvent.setup();
    const actions: LabelFilterAction[] = [];
    mockLabelRowGeometry();
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
    const handles = reorderHandles();
    const secondHandle = handles[1];
    if (secondHandle === undefined) {
      throw new Error("Second Label reorder handle did not render.");
    }
    secondHandle.focus();
    await user.keyboard("[Space]");
    await user.keyboard("[ArrowDown]");

    await waitFor(() => {
      expect(screen.getAllByText("Second")).toHaveLength(2);
    });
    expect(actions).toEqual([]);

    await user.keyboard("[Space]");

    await waitFor(() => {
      expect(hooks.reorder).toHaveBeenCalledWith(["first-label", "third-label", "second-label"]);
    });
  });

  it("cancels a Label drag without requesting a reorder", async () => {
    hooks.catalogLabels = [
      { id: "first-label", name: "First" },
      { id: "second-label", name: "Second" },
      { id: "third-label", name: "Third" },
    ];
    const user = userEvent.setup();
    mockLabelRowGeometry();
    render(
      <LabelChooser
        invocation={{
          kind: "filter",
          onAction: vi.fn(),
          state: createLabelFilterState(),
        }}
        trigger={<button type="button">Open label chooser</button>}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Open label chooser" }));
    const secondHandle = reorderHandles()[1];
    if (secondHandle === undefined) {
      throw new Error("Second Label reorder handle did not render.");
    }
    secondHandle.focus();
    await user.keyboard("[Space]");
    await user.keyboard("[ArrowDown]");
    await waitFor(() => {
      expect(screen.getAllByText("Second")).toHaveLength(2);
    });
    await user.keyboard("[Escape]");

    expect(hooks.reorder).not.toHaveBeenCalled();
  });

  it("registers the overflowing result list as the pointer edge-scrollport", async () => {
    const labels = Array.from({ length: 11 }, (_, index) => ({
      id: `label-${String(index + 1).padStart(2, "0")}`,
      name: `Label ${String(index + 1).padStart(2, "0")}`,
    }));
    hooks.catalogLabels = labels;
    const callbacks = new Map<number, FrameRequestCallback>();
    let nextFrameID = 0;
    vi.spyOn(window, "requestAnimationFrame").mockImplementation((callback) => {
      const frameID = ++nextFrameID;
      callbacks.set(frameID, callback);
      return frameID;
    });
    vi.spyOn(window, "cancelAnimationFrame").mockImplementation((frameID) => {
      callbacks.delete(frameID);
    });
    const originalGetComputedStyle = window.getComputedStyle;
    mockLabelRowGeometry(labels.map((label) => label.name));
    vi.spyOn(window, "getComputedStyle").mockImplementation((element, pseudoElement) => {
      const style = originalGetComputedStyle(element, pseudoElement);
      if (element.getAttribute("role") === "list") {
        Object.defineProperty(style, "overflowY", { configurable: true, value: "auto" });
      }
      return style;
    });
    const view = render(
      <LabelChooser
        invocation={{
          kind: "filter",
          onAction: vi.fn(),
          state: createLabelFilterState(),
        }}
        trigger={<button type="button">Open label chooser</button>}
      />,
    );

    await userEvent.setup().click(screen.getByRole("button", { name: "Open label chooser" }));
    const scrollport = screen.getByRole("list");
    Object.defineProperties(scrollport, {
      clientHeight: { configurable: true, value: 100 },
      scrollHeight: { configurable: true, value: 800 },
      scrollTop: { configurable: true, value: 0, writable: true },
    });
    const handle = reorderHandles()[5];
    if (handle === undefined) {
      throw new Error("Scrollable Label reorder handle did not render.");
    }

    act(() => {
      activatePointerDrag(handle);
    });

    expect(callbacks).toHaveLength(1);
    expect(scrollport.scrollTop).toBe(0);
    const pending = callbacks.entries().next().value;
    if (pending === undefined) {
      throw new Error("Expected a pending Label chooser edge-scroll frame.");
    }
    callbacks.delete(pending[0]);
    act(() => {
      pending[1](16);
    });
    expect(scrollport.scrollTop).toBeGreaterThan(0);

    act(() => {
      cancelPointerDrag();
    });
    view.unmount();
    expect(callbacks).toHaveLength(0);
    expect(hooks.reorder).not.toHaveBeenCalled();
  });
});

function activatePointerDrag(handle: HTMLElement): void {
  fireEvent.pointerDown(handle, { button: 0, clientX: 20, clientY: 50, isPrimary: true, pointerId: 1 });
  fireEvent.pointerMove(document, { buttons: 1, clientX: 20, clientY: 60, isPrimary: true, pointerId: 1 });
  fireEvent.pointerMove(document, { buttons: 1, clientX: 20, clientY: 95, isPrimary: true, pointerId: 1 });
}

function cancelPointerDrag(): void {
  fireEvent.pointerCancel(document, { clientX: 20, clientY: 95, pointerId: 1 });
}

function reorderHandles(): HTMLElement[] {
  return Array.from(document.querySelectorAll<HTMLElement>('button[aria-roledescription="sortable"]'));
}

function mockLabelRowGeometry(labelNames = ["First", "Second", "Third"]): void {
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.getAttribute("role") === "list") {
      return {
        bottom: 100,
        height: 100,
        left: 0,
        right: 240,
        toJSON: () => ({}),
        top: 0,
        width: 240,
        x: 0,
        y: 0,
      };
    }
    const index = labelNames.findIndex((name) => this.textContent.includes(name));
    const top = (index < 0 ? 0 : index) * 40;
    return {
      bottom: top + 32,
      height: 32,
      left: 0,
      right: 240,
      toJSON: () => ({}),
      top,
      width: 240,
      x: 0,
      y: top,
    };
  });
}
