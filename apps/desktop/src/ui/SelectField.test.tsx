import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { SelectField, type SelectFieldPaging } from "./SelectField";

const options = [
  { label: "First workspace", value: "first" },
  { label: "Second workspace", value: "second" },
] as const;

function renderSelect(paging?: SelectFieldPaging) {
  const onValueChange = vi.fn();
  render(
    <SelectField
      label="Workspace"
      onValueChange={onValueChange}
      options={options}
      paging={paging}
      value="first"
    />,
  );
  return onValueChange;
}

function setScroll(element: HTMLElement, top: number) {
  Object.defineProperties(element, {
    clientHeight: { configurable: true, value: 100 },
    scrollHeight: { configurable: true, value: 300 },
    scrollTop: { configurable: true, value: top, writable: true },
  });
}

describe("SelectField paging", () => {
  it("loads once when its option-list scroll surface reaches an edge", async () => {
    const onLoadNext = vi.fn();
    renderSelect({ hasNextPage: true, loadKey: "100", onLoadNext });
    await userEvent.click(screen.getByRole("button", { name: "Workspace" }));
    const menu = screen.getByRole("menu");
    setScroll(menu, 198);
    fireEvent.scroll(menu);
    expect(onLoadNext).not.toHaveBeenCalled();
    menu.scrollTop = 200;
    fireEvent.scroll(menu);
    fireEvent.scroll(menu);
    expect(onLoadNext).toHaveBeenCalledOnce();
  });

  it("retains options and delegates page-edge Retry without auto-loading", async () => {
    const onLoadNext = vi.fn();
    const onRetry = vi.fn();
    renderSelect({
      hasNextPage: true,
      loadKey: "100",
      nextBoundary: {
        state: "error",
        message: "Could not load more.",
        retryLabel: "Retry",
        onRetry,
      },
      onLoadNext,
    });
    await userEvent.click(screen.getByRole("button", { name: "Workspace" }));
    expect(screen.getByRole("menuitemradio", { name: "First workspace" })).toBeInTheDocument();
    expect(screen.getByRole("menuitemradio", { name: "Second workspace" })).toBeInTheDocument();
    const menu = screen.getByRole("menu");
    setScroll(menu, 200);
    fireEvent.scroll(menu);
    expect(onLoadNext).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(onRetry).toHaveBeenCalledOnce();
  });

  it("preserves ordinary static selection", async () => {
    const view = renderSelect();
    await userEvent.click(screen.getByRole("button", { name: "Workspace" }));
    await userEvent.click(screen.getByRole("menuitemradio", { name: "Second workspace" }));
    expect(view).toHaveBeenCalledWith("second");
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  });
});
