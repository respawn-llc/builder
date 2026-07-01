import { ChevronDown } from "lucide-react";
import { forwardRef, type ComponentPropsWithoutRef } from "react";

import { cx } from "./classes";
import type { FieldError } from "./Field";
import { fieldInputClassName } from "./fieldInputStyles";
import type { SelectFieldOption } from "./SelectField";
import {
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
} from "../components/ui/dropdown-menu";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "../components/ui/tooltip";

type SelectTriggerButtonProps = Omit<
  ComponentPropsWithoutRef<"button">,
  "children" | "disabled" | "id" | "type"
>;

export type SelectTriggerProps = SelectTriggerButtonProps &
  Readonly<{
    inputId: string;
    menuId: string;
    hintId: string;
    errorId: string;
    error?: FieldError | undefined;
    selectedOption: SelectFieldOption | undefined;
    placeholder?: string | undefined;
    open: boolean;
    disabled: boolean;
    className?: string | undefined;
  }>;

export const SelectTrigger = forwardRef<HTMLButtonElement, SelectTriggerProps>(function SelectTrigger(
  {
    className,
    inputId,
    menuId,
    hintId,
    errorId,
    error,
    selectedOption,
    placeholder,
    open,
    disabled,
    ...buttonProps
  }: SelectTriggerProps,
  ref,
) {
  return (
    <button
      {...buttonProps}
      aria-controls={menuId}
      aria-describedby={`${hintId} ${errorId}`}
      aria-expanded={open}
      aria-haspopup="menu"
      aria-invalid={error === undefined ? undefined : true}
      className={cx(
        fieldInputClassName,
        "flex items-center justify-between gap-[var(--space-2)] text-left outline-none transition-[border-color,box-shadow] focus-visible:border-[var(--color-primary)] focus-visible:ring-[3px] focus-visible:ring-[color-mix(in_srgb,var(--color-primary)_35%,transparent)] disabled:cursor-not-allowed disabled:opacity-55",
        selectedOption === undefined ? "text-[var(--color-muted)]" : undefined,
        className,
      )}
      data-placeholder={selectedOption === undefined ? "true" : undefined}
      data-slot="select-trigger"
      disabled={disabled}
      id={inputId}
      ref={ref}
      type="button"
    >
      <span className="min-w-0 truncate">{selectedOption?.label ?? placeholder}</span>
      <ChevronDown
        aria-hidden="true"
        className={cx(
          "shrink-0 text-[var(--color-muted)] transition-transform",
          open ? "rotate-180" : undefined,
        )}
        size={16}
        strokeWidth={1.5}
      />
    </button>
  );
});

export type SelectOptionsListProps = Readonly<{
  menuId: string;
  options: readonly SelectFieldOption[];
  value: string;
  onValueChange: (value: string) => void;
}>;

export function SelectOptionsList({ menuId, options, value, onValueChange }: SelectOptionsListProps) {
  return (
    <DropdownMenuContent
      className="max-h-[min(var(--radix-dropdown-menu-content-available-height),20rem)] w-[var(--radix-dropdown-menu-trigger-width)]"
      id={menuId}
      level={3}
      loop
    >
      <TooltipProvider delayDuration={0}>
        <DropdownMenuRadioGroup onValueChange={onValueChange} value={value}>
          {options.map((option) => (
            <SelectOptionItem key={option.value} option={option} />
          ))}
        </DropdownMenuRadioGroup>
      </TooltipProvider>
    </DropdownMenuContent>
  );
}

function SelectOptionItem({ option }: Readonly<{ option: SelectFieldOption }>) {
  const item = (
    <DropdownMenuRadioItem
      disabled={option.disabled === true}
      {...(option.textValue === undefined ? {} : { textValue: option.textValue })}
      value={option.value}
    >
      <span className="min-w-0 truncate">{option.label}</span>
    </DropdownMenuRadioItem>
  );
  if (option.disabled !== true || option.disabledReason === undefined || option.disabledReason.length === 0) {
    return item;
  }
  return (
    <Tooltip>
      <TooltipTrigger asChild>{item}</TooltipTrigger>
      <TooltipContent level={3}>{option.disabledReason}</TooltipContent>
    </Tooltip>
  );
}
