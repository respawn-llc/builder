import { render } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it } from "vitest";
import { commands, page as screen, userEvent } from "vitest/browser";
import { VerticalReorder } from "./VerticalReorder";

declare module "vitest/browser" {
  interface BrowserCommands {
    activatePointerInSource: (selector: string) => Promise<void>;
    dragToAdjacent: (sourceSelector: string, targetSelector: string) => Promise<void>;
    dragToGap: (sourceSelector: string, targetSelector: string) => Promise<void>;
  }
}

declare module "vitest/internal/browser" {
  interface BrowserCommands {
    activatePointerInSource: (selector: string) => Promise<void>;
    dragToAdjacent: (sourceSelector: string, targetSelector: string) => Promise<void>;
    dragToGap: (sourceSelector: string, targetSelector: string) => Promise<void>;
  }
}

type ReorderItem = Readonly<{ id: string; label: string }>;

const items: readonly ReorderItem[] = [
  { id: "first", label: "First" },
  { id: "second", label: "Second" },
  { id: "third", label: "Third" },
];

describe("VerticalReorder transformed browser regression", () => {
  it("does not commit a pointer activation before the destination is crossed", async () => {
    render(<BrowserReorderHarness />);
    await commands.activatePointerInSource('[data-testid="row-first"] button');

    await expect.poll(() => screen.getByTestId("committed-order").element().textContent).toBe("");
  });

  it("commits one adjacent pointer move through a translated surface", async () => {
    render(<BrowserReorderHarness />);
    await commands.dragToAdjacent('[data-testid="row-second"] button', '[data-testid="row-third"]');

    await expect
      .poll(() => screen.getByTestId("committed-order").element().textContent)
      .toBe("first,third,second");
  });

  it("commits the adjacent destination through a translated surface", async () => {
    render(<BrowserReorderHarness />);
    await commands.dragToGap('[data-testid="row-first"] button', '[data-testid="row-second"]');

    await expect
      .poll(() => screen.getByTestId("committed-order").element().textContent)
      .toBe("second,first,third");
  });

  it("commits one adjacent keyboard move through a translated surface", async () => {
    render(<BrowserReorderHarness />);
    const source = screen.getByRole("button", { name: "Reorder First" });
    source.element().focus();
    await userEvent.keyboard("{Space}");
    await userEvent.keyboard("{ArrowDown}");
    await userEvent.keyboard("{Space}");

    await expect
      .poll(() => screen.getByTestId("committed-order").element().textContent)
      .toBe("second,first,third");
  });
});

function BrowserReorderHarness() {
  const [orderedItems, setOrderedItems] = useState(items);
  const [committedOrder, setCommittedOrder] = useState("");
  return (
    <div
      data-testid="transformed-surface"
      style={{
        "--space-3": "8px",
        transform: "translateY(50px)",
        padding: "24px",
        width: "320px",
      }}
    >
      <VerticalReorder
        getItemID={(item) => item.id}
        items={orderedItems}
        onCommit={(orderedIDs) => {
          setCommittedOrder(orderedIDs.join(","));
          setOrderedItems((current) => move(current, orderedIDs));
        }}
        renderActivator={(item) => (
          <button aria-label={`Reorder ${item.label}`} type="button">
            {item.label}
          </button>
        )}
        renderItem={(item, row) => (
          <div
            data-row-id={row.isOverlay ? undefined : item.id}
            data-testid={row.isOverlay ? "reorder-overlay" : `row-${item.id}`}
            style={{ height: "40px", width: "240px" }}
          >
            {row.activator}
          </div>
        )}
      />
      <output data-testid="committed-order">{committedOrder}</output>
    </div>
  );
}

function move(rows: readonly ReorderItem[], orderedIDs: readonly string[]): readonly ReorderItem[] {
  const itemsByID = new Map(rows.map((row) => [row.id, row]));
  return orderedIDs.flatMap((id) => {
    const item = itemsByID.get(id);
    return item === undefined ? [] : [item];
  });
}
