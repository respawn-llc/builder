import { forwardRef, type ComponentPropsWithoutRef, type MouseEvent, type ReactNode } from "react";

import { cx } from "./classes";

// Real elevation-1 island surface (border + shadow + blur) rather than a flat island-1 fill.
export const homeListCardShellClassName =
  "relative rounded-[var(--radius-l)] island-surface island-surface-1";

export const homeListCardButtonClassName =
  "grid w-full gap-[var(--space-1)] p-[var(--space-3)] pr-14 text-left text-[var(--color-on-island)]";

export const homeListCardMaxWidthClassName = "max-w-[600px]";

export const homeListCardListMaxWidthClassName = "[&>*]:max-w-[600px]";

export const HomeListCard = forwardRef<HTMLElement, HomeListCardProps>(function HomeListCard(
  { action, ariaLabel, children, className, onClick, title, ...articleProps },
  ref,
) {
  return (
    <article
      {...articleProps}
      className={cx(homeListCardShellClassName, className)}
      data-slot="home-list-card"
      data-testid="home-list-card"
      ref={ref}
    >
      <button
        aria-label={ariaLabel}
        className={homeListCardButtonClassName}
        data-slot="home-list-card-button"
        data-testid="home-list-card-button"
        onClick={onClick}
        title={title}
        type="button"
      >
        {children}
      </button>
      {action}
    </article>
  );
});

export type HomeListCardProps = Readonly<{
  action?: ReactNode | undefined;
  ariaLabel: string;
  children: ReactNode;
  className?: string | undefined;
  onClick: (event: MouseEvent<HTMLButtonElement>) => void;
  title?: string | undefined;
}> &
  Omit<ComponentPropsWithoutRef<"article">, "children" | "className" | "onClick" | "title">;
