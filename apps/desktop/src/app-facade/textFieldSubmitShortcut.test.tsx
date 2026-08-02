import { createEvent, fireEvent, render, screen } from "@testing-library/react";
import type { NativePlatform } from "@app/native-bridge";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { isTextFieldSubmitShortcut, useTextFieldSubmitShortcut } from "./textFieldSubmitShortcut";
const nativePlatform = vi.hoisted((): { current: NativePlatform } => ({ current: "macos" }));
vi.mock("./useAppServices", () => ({
  useAppServices: () => ({ nativeBridge: { capabilities: { platform: nativePlatform.current } } }),
}));
type DirectProps = Readonly<{ action: (() => void) | null; available?: boolean }>;
function DirectHarness({ action, available = true }: DirectProps) {
  const onKeyDown = useTextFieldSubmitShortcut({ action, available, kind: "direct" });
  return <input aria-label="text" onKeyDown={onKeyDown} />;
}
function FormHarness({
  children,
  id = "owner-form",
  onSubmitted,
}: Readonly<{ children: ReactNode; id?: string; onSubmitted?: () => void }>) {
  const onKeyDown = useTextFieldSubmitShortcut({ available: true, kind: "form" });
  return (
    <form
      data-testid={id}
      id={id}
      onKeyDown={onKeyDown}
      onSubmit={(event) => {
        event.preventDefault();
        onSubmitted?.();
      }}
    >
      {children}
    </form>
  );
}
function press(name: string, options: KeyboardEventInit, role = "textbox") {
  fireEvent.keyDown(screen.getByRole(role, { name }), { key: "Enter", ...options });
}
describe("text field submit shortcut", () => {
  it.each([
    ["macos", { metaKey: true }],
    ["windows", { ctrlKey: true }],
    ["linux", { ctrlKey: true }],
  ] as const)("submits the direct action on %s", (platform, modifier) => {
    nativePlatform.current = platform;
    const action = vi.fn();
    render(<DirectHarness action={action} />);
    press("text", modifier);
    expect(action).toHaveBeenCalledOnce();
  });
  it.each([
    ["macos", { key: "Enter", ctrlKey: true }],
    ["windows", { key: "Enter", metaKey: true }],
    ["macos", { key: "Enter", metaKey: true, isComposing: true }],
    ["browser", { key: "Enter", metaKey: true }],
    ["unknown", { key: "Enter", ctrlKey: true }],
  ] as const)("ignores unsupported shortcut %s", (platform, event) => {
    expect(isTextFieldSubmitShortcut(new KeyboardEvent("keydown", event), platform)).toBe(false);
  });
  it("consumes repeated and unavailable direct shortcuts", () => {
    nativePlatform.current = "macos";
    const action = vi.fn();
    const view = render(<DirectHarness action={action} />);
    press("text", { metaKey: true });
    const repeated = createEvent.keyDown(screen.getByRole("textbox", { name: "text" }), {
      key: "Enter",
      metaKey: true,
      repeat: true,
    });
    fireEvent(screen.getByRole("textbox", { name: "text" }), repeated);
    view.rerender(<DirectHarness action={action} available={false} />);
    const unavailable = createEvent.keyDown(screen.getByRole("textbox", { name: "text" }), {
      key: "Enter",
      metaKey: true,
    });
    fireEvent(screen.getByRole("textbox", { name: "text" }), unavailable);
    expect(action).toHaveBeenCalledOnce();
    expect([repeated.defaultPrevented, unavailable.defaultPrevented]).toEqual([true, true]);
  });
  it("requests only exact owning text targets", () => {
    nativePlatform.current = "macos";
    const submissions: string[] = [];
    render(
      <>
        <FormHarness onSubmitted={() => submissions.push("outer")}>
          <textarea aria-label="body" />
          <input aria-label="read-only" readOnly />
          <input aria-label="radio" type="radio" />
          <input aria-label="unassociated" form="missing-form" />
          <input aria-label="inner-associated" form="inner-form" />
          <input
            aria-label="consumed"
            onKeyDown={(event) => {
              event.preventDefault();
            }}
          />
          <input aria-label="repeat" />
        </FormHarness>
        <FormHarness id="inner-form" onSubmitted={() => submissions.push("inner")}>
          <input aria-label="inner-owned" />
        </FormHarness>
      </>,
    );
    press("body", { metaKey: true });
    press("read-only", { metaKey: true });
    press("unassociated", { metaKey: true });
    press("radio", { metaKey: true }, "radio");
    press("consumed", { metaKey: true });
    press("repeat", { metaKey: true, repeat: true });
    press("inner-associated", { metaKey: true });
    press("inner-owned", { metaKey: true });
    expect(submissions).toEqual(["outer", "outer", "inner"]);
  });
});
