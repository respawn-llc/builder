import type { ButtonHTMLAttributes, CSSProperties, ReactNode } from "react";

import { cx } from "./classes";

export type ButtonVariant = "primary" | "secondary" | "ghost" | "danger";
export type ButtonSize = "default" | "icon" | "icon-sm";

export type ButtonProps = Readonly<{
  children: ReactNode;
  size?: ButtonSize;
  variant?: ButtonVariant;
}> &
  ButtonHTMLAttributes<HTMLButtonElement>;

export function Button({
  children,
  className,
  size = "default",
  style,
  variant = "secondary",
  type = "button",
  ...props
}: ButtonProps) {
  return (
    <button
      className={cx(
        "border border-[var(--button-border)] bg-[var(--button-bg)] text-[var(--button-color)] disabled:cursor-not-allowed disabled:opacity-55",
        buttonSizeClassNames[size],
        className,
      )}
      style={{ ...buttonVariantStyles[variant], ...style }}
      type={type}
      {...props}
    >
      {children}
    </button>
  );
}

const buttonSizeClassNames = {
  default: "rounded-[var(--radius-m)] px-[10px] py-[4px]",
  icon: "grid h-9 w-9 shrink-0 place-items-center rounded-full p-0",
  "icon-sm": "grid h-7 w-7 shrink-0 place-items-center rounded-full p-0",
} satisfies Record<ButtonSize, string>;

type ButtonVariantStyle = CSSProperties &
  Record<"--button-bg" | "--button-border" | "--button-color", string>;

const buttonVariantStyles = {
  danger: {
    "--button-bg": "var(--color-island-1)",
    "--button-border": "var(--color-error)",
    "--button-color": "var(--color-error)",
  },
  ghost: {
    "--button-bg": "transparent",
    "--button-border": "transparent",
    "--button-color": "var(--color-on-island)",
  },
  primary: {
    "--button-bg": "var(--color-primary)",
    "--button-border": "transparent",
    "--button-color": "var(--color-on-primary)",
  },
  secondary: {
    "--button-bg": "var(--color-island-1)",
    "--button-border": "var(--color-outline)",
    "--button-color": "var(--color-on-island)",
  },
} satisfies Record<ButtonVariant, ButtonVariantStyle>;
