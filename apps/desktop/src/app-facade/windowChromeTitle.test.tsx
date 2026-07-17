import { fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";

import { useCurrentWindowChromeTitle, useWindowChromeTitle } from "./windowChromeTitle";
import { WindowChromeTitleProvider } from "./WindowChromeTitleProvider";

describe("window chrome title", () => {
  it("sets the current destination title from a one-line hook call", () => {
    render(
      <WindowChromeTitleProvider>
        <TitleReader />
        <TitleSetter title="Static title" />
      </WindowChromeTitleProvider>,
    );

    expect(screen.getByRole("status")).toHaveAttribute("data-title", "Static title");
  });

  it("updates the title asynchronously when destination content loads", () => {
    render(
      <WindowChromeTitleProvider>
        <TitleReader />
        <AsyncTitleSetter />
      </WindowChromeTitleProvider>,
    );

    expect(screen.getByRole("status")).not.toHaveAttribute("data-title");
    fireEvent.click(screen.getByRole("button", { name: "Load title" }));
    expect(screen.getByRole("status")).toHaveAttribute("data-title", "Loaded title");
  });

  it("clears an existing title when a destination explicitly sets null", () => {
    const { rerender } = render(
      <WindowChromeTitleProvider>
        <TitleReader />
        <TitleSetter title="Previous title" />
      </WindowChromeTitleProvider>,
    );

    expect(screen.getByRole("status")).toHaveAttribute("data-title", "Previous title");

    rerender(
      <WindowChromeTitleProvider>
        <TitleReader />
        <TitleSetter title={null} />
      </WindowChromeTitleProvider>,
    );

    expect(screen.getByRole("status")).not.toHaveAttribute("data-title");
  });

  it("does not override the active route title when a nested surface disables registration", () => {
    render(
      <WindowChromeTitleProvider>
        <TitleReader />
        <TitleSetter title="Workflow Library" />
        <TitleSetter enabled={false} title="Nested workflow editor" />
      </WindowChromeTitleProvider>,
    );

    expect(screen.getByRole("status")).toHaveAttribute("data-title", "Workflow Library");
  });
});

function TitleSetter({ enabled, title }: Readonly<{ enabled?: boolean | undefined; title: string | null }>) {
  useWindowChromeTitle(title, enabled);
  return null;
}

function AsyncTitleSetter() {
  const [title, setTitle] = useState<string | null>(null);
  useWindowChromeTitle(title);

  return (
    <button
      onClick={() => {
        setTitle("Loaded title");
      }}
      type="button"
    >
      Load title
    </button>
  );
}

function TitleReader() {
  const title = useCurrentWindowChromeTitle();
  return <p data-title={title ?? undefined} role="status" />;
}
