import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { createLabelFilterState, type LabelFilterAction } from "./labelFilterState";
import { LabelChooser } from "./LabelChooser";
import type * as ProjectLabelHooks from "./projectLabelHooks";
import { ReorderableList, type ReorderableListItemRenderProps } from "@app/ui-kit";

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
  reorder: vi.fn(async () => hooks.catalog.data),
  reorderPending: false,
  reset: vi.fn(),
}));

vi.mock("./projectLabelHooks", async (importOriginal) => {
  const actual = await importOriginal<typeof ProjectLabelHooks>();
  return {
    ...actual,
    useProjectLabelCatalog: () => hooks.catalog,
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

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t(key: string, values?: Readonly<Record<string, string>>): string {
      if (key === "labels.search") {
        return "Search or create labels";
      }
      if (key === "labels.unlabeled") {
        return "No labels";
      }
      if (key === "labels.reorder") {
        return `Reorder ${values?.name ?? ""}`.trim();
      }
      if (key === "labels.filterConditionNeutral") {
        return "Neutral label condition";
      }
      if (key === "workflowEditor.reorderParameter") {
        return "Reorder parameter";
      }
      if (key === "workflowEditor.deleteParameter") {
        return "Delete parameter";
      }
      return key;
    },
  }),
}));

beforeEach(() => {
  hooks.reorderPending = false;
  hooks.reorder.mockClear();
  hooks.create.mockClear();
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
    await user.click(screen.getByRole("button", { name: /^Alpha/ }));
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
