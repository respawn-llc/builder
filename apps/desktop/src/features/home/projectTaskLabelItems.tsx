import type { TaskListItem } from "@/api";
import { Badge, type OneLineOverflowItem } from "@/ui";

export function projectTaskLabelItems(labels: TaskListItem["labels"]): readonly OneLineOverflowItem[] {
  return labels.map((label) => ({
    content: (
      <Badge className="py-[3px]" size="compact" tone="neutral">
        {label.name}
      </Badge>
    ),
    id: label.id,
  }));
}
