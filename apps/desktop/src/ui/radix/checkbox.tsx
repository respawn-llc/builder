"use client";

import * as React from "react";
import * as CheckboxPrimitive from "@radix-ui/react-checkbox";
import { Check } from "lucide-react";

import { cn } from "../classes";

function Checkbox({ className, ...props }: React.ComponentProps<typeof CheckboxPrimitive.Root>) {
  return (
    <CheckboxPrimitive.Root
      className={cn(
        "app-region-no-drag peer grid size-4 shrink-0 place-items-center rounded-[4px] border border-[var(--color-outline)] bg-[var(--color-island-1)] text-[var(--color-on-primary)] outline-none transition-[background-color,border-color,box-shadow] focus-visible:border-[var(--color-primary)] focus-visible:ring-[3px] focus-visible:ring-[color-mix(in_srgb,var(--color-primary)_35%,transparent)] disabled:cursor-not-allowed disabled:opacity-50 data-[state=checked]:border-[var(--color-primary)] data-[state=checked]:bg-[var(--color-primary)]",
        className,
      )}
      data-slot="checkbox"
      {...props}
    >
      <CheckboxPrimitive.Indicator
        className="grid place-items-center text-current"
        data-slot="checkbox-indicator"
      >
        <Check aria-hidden="true" size={14} strokeWidth={2.4} />
      </CheckboxPrimitive.Indicator>
    </CheckboxPrimitive.Root>
  );
}

export { Checkbox };
