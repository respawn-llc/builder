import { TaskDetailSurface } from "./TaskDetailSurface";

/**
 * Full-bleed native-window host for a popped-out task detail. Unlike the padded
 * dialog shell used by forms, the surface ({@link TaskDetailSurface} via
 * `TaskDetailList`) already owns its own padding and scrolling, so this shell adds
 * only the glass fill, a top drag strip, and the titlebar inset that keeps the
 * macOS traffic lights off the content — exactly how the in-app sidebar hosts it.
 */
export function TaskDetailWindowRoute({ taskID }: Readonly<{ taskID: string }>) {
  return (
    <main className="window-glass-fill grid h-screen w-screen grid-rows-[minmax(0,1fr)] overflow-hidden pt-[var(--native-titlebar-height)]">
      <div
        className="app-region-drag fixed inset-x-0 top-0 h-[var(--native-titlebar-height)]"
        data-tauri-drag-region
      />
      <div className="app-region-no-drag min-h-0 overflow-hidden">
        <TaskDetailSurface enabled taskId={taskID} />
      </div>
    </main>
  );
}
