import { act, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { TranscriptDisclosure, type TranscriptDisclosureIconTone } from "./index";

type FixtureProps = Readonly<{
  actions?: ReactNode;
  body?: ReactNode;
  defaultExpanded?: boolean;
  iconTone?: TranscriptDisclosureIconTone;
  liveStatus?: ReactNode;
  summary?: ReactNode;
  typeLabel?: ReactNode;
}>;

function DisclosureFixture({
  body = "Full transcript content",
  defaultExpanded = false,
  iconTone = "neutral",
  liveStatus,
  summary = "Compact transcript summary",
  typeLabel = "Context",
  ...props
}: FixtureProps) {
  const actions = Object.prototype.hasOwnProperty.call(props, "actions")
    ? props.actions
    : (
        <button type="button">
          Copy
        </button>
      );
  return (
    <TranscriptDisclosure
      actions={actions}
      body={body}
      defaultExpanded={defaultExpanded}
      expandLabel="Expand transcript item"
      icon={<span>!</span>}
      iconTone={iconTone}
      collapseLabel="Collapse transcript item"
      liveStatus={liveStatus}
      summary={summary}
      typeLabel={typeLabel}
    />
  );
}

function renderDisclosure(props: FixtureProps = {}) {
  return render(<DisclosureFixture {...props} />);
}

function RemountFixture({ mounted }: Readonly<{ mounted: boolean }>) {
  return mounted ? <DisclosureFixture /> : null;
}

function getControlledBody(disclosure: HTMLElement): HTMLElement {
  const bodyID = disclosure.getAttribute("aria-controls");
  if (bodyID === null) {
    throw new Error("Disclosure is missing aria-controls.");
  }
  return screen.getByText((_, element) => element?.id === bodyID, {
    selector: "[id]",
  });
}

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("TranscriptDisclosure", () => {
  it("mounts collapsed by default and exposes no body or durable actions", () => {
    renderDisclosure();

    expect(screen.getByRole("button", { name: "Expand transcript item" })).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("Full transcript content")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Copy" })).not.toBeInTheDocument();
  });

  it("mounts expanded when the row type supplies an expanded default", () => {
    renderDisclosure({ defaultExpanded: true });

    expect(screen.getByRole("button", { name: "Collapse transcript item" })).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("Full transcript content")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Copy" })).toBeInTheDocument();
  });

  it("toggles from the whole disclosure header with pointer and keyboard input", async () => {
    const user = userEvent.setup();
    renderDisclosure();
    const header = screen.getByRole("button", { name: "Expand transcript item" });

    await user.click(header);
    expect(header).toHaveAttribute("aria-expanded", "true");

    await user.keyboard("{Enter}");
    expect(screen.getByRole("button", { name: "Expand transcript item" })).toHaveAttribute("aria-expanded", "false");
  });

  it("keeps status and actions independent from disclosure activation", async () => {
    const user = userEvent.setup();
    const onStatus = vi.fn();
    const onAction = vi.fn();

    renderDisclosure({
      actions: (
        <button onClick={onAction} type="button">
          Copy
        </button>
      ),
      defaultExpanded: true,
      liveStatus: (
        <button onClick={onStatus} type="button">
          Streaming
        </button>
      ),
    });

    const disclosure = screen.getByRole("button", { name: "Collapse transcript item" });
    await user.click(screen.getByRole("button", { name: "Streaming" }));
    await user.click(screen.getByRole("button", { name: "Copy" }));

    expect(onStatus).toHaveBeenCalledOnce();
    expect(onAction).toHaveBeenCalledOnce();
    expect(disclosure).toHaveAttribute("aria-expanded", "true");
  });

  it("keeps live status visible while omitting durable actions when no action slot is supplied", () => {
    renderDisclosure({
      actions: undefined,
      defaultExpanded: true,
      liveStatus: <span>Running</span>,
    });

    expect(screen.getByText("Running")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Copy" })).not.toBeInTheDocument();
  });

  it("accepts every semantic leading-icon tone through the public component", () => {
    const iconTones = ["neutral", "warning", "error", "success"] satisfies TranscriptDisclosureIconTone[];

    render(
      <div>
        {iconTones.map((iconTone) => (
          <TranscriptDisclosure
            body={`${iconTone} body`}
            collapseLabel={`Collapse ${iconTone}`}
            defaultExpanded={false}
            expandLabel={`Expand ${iconTone}`}
            icon={<span>{`${iconTone} leading icon`}</span>}
            iconTone={iconTone}
            key={iconTone}
            summary={`${iconTone} summary`}
          />
        ))}
      </div>,
    );

    for (const iconTone of iconTones) {
      expect(screen.getByRole("button", { name: `Expand ${iconTone}` })).toBeInTheDocument();
    }
  });

  it("reveals the body through the disclosure button and connects it for accessibility", async () => {
    const user = userEvent.setup();
    renderDisclosure();
    const header = screen.getByRole("button", { name: "Expand transcript item" });

    expect(header).toHaveAttribute("aria-controls");
    expect(screen.queryByText("Full transcript content")).not.toBeInTheDocument();

    await user.click(header);

    const body = getControlledBody(header);
    expect(header).toHaveAttribute("aria-expanded", "true");
    expect(body).toHaveAttribute("id", header.getAttribute("aria-controls"));
  });

  it("retains the body during close motion and removes it after the shared exit duration", () => {
    vi.useFakeTimers();
    renderDisclosure({ defaultExpanded: true });

    const disclosure = screen.getByRole("button", { name: "Collapse transcript item" });
    fireEvent.click(disclosure);

    const body = getControlledBody(disclosure);
    expect(body).toHaveAttribute("aria-hidden", "true");

    act(() => {
      vi.advanceTimersByTime(140);
    });
    expect(screen.queryByText("Full transcript content")).not.toBeInTheDocument();
  });

  it("cancels pending body removal when the row is reopened", () => {
    vi.useFakeTimers();
    renderDisclosure({ defaultExpanded: true });

    fireEvent.click(screen.getByRole("button", { name: "Collapse transcript item" }));
    fireEvent.click(screen.getByRole("button", { name: "Expand transcript item" }));
    act(() => {
      vi.advanceTimersByTime(140);
    });

    expect(screen.getByText("Full transcript content")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Collapse transcript item" })).toHaveAttribute("aria-expanded", "true");
  });

  it("does not treat content updates as a new expansion command", async () => {
    const user = userEvent.setup();
    const { rerender } = render(<DisclosureFixture />);

    await user.click(screen.getByRole("button", { name: "Expand transcript item" }));
    rerender(<DisclosureFixture body="Updated transcript content" summary="Updated compact summary" />);

    expect(screen.getByRole("button", { name: "Collapse transcript item" })).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("Updated transcript content")).toBeInTheDocument();
  });

  it("resets local expansion when a keyed visible row unmounts and mounts again", async () => {
    const user = userEvent.setup();
    const { rerender } = render(<RemountFixture mounted />);

    await user.click(screen.getByRole("button", { name: "Expand transcript item" }));
    expect(screen.getByRole("button", { name: "Collapse transcript item" })).toBeInTheDocument();

    rerender(<RemountFixture mounted={false} />);
    rerender(<RemountFixture mounted />);

    expect(screen.getByRole("button", { name: "Expand transcript item" })).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("Full transcript content")).not.toBeInTheDocument();
  });

  it("removes the body immediately when reduced motion is requested", async () => {
    const originalMatchMediaDescriptor = Object.getOwnPropertyDescriptor(window, "matchMedia");
    try {
      Object.defineProperty(window, "matchMedia", {
        configurable: true,
        value: vi.fn((query: string) => ({
          matches: query === "(prefers-reduced-motion: reduce)",
          media: query,
          onchange: null,
          addListener: vi.fn(),
          removeListener: vi.fn(),
          addEventListener: vi.fn(),
          removeEventListener: vi.fn(),
          dispatchEvent: vi.fn(),
        })),
      });
      const user = userEvent.setup();
      renderDisclosure({ defaultExpanded: true });

      await user.click(screen.getByRole("button", { name: "Collapse transcript item" }));

      expect(screen.queryByText("Full transcript content")).not.toBeInTheDocument();
    } finally {
      if (originalMatchMediaDescriptor === undefined) {
        Reflect.deleteProperty(window, "matchMedia");
      } else {
        Object.defineProperty(window, "matchMedia", originalMatchMediaDescriptor);
      }
    }
  });

  it("renders the compact identity in both collapsed and expanded states", async () => {
    const user = userEvent.setup();
    renderDisclosure({ defaultExpanded: true, summary: "Stable compact identity" });

    expect(screen.getByText("Stable compact identity")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Collapse transcript item" }));
    expect(screen.getByText("Stable compact identity")).toBeInTheDocument();
  });
});
