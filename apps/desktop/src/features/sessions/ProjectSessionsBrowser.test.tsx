import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { z } from "zod";

import type { SessionCategory, SessionPagePosition } from "@/api";
import {
  createTestServices,
  TestAppProviders,
  type TestAppServices,
} from "@/test-support/app-services";
import type { FakeRoute } from "@/test-support/api";
import { ProjectSessionsBrowser } from "./ProjectSessionsBrowser";

describe("Project Sessions browser presentation", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("keeps Sessions/Subagents controls visible through loading and requests only the selected category", async () => {
    const pending = new Promise<never>(() => undefined);
    const { services } = renderBrowser({
      method: "session.page",
      handler: async () => pending,
    });

    expect(screen.getByRole("tab", { name: "Sessions" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByRole("tab", { name: "Subagents" })).toHaveAttribute(
      "aria-selected",
      "false",
    );
    expect(await screen.findByTestId("loading-state")).toBeInTheDocument();
    expect(sessionRequests(services)).toEqual([
      request("main", { kind: "newest" }),
    ]);

    fireEvent.click(screen.getByRole("tab", { name: "Subagents" }));
    await waitFor(() => {
      expect(sessionRequests(services)).toEqual([
        request("main", { kind: "newest" }),
        request("subagent", { kind: "newest" }),
      ]);
    });
  });

  it("renders standard empty and retryable whole-list error states", async () => {
    const { view } = renderBrowser({
      method: "session.page",
      result: sessionPage("main", []),
    });
    expect(await screen.findByText("No sessions")).toBeInTheDocument();
    view.unmount();

    const { services } = renderBrowser({
      method: "session.page",
      error: new Error("catalog failed"),
    });
    expect(await screen.findByText("catalog failed")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => {
      expect(sessionRequests(services)).toHaveLength(2);
    });
  });

  it("projects compact deduplicated rows with fallback identity and no row interaction", async () => {
    vi.spyOn(Date, "now").mockReturnValue(Date.parse("2026-08-11T22:00:00Z"));
    renderBrowser({
      method: "session.page",
      result: sessionPage("main", [
        session("session-1", {
          name: "  Named Session  ",
          preview: "  Preview copy  ",
          updatedAt: "2026-08-11T20:00:00Z",
        }),
        session("session-1", {
          name: "Older duplicate",
          preview: "Duplicate preview",
          updatedAt: "2026-08-11T19:00:00Z",
        }),
        session("session-2", {
          preview: "   ",
          updatedAt: "2026-08-11T21:00:00Z",
        }),
      ]),
    });

    const rows = await screen.findAllByTestId("session-row");
    expect(rows).toHaveLength(2);
    expect(screen.getByText("Named Session")).toBeInTheDocument();
    expect(screen.queryByText("Older duplicate")).not.toBeInTheDocument();
    expect(screen.getByText("Preview copy")).toBeInTheDocument();
    expect(screen.getByText("session-2")).toBeInTheDocument();
    expect(screen.getByText("2 hours ago")).toBeInTheDocument();
    for (const row of rows) {
      expect(within(row).queryByRole("button")).not.toBeInTheDocument();
      expect(within(row).queryByRole("link")).not.toBeInTheDocument();
      expect(row).not.toHaveAttribute("tabindex");
    }

    const destination = window.location.href;
    const firstRow = rows[0];
    if (firstRow === undefined) throw new Error("Expected a rendered Session row.");
    fireEvent.click(firstRow);
    expect(window.location.href).toBe(destination);
    await userEvent.tab();
    await userEvent.tab();
    for (const row of rows) {
      expect(row).not.toHaveFocus();
    }
  });

  it.each([
    {
      direction: "older" as const,
      boundaryTestID: "virtual-boundary-next",
      cursor: "older-1",
    },
    {
      direction: "newer" as const,
      boundaryTestID: "virtual-boundary-previous",
      cursor: "newer-1",
    },
  ])("retains rows behind an independent $direction Retry boundary", async ({
    boundaryTestID,
    cursor,
    direction,
  }) => {
    renderBrowser({
      method: "session.page",
      handler: (params) => {
        const position = sessionRequestSchema.parse(params).position;
        if (position.kind === direction) throw new Error(`${direction} failed`);
        return sessionPage(
          "main",
          [session("session-1")],
          direction === "older" ? cursor : undefined,
          direction === "newer" ? cursor : undefined,
        );
      },
    });

    expect(await screen.findByText("session-1")).toBeInTheDocument();
    expect(await screen.findByText(`${direction} failed`)).toBeInTheDocument();
    expect(screen.getByTestId(boundaryTestID)).toBeInTheDocument();
    expect(screen.getByTestId("session-row")).toBeInTheDocument();
    expect(
      within(screen.getByTestId(boundaryTestID)).getByRole("button", {
        name: "Try again",
      }),
    ).toBeInTheDocument();
  });

  it("switches category content without retaining a hidden observer", async () => {
    const { services } = renderBrowser({
      method: "session.page",
      handler: (params) => {
        const category = sessionRequestSchema.parse(params).category;
        return sessionPage(category, [session(`${category}-session`, { category })]);
      },
    });
    expect(await screen.findByText("main-session")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("tab", { name: "Subagents" }));
    expect(await screen.findByText("subagent-session")).toBeInTheDocument();
    expect(screen.queryByText("main-session")).not.toBeInTheDocument();
    expect(sessionRequests(services).map((entry) => entry.category)).toEqual([
      "main",
      "subagent",
    ]);
  });
});

function renderBrowser(route: FakeRoute) {
  const services = createTestServices([route]);
  const view = render(
    <TestAppProviders services={services}>
      <ProjectSessionsBrowser projectID="project-1" />
    </TestAppProviders>,
  );
  return { services, view };
}

function sessionRequests(services: TestAppServices) {
  return services.transport.calls
    .filter((call) => call.method === "session.page")
    .map((call) => {
      const params = sessionRequestSchema.parse(call.params);
      return request(params.category, params.position);
    });
}

const sessionRequestSchema = z.object({
  category: z.enum(["main", "subagent"]),
  position: z.discriminatedUnion("kind", [
    z.object({ kind: z.literal("newest") }),
    z.object({ kind: z.literal("older"), token: z.string() }),
    z.object({ kind: z.literal("newer"), token: z.string() }),
  ]),
});

function request(category: SessionCategory, position: SessionPagePosition) {
  return { category, position };
}

function sessionPage(
  category: SessionCategory,
  sessions: readonly ReturnType<typeof session>[],
  older?: string,
  newer?: string,
) {
  return {
    project_id: "project-1",
    category,
    sessions,
    ...(older === undefined ? {} : { older }),
    ...(newer === undefined ? {} : { newer }),
  };
}

function session(
  id: string,
  input: Readonly<{
    category?: SessionCategory;
    name?: string;
    preview?: string;
    updatedAt?: string;
  }> = {},
) {
  return {
    session_id: id,
    category: input.category ?? "main",
    ...(input.name === undefined ? {} : { name: input.name }),
    ...(input.preview === undefined ? {} : { first_prompt_preview: input.preview }),
    updated_at: input.updatedAt ?? "2026-08-11T20:00:00Z",
  };
}
