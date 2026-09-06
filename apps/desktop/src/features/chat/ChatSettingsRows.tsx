import type { ReactNode } from "react";

import { DisabledInteractionGuard, Switch } from "@/ui";

export function SettingsRow({
  children,
  reason,
  onActivate,
}: Readonly<{
  children: ReactNode;
  reason: string | undefined;
  onActivate?: () => void;
}>) {
  return (
    <DisabledInteractionGuard
      className="rounded-[var(--radius-m)] transition-opacity duration-[var(--motion-fast)] motion-reduce:transition-none data-[disabled=true]:opacity-45"
      disabled={reason !== undefined}
      reason={reason}
    >
      <div
        className="flex min-w-0 items-center gap-[var(--space-3)] rounded-[var(--radius-m)] px-[var(--space-2)] py-[var(--space-2)] transition-colors duration-[var(--motion-fast)] motion-reduce:transition-none data-[interactive=true]:cursor-pointer data-[interactive=true]:hover:bg-[var(--color-island-2)]"
        data-interactive={onActivate !== undefined}
        onClick={onActivate}
      >
        {children}
      </div>
    </DisabledInteractionGuard>
  );
}

export function SettingsSwitch({
  checked,
  label,
  icon,
  onChange,
  reason,
}: Readonly<{
  checked: boolean;
  label: string;
  icon?: ReactNode;
  reason: string | undefined;
  onChange(checked: boolean): void;
}>) {
  return (
    <SettingsRow
      onActivate={() => {
        onChange(!checked);
      }}
      reason={reason}
    >
      <span className="flex min-w-0 flex-1 items-center gap-[var(--space-1)]">
        {icon}
        <span className="truncate">{label}</span>
      </span>
      <Switch
        aria-label={label}
        checked={checked}
        disabled={reason !== undefined}
        onCheckedChange={onChange}
        onClick={(event) => {
          event.stopPropagation();
        }}
        size="sm"
      />
    </SettingsRow>
  );
}
