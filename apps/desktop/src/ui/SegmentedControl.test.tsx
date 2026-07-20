import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { vi } from "vitest";

import { SegmentedControl } from "./index";

describe("SegmentedControl", () => {
  it("exposes one selected option and supports pointer and arrow-key selection", async () => {
    const onValueChange = vi.fn();
    const user = userEvent.setup();

    function Harness() {
      const [value, setValue] = useState("any");
      return (
        <SegmentedControl
          ariaLabel="Match mode"
          onValueChange={(nextValue) => {
            onValueChange(nextValue);
            setValue(nextValue);
          }}
          options={[
            { label: "OR", value: "any" },
            { label: "AND", value: "all" },
          ]}
          value={value}
        />
      );
    }

    render(<Harness />);

    const any = screen.getByRole("radio", { name: "OR" });
    const all = screen.getByRole("radio", { name: "AND" });
    expect(any).toBeChecked();
    expect(all).not.toBeChecked();

    await user.click(all);
    expect(onValueChange).toHaveBeenLastCalledWith("all");
    expect(all).toBeChecked();

    act(() => {
      any.focus();
    });
    await user.keyboard("{ArrowRight}");
    expect(onValueChange).toHaveBeenLastCalledWith("all");
  });

  it("rejects a controlled value outside its unique enabled options", () => {
    expect(() =>
      render(
        <SegmentedControl
          ariaLabel="Invalid mode"
          onValueChange={() => undefined}
          options={[
            { label: "First", value: "one" },
            { label: "Duplicate", value: "one" },
          ]}
          value="missing"
        />,
      ),
    ).toThrow();
  });
});
