import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type * as AppFacade from "@/app-facade";
import { createLabelFilterState, type LabelFilterAction } from "./labelFilterState";
import { LabelChooser } from "./LabelChooser";
import type * as ProjectLabelHooks from "./projectLabelHooks";

const createdLabelID = "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";

Object.defineProperty(HTMLElement.prototype, "scrollIntoView", {
  configurable: true,
  value: vi.fn(),
  writable: true,
});
Object.defineProperty(window, "matchMedia", {
  configurable: true,
  value: vi.fn((query: string): MediaQueryList => ({
    addEventListener: vi.fn(),
    addListener: vi.fn(),
    dispatchEvent: vi.fn(),
    matches: false,
    media: query,
    onchange: null,
    removeEventListener: vi.fn(),
    removeListener: vi.fn(),
  })),
});

const hooks = vi.hoisted(() => ({
  catalog: {
    data: {
      projectID: "project-1",
      labels: [
        { id: "38bf0da7-a3f7-4c15-bc5f-c8fca538e667", name: "Alpha" },
        { id: "942495c2-5958-4959-8445-94046ad74fbd", name: "Beta" },
      ],
    },
    isError: false,
    isPending: false,
    refetch: vi.fn(),
  },
  create: vi.fn(async () => ({ id: createdLabelID, name: "New label" })),
  createPending: false,
  reorder: vi.fn(async () => hooks.catalog.data),
  reorderPending: false,
  reset: vi.fn(),
}));
const statusPush = vi.hoisted(() => vi.fn());

vi.mock("@/app-facade", async (importOriginal) => {
  const actual = await importOriginal<typeof AppFacade>();
  return {
    ...actual,
    useAppServices: () => ({ nativeBridge: { capabilities: { platform: "macos" } } }),
    useStatusController: () => ({ dismiss: vi.fn(), push: statusPush }),
  };
});

vi.mock("./projectLabelHooks", async (importOriginal) => {
  const actual = await importOriginal<typeof ProjectLabelHooks>();
  return {
    ...actual,
    useProjectLabelCatalog: () => hooks.catalog,
    useProjectLabelCatalogMutations: () => ({
      create: {
        error: null,
        isError: false,
        isPending: hooks.createPending,
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
      reorder: {
        error: null,
        isError: false,
        isPending: hooks.reorderPending,
        mutateAsync: hooks.reorder,
        reset: hooks.reset,
      },
    }),
  };
});

vi.mock("react-i18next", () => {
  const translations: Readonly<Record<string, (values?: Readonly<Record<string, string>>) => string>> = {
    "labels.create": (values) => `Create “${values?.name ?? ""}”`,
    "labels.delete": (values) => `Delete ${values?.name ?? ""}`.trim(),
    "labels.filterConditionNeutral": () => "Neutral label condition",
    "labels.rename": (values) => `Rename ${values?.name ?? ""}`.trim(),
    "labels.reorder": (values) => `Reorder ${values?.name ?? ""}`.trim(),
    "labels.search": () => "Search or create labels",
    "labels.unlabeled": () => "No labels",
    "workflowEditor.deleteParameter": () => "Delete parameter",
    "workflowEditor.reorderParameter": () => "Reorder parameter",
  };
  return {
    useTranslation: () => ({
      t(key: string, values?: Readonly<Record<string, string>>): string {
        return translations[key]?.(values) ?? key;
      },
    }),
  };
});

beforeEach(() => {
  hooks.catalog.data = {
    projectID: "project-1",
    labels: [
      { id: "38bf0da7-a3f7-4c15-bc5f-c8fca538e667", name: "Alpha" },
      { id: "942495c2-5958-4959-8445-94046ad74fbd", name: "Beta" },
    ],
  };
  hooks.createPending = false;
  hooks.reorderPending = false;
  hooks.reorder.mockClear();
  hooks.create.mockClear();
  statusPush.mockClear();
});

afterEach(() => {
  Reflect.deleteProperty(Element.prototype, "setPointerCapture");
  Reflect.deleteProperty(Element.prototype, "releasePointerCapture");
});

describe("LabelChooser", () => {
  it("creates a filter label without changing the active filter", async () => {
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
      expect(actions).toEqual([]);
    });
  });

  it("shows handle-only reorder controls in catalog order and hides them during search", async () => {
    const user = userEvent.setup();
    render(
      <LabelChooser
        invocation={{ kind: "filter", onAction: vi.fn(), state: createLabelFilterState() }}
        trigger={<button type="button">Open label chooser</button>}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Open label chooser" }));

    expect(screen.getByRole("button", { name: "No labels" })).toBeInTheDocument();
    const reorderAlpha = screen.getByRole("button", { name: "Reorder Alpha" });
    expect(reorderAlpha).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reorder Beta" })).toBeInTheDocument();
    expect(reorderAlpha.compareDocumentPosition(screen.getByRole("button", { name: /^Alpha/ }))).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING,
    );

    await user.type(screen.getByRole("textbox", { name: "Search or create labels" }), "Beta");
    expect(screen.queryByRole("button", { name: "Reorder Beta" })).not.toBeInTheDocument();
  });

  it("keeps selection available while local reorder controls are pending", async () => {
    const user = userEvent.setup();
    hooks.reorderPending = true;
    const onAction = vi.fn();
    render(
      <LabelChooser
        invocation={{ kind: "filter", onAction, state: createLabelFilterState() }}
        trigger={<button type="button">Open label chooser</button>}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Open label chooser" }));

    expect(screen.getByRole("button", { name: "Reorder Alpha" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Rename Alpha" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Delete Alpha" })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: /^Alpha/ }));
    await user.type(screen.getByRole("textbox", { name: "Search or create labels" }), "Gamma");
    expect(screen.getByRole("button", { name: "Create “Gamma”" })).toBeDisabled();
    expect(onAction).toHaveBeenCalledWith({
      labelID: "38bf0da7-a3f7-4c15-bc5f-c8fca538e667",
      type: "named.cycle",
    });
  });

  it("keeps the keyboard highlight aligned with the fixed unlabeled row", async () => {
    const user = userEvent.setup();
    const onAction = vi.fn();
    render(
      <LabelChooser
        invocation={{ kind: "filter", onAction, state: createLabelFilterState() }}
        trigger={<button type="button">Open label chooser</button>}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Open label chooser" }));
    await user.keyboard("{ArrowDown}{ArrowDown}{Enter}");

    expect(onAction).toHaveBeenCalledWith({
      labelID: "38bf0da7-a3f7-4c15-bc5f-c8fca538e667",
      type: "named.cycle",
    });
  });

  it("selects an assignment-created label only after creation succeeds", async () => {
    const user = userEvent.setup();
    const onSelectionChange = vi.fn();
    render(
      <LabelChooser
        invocation={{ kind: "assignment", onSelectionChange, selectedLabelIDs: [] }}
        trigger={<button type="button">Open assignment chooser</button>}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Open assignment chooser" }));
    await user.type(screen.getByRole("textbox", { name: "Search or create labels" }), "New label");
    await user.keyboard("{Enter}");

    await waitFor(() => {
      expect(onSelectionChange).toHaveBeenCalledWith(createdLabelID, true);
    });
  });

  it("keeps selection and search available while disabling creation at the 100-Label bound", async () => {
    const user = userEvent.setup();
    hooks.catalog.data = {
      projectID: "project-1",
      labels: Array.from({ length: 100 }, (_, index) => ({
        id: `00000000-0000-4000-8000-${String(index).padStart(12, "0")}`,
        name: `Label ${String(index)}`,
      })),
    };
    render(
      <LabelChooser
        invocation={{ kind: "assignment", onSelectionChange: vi.fn(), selectedLabelIDs: [] }}
        trigger={<button type="button">Open assignment chooser</button>}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Open assignment chooser" }));
    await user.type(screen.getByRole("textbox", { name: "Search or create labels" }), "New label");

    expect(screen.getByRole("button", { name: "labels.catalogLimit" })).toBeDisabled();
    expect(screen.getByRole("textbox", { name: "Search or create labels" })).toBeEnabled();
  });

  it("omits reorder handles outside an eligible filter catalog", async () => {
    const user = userEvent.setup();
    const view = render(
      <LabelChooser
        invocation={{ kind: "assignment", onSelectionChange: vi.fn(), selectedLabelIDs: [] }}
        trigger={<button type="button">Open assignment chooser</button>}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Open assignment chooser" }));
    expect(screen.queryByRole("button", { name: "Reorder Alpha" })).not.toBeInTheDocument();
    view.unmount();
    hooks.catalog.data = {
      projectID: "project-1",
      labels: [{ id: "38bf0da7-a3f7-4c15-bc5f-c8fca538e667", name: "Alpha" }],
    };
    render(
      <LabelChooser
        invocation={{ kind: "filter", onAction: vi.fn(), state: createLabelFilterState() }}
        trigger={<button type="button">Open one-label chooser</button>}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Open one-label chooser" }));
    expect(screen.queryByRole("button", { name: "Reorder Alpha" })).not.toBeInTheDocument();
  });

  it("commits a keyboard reorder through the Label handle", async () => {
    const user = userEvent.setup();
    render(
      <LabelChooser
        invocation={{ kind: "filter", onAction: vi.fn(), state: createLabelFilterState() }}
        trigger={<button type="button">Open label chooser</button>}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Open label chooser" }));

    mockLabelRowRects();
    const handle = screen.getByRole("button", { name: "Reorder Alpha" });
    handle.focus();
    fireEvent.keyDown(handle, { code: "Space", key: " " });
    await new Promise<void>((resolve) => setTimeout(resolve, 0));
    fireEvent.keyDown(handle, { code: "ArrowDown", key: "ArrowDown" });
    fireEvent.keyDown(handle, { code: "Space", key: " " });

    await waitFor(() => {
      expect(hooks.reorder).toHaveBeenCalledWith([
        "942495c2-5958-4959-8445-94046ad74fbd",
        "38bf0da7-a3f7-4c15-bc5f-c8fca538e667",
      ]);
    });
  });

  it("surfaces a reorder failure through the status controller", async () => {
    const user = userEvent.setup();
    hooks.reorder.mockRejectedValueOnce(new Error("save failed"));
    render(
      <LabelChooser
        invocation={{ kind: "filter", onAction: vi.fn(), state: createLabelFilterState() }}
        trigger={<button type="button">Open label chooser</button>}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Open label chooser" }));
    mockLabelRowRects();
    const handle = screen.getByRole("button", { name: "Reorder Alpha" });
    handle.focus();
    fireEvent.keyDown(handle, { code: "Space", key: " " });
    await new Promise<void>((resolve) => setTimeout(resolve, 0));
    fireEvent.keyDown(handle, { code: "ArrowDown", key: "ArrowDown" });
    fireEvent.keyDown(handle, { code: "Space", key: " " });

    await waitFor(() => {
      expect(statusPush).toHaveBeenCalledWith(
        expect.objectContaining({ body: "save failed", tone: "danger" }),
      );
    });
  });

  it("commits a pointer reorder from the Label handle", async () => {
    const user = userEvent.setup();
    render(
      <LabelChooser
        invocation={{ kind: "filter", onAction: vi.fn(), state: createLabelFilterState() }}
        trigger={<button type="button">Open label chooser</button>}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Open label chooser" }));
    mockLabelRowRects();
    const setPointerCapture = vi.fn();
    const releasePointerCapture = vi.fn();
    Object.defineProperty(Element.prototype, "setPointerCapture", {
      configurable: true,
      value: setPointerCapture,
    });
    Object.defineProperty(Element.prototype, "releasePointerCapture", {
      configurable: true,
      value: releasePointerCapture,
    });

    const handle = screen.getByRole("button", { name: "Reorder Alpha" });
    fireEvent.pointerDown(handle, {
      buttons: 1,
      clientX: 10,
      clientY: 10,
      isPrimary: true,
      pointerId: 1,
      pointerType: "mouse",
    });
    await new Promise<void>((resolve) => setTimeout(resolve, 0));
    fireEvent.pointerMove(handle, {
      buttons: 1,
      clientX: 10,
      clientY: 20,
      isPrimary: true,
      pointerId: 1,
      pointerType: "mouse",
    });
    await new Promise<void>((resolve) => setTimeout(resolve, 0));
    fireEvent.pointerMove(handle, {
      buttons: 1,
      clientX: 10,
      clientY: 100,
      isPrimary: true,
      pointerId: 1,
      pointerType: "mouse",
    });
    await new Promise<void>((resolve) => setTimeout(resolve, 0));
    expect(screen.getAllByRole("button", { hidden: true, name: "Reorder Alpha" })).toHaveLength(2);
    fireEvent.pointerUp(handle, { isPrimary: true, pointerId: 1, pointerType: "mouse" });

    await waitFor(() => {
      expect(hooks.reorder).toHaveBeenCalledWith([
        "942495c2-5958-4959-8445-94046ad74fbd",
        "38bf0da7-a3f7-4c15-bc5f-c8fca538e667",
      ]);
    });
    await waitFor(() => {
      expect(screen.getAllByRole("button", { hidden: true, name: "Reorder Alpha" })).toHaveLength(1);
    });
  });
});

function mockLabelRowRects(): void {
  ["38bf0da7-a3f7-4c15-bc5f-c8fca538e667", "942495c2-5958-4959-8445-94046ad74fbd"].forEach(
    (labelID, index) => {
      const sortableRow = screen.getByTestId(`label-reorder-item-${labelID}`);
      const top = index * 40;
      Object.defineProperty(sortableRow, "getBoundingClientRect", {
        configurable: true,
        value: () => ({
          bottom: top + 40,
          height: 40,
          left: 0,
          right: 320,
          top,
          width: 320,
          x: 0,
          y: top,
          toJSON: () => ({}),
        }),
      });
    },
  );
}
