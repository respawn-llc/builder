import type { AttentionNotificationEventHandler } from "./attentionNotifications";
import { ContractError } from "./errors";
import {
  attentionNotificationEventParamsSchema,
  isUnsupportedAttentionNotificationEventParams,
} from "./schemas/attentionNotification";
import type { RpcEventHandler } from "./transport";

export function attentionNotificationRpcHandler(handler: AttentionNotificationEventHandler): RpcEventHandler {
  return {
    ...(handler.onOpen !== undefined ? { onOpen: handler.onOpen } : {}),
    onComplete: handler.onComplete,
    onError: handler.onError,
    onEvent(method, params) {
      if (method !== "attention.notification") {
        return;
      }
      const parsed = attentionNotificationEventParamsSchema.safeParse(params);
      if (parsed.success) {
        handler.onEvent(parsed.data.event);
        return;
      }
      if (isUnsupportedAttentionNotificationEventParams(params)) {
        return;
      }
      handler.onError(new ContractError("attention.notification event did not match GUI contract."));
    },
  };
}
