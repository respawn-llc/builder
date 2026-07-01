import { useId, useMemo, useState, type ReactNode } from "react";

import { DisabledInteractionGuard } from "./DisabledInteractionGuard";
import { FieldShell, type FieldError } from "./Field";
import { SelectOptionsList, SelectTrigger } from "./SelectFieldParts";
import { DropdownMenu, DropdownMenuTrigger } from "../components/ui/dropdown-menu";

export type SelectFieldOption = Readonly<{
  label: ReactNode;
  textValue?: string;
  value: string;
  disabled?: boolean;
  disabledReason?: string | undefined;
}>;

export type SelectFieldProps = Readonly<{
  label: string;
  labelHelp?: string | undefined;
  value: string;
  options: readonly SelectFieldOption[];
  onValueChange: (value: string) => void;
  className?: string | undefined;
  error?: FieldError | undefined;
  hint?: ReactNode | undefined;
  placeholder?: string | undefined;
  disabled?: boolean | undefined;
  disabledReason?: string | undefined;
  name?: string | undefined;
}>;

export function SelectField({
  label,
  labelHelp,
  value,
  options,
  onValueChange,
  className,
  error,
  hint,
  placeholder,
  disabled = false,
  disabledReason,
  name,
}: SelectFieldProps) {
  const inputId = useId();
  const hintId = `${inputId}-hint`;
  const errorId = `${inputId}-error`;
  const controlMode = disabled || options.length === 0 ? "disabled" : "enabled";

  const control = (
    <SelectFieldControl
      className={className}
      disabled={disabled}
      error={error}
      errorId={errorId}
      hintId={hintId}
      inputId={inputId}
      key={controlMode}
      name={name}
      onValueChange={onValueChange}
      options={options}
      placeholder={placeholder}
      value={value}
    />
  );

  return (
    <FieldShell
      error={error}
      errorId={errorId}
      hint={hint}
      hintId={hintId}
      inputId={inputId}
      label={label}
      labelHelp={labelHelp}
    >
      {disabled && disabledReason !== undefined && disabledReason.length > 0 ? (
        <DisabledInteractionGuard disabled reason={disabledReason}>
          {control}
        </DisabledInteractionGuard>
      ) : (
        control
      )}
    </FieldShell>
  );
}

type SelectFieldControlProps = Readonly<{
  value: string;
  options: readonly SelectFieldOption[];
  onValueChange: (value: string) => void;
  inputId: string;
  hintId: string;
  errorId: string;
  error?: FieldError | undefined;
  placeholder?: string | undefined;
  className?: string | undefined;
  disabled: boolean;
  name?: string | undefined;
}>;

function SelectFieldControl({
  value,
  options,
  onValueChange,
  inputId,
  hintId,
  errorId,
  error,
  placeholder,
  className,
  disabled,
  name,
}: SelectFieldControlProps) {
  const [open, setOpen] = useState(false);
  const menuId = `${inputId}-menu`;
  const selectedOption = useMemo(() => options.find((option) => option.value === value), [options, value]);
  const interactiveDisabled = disabled || options.length === 0;

  return (
    <DropdownMenu
      modal={false}
      onOpenChange={(nextOpen) => {
        setOpen(interactiveDisabled ? false : nextOpen);
      }}
      open={interactiveDisabled ? false : open}
    >
      <DropdownMenuTrigger asChild>
        <SelectTrigger
          className={className}
          disabled={interactiveDisabled}
          error={error}
          errorId={errorId}
          hintId={hintId}
          inputId={inputId}
          menuId={menuId}
          open={interactiveDisabled ? false : open}
          placeholder={placeholder}
          selectedOption={selectedOption}
        />
      </DropdownMenuTrigger>
      {name === undefined || interactiveDisabled ? null : <input name={name} type="hidden" value={value} />}
      {interactiveDisabled ? null : (
        <SelectOptionsList
          menuId={menuId}
          onValueChange={(nextValue) => {
            onValueChange(nextValue);
            setOpen(false);
          }}
          options={options}
          value={value}
        />
      )}
    </DropdownMenu>
  );
}
