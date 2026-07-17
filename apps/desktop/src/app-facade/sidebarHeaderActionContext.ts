import { createContext, useContext, useEffect, type ReactNode } from "react";

// Lets a sidebar destination body publish a primary action (e.g. a save button) into the shared
// sidebar header, rendered to the left of the destination accessory. The body owns the action's
// state/handler; the header only hosts the slot, so neither side depends on the other's internals.
export type SidebarHeaderActionSetter = (action: ReactNode) => void;

export const SidebarHeaderActionStateContext = createContext<ReactNode>(null);
export const SidebarHeaderActionSetContext = createContext<SidebarHeaderActionSetter | null>(null);

// Publishes (and clears on unmount/change) the header action. Pass a memoized node so the effect
// only re-runs when the action meaningfully changes rather than on every keystroke.
export function usePublishSidebarHeaderAction(action: ReactNode): void {
  const setAction = useContext(SidebarHeaderActionSetContext);
  useEffect(() => {
    if (setAction === null) {
      return;
    }
    setAction(action);
    return () => {
      setAction(null);
    };
  }, [action, setAction]);
}
