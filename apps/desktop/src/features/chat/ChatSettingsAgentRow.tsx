import { Check } from "lucide-react";

import type { ChatSettingsAgent } from "@/api";

import { SettingsRow } from "./ChatSettingsRows";

export function ChatSettingsAgentRow({
  agent,
  selected,
  transitionName,
  reason,
  onActivate,
}: Readonly<{
  agent: ChatSettingsAgent;
  selected: boolean;
  transitionName: string;
  reason: string | undefined;
  onActivate(): void;
}>) {
  return (
    <SettingsRow onActivate={onActivate} reason={reason}>
      <div
        className="grid min-w-0 flex-1 text-left"
        style={{ viewTransitionName: selected ? transitionName : undefined }}
      >
        <span className="flex min-w-0 items-center gap-[var(--space-2)] font-bold text-[var(--color-on-island)]">
          <span className="truncate">{agent.role}</span>
          {selected ? <Check className="shrink-0" size={14} /> : null}
        </span>
        <span className="truncate font-mono text-xs text-[var(--color-muted)]">
          {agent.model} {agent.thinking}
        </span>
      </div>
    </SettingsRow>
  );
}
