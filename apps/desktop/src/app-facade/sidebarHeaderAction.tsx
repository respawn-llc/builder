import { useContext, useState, type ReactNode } from "react";

import { SidebarHeaderActionSetContext, SidebarHeaderActionStateContext } from "./sidebarHeaderActionContext";

export function SidebarHeaderActionProvider({ children }: Readonly<{ children: ReactNode }>) {
  const [action, setAction] = useState<ReactNode>(null);
  return (
    <SidebarHeaderActionSetContext.Provider value={setAction}>
      <SidebarHeaderActionStateContext.Provider value={action}>
        {children}
      </SidebarHeaderActionStateContext.Provider>
    </SidebarHeaderActionSetContext.Provider>
  );
}

export function SidebarHeaderActionSlot() {
  return <>{useContext(SidebarHeaderActionStateContext)}</>;
}
