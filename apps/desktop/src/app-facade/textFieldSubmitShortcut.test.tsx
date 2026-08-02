import { createEvent, fireEvent, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";

import { isTextFieldSubmitShortcut, useTextFieldSubmitShortcut } from "./textFieldSubmitShortcut";

type Platform = "browser" | "linux" | "macos" | "unknown" | "windows";
const nativePlatform = vi.hoisted((): { current: Platform } => ({ current: "macos" }));
vi.mock("./useAppServices", () => ({
  useAppServices: () => ({ nativeBridge: { capabilities: { platform: nativePlatform.current } } }),
}));

function DirectHarness({ action, available = true }: Readonly<{ action: (() => void) | null; available?: boolean }>) {
  return <input aria-label="text" onKeyDown={useTextFieldSubmitShortcut({ action, available, kind: "direct" })} />;
}
function FormHarness({ children, id = "owner-form" }: Readonly<{ children: ReactNode; id?: string }>) {
  return (
    <form data-testid={id} id={id} onKeyDown={useTextFieldSubmitShortcut({ available: true, kind: "form" })} onSubmit={(event) => { event.preventDefault(); }}>
      {children}
    </form>
  );
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
    fireEvent.keyDown(screen.getByRole("textbox"), { key: "Enter", ...modifier });
    expect(action).toHaveBeenCalledOnce();
  });
  it("recognizes only the platform shortcut and ignores composition", () => {
    for (const [platform, event] of [
      ["macos", new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true })],
      ["windows", new KeyboardEvent("keydown", { key: "Enter", metaKey: true })],
      ["macos", new KeyboardEvent("keydown", { key: "a", metaKey: true })],
      ["macos", new KeyboardEvent("keydown", { key: "Enter", metaKey: true, isComposing: true })],
      ["browser", new KeyboardEvent("keydown", { key: "Enter", metaKey: true })],
      ["unknown", new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true })],
    ] as const) {
      expect(isTextFieldSubmitShortcut(event, platform)).toBe(false);
    }
  });
  it("consumes repeated and unavailable direct shortcuts", () => {
    nativePlatform.current = "macos";
    const action = vi.fn();
    const view = render(<DirectHarness action={action} />);
    fireEvent.keyDown(screen.getByRole("textbox"), { key: "Enter", metaKey: true });
    const repeated = createEvent.keyDown(screen.getByRole("textbox"), { key: "Enter", metaKey: true, repeat: true });
    fireEvent(screen.getByRole("textbox"), repeated);
    view.rerender(<DirectHarness action={action} available={false} />);
    const unavailable = createEvent.keyDown(screen.getByRole("textbox"), { key: "Enter", metaKey: true });
    fireEvent(screen.getByRole("textbox"), unavailable);
    expect(action).toHaveBeenCalledOnce();
    expect(repeated.defaultPrevented).toBe(true);
    expect(unavailable.defaultPrevented).toBe(true);
  });
  it("requests only exact owning text targets", () => {
    nativePlatform.current = "macos";
    render(
      <FormHarness>
        <textarea aria-label="body" /><input aria-label="read-only" readOnly /><input aria-label="radio" type="radio" />
        <input aria-label="unassociated" form="missing-form" />
      </FormHarness>,
    );
    const form = screen.getByTestId("owner-form");
    if (!(form instanceof HTMLFormElement)) throw new Error("Expected an owning form.");
    const submitSpy = vi.spyOn(form, "requestSubmit");
    for (const [role, name] of [["textbox", "body"], ["textbox", "read-only"], ["radio", "radio"], ["textbox", "unassociated"]] as const) {
      fireEvent.keyDown(screen.getByRole(role, { name }), { key: "Enter", metaKey: true });
    }
    expect(submitSpy).toHaveBeenCalledTimes(2);
  });
  it("keeps an associated inner form responsible for its own target", () => {
    nativePlatform.current = "macos";
    render(
      <>
        <FormHarness><input aria-label="inner-associated" form="inner-form" /></FormHarness>
        <FormHarness id="inner-form"><input aria-label="inner-owned" /></FormHarness>
      </>,
    );
    const outer = screen.getByTestId("owner-form");
    const inner = screen.getByTestId("inner-form");
    if (!(outer instanceof HTMLFormElement) || !(inner instanceof HTMLFormElement)) throw new Error("Expected owning forms.");
    const outerSubmit = vi.spyOn(outer, "requestSubmit");
    const innerSubmit = vi.spyOn(inner, "requestSubmit");
    fireEvent.keyDown(screen.getByRole("textbox", { name: "inner-associated" }), { key: "Enter", metaKey: true });
    fireEvent.keyDown(screen.getByRole("textbox", { name: "inner-owned" }), { key: "Enter", metaKey: true });
    expect(outerSubmit).not.toHaveBeenCalled();
    expect(innerSubmit).toHaveBeenCalledOnce();
  });
  it("skips consumed descendants and consumes form auto-repeat", () => {
    nativePlatform.current = "windows";
    render(
      <FormHarness>
        <input aria-label="consumed" onKeyDown={(event) => { event.preventDefault(); }} /><input aria-label="repeat" />
      </FormHarness>,
    );
    const form = screen.getByTestId("owner-form");
    if (!(form instanceof HTMLFormElement)) throw new Error("Expected an owning form.");
    const submitSpy = vi.spyOn(form, "requestSubmit");
    fireEvent.keyDown(screen.getByRole("textbox", { name: "consumed" }), { key: "Enter", ctrlKey: true });
    const repeated = createEvent.keyDown(screen.getByRole("textbox", { name: "repeat" }), { key: "Enter", ctrlKey: true, repeat: true });
    fireEvent(screen.getByRole("textbox", { name: "repeat" }), repeated);
    expect(submitSpy).not.toHaveBeenCalled();
    expect(repeated.defaultPrevented).toBe(true);
  });
});
