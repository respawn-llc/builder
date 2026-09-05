import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";

import type { ChatSettingsMutation, ChatSettingsRead, InitialChatSettings } from "@/api";
import { createTestServices, TestAppProviders } from "@/test-support/app-services";
import { useChatSettings } from "./index";

const target = {
  kind: "new_chat",
  projectID: "project-1",
  workspace: { workspaceID: "workspace-1" },
} as const;

const defaultBaseline: InitialChatSettings = {
  agentRole: "default",
  supervisor: "edits",
  thinking: "medium",
  fast: true,
  questionsEnabled: false,
  autoCompactionEnabled: false,
};
const reviewerBaseline: InitialChatSettings = {
  agentRole: "reviewer",
  supervisor: "off",
  thinking: null,
  fast: null,
  questionsEnabled: true,
  autoCompactionEnabled: true,
};
const catalogRead: Extract<ChatSettingsRead, { kind: "new_chat" }> = {
  kind: "new_chat",
  initialSettings: defaultBaseline,
  catalog: {
    choices: [
      {
        agent: {
          role: "default",
          model: "default-model",
          thinking: "medium",
          tools: [],
          customSystemPrompt: false,
          customCapabilities: false,
          agentCallable: true,
        },
        baseline: defaultBaseline,
        supervisor: { value: "edits", baseline: "edits", editability: { kind: "editable" } },
        thinking: {
          kind: "custom",
          value: "medium",
          baselineValue: "medium",
          editability: { kind: "editable" },
        },
        fast: { kind: "supported", value: true, editability: { kind: "editable" } },
        questions: { capable: false, enabled: false, editability: { kind: "editable" } },
        autoCompaction: {
          policy: "optional",
          stored: false,
          effective: false,
          editability: { kind: "editable" },
        },
      },
      {
        agent: {
          role: "reviewer",
          model: "reviewer-model",
          thinking: "none",
          tools: ["ask_question"],
          customSystemPrompt: true,
          customCapabilities: true,
          agentCallable: true,
        },
        baseline: reviewerBaseline,
        supervisor: { value: "off", baseline: "off", editability: { kind: "editable" } },
        thinking: { kind: "unsupported" },
        fast: { kind: "unsupported" },
        questions: { capable: true, enabled: true, editability: { kind: "editable" } },
        autoCompaction: {
          policy: "optional",
          stored: true,
          effective: true,
          editability: { kind: "editable" },
        },
      },
    ],
  },
};

it("replaces all five New Chat settings with the selected Agent's complete server baseline", async () => {
  const services = createTestServices([]);
  vi.spyOn(services.api.chat, "getSettings").mockResolvedValue(catalogRead);
  const { result } = renderHook(() => useChatSettings({ target, onInitialSettingsChange: vi.fn() }), {
    wrapper: ({ children }: Readonly<{ children: ReactNode }>) => (
      <TestAppProviders services={services}>{children}</TestAppProviders>
    ),
  });
  await waitFor(() => {
    expect(result.current.kind).toBe("ready-new-chat");
  });
  act(() => {
    if (result.current.kind !== "ready-new-chat") throw new Error("Expected ready New Chat.");
    result.current.activate({ kind: "agent", role: "reviewer" });
  });
  expect(result.current).toMatchObject({
    kind: "ready-new-chat",
    initialSettings: reviewerBaseline,
  });
  expect(services.transport.descriptorCalls).toHaveLength(0);
  expect(services.transport.calls).toHaveLength(0);
});

it("preserves the current Agent's transient settings after multiple local edits and repeated selection", async () => {
  const services = createTestServices([]);
  const read = vi.spyOn(services.api.chat, "getSettings").mockResolvedValue(catalogRead);
  const { result } = renderHook(() => useChatSettings({ target, onInitialSettingsChange: vi.fn() }), {
    wrapper: ({ children }: Readonly<{ children: ReactNode }>) => (
      <TestAppProviders services={services}>{children}</TestAppProviders>
    ),
  });
  await waitFor(() => {
    expect(result.current.kind).toBe("ready-new-chat");
  });
  const edits: readonly ChatSettingsMutation[] = [
    { kind: "supervisor", value: "all" },
    { kind: "thinking", value: "  custom effort  " },
    { kind: "fast", enabled: false },
    { kind: "questions", enabled: true },
    { kind: "auto_compaction", enabled: true },
    { kind: "agent", role: "default" },
    { kind: "thinking", value: " \t " },
    { kind: "agent", role: "default" },
  ];
  act(() => {
    if (result.current.kind !== "ready-new-chat") throw new Error("Expected ready New Chat.");
    for (const operation of edits) result.current.activate(operation);
  });
  expect(result.current).toMatchObject({
    kind: "ready-new-chat",
    initialSettings: {
      agentRole: "default",
      supervisor: "all",
      thinking: "custom effort",
      fast: false,
      questionsEnabled: true,
      autoCompactionEnabled: true,
    },
  });
  expect(read).toHaveBeenCalledOnce();
  expect(services.transport.descriptorCalls).toHaveLength(0);
  expect(services.transport.calls).toHaveLength(0);
});

it("reports exactly InitialChatSettings to the host without catalog or display metadata", async () => {
  const services = createTestServices([]);
  const read = vi.spyOn(services.api.chat, "getSettings").mockResolvedValue(catalogRead);
  const report = vi.fn<(settings: InitialChatSettings) => void>();
  const options = {
    wrapper: ({ children }: Readonly<{ children: ReactNode }>) => (
      <TestAppProviders services={services}>{children}</TestAppProviders>
    ),
  };
  const { result, unmount } = renderHook(
    () => useChatSettings({ target, onInitialSettingsChange: report }),
    options,
  );
  await waitFor(() => {
    expect(result.current.kind).toBe("ready-new-chat");
  });
  expect(report).toHaveBeenLastCalledWith(defaultBaseline);
  act(() => {
    if (result.current.kind !== "ready-new-chat") throw new Error("Expected ready New Chat.");
    result.current.activate({ kind: "thinking", value: "  exact committed value  " });
  });
  expect(report).toHaveBeenLastCalledWith({ ...defaultBaseline, thinking: "exact committed value" });
  act(() => {
    if (result.current.kind !== "ready-new-chat") throw new Error("Expected ready New Chat.");
    result.current.activate({ kind: "agent", role: "reviewer" });
  });
  expect(report).toHaveBeenLastCalledWith(reviewerBaseline);
  if (result.current.kind !== "ready-new-chat") throw new Error("Expected ready New Chat.");
  expect(report.mock.lastCall?.[0]).toBe(result.current.initialSettings);
  expect(services.transport.descriptorCalls).toHaveLength(0);
  expect(services.transport.calls).toHaveLength(0);

  unmount();
  const { result: reopened } = renderHook(
    () => useChatSettings({ target, onInitialSettingsChange: report }),
    options,
  );
  expect(reopened.current.kind).toBe("loading-new-chat");
  await waitFor(() => {
    expect(reopened.current.kind).toBe("ready-new-chat");
  });
  expect(read).toHaveBeenCalledTimes(2);
  expect(report).toHaveBeenLastCalledWith(defaultBaseline);
});

it("keeps loaded New Chat edits available through connection loss and host refresh changes", async () => {
  const services = createTestServices([]);
  const read = vi.spyOn(services.api.chat, "getSettings").mockResolvedValue(catalogRead);
  const report = vi.fn<(value: InitialChatSettings) => void>();
  const { result, rerender } = renderHook(
    (
      host: Readonly<{
        serverMutationAvailability: "available" | "disconnected";
        authoritativeRefreshGeneration: symbol;
      }>,
    ) => useChatSettings({ ...host, target, onInitialSettingsChange: report }),
    {
      initialProps: {
        serverMutationAvailability: "available",
        authoritativeRefreshGeneration: Symbol("initial"),
      },
      wrapper: ({ children }: Readonly<{ children: ReactNode }>) => (
        <TestAppProviders services={services}>{children}</TestAppProviders>
      ),
    },
  );
  await waitFor(() => {
    expect(result.current.kind).toBe("ready-new-chat");
  });
  act(() => {
    if (result.current.kind !== "ready-new-chat") throw new Error("Expected New Chat.");
    result.current.activate({ kind: "supervisor", value: "all" });
    services.transport.connection.set("disconnected");
  });
  rerender({ serverMutationAvailability: "disconnected", authoritativeRefreshGeneration: Symbol("refresh") });
  act(() => {
    if (result.current.kind !== "ready-new-chat") throw new Error("Expected New Chat.");
    result.current.activate({ kind: "thinking", value: "offline edit" });
  });
  expect(report).toHaveBeenLastCalledWith({
    ...defaultBaseline,
    supervisor: "all",
    thinking: "offline edit",
  });
  expect(read).toHaveBeenCalledOnce();
  expect(services.transport.descriptorCalls).toHaveLength(0);
});
