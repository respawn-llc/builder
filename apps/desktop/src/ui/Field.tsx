import { forwardRef, useId, type InputHTMLAttributes, type ReactNode, type TextareaHTMLAttributes } from "react";
import { z } from "zod";

import { cx } from "./classes";
import { fieldInputClassName } from "./fieldInputStyles";
import { fieldLabelClassName } from "./fieldStyles";
import { HelpHint } from "./HelpHint";

export type FieldError = string | readonly string[];
const fieldErrorListSchema = z.array(z.string());
const fieldErrorMessageSchema = z.string();

type FieldShellProps = Readonly<{
  label: string;
  labelHelp?: string | undefined;
  error?: FieldError | undefined;
  hint?: ReactNode | undefined;
  inputId: string;
  errorId: string;
  hintId: string;
  children: ReactNode;
}>;

export function FieldShell({
  label,
  labelHelp,
  error,
  hint,
  inputId,
  errorId,
  hintId,
  children,
}: FieldShellProps) {
  const errors = normalizeErrors(error);

  return (
    <div className="grid gap-[var(--space-3)]">
      <span className="inline-flex items-center gap-[var(--space-1)]">
        <label className={fieldLabelClassName} htmlFor={inputId}>
          {label}
        </label>
        {labelHelp === undefined ? null : <HelpHint className="shrink-0" label={labelHelp} side="right" />}
      </span>
      {children}
      {hint !== undefined ? (
        <span className="text-[var(--color-muted)]" id={hintId}>
          {hint}
        </span>
      ) : null}
      <span
        aria-live="polite"
        className="grid overflow-hidden opacity-0 transition-[grid-template-rows,opacity] duration-[var(--motion-normal)] data-[visible=true]:grid-rows-[1fr] data-[visible=true]:opacity-100 grid-rows-[0fr]"
        data-visible={errors.length > 0 ? "true" : "false"}
        id={errorId}
      >
        <span className="grid min-h-0 gap-[var(--space-1)]">
          {errors.map((message) => (
            <span className="text-[var(--color-error)]" key={message}>
              {message}
            </span>
          ))}
        </span>
      </span>
    </div>
  );
}

export type TextInputProps = Readonly<{
  label: string;
  labelHelp?: string | undefined;
  error?: FieldError | undefined;
  hint?: ReactNode | undefined;
}> &
  InputHTMLAttributes<HTMLInputElement>;

export function TextInput({ label, labelHelp, error, hint, className, ...props }: TextInputProps) {
  const generatedId = useId();
  const inputId = props.id ?? generatedId;
  const hintId = `${inputId}-hint`;
  const errorId = `${inputId}-error`;

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
      <input
        aria-describedby={`${hintId} ${errorId}`}
        aria-invalid={error === undefined ? undefined : true}
        className={cx(fieldInputClassName, className)}
        {...props}
        id={inputId}
      />
    </FieldShell>
  );
}

export type TextAreaProps = Readonly<{
  label: string;
  labelHelp?: string | undefined;
  error?: FieldError | undefined;
  hint?: ReactNode | undefined;
}> &
  TextareaHTMLAttributes<HTMLTextAreaElement>;

export const TextArea = forwardRef<HTMLTextAreaElement, TextAreaProps>(function TextArea(
  { label, labelHelp, error, hint, className, ...props },
  ref,
) {
  const generatedId = useId();
  const inputId = props.id ?? generatedId;
  const hintId = `${inputId}-hint`;
  const errorId = `${inputId}-error`;

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
      <textarea
        aria-describedby={`${hintId} ${errorId}`}
        aria-invalid={error === undefined ? undefined : true}
        className={cx(fieldInputClassName, "min-h-24", className)}
        {...props}
        id={inputId}
        ref={ref}
      />
    </FieldShell>
  );
});

function normalizeErrors(error: FieldError | undefined): readonly string[] {
  if (error === undefined) {
    return [];
  }
  const messages = fieldErrorListSchema.safeParse(error);
  if (messages.success) {
    return messages.data.filter(nonEmptyString);
  }
  const message = fieldErrorMessageSchema.safeParse(error);
  return message.success && message.data.length > 0 ? [message.data] : [];
}

function nonEmptyString(value: string): boolean {
  return value.length > 0;
}
