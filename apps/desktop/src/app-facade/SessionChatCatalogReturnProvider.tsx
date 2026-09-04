import { useRouter } from "@tanstack/react-router";
import { useLayoutEffect, useState, type ReactNode } from "react";

import { SessionChatCatalogReturnContext, type SessionChatCatalogReturn } from "./sessionChatCatalogReturn";
import { sessionChatHistoryStateSchema } from "./sessionChatHistory";

type SessionChatHistoryRead =
  | Readonly<{ kind: "absent" }>
  | Readonly<{ kind: "withoutCatalog" }>
  | Readonly<{ kind: "withCatalog"; value: SessionChatCatalogReturn }>;

export function SessionChatCatalogReturnProvider({ children }: Readonly<{ children: ReactNode }>) {
  const router = useRouter();
  const [catalogReturn, setCatalogReturn] = useState<SessionChatCatalogReturn | null>(() => {
    const historyRead = readSessionChatHistory(router.history.location.state);
    return historyRead.kind === "withCatalog" ? historyRead.value : null;
  });

  useLayoutEffect(() => {
    return router.history.subscribe(({ location }) => {
      const historyRead = readSessionChatHistory(location.state);
      if (historyRead.kind === "withCatalog") {
        setCatalogReturn(historyRead.value);
      } else if (historyRead.kind === "withoutCatalog") {
        setCatalogReturn(null);
      }
    });
  }, [router.history]);

  return (
    <SessionChatCatalogReturnContext.Provider
      value={{
        catalogReturn,
        consume: (projectID) => {
          setCatalogReturn((current) => (current?.projectID === projectID ? null : current));
        },
      }}
    >
      {children}
    </SessionChatCatalogReturnContext.Provider>
  );
}

function readSessionChatHistory(state: unknown): SessionChatHistoryRead {
  const parsed = sessionChatHistoryStateSchema.safeParse(state);
  if (!parsed.success || !Object.hasOwn(parsed.data, "sessionChat")) {
    return { kind: "absent" };
  }
  const sessionChat = parsed.data.sessionChat;
  const projectID = sessionChat?.projectID;
  const catalogOrigin = sessionChat?.catalogOrigin;
  if (projectID === undefined || catalogOrigin === undefined || catalogOrigin === null) {
    return { kind: "withoutCatalog" };
  }
  return {
    kind: "withCatalog",
    value: {
      category: catalogOrigin.category,
      projectID,
    },
  };
}
