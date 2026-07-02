import { useQuery } from "@tanstack/react-query";

import { useAppServices } from "../../app/useAppServices";

export type ScriptOpenAvailabilityTarget = Readonly<{
  scriptPath: string;
  worktreePath: string;
}>;

export function useScriptOpenAvailability(target: ScriptOpenAvailabilityTarget): boolean {
  const { nativeBridge } = useAppServices();
  const scriptPath = target.scriptPath.trim();
  const canCheck =
    scriptPath.length > 0 &&
    nativeBridge.capabilities.files.stat &&
    nativeBridge.capabilities.files.open;
  const availability = useQuery({
    enabled: canCheck,
    queryFn: async () => nativeBridge.files.fileAvailable({ basePath: target.worktreePath, path: scriptPath }),
    queryKey: [
      "task-detail",
      "script-open-availability",
      target.worktreePath,
      scriptPath,
      nativeBridge.capabilities.files.stat,
      nativeBridge.capabilities.files.open,
    ],
  });
  return canCheck && availability.data === true;
}
