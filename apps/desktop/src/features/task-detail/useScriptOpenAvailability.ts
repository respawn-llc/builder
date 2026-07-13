import { useQuery } from "@tanstack/react-query";

import { useAppServices } from "../../app/useAppServices";

export type ScriptOpenAvailabilityTarget = Readonly<{
  basePath: string | null;
  scriptPath: string;
}>;

export function useScriptOpenAvailability(target: ScriptOpenAvailabilityTarget): boolean {
  const { nativeBridge } = useAppServices();
  const basePath = nonBlank(target.basePath);
  const scriptPath = nonBlank(target.scriptPath);
  const canCheck =
    basePath !== null &&
    scriptPath !== null &&
    nativeBridge.capabilities.files.stat &&
    nativeBridge.capabilities.files.open;
  const availability = useQuery({
    enabled: canCheck,
    queryFn: async () => {
      if (basePath === null || scriptPath === null) {
        throw new Error("enabled script availability requires a base path and script path");
      }
      return nativeBridge.files.fileAvailable({ basePath, path: scriptPath });
    },
    queryKey: [
      "task-detail",
      "script-open-availability",
      basePath,
      scriptPath,
      nativeBridge.capabilities.files.stat,
      nativeBridge.capabilities.files.open,
    ],
  });
  return canCheck && availability.data === true;
}

function nonBlank(value: string | null): string | null {
  const trimmed = value?.trim();
  return trimmed === undefined || trimmed.length === 0 ? null : trimmed;
}
