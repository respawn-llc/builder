import { useTranslation } from "react-i18next";

import { SidebarRootOwner, useAppNavigation, useOwnedSidebarRoots } from "@/app-facade";
import { Button } from "@/ui";
import { TaskDetailSurface } from "./TaskDetailSurface";
import { useExactTaskDetailDeleteDismissal } from "./taskDetailDismissal";

export type StandaloneTaskRouteProps = Readonly<{
  taskId: string;
}>;

export function StandaloneTaskRoute({ taskId }: StandaloneTaskRouteProps) {
  return (
    <SidebarRootOwner>
      <OwnedStandaloneTaskRoute taskId={taskId} />
    </SidebarRootOwner>
  );
}

function OwnedStandaloneTaskRoute({ taskId }: StandaloneTaskRouteProps) {
  const { t } = useTranslation();
  const navigation = useAppNavigation();
  const { open } = useOwnedSidebarRoots();
  const onDeleteDismiss = useExactTaskDetailDeleteDismissal(taskId, async () => {
    await navigation.openHome();
  });
  return (
    <section className="grid h-full min-h-0 grid-rows-[auto_minmax(0,1fr)] gap-[var(--space-3)] p-[var(--space-3)]">
      <header className="flex items-center justify-end gap-[var(--space-2)]">
        <h1 className="sr-only">{t("task.title")}</h1>
        <Button onClick={() => void navigation.openHome()} variant="ghost">
          {t("app.backHome")}
        </Button>
      </header>
      <TaskDetailSurface enabled onDeleteDismiss={onDeleteDismiss} openSidebar={open} taskId={taskId} />
    </section>
  );
}
