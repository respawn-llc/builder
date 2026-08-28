import { ListTodo, Plus } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import type { WorkflowPickerItem } from "@/api";
import {
  AnimatedChipSummary,
  InteractiveChip,
  Popover,
  PopoverContent,
  PopoverTrigger,
  RadioGroup,
  RadioGroupItem,
} from "@/ui";

export function BoardWorkflowPicker({
  activeWorkflow,
  canLinkWorkflow,
  onLinkWorkflow,
  onOpenTasks,
  onSelectWorkflow,
  workflows,
}: Readonly<{
  activeWorkflow: WorkflowPickerItem;
  canLinkWorkflow: boolean;
  onLinkWorkflow: () => void;
  onOpenTasks: () => void;
  onSelectWorkflow: (workflowID: string) => void;
  workflows: readonly WorkflowPickerItem[];
}>) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  return (
    <Popover onOpenChange={setOpen} open={open}>
      <PopoverTrigger asChild>
        <InteractiveChip aria-label={activeWorkflow.name}>
          <AnimatedChipSummary text={activeWorkflow.name} />
        </InteractiveChip>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-64" level={4}>
        <RadioGroup
          aria-label={t("board.workflowPicker")}
          onValueChange={(value) => {
            setOpen(false);
            onSelectWorkflow(value);
          }}
          value={activeWorkflow.id}
        >
          <button
            className="flex cursor-pointer items-center gap-[var(--space-2)] rounded-[var(--radius-m)] px-[var(--space-2)] py-[var(--space-2)] text-left hover:bg-[var(--color-island-2)]"
            onClick={() => {
              setOpen(false);
              onOpenTasks();
            }}
            type="button"
          >
            <ListTodo aria-hidden="true" size={16} strokeWidth={1.7} />
            <span>{t("home.prototype.tasks")}</span>
          </button>
          {workflows.map((workflow) => (
            <label
              className="flex cursor-pointer items-center gap-[var(--space-2)] rounded-[var(--radius-m)] px-[var(--space-2)] py-[var(--space-2)] hover:bg-[var(--color-island-2)]"
              key={workflow.id}
            >
              <RadioGroupItem aria-label={workflow.name} value={workflow.id} />
              <span className="min-w-0 truncate">{workflow.name}</span>
            </label>
          ))}
          <button
            className="mt-[var(--space-1)] flex cursor-pointer items-center gap-[var(--space-2)] border-t border-[var(--color-outline)] px-[var(--space-2)] pt-[var(--space-3)] pb-[var(--space-2)] text-left hover:text-[var(--color-on-island)] disabled:cursor-not-allowed disabled:opacity-45"
            disabled={!canLinkWorkflow}
            onClick={() => {
              setOpen(false);
              onLinkWorkflow();
            }}
            type="button"
          >
            <Plus aria-hidden="true" size={16} strokeWidth={1.7} />
            <span>{t("workflowLibrary.linkWorkflow")}</span>
          </button>
        </RadioGroup>
      </PopoverContent>
    </Popover>
  );
}
