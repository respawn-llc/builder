import {
  createElement,
  useCallback,
  useEffect,
  useEffectEvent,
  useMemo,
  useReducer,
  useRef,
  type ReactElement,
} from "react";
import { useTranslation } from "react-i18next";

import {
  ContractError,
  type ChatSettingsMutation,
  type ChatContext,
  type ChatSettings,
  type ChatSettingsSessionFacts,
  type ChatSettingsMutationResponse,
  type ChatSettingsRead,
  type ChatSettingsTarget,
  type InitialChatSettings,
  type NewChatSettingsCatalog,
} from "@/api";
import { useAppServices } from "@/app-facade";
import { showStatusToast } from "@/ui";
import { ChatSettingsView, type ChatSettingsViewProps } from "./ChatSettingsView";
import { settingsOperationFailureMessage } from "./chatSettingsPresentation";

export type ChatSettingsNavigation = Readonly<{
  openTask(taskID: string): void;
  openParentSession(previousSessionID: string): void;
}>;

export type ChatSettingsOptions =
  | Readonly<{
      target: Extract<ChatSettingsTarget, { kind: "new_chat" }>;
      onInitialSettingsChange(settings: InitialChatSettings): void;
    }>
  | (ChatSettingsNavigation &
      Readonly<{
        target: Extract<ChatSettingsTarget, { kind: "session" }>;
        serverMutationAvailability: "available" | "disconnected";
        authoritativeRefreshGeneration: unknown;
        onContextChange(context: ChatContext): void;
      }>);

type ReadyNewChat = Readonly<{
  kind: "ready-new-chat";
  catalog: NewChatSettingsCatalog;
  initialSettings: InitialChatSettings;
}>;
type ReadySession = Readonly<{
  kind: "ready-session";
  settings: ChatSettings;
  session: ChatSettingsSessionFacts;
  lastDelivered: ChatSettings;
}>;
type LoadingState = Readonly<{ kind: "loading-new-chat" }> | Readonly<{ kind: "loading-session" }>;
type SettingsState =
  | LoadingState
  | Readonly<{ kind: "failed-new-chat"; error: unknown }>
  | Readonly<{ kind: "failed-session"; error: unknown }>
  | ReadyNewChat
  | ReadySession;

export type ReadyChatSettings =
  | (ReadyNewChat & Readonly<{ activate(operation: ChatSettingsMutation): void }>)
  | (Omit<ReadySession, "lastDelivered"> &
      (
        | Readonly<{
            serverMutationAvailability: "available";
            activate(operation: ChatSettingsMutation): Promise<ChatSettingsMutationResponse>;
          }>
        | Readonly<{ serverMutationAvailability: "disconnected" }>
      ));

export type ChatSettingsFeature =
  | Exclude<SettingsState, ReadyNewChat | ReadySession>
  | (ReadyChatSettings & Readonly<{ settingsChip: ReactElement }>);

type SettingsAction =
  | Readonly<{ kind: "loading" }>
  | Readonly<{ kind: "loaded"; response: ChatSettingsRead }>
  | Readonly<{ kind: "failed"; targetKind: ChatSettingsTarget["kind"]; error: unknown }>
  | Readonly<{ kind: "mutated"; response: ChatSettingsMutationResponse }>
  | Readonly<{ kind: "mutation-failed" }>
  | Readonly<{ kind: "activated"; operation: ChatSettingsMutation }>;
type TargetState = Readonly<{ target: ChatSettingsTarget; state: SettingsState }>;

export function useChatSettings(options: ChatSettingsOptions): ChatSettingsFeature {
  const feature = useSettingsState(options);
  if (feature.kind !== "ready-new-chat" && feature.kind !== "ready-session") return feature;
  let viewProps: ChatSettingsViewProps;
  if (feature.kind === "ready-session") {
    if (!("openTask" in options)) throw new Error("Session Settings requires Session navigation callbacks.");
    viewProps = {
      feature,
      navigation: { openTask: options.openTask, openParentSession: options.openParentSession },
    };
  } else {
    viewProps = { feature };
  }
  return { ...feature, settingsChip: createElement(ChatSettingsView, viewProps) };
}

function useSettingsState(
  options: ChatSettingsOptions,
): Exclude<SettingsState, ReadyNewChat | ReadySession> | ReadyChatSettings {
  const { target } = options;
  const { api } = useAppServices();
  const { t } = useTranslation();
  const { projectID } = target;
  const targetKind = target.kind;
  const sessionID = target.kind === "session" ? target.sessionID : null;
  const workspaceKind = "workspaceID" in target.workspace ? "workspaceID" : "workspaceRoot";
  const workspaceValue =
    "workspaceID" in target.workspace ? target.workspace.workspaceID : target.workspace.workspaceRoot;
  const requestedTarget = useMemo<ChatSettingsTarget>(() => {
    const project = {
      projectID,
      workspace:
        workspaceKind === "workspaceID" ? { workspaceID: workspaceValue } : { workspaceRoot: workspaceValue },
    };
    return sessionID === null ? { ...project, kind: "new_chat" } : { ...project, kind: "session", sessionID };
  }, [projectID, workspaceKind, workspaceValue, sessionID]);
  const [owned, dispatchOwned] = useReducer(
    (
      current: TargetState,
      action: Readonly<{ target: ChatSettingsTarget; action: SettingsAction }>,
    ): TargetState => {
      if (action.action.kind === "loading") {
        return { target: action.target, state: loadingState(action.target.kind) };
      }
      if (current.target !== action.target) return current;
      return { target: current.target, state: settingsReducer(current.state, action.action) };
    },
    requestedTarget,
    (initialTarget): TargetState => ({ target: initialTarget, state: loadingState(initialTarget.kind) }),
  );
  const state = owned.target === requestedTarget ? owned.state : loadingState(targetKind);
  const dispatch = useCallback(
    (action: SettingsAction) => {
      dispatchOwned({ target: requestedTarget, action });
    },
    [requestedTarget],
  );
  const observation = useRef<ChatSettingsTarget | null>(null);
  const generation =
    "authoritativeRefreshGeneration" in options ? options.authoritativeRefreshGeneration : null;
  const observedRefresh = useRef<Readonly<{ target: ChatSettingsTarget; generation: unknown }> | null>(null);
  function reportOperationFailure(body: string) {
    showStatusToast({
      id: "chat-settings-operation",
      tone: "danger",
      title: t("chatSettings.operationFailed"),
      body,
    });
  }
  const readSettings = useEffectEvent((kind: "initial" | "refresh") => {
    function failed(error: unknown) {
      if (observation.current !== requestedTarget) return;
      if (kind === "refresh") reportOperationFailure(settingsOperationFailureMessage(t, error));
      else dispatch({ kind: "failed", targetKind, error });
    }
    void api.chat.getSettings(requestedTarget).then((response) => {
      if (observation.current !== requestedTarget) return;
      if (response.kind !== targetKind) {
        failed(new ContractError("Chat Settings returned a different target kind."));
        return;
      }
      dispatch({ kind: "loaded", response });
    }, failed);
  });
  useEffect(() => {
    observation.current = requestedTarget;
    dispatch({ kind: "loading" });
    readSettings("initial");
    return () => {
      observation.current = null;
    };
  }, [api, requestedTarget, targetKind, dispatch]);

  useEffect(() => {
    const previous = observedRefresh.current;
    observedRefresh.current = { target: requestedTarget, generation };
    if (previous?.target !== requestedTarget || Object.is(previous.generation, generation)) return;
    if (requestedTarget.kind !== "session" || state.kind !== "ready-session") return;
    readSettings("refresh");
  }, [api, requestedTarget, generation, state.kind, dispatch]);

  const reportSelection = useEffectEvent((selection: InitialChatSettings) => {
    if ("onInitialSettingsChange" in options) options.onInitialSettingsChange(selection);
  });
  useEffect(() => {
    if (state.kind === "ready-new-chat") reportSelection(state.initialSettings);
  }, [state]);

  if (state.kind === "ready-session") {
    if (target.kind !== "session" || !("onContextChange" in options)) return loadingState(target.kind);
    if (options.serverMutationAvailability === "disconnected")
      return {
        kind: state.kind,
        settings: state.settings,
        session: state.session,
        serverMutationAvailability: "disconnected",
      };
    const ready: Extract<
      ReadyChatSettings,
      { kind: "ready-session"; serverMutationAvailability: "available" }
    > = {
      kind: state.kind,
      serverMutationAvailability: "available",
      settings: state.settings,
      session: state.session,
      async activate(operation) {
        dispatch({ kind: "activated", operation });
        let response: ChatSettingsMutationResponse;
        try {
          response = await api.chat.mutateSettings(target, operation);
        } catch (error) {
          if (observation.current !== requestedTarget) throw error;
          dispatch({ kind: "mutation-failed" });
          reportOperationFailure(settingsOperationFailureMessage(t, error));
          throw error;
        }
        if (observation.current !== requestedTarget) return response;
        dispatch({ kind: "mutated", response });
        options.onContextChange(response.context);
        if (response.result.kind === "rejected")
          reportOperationFailure(t(`chatSettings.rejections.${response.result.reason}`));
        return response;
      },
    };
    return ready;
  }
  if (state.kind !== "ready-new-chat") return state;
  return {
    ...state,
    activate: (operation) => {
      dispatch({ kind: "activated", operation });
    },
  };
}

function loadingState(kind: ChatSettingsTarget["kind"]): LoadingState {
  return kind === "new_chat" ? { kind: "loading-new-chat" } : { kind: "loading-session" };
}

function settingsReducer(
  state: SettingsState,
  action: Exclude<SettingsAction, { kind: "loading" }>,
): SettingsState {
  switch (action.kind) {
    case "loaded":
      return loadedState(action.response);
    case "failed":
      return action.targetKind === "new_chat"
        ? { kind: "failed-new-chat", error: action.error }
        : { kind: "failed-session", error: action.error };
    case "mutated":
      return loadedState({
        kind: "session",
        settings: action.response.settings,
        session: action.response.session,
      });
    case "mutation-failed":
      return state.kind === "ready-session" ? { ...state, settings: state.lastDelivered } : state;
    case "activated": {
      if (state.kind === "ready-session")
        return { ...state, settings: activateSession(state.settings, action.operation) };
      if (state.kind !== "ready-new-chat") return state;
      const initialSettings = activateNewChat(state, action.operation);
      return initialSettings === state.initialSettings ? state : { ...state, initialSettings };
    }
  }
}

function loadedState(response: ChatSettingsRead): ReadyNewChat | ReadySession {
  return response.kind === "session"
    ? {
        kind: "ready-session",
        settings: response.settings,
        session: response.session,
        lastDelivered: response.settings,
      }
    : {
        kind: "ready-new-chat",
        catalog: response.catalog,
        initialSettings: response.initialSettings,
      };
}

function activateSession(current: ChatSettings, operation: ChatSettingsMutation): ChatSettings {
  switch (operation.kind) {
    case "agent": {
      const choice = current.agentChoices.find((candidate) => candidate.role === operation.role);
      if (choice === undefined) throw new Error("Selected Agent is not in the Session choices.");
      return {
        ...current,
        selectedAgent: { role: choice.role, model: choice.model, thinking: choice.thinking },
      };
    }
    case "supervisor":
      return { ...current, supervisor: { ...current.supervisor, value: operation.value } };
    case "thinking":
      return current.thinking.kind === "unsupported"
        ? current
        : {
            ...current,
            thinking: { ...current.thinking, value: operation.value },
            selectedAgent: { ...current.selectedAgent, thinking: operation.value },
          };
    case "fast":
      return current.fast.kind === "unsupported"
        ? current
        : { ...current, fast: { ...current.fast, value: operation.enabled } };
    case "questions":
      return { ...current, questions: { ...current.questions, enabled: operation.enabled } };
    case "auto_compaction":
      return current.autoCompaction.policy === "optional"
        ? {
            ...current,
            autoCompaction: {
              ...current.autoCompaction,
              stored: operation.enabled,
              effective: operation.enabled,
            },
          }
        : current;
  }
}

function activateNewChat(state: ReadyNewChat, operation: ChatSettingsMutation): InitialChatSettings {
  const current = state.initialSettings;
  switch (operation.kind) {
    case "agent": {
      if (operation.role === current.agentRole) return current;
      const selected = state.catalog.choices.find((choice) => choice.agent.role === operation.role);
      if (selected === undefined) throw new Error("Selected Agent is not in the New Chat catalog.");
      return selected.baseline;
    }
    case "supervisor":
      return { ...current, supervisor: operation.value };
    case "thinking": {
      const value = operation.value.trim();
      return value.length === 0 ? current : { ...current, thinking: value };
    }
    case "fast":
      return { ...current, fast: operation.enabled };
    case "questions":
      return { ...current, questionsEnabled: operation.enabled };
    case "auto_compaction":
      return { ...current, autoCompactionEnabled: operation.enabled };
  }
}
