import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type * as AppFacade from "@/app-facade";
import { createLabelFilterState, type LabelFilterAction } from "./labelFilterState";
import { LabelChooser } from "./LabelChooser";
import type * as ProjectLabelHooks from "./projectLabelHooks";
import { projectLabelReorderFailureNotice, submitProjectLabelReorder } from "./projectLabelHooks";
import { ReorderableList, type ReorderableListItemRenderProps } from "@app/ui-kit";

const createdLabelID = "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";
const testT = (key: string) => key;

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
    expect(screen.getByRole("button", { name: "Reorder Alpha" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reorder Beta" })).toBeInTheDocument();

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
    render(
      <LabelChooser
        invocation={{ kind: "filter", onAction: vi.fn(), state: createLabelFilterState() }}
        trigger={<button type="button">Open label chooser</button>}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Open label chooser" }));
    await user.keyboard("{ArrowDown}{ArrowDown}");

    expect(screen.getAllByRole("listitem")[1]).toHaveClass(
      "bg-[var(--color-island-1)]",
    );
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

  it("builds the visible reorder failure notice", () => {
    statusPush(projectLabelReorderFailureNotice(new Error("save failed"), testT));
    expect(statusPush).toHaveBeenCalledWith(
      expect.objectContaining({ body: "save failed", tone: "danger" }),
    );
  });

  it("submits the complete chooser sequence and receives the authoritative catalog", async () => {
    const authoritative = {
      projectID: "project-1",
      labels: [...hooks.catalog.data.labels].reverse(),
    };
    const mutateAsync = vi.fn(async (labelIDs: readonly string[]) => {
      expect(labelIDs).toEqual(authoritative.labels.map((label) => label.id));
      return authoritative;
    });

    await expect(
      submitProjectLabelReorder({
        labelIDs: authoritative.labels.map((label) => label.id),
        mutateAsync,
        onError: statusPush,
      }),
    ).resolves.toEqual(authoritative);
    expect(statusPush).not.toHaveBeenCalled();
  });

  it("routes chooser reorder failure to its visible error callback", async () => {
    const error = new Error("save failed");
    await expect(
      submitProjectLabelReorder({
        labelIDs: ["label-1", "label-2"],
        mutateAsync: async () => {
          throw error;
        },
        onError: (failure) => {
          statusPush(projectLabelReorderFailureNotice(failure, testT));
        },
      }),
    ).resolves.toBeNull();
    expect(statusPush).toHaveBeenCalledWith(expect.objectContaining({ body: "save failed", tone: "danger" }));
  });

});

type ReorderableTestItem = Readonly<{
  id: string;
  name: string;
}>;

const reorderableInitialItems: readonly ReorderableTestItem[] = [
  { id: "first", name: "First" },
  { id: "second", name: "Second" },
  { id: "third", name: "Third" },
];

function ReorderableTestList({
  disabled = false,
  items: initial = reorderableInitialItems,
  onCommit = vi.fn(),
}: Readonly<{
  disabled?: boolean;
  items?: readonly ReorderableTestItem[];
  onCommit?: (items: readonly ReorderableTestItem[]) => void;
}>) {
  const [items, setItems] = useState(initial);
  return (
    <ReorderableList
      disabled={disabled}
      getItemID={(item) => item.id}
      items={items}
      onCommit={(nextItems) => {
        setItems(nextItems);
        onCommit(nextItems);
      }}
      renderItem={(item, props) => <ReorderableTestRow item={item} {...props} />}
    />
  );
}

function ReorderableTestRow({
  item,
  ...props
}: Readonly<{ item: ReorderableTestItem }> & ReorderableListItemRenderProps) {
  const { activatorAttributes, activatorListeners, activatorRef, itemRef } = props;
  return (
    <div data-testid={`row-${item.id}`} ref={itemRef}>
      <button
        aria-label={`Move ${item.name}`}
        ref={activatorRef}
        {...activatorAttributes}
        {...activatorListeners}
      >
        Move
      </button>
      <span>{item.name}</span>
    </div>
  );
}

async function startKeyboardReorder(label: string) {
  const handle = screen.getByRole("button", { name: label });
  handle.focus();
  fireEvent.keyDown(handle, { code: "Space", key: " " });
  await new Promise<void>((resolve) => {
    setTimeout(resolve, 0);
  });
  return handle;
}

function mockVerticalRects() {
  ["first", "second", "third"].forEach((id, index) => {
    const top = index * 40;
    Object.defineProperty(screen.getByTestId(`row-${id}`), "getBoundingClientRect", {
      configurable: true,
      value: () => ({
        bottom: top + 40,
        height: 40,
        left: 0,
        right: 200,
        top,
        width: 200,
        x: 0,
        y: top,
        toJSON: () => ({}),
      }),
    });
  });
}

describe("ReorderableList", () => {
  it("commits one stable-ID keyboard reorder after move and drop", async () => {
    const onCommit = vi.fn();
    render(<ReorderableTestList onCommit={onCommit} />);
    mockVerticalRects();

    const handle = await startKeyboardReorder("Move First");
    fireEvent.keyDown(handle, { code: "ArrowDown", key: "ArrowDown" });
    fireEvent.keyDown(handle, { code: "Space", key: " " });

    expect(onCommit).toHaveBeenCalledTimes(1);
    expect(onCommit).toHaveBeenCalledWith([
      { id: "second", name: "Second" },
      { id: "first", name: "First" },
      { id: "third", name: "Third" },
    ]);
  });

  it("commits a pointer reorder from the consumer activator", async () => {
    const onCommit = vi.fn();
    render(<ReorderableTestList onCommit={onCommit} />);
    mockVerticalRects();
    Object.defineProperty(Element.prototype, "setPointerCapture", {
      configurable: true,
      value: vi.fn(),
    });
    Object.defineProperty(Element.prototype, "releasePointerCapture", {
      configurable: true,
      value: vi.fn(),
    });

    const handle = screen.getByRole("button", { name: "Move First" });
    fireEvent.pointerDown(handle, {
      buttons: 1,
      clientX: 10,
      clientY: 10,
      isPrimary: true,
      pointerId: 1,
      pointerType: "mouse",
    });
    await new Promise<void>((resolve) => {
      setTimeout(resolve, 0);
    });
    fireEvent.pointerMove(handle, {
      buttons: 1,
      clientX: 10,
      clientY: 20,
      isPrimary: true,
      pointerId: 1,
      pointerType: "mouse",
    });
    fireEvent.pointerMove(handle, {
      buttons: 1,
      clientX: 10,
      clientY: 60,
      isPrimary: true,
      pointerId: 1,
      pointerType: "mouse",
    });
    await new Promise<void>((resolve) => {
      setTimeout(resolve, 0);
    });
    fireEvent.pointerUp(handle, { isPrimary: true, pointerId: 1, pointerType: "mouse" });

    await waitFor(() => {
      expect(onCommit).toHaveBeenCalledWith([
        { id: "second", name: "Second" },
        { id: "first", name: "First" },
        { id: "third", name: "Third" },
      ]);
    });
  });

  it("cancels a keyboard reorder without committing", async () => {
    const onCommit = vi.fn();
    render(<ReorderableTestList onCommit={onCommit} />);
    mockVerticalRects();

    const handle = await startKeyboardReorder("Move First");
    fireEvent.keyDown(handle, { code: "ArrowDown", key: "ArrowDown" });
    fireEvent.keyDown(handle, { code: "Escape", key: "Escape" });

    expect(onCommit).not.toHaveBeenCalled();
    expect(screen.getByText("First")).toBeInTheDocument();
    expect(screen.getByText("Second")).toBeInTheDocument();
  });

  it("suppresses an unchanged keyboard drop", async () => {
    const onCommit = vi.fn();
    render(<ReorderableTestList onCommit={onCommit} />);
    mockVerticalRects();

    const handle = await startKeyboardReorder("Move First");
    fireEvent.keyDown(handle, { code: "Space", key: " " });

    expect(onCommit).not.toHaveBeenCalled();
  });

  it("requires the consumer activator and leaves row content inert", () => {
    const onCommit = vi.fn();
    render(<ReorderableTestList onCommit={onCommit} />);

    const row = screen.getByTestId("row-first");
    fireEvent.pointerDown(row, { pointerId: 1, clientX: 0, clientY: 0 });
    fireEvent.pointerMove(row, { pointerId: 1, clientX: 0, clientY: 80 });
    fireEvent.pointerUp(row, { pointerId: 1, clientX: 0, clientY: 80 });

    expect(onCommit).not.toHaveBeenCalled();
  });

  it("does not expose reorder interaction while disabled", async () => {
    const onCommit = vi.fn();
    render(<ReorderableTestList disabled onCommit={onCommit} />);

    const handle = await startKeyboardReorder("Move First");
    fireEvent.keyDown(handle, { code: "ArrowDown", key: "ArrowDown" });
    fireEvent.keyDown(handle, { code: "Space", key: " " });

    expect(onCommit).not.toHaveBeenCalled();
  });

  it("scrolls a keyboard destination into view", async () => {
    const scrollIntoView = vi.fn();
    HTMLElement.prototype.scrollIntoView = scrollIntoView;
    render(<ReorderableTestList />);
    mockVerticalRects();

    const handle = await startKeyboardReorder("Move First");
    fireEvent.keyDown(handle, { code: "ArrowDown", key: "ArrowDown" });

    expect(scrollIntoView).toHaveBeenCalled();
  });

  it("keeps reorder behavior available when reduced motion is preferred", async () => {
    const onCommit = vi.fn();
    Object.defineProperty(window, "matchMedia", {
      configurable: true,
      value: vi.fn(),
    });
    const matchMedia = vi.spyOn(window, "matchMedia").mockReturnValue({
      matches: true,
      media: "(prefers-reduced-motion: reduce)",
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
    });
    render(<ReorderableTestList onCommit={onCommit} />);
    mockVerticalRects();

    const handle = await startKeyboardReorder("Move First");
    fireEvent.keyDown(handle, { code: "ArrowDown", key: "ArrowDown" });
    fireEvent.keyDown(handle, { code: "Space", key: " " });

    expect(onCommit).toHaveBeenCalledTimes(1);
    matchMedia.mockRestore();
  });
});
