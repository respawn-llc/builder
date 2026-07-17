import type { NativeDialogWindowOptions } from "@app/native-bridge";

import type { SidebarDestination } from "@/app-facade";

export const taskDetailNativeDialogPath = "/native-dialog/task-detail";

/**
 * Maps a sidebar destination to the native window that reproduces it as a
 * standalone, resizable pop-out. Returning `null` means the destination has no
 * pop-out surface yet; add a case here to make a future sidebar poppable without
 * touching the header button or native-bridge plumbing.
 */
export function sidebarPopOutOptions(
  destination: SidebarDestination,
  title: string,
): NativeDialogWindowOptions | null {
  if (destination.kind === "taskDetail") {
    return {
      initialHeight: 760,
      initialWidth: 560,
      label: `task-detail-${destination.taskID}`,
      maximizable: true,
      params: { taskID: destination.taskID },
      resizable: true,
      route: taskDetailNativeDialogPath,
      title,
    };
  }
  return null;
}
