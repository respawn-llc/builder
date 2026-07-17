"use client";

import * as RadioGroupPrimitive from "@radix-ui/react-radio-group";
import * as React from "react";

import { cn } from "../classes";

function RadioGroup({ className, ...props }: React.ComponentProps<typeof RadioGroupPrimitive.Root>) {
  return (
    <RadioGroupPrimitive.Root
      className={cn("grid w-full gap-2", className)}
      data-slot="radio-group"
      {...props}
    />
  );
}

function RadioGroupItem({ className, ...props }: React.ComponentProps<typeof RadioGroupPrimitive.Item>) {
  return (
    <RadioGroupPrimitive.Item
      className={cn(
        "app-region-no-drag group/radio-group-item relative flex aspect-square size-4 shrink-0 rounded-full border border-input outline-none after:absolute after:-inset-x-3 after:-inset-y-2 focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive aria-invalid:ring-3 aria-invalid:ring-destructive/20 data-[state=checked]:border-primary data-[state=checked]:bg-primary data-[state=checked]:text-primary-foreground",
        className,
      )}
      data-slot="radio-group-item"
      {...props}
    >
      <RadioGroupPrimitive.Indicator
        className="flex size-4 items-center justify-center"
        data-slot="radio-group-indicator"
      >
        <span className="absolute left-1/2 top-1/2 size-2 -translate-x-1/2 -translate-y-1/2 rounded-full bg-primary-foreground" />
      </RadioGroupPrimitive.Indicator>
    </RadioGroupPrimitive.Item>
  );
}

export { RadioGroup, RadioGroupItem };
