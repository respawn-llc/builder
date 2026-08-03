import { CheckCircle2, Circle, CircleDot } from "lucide-react";
import type { ReactNode } from "react";

import type { TaskStatusKind } from "@/api";
import { Spinner } from "@/ui";

export function TaskStatusIcon({ status }: Readonly<{ status: TaskStatusKind }>): ReactNode {
  switch (status) {
    case "done":
      return <CheckCircle2 aria-hidden="true" className="text-[var(--color-success)]" size={15} />;
    case "backlog":
      return <Circle aria-hidden="true" size={15} />;
    case "active":
      return <CircleDot aria-hidden="true" className="text-[var(--color-primary)]" size={15} />;
    case "queued":
    case "running":
      return <Spinner className="size-[15px]" size="sm" strokeWidth={2} />;
    case "waiting_approval":
    case "interrupted":
      return <CircleDot aria-hidden="true" className="text-[var(--color-secondary)]" size={15} />;
    case "waiting_question":
      return <CircleDot aria-hidden="true" className="text-[var(--color-primary)]" size={15} />;
  }
}
