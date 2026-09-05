import { fireEvent, render, screen, within } from "@testing-library/react";
import type { ReactElement } from "react";

import { createVirtualizedPixelOffsetRequest } from "@/ui";
import { installResizeObserverGeometry } from "@/test-support/resize-observer";

import {
  TranscriptWindow,
  TranscriptWindowView,
  type TranscriptRenderSlots,
  type TranscriptViewportMeasurement,
} from "./index";
import { hydration } from "./fixtures";
import type { CommittedRow } from "./types";

let geometry: ReturnType<typeof installResizeObserverGeometry>;

const reasoningIdentity = {
  Provider: { ItemID: "reasoning-item", SummaryIndex: 0 },
  Kent: null,
} as const;

function committedPromotions(): readonly CommittedRow[] {
  return [
    {
      Kind: "assistant",
      Locator: { event_sequence: 1, row_ordinal: 1 },
      Visibility: "ongoing",
      Integrity: 0,
      User: null,
      Assistant: {
        StepID: "step",
        StreamID: "assistant-stream",
        Text: "Committed assistant",
        CondensedText: "",
        Phase: "commentary",
        committed_at_unix_ms: null,
      },
      Tool: null,
      ReasoningTrace: null,
      Notice: null,
      ReviewerFeedback: null,
      ReviewerError: null,
    },
    {
      Kind: "tool",
      Locator: { event_sequence: 2, row_ordinal: 1 },
      Visibility: "ongoing",
      Integrity: 0,
      User: null,
      Assistant: null,
      Tool: {
        StepID: "step",
        ToolCallID: "tool-call",
        ToolName: "shell",
        Text: "Committed tool",
        IsError: false,
        ResultSummary: "",
        CondensedText: "",
        Presentation: null,
      },
      ReasoningTrace: null,
      Notice: null,
      ReviewerFeedback: null,
      ReviewerError: null,
    },
    {
      Kind: "reasoning_trace",
      Locator: { event_sequence: 3, row_ordinal: 1 },
      Visibility: "ongoing",
      Integrity: 0,
      User: null,
      Assistant: null,
      Tool: null,
      ReasoningTrace: {
        StepID: "step",
        CompactText: "Committed reasoning",
        Text: "Committed reasoning detail",
        duration_ms: null,
        ProvisionalIdentity: reasoningIdentity,
      },
      Notice: null,
      ReviewerFeedback: null,
      ReviewerError: null,
    },
  ];
}

function rect(top: number, height: number): DOMRect {
  return {
    bottom: top + height,
    height,
    left: 0,
    right: 800,
    top,
    width: 800,
    x: 0,
    y: top,
    toJSON: () => ({}),
  };
}

const slots: TranscriptRenderSlots<ReactElement | null> = {
  user: (item) => <div data-testid={`family-${item.kind}`}>{item.value.Text}</div>,
  assistant: (item) => <div data-testid={`family-${item.kind}`}>{item.state}</div>,
  tool: (item) => <div data-testid={`family-${item.kind}`}>{item.state}</div>,
  reasoning: (item) => <div data-testid={`family-${item.kind}`}>{item.state}</div>,
  notice: (item) => <div data-testid={`family-${item.kind}`} />,
  thinkingStatus: (item) => <div data-testid={`family-${item.kind}`}>{item.value.Text}</div>,
};

describe("TranscriptWindowView promotion and measurements", () => {
  beforeEach(() => {
    geometry = installResizeObserverGeometry();
  });

  afterEach(() => {
    geometry.restore();
  });

  it("keeps typed family presentations mounted and reports a surviving anchor with raw viewport facts", () => {
    const window = new TranscriptWindow();
    const initial = hydration([]);
    window.dispatch({
      kind: "initial-hydration",
      hydration: {
        ...initial,
        ActiveAssistant: {
          StepID: "step",
          StreamID: "assistant-stream",
          Phase: "commentary",
          Text: "Live assistant",
        },
        InFlightTools: [
          {
            StepID: "step",
            ToolCallID: "tool-call",
            ToolName: "shell",
            Presentation: null,
          },
        ],
        ActiveReasoningTraces: [
          {
            StepID: "step",
            Identity: reasoningIdentity,
            CompactText: "Live reasoning",
            Text: "Live reasoning detail",
          },
        ],
      },
    });
    const measurements: TranscriptViewportMeasurement[] = [];
    const correction = createVirtualizedPixelOffsetRequest("correction", 240);
    const onInput = vi.fn();
    const view = render(
      <TranscriptWindowView
        boundaryErrorMessage={(error) => error.message}
        estimateSize={() => 60}
        loadingLabel="Loading"
        onInput={onInput}
        onMeasurement={(measurement) => measurements.push(measurement)}
        retryLabel="Retry"
        slots={slots}
        snapshot={window.snapshot}
      />,
    );
    const scrollport = screen.getByRole("list");
    const before = new Map(
      ["assistant", "tool", "reasoning_trace"].map((family) => [
        family,
        screen.getByTestId(`family-${family}`),
      ]),
    );
    const mountedRows = within(scrollport).getAllByRole("listitem");
    const presentationKeys = window.snapshot.items.map((item) => item.key);
    geometry.setGeometry(scrollport, {
      clientHeight: 300,
      scrollHeight: 900,
      rect: rect(100, 300),
    });
    presentationKeys.forEach((key, index) => {
      const wrapper = mountedRows[index];
      if (wrapper === undefined) throw new Error("Expected mounted transcript row wrapper.");
      geometry.setGeometry(wrapper, { rect: rect(120 + index * 60, 40) });
    });
    scrollport.scrollTop = 120;
    fireEvent.scroll(scrollport);

    const firstPresentationKey = presentationKeys[0];
    if (firstPresentationKey === undefined) throw new Error("Expected a first presentation key.");
    const firstWrapper = mountedRows[0];
    if (firstWrapper === undefined) throw new Error("Expected first mounted transcript row wrapper.");
    geometry.setGeometry(firstWrapper, { rect: rect(145, 40) });
    for (const row of committedPromotions()) {
      expect(window.dispatch({ kind: "committed-row", row }).kind).toBe("accepted");
    }
    view.rerender(
      <TranscriptWindowView
        boundaryErrorMessage={(error) => error.message}
        estimateSize={() => 60}
        loadingLabel="Loading"
        onInput={onInput}
        onMeasurement={(measurement) => measurements.push(measurement)}
        pixelOffsetRequest={correction}
        retryLabel="Retry"
        slots={slots}
        snapshot={window.snapshot}
      />,
    );

    for (const family of ["assistant", "tool", "reasoning_trace"]) {
      const presentation = screen.getByTestId(`family-${family}`);
      expect(presentation).toBe(before.get(family));
      expect(presentation).toHaveTextContent("committed");
    }
    expect(scrollport.scrollTop).toBe(240);
    expect(measurements).toContainEqual({
      absoluteScrollOffsetPx: 240,
      viewportExtentPx: 300,
      loadedContentEndExtentPx: 900,
      edges: { olderAvailable: true, newerAvailable: false },
      anchor: {
        presentationKey: firstPresentationKey,
        beforeViewportOffsetPx: 20,
        afterViewportOffsetPx: 45,
      },
    });
  });

  it("derives exact edge failure presentation from the reducer snapshot and emits its Retry transition", () => {
    const window = new TranscriptWindow();
    window.dispatch({
      kind: "initial-hydration",
      hydration: {
        ...hydration([]),
        TailSegment: { Entries: [], HasMoreAbove: true, OlderCursor: 987 },
      },
    });
    const request = window.dispatch({
      kind: "edge-visit",
      direction: "older",
      older: true,
      newer: false,
    }).effects[0];
    if (request?.kind !== "page-request") throw new Error("Expected older page request.");
    const onInput = vi.fn();
    const boundaryErrorMessage = vi.fn(() => "Localized history failure");

    const view = render(
      <TranscriptWindowView
        boundaryErrorMessage={boundaryErrorMessage}
        estimateSize={() => 60}
        loadingLabel="Loading"
        onInput={onInput}
        onMeasurement={() => undefined}
        retryLabel="Retry"
        slots={slots}
        snapshot={window.snapshot}
      />,
    );
    expect(screen.getByRole("status")).toBeInTheDocument();

    const error = new Error("History read failed");
    window.dispatch({ kind: "page-failure", request: request.request, error });
    view.rerender(
      <TranscriptWindowView
        boundaryErrorMessage={boundaryErrorMessage}
        estimateSize={() => 60}
        loadingLabel="Loading"
        onInput={onInput}
        onMeasurement={() => undefined}
        retryLabel="Retry"
        slots={slots}
        snapshot={window.snapshot}
      />,
    );

    expect(boundaryErrorMessage).toHaveBeenCalledWith(error);
    fireEvent.click(within(screen.getByRole("alert")).getByRole("button"));
    expect(onInput).toHaveBeenCalledOnce();
    expect(onInput).toHaveBeenCalledWith({ kind: "retry", direction: "older" });
  });
});
