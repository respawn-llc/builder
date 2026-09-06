import { act, fireEvent, render, renderHook, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";

import {
  ChatOperationError,
  RpcError,
  type ChatContext,
  type ChatSettings,
  type ChatSettingsMutationResponse,
  type ChatSettingsRead,
  type InitialChatSettings,
} from "@/api";
import { createTestServices, TestAppProviders } from "@/test-support/app-services";
import { useChatSettings, type ChatSettingsOptions, type ChatSettingsFeature } from "./index";
import * as ui from "@/ui";
import { appI18n } from "@/i18n";

const sessionID = "123e4567-e89b-42d3-a456-426614174000";
const target = {
  kind: "session",
  projectID: "project-1",
  workspace: { workspaceID: "workspace-1" },
  sessionID,
} as const;
const navigation = {
  openTask: vi.fn<(taskID: string) => void>(),
  openParentSession: vi.fn<(previousSessionID: string) => void>(),
};
const connected = {
  ...navigation,
  serverMutationAvailability: "available",
  authoritativeRefreshGeneration: Symbol("initial"),
} as const;
const settings: ChatSettings = {
  selectedAgent: { role: "default", model: "model", thinking: "medium" },
  agentChoices: [
    {
      role: "default",
      model: "model",
      thinking: "medium",
      tools: [],
      customSystemPrompt: false,
      customCapabilities: false,
      agentCallable: true,
    },
  ],
  agentEditability: { kind: "editable" },
  agentLocked: false,
  cachingLocked: false,
  workflowLocked: false,
  supervisor: { value: "off", baseline: "off", editability: { kind: "editable" } },
  thinking: { kind: "custom", value: "medium", baselineValue: "medium", editability: { kind: "editable" } },
  fast: { kind: "supported", value: false, editability: { kind: "editable" } },
  questions: { capable: true, enabled: true, editability: { kind: "editable" } },
  autoCompaction: { policy: "optional", stored: true, effective: true, editability: { kind: "editable" } },
};
const initialRead: Extract<ChatSettingsRead, { kind: "session" }> = {
  kind: "session",
  settings,
  session: { sessionID, previousSessionID: null, task: null },
};
const context: ChatContext = {
  contextWindowTokens: 100,
  usedTokens: 20,
  remainingTokens: 80,
  automaticThresholdTokens: 80,
  autoCompactionEnabled: true,
  compactionMode: "local",
  completedCompactionCount: 0,
  compactionRunning: false,
  manualCompactAvailable: true,
};
function response(value: ChatSettings = settings): ChatSettingsMutationResponse {
  return {
    settings: value,
    session: initialRead.session,
    context,
    result: { kind: "applied", changed: true },
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((resolveValue, rejectError) => {
    resolve = resolveValue;
    reject = rejectError;
  });
  return { promise, resolve, reject };
}

const settingsFailureOperation = "settings.commit";
const settingsFailureCause = "disk full";

function typedSettingsFailure() {
  return new ChatOperationError(
    new RpcError({
      code: -32_000,
      message: "settings.commit (internal_failure)",
      method: "kent.api.chat_settings.chat_settings_service.mutate",
    }),
    {
      kind: "internal_failure",
      operation: settingsFailureOperation,
      cause: settingsFailureCause,
    },
  );
}

function SessionSettingsChipHost({
  availability = "available",
}: Readonly<{ availability?: "available" | "disconnected" }>) {
  const feature = useChatSettings({
    ...connected,
    target,
    serverMutationAvailability: availability,
    onContextChange: () => undefined,
  });
  return feature.kind === "ready-session" ? feature.settingsChip : null;
}

it("activates nested Settings controls once and suppresses disconnected Session activations", async () => {
  const services = createTestServices([]);
  vi.spyOn(services.api.chat, "getSettings").mockResolvedValue(initialRead);
  const mutate = vi.spyOn(services.api.chat, "mutateSettings").mockResolvedValue(response());
  const user = userEvent.setup();
  const view = render(
    <TestAppProviders services={services}>
      <SessionSettingsChipHost availability="available" />
    </TestAppProviders>,
  );
  await user.click(await screen.findByRole("button", { name: appI18n.t("chatSettings.open") }));
  const popover = screen.getByRole("dialog");
  await user.click(within(popover).getByRole("radio", { name: appI18n.t("chatSettings.supervisorAlways") }));
  expect(mutate).toHaveBeenCalledExactlyOnceWith(target, { kind: "supervisor", value: "all" });
  mutate.mockClear();
  for (const [label, operation] of [
    [appI18n.t("chatSettings.fast"), { kind: "fast", enabled: true }],
    [appI18n.t("chatSettings.questions"), { kind: "questions", enabled: false }],
    [appI18n.t("chatSettings.autoCompaction"), { kind: "auto_compaction", enabled: false }],
  ] as const) {
    await user.click(within(popover).getByRole("switch", { name: label }));
    expect(mutate).toHaveBeenCalledExactlyOnceWith(target, operation);
    mutate.mockClear();
  }
  const thinkingInput = within(popover).getByRole("textbox", { name: appI18n.t("chatSettings.thinking") });
  await user.clear(thinkingInput);
  await user.type(thinkingInput, "custom value");
  await user.click(within(popover).getByRole("button", { name: appI18n.t("chatSettings.commitThinking") }));
  expect(mutate).toHaveBeenCalledExactlyOnceWith(target, { kind: "thinking", value: "custom value" });
  mutate.mockClear();
  view.rerender(
    <TestAppProviders services={services}>
      <SessionSettingsChipHost availability="disconnected" />
    </TestAppProviders>,
  );
  for (const control of [
    ...within(popover).getAllByRole("switch"),
    ...within(popover).getAllByRole("radio"),
    within(popover).getByRole("button", { name: appI18n.t("chatSettings.commitThinking") }),
  ]) {
    fireEvent.click(control);
  }
  expect(mutate).not.toHaveBeenCalled();
});

it.each([
  {
    policy: "disabled" as const,
    autoCompaction: {
      policy: "disabled" as const,
      stored: true,
      effective: false as const,
      editability: { kind: "policy_disabled" as const },
    },
  },
  {
    policy: "required" as const,
    autoCompaction: {
      policy: "required" as const,
      stored: false,
      effective: true as const,
      editability: { kind: "workflow_lock" as const },
    },
  },
])("shows authoritative $policy Auto-compaction as on while unavailable", async ({ autoCompaction }) => {
  const services = createTestServices([]);
  vi.spyOn(services.api.chat, "getSettings").mockResolvedValue({
    ...initialRead,
    settings: { ...settings, autoCompaction },
  });
  const user = userEvent.setup();
  render(
    <TestAppProviders services={services}>
      <SessionSettingsChipHost />
    </TestAppProviders>,
  );
  await user.click(await screen.findByRole("button", { name: appI18n.t("chatSettings.open") }));
  const control = within(screen.getByRole("dialog")).getByRole("switch", {
    name: appI18n.t("chatSettings.autoCompaction"),
  });
  expect(control).toBeChecked();
  expect(control).toBeDisabled();
});

it("sends repeated Supervisor segment activations while an earlier request is pending", async () => {
  const services = createTestServices([]);
  vi.spyOn(services.api.chat, "getSettings").mockResolvedValue(initialRead);
  const first = deferred<ChatSettingsMutationResponse>();
  const second = deferred<ChatSettingsMutationResponse>();
  const mutate = vi
    .spyOn(services.api.chat, "mutateSettings")
    .mockReturnValueOnce(first.promise)
    .mockReturnValueOnce(second.promise);
  const user = userEvent.setup();
  render(
    <TestAppProviders services={services}>
      <SessionSettingsChipHost />
    </TestAppProviders>,
  );
  await user.click(await screen.findByRole("button", { name: appI18n.t("chatSettings.open") }));
  const always = within(screen.getByRole("dialog")).getByRole("radio", {
    name: appI18n.t("chatSettings.supervisorAlways"),
  });
  await user.click(always);
  expect(always).toBeChecked();
  await user.click(always);
  expect(mutate).toHaveBeenCalledTimes(2);
  expect(mutate).toHaveBeenNthCalledWith(1, target, { kind: "supervisor", value: "all" });
  expect(mutate).toHaveBeenNthCalledWith(2, target, { kind: "supervisor", value: "all" });
  await act(async () => {
    first.resolve(response());
    second.resolve(response());
    await Promise.all([first.promise, second.promise]);
  });
});

it("keeps server-editable single-value Thinking available for explicit activation", async () => {
  const services = createTestServices([]);
  const singleValueThinking: ChatSettings = {
    ...settings,
    thinking: {
      kind: "enumerated",
      value: "medium",
      baselineValue: "medium",
      values: ["medium"],
      editability: { kind: "editable" },
    },
  };
  vi.spyOn(services.api.chat, "getSettings").mockResolvedValue({
    ...initialRead,
    settings: singleValueThinking,
  });
  const mutate = vi
    .spyOn(services.api.chat, "mutateSettings")
    .mockResolvedValue(response(singleValueThinking));
  const user = userEvent.setup();
  render(
    <TestAppProviders services={services}>
      <SessionSettingsChipHost />
    </TestAppProviders>,
  );
  await user.click(await screen.findByRole("button", { name: appI18n.t("chatSettings.open") }));
  const control = within(screen.getByRole("dialog")).getByRole("slider");
  expect(control).toBeEnabled();
  Object.defineProperties(control, {
    setPointerCapture: { value: vi.fn() },
    releasePointerCapture: { value: vi.fn() },
  });
  await user.click(control);
  expect(mutate).toHaveBeenCalledExactlyOnceWith(target, { kind: "thinking", value: "medium" });
});

it("surfaces typed Chat operation diagnostics for an ordinary mutation failure", async () => {
  const services = createTestServices([]);
  vi.spyOn(services.api.chat, "getSettings").mockResolvedValue(initialRead);
  const failure = typedSettingsFailure();
  vi.spyOn(services.api.chat, "mutateSettings").mockRejectedValue(failure);
  const notice = vi.spyOn(ui, "showStatusToast").mockImplementation(() => undefined);
  const { result } = renderHook(() => useChatSettings({ ...connected, target, onContextChange: vi.fn() }), {
    wrapper: ({ children }: Readonly<{ children: ReactNode }>) => (
      <TestAppProviders services={services}>{children}</TestAppProviders>
    ),
  });
  await waitFor(() => {
    expect(result.current.kind).toBe("ready-session");
  });
  await act(async () => {
    if (result.current.kind !== "ready-session" || result.current.serverMutationAvailability !== "available")
      throw new Error("Expected available Session.");
    await result.current.activate({ kind: "questions", enabled: false }).catch(() => undefined);
  });
  const body = notice.mock.lastCall?.[0].body;
  expect(body).toContain(settingsFailureOperation);
  expect(body).toContain(settingsFailureCause);
  expect(notice.mock.lastCall?.[0].onAction).toBeUndefined();
  notice.mockRestore();
});

it("loads ordinary Session Settings through the same feature boundary", async () => {
  const services = createTestServices([]);
  const read = deferred<ChatSettingsRead>();
  const getSettings = vi.spyOn(services.api.chat, "getSettings").mockReturnValue(read.promise);
  const { result } = renderHook(() => useChatSettings({ ...connected, target, onContextChange: vi.fn() }), {
    wrapper: ({ children }: Readonly<{ children: ReactNode }>) => (
      <TestAppProviders services={services}>{children}</TestAppProviders>
    ),
  });
  expect(result.current).toEqual({ kind: "loading-session" });
  await act(async () => {
    read.resolve(initialRead);
    await read.promise;
  });
  expect(result.current).toMatchObject({
    kind: "ready-session",
    settings,
    session: initialRead.session,
  });
  expect(getSettings).toHaveBeenCalledExactlyOnceWith(target);
});

it("applies overlapping successes in delivery order and reports each mutation Context", async () => {
  const services = createTestServices([]);
  const read = vi.spyOn(services.api.chat, "getSettings").mockResolvedValue(initialRead);
  const first = deferred<ChatSettingsMutationResponse>();
  const second = deferred<ChatSettingsMutationResponse>();
  const mutate = vi
    .spyOn(services.api.chat, "mutateSettings")
    .mockReturnValueOnce(first.promise)
    .mockReturnValueOnce(second.promise);
  const onContextChange = vi.fn<(value: ChatContext) => void>();
  const { result } = renderHook(() => useChatSettings({ ...connected, target, onContextChange }), {
    wrapper: ({ children }: Readonly<{ children: ReactNode }>) => (
      <TestAppProviders services={services}>{children}</TestAppProviders>
    ),
  });
  await waitFor(() => {
    expect(result.current.kind).toBe("ready-session");
  });
  act(() => {
    if (result.current.kind !== "ready-session" || result.current.serverMutationAvailability !== "available")
      throw new Error("Expected available Session.");
    void result.current.activate({ kind: "questions", enabled: false });
    void result.current.activate({ kind: "fast", enabled: true });
  });
  expect(mutate).toHaveBeenCalledTimes(2);
  expect(result.current).toMatchObject({
    settings: { questions: { enabled: false }, fast: { value: true } },
  });
  const deliveredSecond = response({ ...settings, supervisor: { ...settings.supervisor, value: "all" } });
  await act(async () => {
    second.resolve(deliveredSecond);
    await second.promise;
  });
  expect(result.current).toMatchObject({ settings: deliveredSecond.settings });
  expect(onContextChange).toHaveBeenLastCalledWith(deliveredSecond.context);
  const deliveredFirst = response({ ...settings, questions: { ...settings.questions, enabled: false } });
  await act(async () => {
    first.resolve(deliveredFirst);
    await first.promise;
  });
  expect(result.current).toMatchObject({ settings: deliveredFirst.settings });
  expect(onContextChange).toHaveBeenCalledTimes(2);
  expect(onContextChange.mock.calls.map((call) => call[0])).toEqual([
    deliveredSecond.context,
    deliveredFirst.context,
  ]);
  expect(read).toHaveBeenCalledOnce();
});

it("applies a late typed rejection as complete authoritative state and reports its diagnostic without Retry", async () => {
  const services = createTestServices([]);
  vi.spyOn(services.api.chat, "getSettings").mockResolvedValue(initialRead);
  const first = deferred<ChatSettingsMutationResponse>();
  const second = deferred<ChatSettingsMutationResponse>();
  vi.spyOn(services.api.chat, "mutateSettings")
    .mockReturnValueOnce(first.promise)
    .mockReturnValueOnce(second.promise);
  const notice = vi.spyOn(ui, "showStatusToast").mockImplementation(() => undefined);
  const onContextChange = vi.fn<(value: ChatContext) => void>();
  const { result } = renderHook(() => useChatSettings({ ...connected, target, onContextChange }), {
    wrapper: ({ children }: Readonly<{ children: ReactNode }>) => (
      <TestAppProviders services={services}>{children}</TestAppProviders>
    ),
  });
  await waitFor(() => {
    expect(result.current.kind).toBe("ready-session");
  });
  act(() => {
    if (result.current.kind !== "ready-session" || result.current.serverMutationAvailability !== "available")
      throw new Error("Expected available Session.");
    void result.current.activate({ kind: "thinking", value: "unsupported request" });
    void result.current.activate({ kind: "questions", enabled: false });
  });
  await act(async () => {
    second.resolve(response());
    await second.promise;
  });
  const rejected: ChatSettingsMutationResponse = {
    ...response({ ...settings, supervisor: { ...settings.supervisor, value: "all" } }),
    result: { kind: "rejected", reason: "thinking_unavailable" },
    context: { ...context, usedTokens: 30, remainingTokens: 70 },
  };
  await act(async () => {
    first.resolve(rejected);
    await first.promise;
  });
  expect(result.current).toMatchObject({ settings: rejected.settings, session: rejected.session });
  expect(onContextChange).toHaveBeenLastCalledWith(rejected.context);
  expect(notice).toHaveBeenCalledOnce();
  expect(notice.mock.lastCall?.[0].onAction).toBeUndefined();
  notice.mockRestore();
});

it.each(["applied", "rejected"] as const)(
  "rolls an ordinary failure back to the last-delivered %s projection",
  async (outcome) => {
    const services = createTestServices([]);
    const read = vi.spyOn(services.api.chat, "getSettings").mockResolvedValue(initialRead);
    const first = deferred<ChatSettingsMutationResponse>();
    const second = deferred<ChatSettingsMutationResponse>();
    const third = deferred<ChatSettingsMutationResponse>();
    vi.spyOn(services.api.chat, "mutateSettings")
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise)
      .mockReturnValueOnce(third.promise);
    const notice = vi.spyOn(ui, "showStatusToast").mockImplementation(() => undefined);
    const onContextChange = vi.fn<(value: ChatContext) => void>();
    const { result } = renderHook(() => useChatSettings({ ...connected, target, onContextChange }), {
      wrapper: ({ children }: Readonly<{ children: ReactNode }>) => (
        <TestAppProviders services={services}>{children}</TestAppProviders>
      ),
    });
    await waitFor(() => {
      expect(result.current.kind).toBe("ready-session");
    });
    act(() => {
      if (
        result.current.kind !== "ready-session" ||
        result.current.serverMutationAvailability !== "available"
      )
        throw new Error("Expected available Session.");
      void result.current.activate({ kind: "thinking", value: "will fail" }).catch(() => undefined);
      void result.current.activate({ kind: "questions", enabled: false });
    });
    const delivered: ChatSettingsMutationResponse = {
      ...response({ ...settings, supervisor: { ...settings.supervisor, value: "all" } }),
      result:
        outcome === "applied"
          ? { kind: "applied", changed: true }
          : { kind: "rejected", reason: "agent_locked" },
    };
    await act(async () => {
      second.resolve(delivered);
      await second.promise;
    });
    act(() => {
      if (
        result.current.kind !== "ready-session" ||
        result.current.serverMutationAvailability !== "available"
      )
        throw new Error("Expected available Session.");
      void result.current.activate({ kind: "questions", enabled: false });
    });
    expect(result.current).toMatchObject({ settings: { questions: { enabled: false } } });
    await act(async () => {
      first.reject(new Error("disk full"));
      await first.promise.catch(() => undefined);
    });
    expect(result.current).toMatchObject({ settings: delivered.settings, session: delivered.session });
    expect(onContextChange).toHaveBeenCalledOnce();
    expect(notice).toHaveBeenCalledTimes(outcome === "applied" ? 1 : 2);
    expect(notice.mock.lastCall?.[0].onAction).toBeUndefined();
    await act(async () => {
      third.resolve(response());
      await third.promise;
    });
    expect(result.current).toMatchObject({ settings });
    expect(read).toHaveBeenCalledOnce();
    notice.mockRestore();
  },
);

it("drops old Session completions after target replacement and installs the newly read Agent lock", async () => {
  const services = createTestServices([]);
  const newSessionID = "223e4567-e89b-42d3-a456-426614174000";
  const nextRead = deferred<ChatSettingsRead>();
  vi.spyOn(services.api.chat, "getSettings")
    .mockResolvedValueOnce(initialRead)
    .mockReturnValueOnce(nextRead.promise);
  const mutation = deferred<ChatSettingsMutationResponse>();
  vi.spyOn(services.api.chat, "mutateSettings").mockReturnValue(mutation.promise);
  const onContextChange = vi.fn<(value: ChatContext) => void>();
  const { result, rerender } = renderHook<ChatSettingsFeature, ChatSettingsOptions>(
    (options) => useChatSettings(options),
    {
      initialProps: { ...connected, target, onContextChange },
      wrapper: ({ children }: Readonly<{ children: ReactNode }>) => (
        <TestAppProviders services={services}>{children}</TestAppProviders>
      ),
    },
  );
  await waitFor(() => {
    expect(result.current.kind).toBe("ready-session");
  });
  act(() => {
    if (result.current.kind !== "ready-session" || result.current.serverMutationAvailability !== "available")
      throw new Error("Expected available Session.");
    void result.current.activate({ kind: "questions", enabled: false });
  });
  rerender({ ...connected, target: { ...target, sessionID: newSessionID }, onContextChange });
  expect(result.current).toEqual({ kind: "loading-session" });
  const rebased: Extract<ChatSettingsRead, { kind: "session" }> = {
    kind: "session",
    settings: {
      ...settings,
      agentLocked: true,
      cachingLocked: true,
      agentEditability: { kind: "caching_lock" },
      supervisor: { ...settings.supervisor, value: "all" },
    },
    session: { sessionID: newSessionID, previousSessionID: sessionID, task: null },
  };
  await act(async () => {
    nextRead.resolve(rebased);
    await nextRead.promise;
  });
  expect(result.current).toMatchObject({
    kind: "ready-session",
    settings: rebased.settings,
    session: rebased.session,
  });
  await act(async () => {
    mutation.resolve(response());
    await mutation.promise;
  });
  expect(result.current).toMatchObject({ settings: rebased.settings, session: rebased.session });
  expect(onContextChange).not.toHaveBeenCalled();
});

it("replaces New Chat selection with ordinary Session loading before installing creation-time rebase", async () => {
  const services = createTestServices([]);
  const initialSettings: InitialChatSettings = {
    agentRole: "default",
    supervisor: "off",
    thinking: "medium",
    fast: false,
    questionsEnabled: true,
    autoCompactionEnabled: true,
  };
  const agent = settings.agentChoices[0];
  if (agent === undefined) throw new Error("Incomplete fixture.");
  const newRead: ChatSettingsRead = {
    kind: "new_chat",
    initialSettings,
    catalog: {
      choices: [
        {
          agent,
          baseline: initialSettings,
          supervisor: settings.supervisor,
          thinking: settings.thinking,
          fast: settings.fast,
          questions: settings.questions,
          autoCompaction: settings.autoCompaction,
        },
      ],
    },
  };
  const sessionRead = deferred<ChatSettingsRead>();
  vi.spyOn(services.api.chat, "getSettings")
    .mockResolvedValueOnce(newRead)
    .mockReturnValueOnce(sessionRead.promise);
  const seen: string[] = [];
  const { result, rerender } = renderHook<ChatSettingsFeature, ChatSettingsOptions>(
    (options: ChatSettingsOptions) => {
      const value = useChatSettings(options);
      seen.push(value.kind);
      return value;
    },
    {
      initialProps: { target: { ...target, kind: "new_chat" }, onInitialSettingsChange: vi.fn() },
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
    result.current.activate({ kind: "thinking", value: "transient input" });
  });
  seen.length = 0;
  rerender({ ...connected, target, onContextChange: vi.fn() });
  expect(seen.every((kind) => kind === "loading-session")).toBe(true);
  expect(result.current).toEqual({ kind: "loading-session" });
  const rebased: ChatSettingsRead = {
    ...initialRead,
    settings: {
      ...settings,
      agentLocked: true,
      workflowLocked: true,
      agentEditability: { kind: "workflow_lock" },
      thinking: { kind: "unsupported" },
    },
  };
  await act(async () => {
    sessionRead.resolve(rebased);
    await sessionRead.promise;
  });
  expect(result.current).toMatchObject({
    kind: "ready-session",
    settings: rebased.settings,
    session: initialRead.session,
  });
  expect(result.current).not.toHaveProperty("initialSettings");
  expect(result.current).not.toHaveProperty("catalog");
});

it("exposes whole-Chat Session read failure without retained Settings or local Retry", async () => {
  const services = createTestServices([]);
  const read = deferred<ChatSettingsRead>();
  vi.spyOn(services.api.chat, "getSettings").mockReturnValue(read.promise);
  const { result } = renderHook(() => useChatSettings({ ...connected, target, onContextChange: vi.fn() }), {
    wrapper: ({ children }: Readonly<{ children: ReactNode }>) => (
      <TestAppProviders services={services}>{children}</TestAppProviders>
    ),
  });
  const failure = new Error("Session unavailable");
  await act(async () => {
    read.reject(failure);
    await read.promise.catch(() => undefined);
  });
  expect(result.current).toEqual({ kind: "failed-session", error: failure });
});

it("preserves exact custom Thinking input and returns distinct completions for the later editor", async () => {
  const services = createTestServices([]);
  vi.spyOn(services.api.chat, "getSettings").mockResolvedValue(initialRead);
  const accepted = deferred<ChatSettingsMutationResponse>();
  const rejected = deferred<ChatSettingsMutationResponse>();
  const failed = deferred<ChatSettingsMutationResponse>();
  const mutate = vi
    .spyOn(services.api.chat, "mutateSettings")
    .mockReturnValueOnce(accepted.promise)
    .mockReturnValueOnce(rejected.promise)
    .mockReturnValueOnce(failed.promise);
  const notice = vi.spyOn(ui, "showStatusToast").mockImplementation(() => undefined);
  const { result } = renderHook(() => useChatSettings({ ...connected, target, onContextChange: vi.fn() }), {
    wrapper: ({ children }: Readonly<{ children: ReactNode }>) => (
      <TestAppProviders services={services}>{children}</TestAppProviders>
    ),
  });
  await waitFor(() => {
    expect(result.current.kind).toBe("ready-session");
  });
  const activate = async (value: string) => {
    if (result.current.kind !== "ready-session" || result.current.serverMutationAvailability !== "available")
      throw new Error("Expected available Session.");
    return result.current.activate({ kind: "thinking", value });
  };
  let completion: Promise<ChatSettingsMutationResponse>;
  act(() => {
    completion = activate("  submitted exactly  ");
  });
  expect(mutate).toHaveBeenLastCalledWith(target, { kind: "thinking", value: "  submitted exactly  " });
  const canonical = response({
    ...settings,
    thinking: {
      kind: "custom",
      value: "canonical",
      baselineValue: "medium",
      editability: { kind: "editable" },
    },
    selectedAgent: { ...settings.selectedAgent, thinking: "canonical" },
  });
  await act(async () => {
    accepted.resolve(canonical);
    await expect(completion).resolves.toBe(canonical);
  });
  expect(result.current).toMatchObject({ settings: canonical.settings });
  act(() => {
    completion = activate("  keep this draft  ");
  });
  const rejection: ChatSettingsMutationResponse = {
    ...canonical,
    result: { kind: "rejected", reason: "thinking_unavailable" },
  };
  await act(async () => {
    rejected.resolve(rejection);
    await expect(completion).resolves.toBe(rejection);
  });
  expect(result.current).toMatchObject({ settings: canonical.settings });
  act(() => {
    completion = activate("  keep failed draft  ");
  });
  const error = new Error("write failed");
  await act(async () => {
    const assertion = expect(completion).rejects.toBe(error);
    failed.reject(error);
    await assertion;
  });
  expect(result.current).toMatchObject({ settings: canonical.settings });
  expect(notice).toHaveBeenCalledTimes(2);
  notice.mockRestore();
});

it("exposes no Session activation while the host reports disconnected", async () => {
  const services = createTestServices([]);
  const getSettings = vi.spyOn(services.api.chat, "getSettings").mockResolvedValue(initialRead);
  const mutate = vi.spyOn(services.api.chat, "mutateSettings");
  const generation = Symbol("initial");
  const { result, rerender } = renderHook<ChatSettingsFeature, ChatSettingsOptions>(
    (options) => useChatSettings(options),
    {
      initialProps: {
        target,
        onContextChange: vi.fn(),
        serverMutationAvailability: "available",
        ...navigation,
        authoritativeRefreshGeneration: generation,
      },
      wrapper: ({ children }: Readonly<{ children: ReactNode }>) => (
        <TestAppProviders services={services}>{children}</TestAppProviders>
      ),
    },
  );
  await waitFor(() => {
    expect(result.current.kind).toBe("ready-session");
  });
  rerender({
    target,
    onContextChange: vi.fn(),
    serverMutationAvailability: "disconnected",
    ...navigation,
    authoritativeRefreshGeneration: generation,
  });
  expect(result.current).toMatchObject({
    kind: "ready-session",
    settings,
    session: initialRead.session,
    serverMutationAvailability: "disconnected",
  });
  expect(result.current).not.toHaveProperty("activate");
  expect(getSettings).toHaveBeenCalledOnce();
  expect(mutate).not.toHaveBeenCalled();
  rerender({
    target,
    onContextChange: vi.fn(),
    serverMutationAvailability: "available",
    ...navigation,
    authoritativeRefreshGeneration: generation,
  });
  expect(result.current).toMatchObject({ kind: "ready-session", serverMutationAvailability: "available" });
  expect(getSettings).toHaveBeenCalledOnce();
});

it("starts one refresh per changed host generation and re-enables mutations before refresh delivery", async () => {
  const services = createTestServices([]);
  const refresh = deferred<ChatSettingsRead>();
  const getSettings = vi
    .spyOn(services.api.chat, "getSettings")
    .mockResolvedValueOnce(initialRead)
    .mockReturnValue(refresh.promise);
  const mutate = vi.spyOn(services.api.chat, "mutateSettings").mockResolvedValue(response());
  const initialGeneration = Symbol("initial");
  const nextGeneration = Symbol("refresh");
  const { result, rerender } = renderHook<ChatSettingsFeature, ChatSettingsOptions>(
    (options) => useChatSettings(options),
    {
      initialProps: {
        target,
        onContextChange: vi.fn(),
        serverMutationAvailability: "disconnected",
        ...navigation,
        authoritativeRefreshGeneration: initialGeneration,
      },
      wrapper: ({ children }: Readonly<{ children: ReactNode }>) => (
        <TestAppProviders services={services}>{children}</TestAppProviders>
      ),
    },
  );
  await waitFor(() => {
    expect(result.current.kind).toBe("ready-session");
  });
  const available: ChatSettingsOptions = {
    target,
    onContextChange: vi.fn(),
    serverMutationAvailability: "available",
    ...navigation,
    authoritativeRefreshGeneration: nextGeneration,
  };
  rerender(available);
  expect(getSettings).toHaveBeenCalledTimes(2);
  expect(result.current).toMatchObject({
    kind: "ready-session",
    settings,
    serverMutationAvailability: "available",
  });
  rerender({ ...available, onContextChange: vi.fn() });
  expect(getSettings).toHaveBeenCalledTimes(2);
  await act(async () => {
    if (result.current.kind !== "ready-session" || result.current.serverMutationAvailability !== "available")
      throw new Error("Expected available Session.");
    await result.current.activate({ kind: "questions", enabled: false });
  });
  expect(mutate).toHaveBeenCalledOnce();
  await act(async () => {
    refresh.resolve(initialRead);
    await refresh.promise;
  });
  expect(getSettings).toHaveBeenCalledTimes(2);
});

it("preserves whichever authoritative projection is current when a refresh fails", async () => {
  const services = createTestServices([]);
  const refresh = deferred<ChatSettingsRead>();
  const mutation = deferred<ChatSettingsMutationResponse>();
  const getSettings = vi
    .spyOn(services.api.chat, "getSettings")
    .mockResolvedValueOnce(initialRead)
    .mockReturnValue(refresh.promise);
  vi.spyOn(services.api.chat, "mutateSettings").mockReturnValue(mutation.promise);
  const notice = vi.spyOn(ui, "showStatusToast").mockImplementation(() => undefined);
  const { result, rerender } = renderHook<ChatSettingsFeature, ChatSettingsOptions>(
    (options) => useChatSettings(options),
    {
      initialProps: { ...connected, target, onContextChange: vi.fn() },
      wrapper: ({ children }: Readonly<{ children: ReactNode }>) => (
        <TestAppProviders services={services}>{children}</TestAppProviders>
      ),
    },
  );
  await waitFor(() => {
    expect(result.current.kind).toBe("ready-session");
  });
  rerender({
    ...connected,
    target,
    onContextChange: vi.fn(),
    authoritativeRefreshGeneration: Symbol("next"),
  });
  act(() => {
    if (result.current.kind !== "ready-session" || result.current.serverMutationAvailability !== "available")
      throw new Error("Expected available Session.");
    void result.current.activate({ kind: "questions", enabled: false });
  });
  const delivered = response({ ...settings, questions: { ...settings.questions, enabled: false } });
  await act(async () => {
    mutation.resolve(delivered);
    await mutation.promise;
  });
  await act(async () => {
    refresh.reject(typedSettingsFailure());
    await refresh.promise.catch(() => undefined);
  });
  expect(result.current).toMatchObject({ kind: "ready-session", settings: delivered.settings });
  expect(notice).toHaveBeenCalledOnce();
  expect(notice.mock.lastCall?.[0].body).toContain(settingsFailureOperation);
  expect(notice.mock.lastCall?.[0].body).toContain(settingsFailureCause);
  expect(notice.mock.lastCall?.[0].onAction).toBeUndefined();
  expect(getSettings).toHaveBeenCalledTimes(2);
  notice.mockRestore();
});

it.each(["refresh-first", "mutation-first"] as const)(
  "applies refresh and mutation results in %s delivery order",
  async (order) => {
    const services = createTestServices([]);
    const refresh = deferred<ChatSettingsRead>();
    const mutation = deferred<ChatSettingsMutationResponse>();
    vi.spyOn(services.api.chat, "getSettings")
      .mockResolvedValueOnce(initialRead)
      .mockReturnValue(refresh.promise);
    vi.spyOn(services.api.chat, "mutateSettings").mockReturnValue(mutation.promise);
    const onContextChange = vi.fn<(value: ChatContext) => void>();
    const { result, rerender } = renderHook<ChatSettingsFeature, ChatSettingsOptions>(
      (options) => useChatSettings(options),
      {
        initialProps: { ...connected, target, onContextChange },
        wrapper: ({ children }: Readonly<{ children: ReactNode }>) => (
          <TestAppProviders services={services}>{children}</TestAppProviders>
        ),
      },
    );
    await waitFor(() => {
      expect(result.current.kind).toBe("ready-session");
    });
    rerender({ ...connected, target, onContextChange, authoritativeRefreshGeneration: Symbol("next") });
    act(() => {
      if (
        result.current.kind !== "ready-session" ||
        result.current.serverMutationAvailability !== "available"
      )
        throw new Error("Expected available Session.");
      void result.current.activate({ kind: "questions", enabled: false });
    });
    const refreshValue: ChatSettingsRead = {
      ...initialRead,
      settings: { ...settings, supervisor: { ...settings.supervisor, value: "all" } },
    };
    const mutationValue = response({ ...settings, questions: { ...settings.questions, enabled: false } });
    const deliverRefresh = async () => {
      refresh.resolve(refreshValue);
      await refresh.promise;
    };
    const deliverMutation = async () => {
      mutation.resolve(mutationValue);
      await mutation.promise;
    };
    if (order === "refresh-first") {
      await act(deliverRefresh);
      expect(result.current).toMatchObject({ settings: refreshValue.settings });
      await act(deliverMutation);
      expect(result.current).toMatchObject({ settings: mutationValue.settings });
    } else {
      await act(deliverMutation);
      expect(result.current).toMatchObject({ settings: mutationValue.settings });
      await act(deliverRefresh);
      expect(result.current).toMatchObject({ settings: refreshValue.settings });
    }
    expect(onContextChange).toHaveBeenCalledExactlyOnceWith(mutationValue.context);
  },
);

it("discards old reads and mutations through A to B to A target replacement", async () => {
  const services = createTestServices([]);
  const oldRefresh = deferred<ChatSettingsRead>();
  const oldMutation = deferred<ChatSettingsMutationResponse>();
  const abandonedRead = deferred<ChatSettingsRead>();
  const latestRead = deferred<ChatSettingsRead>();
  const getSettings = vi
    .spyOn(services.api.chat, "getSettings")
    .mockResolvedValueOnce(initialRead)
    .mockReturnValueOnce(oldRefresh.promise)
    .mockReturnValueOnce(abandonedRead.promise)
    .mockReturnValueOnce(latestRead.promise);
  vi.spyOn(services.api.chat, "mutateSettings").mockReturnValueOnce(oldMutation.promise);
  const onContextChange = vi.fn<(value: ChatContext) => void>();
  const notice = vi.spyOn(ui, "showStatusToast").mockImplementation(() => undefined);
  const { result, rerender } = renderHook<ChatSettingsFeature, ChatSettingsOptions>(
    (options) => useChatSettings(options),
    {
      initialProps: { ...connected, target, onContextChange },
      wrapper: ({ children }: Readonly<{ children: ReactNode }>) => (
        <TestAppProviders services={services}>{children}</TestAppProviders>
      ),
    },
  );
  await waitFor(() => {
    expect(result.current.kind).toBe("ready-session");
  });
  const generation = Symbol("refresh");
  rerender({ ...connected, target, onContextChange, authoritativeRefreshGeneration: generation });
  act(() => {
    if (result.current.kind !== "ready-session" || result.current.serverMutationAvailability !== "available")
      throw new Error("Expected available Session.");
    void result.current.activate({ kind: "questions", enabled: false });
  });
  rerender({
    ...connected,
    target: { ...target, sessionID: "223e4567-e89b-42d3-a456-426614174000" },
    onContextChange,
  });
  rerender({ ...connected, target, onContextChange, authoritativeRefreshGeneration: generation });
  expect(result.current).toEqual({ kind: "loading-session" });
  const latest: ChatSettingsRead = {
    ...initialRead,
    settings: { ...settings, supervisor: { ...settings.supervisor, value: "all" } },
  };
  await act(async () => {
    latestRead.resolve(latest);
    await latestRead.promise;
  });
  await act(async () => {
    oldRefresh.resolve(initialRead);
    oldMutation.resolve(response());
    abandonedRead.reject(new Error("abandoned target read failed"));
    await Promise.all([
      oldRefresh.promise,
      oldMutation.promise,
      abandonedRead.promise.catch(() => undefined),
    ]);
  });
  expect(result.current).toMatchObject({
    kind: "ready-session",
    settings: latest.settings,
    session: initialRead.session,
  });
  expect(getSettings).toHaveBeenCalledTimes(4);
  expect(onContextChange).not.toHaveBeenCalled();
  expect(notice).not.toHaveBeenCalled();
  notice.mockRestore();
});
