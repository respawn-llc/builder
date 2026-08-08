import { ChevronLeft, ChevronRight } from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import type { SidebarDestination } from "@/app-facade";
import type { SidebarPageNavigator } from "@/app-facade";
import { taskDetailInitialFocusFromAttentionItem } from "@/app-facade";
import { IconTooltipButton } from "@/ui";
import { inboxNavNeighbors, orderedInboxTaskIDs } from "./inboxNavNeighbors";
import { useSidebarGlobalAttentionPages } from "./useHomeData";

type TaskDetailDestination = Extract<SidebarDestination, { kind: "taskDetail" }>;

/**
 * Live Previous/Next controls for a task detail sidebar opened from the Home
 * inbox. It reads the shared attention query (kept fresh by HomeRoute, which
 * stays mounted beneath the overlay sidebar) so navigation always reflects the
 * current inbox, including after the open task is resolved and drops out.
 */
export function SidebarInboxNav({ destination, navigator }: Readonly<{ destination: TaskDetailDestination; navigator: SidebarPageNavigator }>) {
  const { t } = useTranslation();
  const attention = useSidebarGlobalAttentionPages();
  // Remembers the open task's last position so Next still works after it is
  // resolved and drops out of the live inbox; updated only while it is present.
  const [anchorIndex, setAnchorIndex] = useState(0);

  const attentionItems = useMemo(
    () => attention.data?.pages.flatMap((page) => page.items) ?? [],
    [attention.data],
  );
  const taskIDs = useMemo(
    () => orderedInboxTaskIDs(attentionItems.map((item) => item.taskID)),
    [attentionItems],
  );

  // Adjust the remembered anchor while rendering (the sanctioned alternative to a
  // ref read or an effect): whenever the open task is present, its current index
  // becomes the anchor used once it later drops out.
  const found = taskIDs.indexOf(destination.taskID);
  if (found >= 0 && found !== anchorIndex) {
    setAnchorIndex(found);
  }

  const neighbors = inboxNavNeighbors(taskIDs, destination.taskID, anchorIndex);

  const goTo = (taskID: string | null) => {
    if (taskID === null) {
      return;
    }
    navigator.replace({
      ...destination,
      initialFocus: taskDetailInitialFocusFromAttentionItem(
        attentionItems.find((candidate) => candidate.taskID === taskID),
      ),
      taskID,
    });
  };

  const previousTaskID = neighbors.previousTaskID;
  const nextTaskID = neighbors.nextTaskID;

  return (
    <>
      {previousTaskID === null ? null : (
        <IconTooltipButton
          label={t("app.inboxPrevious")}
          onClick={() => {
            goTo(previousTaskID);
          }}
        >
          <ChevronLeft aria-hidden="true" size={18} strokeWidth={1.5} />
        </IconTooltipButton>
      )}
      {nextTaskID === null ? null : (
        <IconTooltipButton
          label={t("app.inboxNext")}
          onClick={() => {
            goTo(nextTaskID);
          }}
        >
          <ChevronRight aria-hidden="true" size={18} strokeWidth={1.5} />
        </IconTooltipButton>
      )}
    </>
  );
}
