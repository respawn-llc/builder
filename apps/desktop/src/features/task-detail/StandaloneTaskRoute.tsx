import { useTranslation } from "react-i18next";

import { useAppNavigation } from "../../app/navigation";
import { Button } from "../../ui";
import { TaskDetailSurface } from "./TaskDetailSurface";

export type StandaloneTaskRouteProps = Readonly<{
  taskId: string;
}>;

export function StandaloneTaskRoute({ taskId }: StandaloneTaskRouteProps) {
  const { t } = useTranslation();
  const navigation = useAppNavigation();
  return (
    <section className="grid h-full min-h-0 grid-rows-[auto_minmax(0,1fr)] gap-[var(--space-3)] p-[var(--space-3)]">
      <header className="flex items-center justify-end gap-[var(--space-2)]">
        <h1 className="sr-only">{t("task.title")}</h1>
        <Button onClick={() => void navigation.openHome()} variant="ghost">
          {t("app.backHome")}
        </Button>
      </header>
      <TaskDetailSurface enabled taskId={taskId} />
    </section>
  );
}
