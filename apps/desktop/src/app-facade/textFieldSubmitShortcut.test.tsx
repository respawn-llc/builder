import { createEvent, fireEvent, render, screen } from "@testing-library/react";
import type { NativePlatform } from "@app/native-bridge";
import { createElement, type ReactNode } from "react";
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
function FormHarness({ children, id = "owner-form" }: Readonly<{ children: ReactNode; id?: string }>) {
  const onKeyDown = useTextFieldSubmitShortcut({ available: true, kind: "form" });
  return createElement("form", { "data-testid": id, id, onKeyDown }, children);
}
function formSpy(id = "owner-form") {
  const form = screen.getByTestId(id);
  if (!(form instanceof HTMLFormElement)) throw new Error("Expected an owning form.");
  return vi.spyOn(form, "requestSubmit").mockImplementation(() => undefined);
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
    ["macos", { key: "a", metaKey: true }],
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
    expect(repeated.defaultPrevented).toBe(true);
    expect(unavailable.defaultPrevented).toBe(true);
  });
  it("requests only exact owning text targets", () => {
    nativePlatform.current = "macos";
    render(
      <>
        <FormHarness>
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
        <FormHarness id="inner-form">
          <input aria-label="inner-owned" />
        </FormHarness>
      </>,
    );
    const submitSpy = formSpy();
    press("body", { metaKey: true });
    press("read-only", { metaKey: true });
    press("unassociated", { metaKey: true });
    press("radio", { metaKey: true }, "radio");
    press("consumed", { metaKey: true });
    const repeated = createEvent.keyDown(screen.getByRole("textbox", { name: "repeat" }), {
      key: "Enter",
      metaKey: true,
      repeat: true,
    });
    fireEvent(screen.getByRole("textbox", { name: "repeat" }), repeated);
    expect(repeated.defaultPrevented).toBe(true);
    const innerSubmit = formSpy("inner-form");
    press("inner-associated", { metaKey: true });
    press("inner-owned", { metaKey: true });
    expect(innerSubmit).toHaveBeenCalledOnce();
    expect(submitSpy).toHaveBeenCalledTimes(2);
  });
});
