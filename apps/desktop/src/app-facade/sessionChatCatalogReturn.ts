import { createContext, useCallback, useContext } from "react";

import type { SessionCategory } from "@/api";

export type SessionChatCatalogReturn = Readonly<{
  category: SessionCategory;
  projectID: string;
}>;

export type SessionChatCatalogReturnContextValue = Readonly<{
  catalogReturn: SessionChatCatalogReturn | null;
  consume(projectID: string): void;
}>;

export const SessionChatCatalogReturnContext = createContext<SessionChatCatalogReturnContextValue | null>(null);

export function useSessionChatCatalogReturn(
  projectID: string,
): Readonly<{ category: SessionCategory | null; consume(): void }> | null {
  const context = useContext(SessionChatCatalogReturnContext);
  const consume = useCallback(() => {
    context?.consume(projectID);
  }, [context, projectID]);
  if (context === null) {
    return null;
  }
  return {
    category: context.catalogReturn?.projectID === projectID ? context.catalogReturn.category : null,
    consume,
  };
}
