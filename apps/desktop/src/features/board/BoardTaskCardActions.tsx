import { Octagon, Play, Square } from "lucide-react";
import { useTranslation } from "react-i18next";

import { IconTooltipButton } from "@/ui";
import type { KanbanCardVM } from "./BoardColumnViewModel";

export function BoardTaskCardActions({
  actionsDisabled,
  card,
  onInterrupt,
  onResume,
  pendingInterrupt,
  pendingResume,
}: Readonly<{
  actionsDisabled: boolean;
  card: KanbanCardVM;
  onInterrupt: (taskID: string) => void;
  onResume: (taskID: string) => void;
  pendingInterrupt: boolean;
  pendingResume: boolean;
}>) {
  const { t } = useTranslation();
  const availableActions = {
    canInterrupt: card.actions.canInterrupt,
    canResume: card.actions.canResume,
  };
  if (!availableActions.canInterrupt && !availableActions.canResume) {
    return null;
  }
  return (
    <div className="flex shrink-0 flex-wrap justify-end gap-[var(--space-2)]">
      {availableActions.canResume ? (
        <IconTooltipButton
          label={card.statusKind === "queued" ? t("board.waitingDueToConcurrencyLimits") : t("board.resume")}
          onClick={(event) => {
            event.stopPropagation();
            onResume(card.id);
          }}
          disabled={actionsDisabled || pendingResume}
          size="icon-sm"
          variant={card.statusKind === "queued" ? "warning" : "primary-outline"}
        >
          {card.statusKind === "queued" ? (
            <Octagon aria-hidden="true" fill="currentColor" size={13} strokeWidth={0} />
          ) : (
            <Play aria-hidden="true" fill="currentColor" size={12} strokeWidth={0} />
          )}
        </IconTooltipButton>
      ) : null}
      {availableActions.canInterrupt ? (
        <IconTooltipButton
          label={t("board.interrupt")}
          onClick={(event) => {
            event.stopPropagation();
            onInterrupt(card.id);
          }}
          disabled={actionsDisabled || pendingInterrupt}
          size="icon-sm"
          variant="danger"
        >
          <Square
            aria-hidden="true"
            className="text-[var(--color-error)]"
            fill="currentColor"
            size={12}
            strokeWidth={0}
          />
        </IconTooltipButton>
      ) : null}
    </div>
  );
}
