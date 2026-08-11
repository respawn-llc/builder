import { act, render, waitFor } from "@testing-library/react";
import { z } from "zod";

import type { SessionCategory, SessionPagePosition } from "@/api";
import { createTestServices, startupRoutes } from "@/test-support/app-services";
import { AppRoot } from "./AppRoot";

afterEach(() => {
  window.history.replaceState(null, "", "/");
  vi.unstubAllGlobals();
});

it("refetches only retained pages for the active Session category after reconnect", async () => {
  vi.stubGlobal("localStorage", new MemoryStorage());
  vi.stubGlobal("sessionStorage", new MemoryStorage());
  window.history.replaceState(null, "", "/projects/project-1");
  const requests: CatalogRequest[] = [];
  const services = createTestServices([
    ...startupRoutes,
    {
      method: "project.edit.get",
      result: {
        project_id: "project-1",
        project_key: "KNT",
        display_name: "Kent",
        default_workspace_id: "workspace-1",
        workspaces: [],
        next_page_token: "",
      },
    },
    {
      method: "session.page",
      handler: (params) => {
        const input = catalogRequest(params);
        requests.push(input);
        return {
          project_id: input.projectID,
          category: input.category,
          sessions: [
            {
              session_id:
                input.position.kind === "newest" ? "session-newest" : "session-older",
              category: input.category,
              updated_at: "2026-08-11T20:00:00Z",
            },
          ],
          ...(input.position.kind === "newest" ? { older: "older-1" } : {}),
        };
      },
    },
  ]);
  render(<AppRoot services={services} />);
  await waitFor(() => {
    expect(requests).toEqual([
      request("main", { kind: "newest" }),
      request("main", { kind: "older", token: "older-1" }),
    ]);
  });
  requests.length = 0;

  await act(async () => {
    services.transport.connection.set("disconnected");
    await Promise.resolve();
  });
  await act(async () => {
    services.transport.connection.set("connected");
    await Promise.resolve();
  });
  await waitFor(() => {
    expect(requests).toEqual([
      request("main", { kind: "newest" }),
      request("main", { kind: "older", token: "older-1" }),
    ]);
  });
});

type CatalogRequest = Readonly<{
  projectID: string;
  category: SessionCategory;
  position: SessionPagePosition;
}>;

const catalogRequestSchema = z.object({
  project_id: z.string(),
  category: z.enum(["main", "subagent"]),
  position: z.discriminatedUnion("kind", [
    z.object({ kind: z.literal("newest") }),
    z.object({ kind: z.literal("older"), token: z.string() }),
    z.object({ kind: z.literal("newer"), token: z.string() }),
  ]),
});

function catalogRequest(params: unknown): CatalogRequest {
  const value = catalogRequestSchema.parse(params);
  return {
    projectID: value.project_id,
    category: value.category,
    position: value.position,
  };
}

function request(
  category: SessionCategory,
  position: SessionPagePosition,
): CatalogRequest {
  return { projectID: "project-1", category, position };
}

class MemoryStorage implements Storage {
  #entries = new Map<string, string>();

  get length(): number {
    return this.#entries.size;
  }

  clear(): void {
    this.#entries.clear();
  }

  getItem(key: string): string | null {
    return this.#entries.get(key) ?? null;
  }

  key(index: number): string | null {
    return [...this.#entries.keys()][index] ?? null;
  }

  removeItem(key: string): void {
    this.#entries.delete(key);
  }

  setItem(key: string, value: string): void {
    this.#entries.set(key, value);
  }
}
