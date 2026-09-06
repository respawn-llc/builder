import { SupervisorValue } from "@app/server-api-contract/gen/kent/api/chat_settings/chat_settings_pb";
import type { InitialChatSettings } from "./chatTypes";
import { ContractError } from "./errors";

export function supervisorToWire(value: InitialChatSettings["supervisor"]): SupervisorValue {
  switch (value) {
    case "off":
      return SupervisorValue.OFF;
    case "edits":
      return SupervisorValue.AFTER_EDITS;
    case "all":
      return SupervisorValue.ALWAYS;
  }
}

export function supervisorFromWire(value: SupervisorValue): InitialChatSettings["supervisor"] {
  switch (value) {
    case SupervisorValue.OFF:
      return "off";
    case SupervisorValue.AFTER_EDITS:
      return "edits";
    case SupervisorValue.ALWAYS:
      return "all";
    case SupervisorValue.UNSPECIFIED:
      throw new ContractError("Chat Settings Supervisor value is invalid.");
  }
}
