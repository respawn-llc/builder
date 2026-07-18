import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";

import { CopyableValueButton } from "./CopyableValueButton";

describe("CopyableValueButton", () => {
  it("uses native pointer and keyboard button activation", async () => {
    const onActivate = vi.fn();
    const user = userEvent.setup();

    render(
      <CopyableValueButton accessibleLabel="Copy value" onActivate={onActivate}>
        Visible value
      </CopyableValueButton>,
    );

    const button = screen.getByRole("button");
    expect(button).toHaveAttribute("type", "button");

    await user.click(button);
    button.focus();
    await user.keyboard("{Enter}");
    await user.keyboard(" ");

    expect(onActivate).toHaveBeenCalledTimes(3);
  });
});
