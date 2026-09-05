import { useEffect, useEffectEvent, useReducer } from "react";

import {
  ContractError,
  type ChatSettingsMutation,
  type ChatSettingsRead,
  type ChatSettingsTarget,
  type InitialChatSettings,
  type NewChatSettingsCatalog,
} from "@/api";
import { useAppServices } from "@/app-facade";

export type ChatSettingsOptions = Readonly<{
  target: Extract<ChatSettingsTarget, { kind: "new_chat" }>;
  onInitialSettingsChange(settings: InitialChatSettings): void;
}>;

type ReadyNewChat = Readonly<{
  kind: "ready-new-chat";
  catalog: NewChatSettingsCatalog;
  initialSettings: InitialChatSettings;
}>;
type SettingsState =
  | Readonly<{ kind: "loading-new-chat" }>
  | Readonly<{ kind: "failed-new-chat"; error: unknown }>
  | ReadyNewChat;

export type ChatSettingsFeature =
  | Exclude<SettingsState, ReadyNewChat>
  | (ReadyNewChat & Readonly<{ activate(operation: ChatSettingsMutation): void }>);

type SettingsAction =
  | Readonly<{ kind: "loading" }>
  | Readonly<{ kind: "loaded"; response: Extract<ChatSettingsRead, { kind: "new_chat" }> }>
  | Readonly<{ kind: "failed"; error: unknown }>
  | Readonly<{ kind: "activated"; operation: ChatSettingsMutation }>;

export function useChatSettings({
  target,
  onInitialSettingsChange,
}: ChatSettingsOptions): ChatSettingsFeature {
  const { api } = useAppServices();
  const [state, dispatch] = useReducer(settingsReducer, { kind: "loading-new-chat" });
  const { projectID } = target;
  const workspaceKind = "workspaceID" in target.workspace ? "workspaceID" : "workspaceRoot";
  const workspaceValue =
    "workspaceID" in target.workspace ? target.workspace.workspaceID : target.workspace.workspaceRoot;
  useEffect(() => {
    let observing = true;
    dispatch({ kind: "loading" });
    const requestedTarget: ChatSettingsOptions["target"] = {
      kind: "new_chat",
      projectID,
      workspace:
        workspaceKind === "workspaceID" ? { workspaceID: workspaceValue } : { workspaceRoot: workspaceValue },
    };
    void api.chat.getSettings(requestedTarget).then(
      (response) => {
        if (!observing) return;
        if (response.kind !== "new_chat") {
          dispatch({ kind: "failed", error: new ContractError("New Chat Settings returned a Session.") });
          return;
        }
        dispatch({ kind: "loaded", response });
      },
      (error: unknown) => {
        if (observing) dispatch({ kind: "failed", error });
      },
    );
    return () => {
      observing = false;
    };
  }, [api, projectID, workspaceKind, workspaceValue]);

  const reportSelection = useEffectEvent(onInitialSettingsChange);
  useEffect(() => {
    if (state.kind === "ready-new-chat") reportSelection(state.initialSettings);
  }, [state]);

  if (state.kind !== "ready-new-chat") return state;
  return {
    ...state,
    activate: (operation) => {
      dispatch({ kind: "activated", operation });
    },
  };
}

function settingsReducer(state: SettingsState, action: SettingsAction): SettingsState {
  switch (action.kind) {
    case "loading":
      return { kind: "loading-new-chat" };
    case "loaded":
      return {
        kind: "ready-new-chat",
        catalog: action.response.catalog,
        initialSettings: action.response.initialSettings,
      };
    case "failed":
      return { kind: "failed-new-chat", error: action.error };
    case "activated": {
      if (state.kind !== "ready-new-chat") return state;
      const initialSettings = activateNewChat(state, action.operation);
      return initialSettings === state.initialSettings ? state : { ...state, initialSettings };
    }
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
