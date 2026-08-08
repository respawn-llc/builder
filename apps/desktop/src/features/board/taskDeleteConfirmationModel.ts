import { taskDeleteDialogWidth } from "@/shared/task-delete";

export const taskDeleteNativeDialogPath = "/native-dialog/task-delete";

export type TaskDeleteTarget = Readonly<{
  taskID: string;
}>;

export function taskDeleteWindowOptions(target: TaskDeleteTarget, title: string) {
  return {
    initialHeight: 230,
    initialWidth: taskDeleteDialogWidth,
    label: `task-delete-${target.taskID}`,
    params: {
      taskID: target.taskID,
    },
    route: taskDeleteNativeDialogPath,
    title,
  };
}
