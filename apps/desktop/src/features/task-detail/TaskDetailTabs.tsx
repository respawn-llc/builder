import { useTranslation } from "react-i18next";

import { IslandTabs } from "@/ui";

export type DetailTab = "comments" | "activity";

export function TaskTabs({
  activityCount,
  commentCount,
  selected,
  onSelect,
}: Readonly<{
  activityCount: number;
  commentCount: number;
  selected: DetailTab;
  onSelect: (tab: DetailTab) => void;
}>) {
  const { t } = useTranslation();
  return (
    <>
      <IslandTabs
        ariaLabel={t("task.title")}
        className="grid-cols-2"
        items={[
          { label: t("task.comments"), meta: commentCount, value: "comments" },
          { label: t("task.activity"), value: "activity" },
        ]}
        onValueChange={onSelect}
        value={selected}
      />
      <span className="sr-only">{t("task.activityCount", { count: activityCount })}</span>
    </>
  );
}
