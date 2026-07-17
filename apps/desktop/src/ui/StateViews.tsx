import { useEffect, useState, type ReactNode } from "react";
import { CircleAlert, Inbox } from "lucide-react";

import { Button } from "./Button";
import { chromeContentPaddingClassName } from "./chromePadding";
import { cx } from "./classes";
import { Island } from "./Island";
import { Spinner } from "./Spinner";

export type LoadingStateProps = Readonly<{
  title?: ReactNode;
  body?: ReactNode;
  fullPage?: boolean;
  chromePadding?: boolean;
  contentWidth?: StateContentWidth;
  reveal?: boolean;
  appearanceDelayMs?: number;
  appearanceDelayKey?: string;
}>;

const defaultLoadingAppearanceDelayMs = 500;
const defaultLoadingAppearanceDelayKey = "global";
const delayedLoadingAppearanceKeys = new Set<string>();

export function LoadingState({
  title = null,
  body = null,
  fullPage = true,
  chromePadding = false,
  contentWidth,
  reveal = true,
  appearanceDelayMs = defaultLoadingAppearanceDelayMs,
  appearanceDelayKey = defaultLoadingAppearanceDelayKey,
}: LoadingStateProps) {
  const visible = useOneShotDelayedAppearance(appearanceDelayMs, appearanceDelayKey);
  if (!visible) {
    return <LoadingPlaceholder chromePadding={chromePadding} fullPage={fullPage} />;
  }
  return (
    <StateIsland
      chromePadding={chromePadding}
      contentTestID="loading-state-content"
      contentWidth={contentWidth}
      fullPage={fullPage}
      icon={<Spinner testID="loading-state-spinner" />}
      iconClassName="text-[var(--color-primary)]"
      reveal={reveal}
      testID="loading-state"
      title={title}
      titleClassName="text-[var(--color-on-island)]"
    >
      {body !== null ? <p className="m-0 max-w-[52ch] text-[var(--color-muted)]">{body}</p> : null}
    </StateIsland>
  );
}

function useOneShotDelayedAppearance(delayMs: number, key: string): boolean {
  const normalizedDelayMs = Math.max(0, delayMs);
  const [shouldDelay] = useState(() => normalizedDelayMs > 0 && !delayedLoadingAppearanceKeys.has(key));
  const [visible, setVisible] = useState(!shouldDelay);

  useEffect(() => {
    if (!shouldDelay || visible) {
      return undefined;
    }
    delayedLoadingAppearanceKeys.add(key);
    const timer = window.setTimeout(() => {
      setVisible(true);
    }, normalizedDelayMs);
    return () => {
      window.clearTimeout(timer);
    };
  }, [key, normalizedDelayMs, shouldDelay, visible]);

  return visible;
}

function LoadingPlaceholder({
  chromePadding,
  fullPage,
}: Readonly<{ chromePadding: boolean; fullPage: boolean }>) {
  return (
    <div
      aria-hidden="true"
      className={cx(
        "loading-state-placeholder",
        fullPage && "h-full min-h-0",
        fullPage && chromePadding && chromeContentPaddingClassName,
      )}
      data-testid="loading-state-placeholder"
    />
  );
}

export type EmptyStateProps = Readonly<{
  title: ReactNode;
  body: ReactNode;
  icon?: ReactNode;
  actions?: ReactNode;
  action?: ReactNode;
  children?: ReactNode;
  fullPage?: boolean;
  chromePadding?: boolean;
  contentWidth?: StateContentWidth;
  testID?: string;
}>;

export function EmptyState({
  title,
  body,
  icon,
  actions,
  action,
  children,
  fullPage = true,
  chromePadding = false,
  contentWidth,
  testID = "empty-state",
}: EmptyStateProps) {
  const renderedActions = actions ?? action;
  return (
    <StateIsland
      chromePadding={chromePadding}
      contentTestID="empty-state-content"
      contentWidth={contentWidth}
      fullPage={fullPage}
      icon={icon ?? <Inbox size={28} strokeWidth={1.5} />}
      reveal={!fullPage}
      testID={testID}
      title={title}
      titleClassName="text-[var(--color-on-island)]"
      tone="secondary"
    >
      <p className="m-0 max-w-[52ch] text-[var(--color-muted)]">{body}</p>
      {children}
      {renderedActions !== undefined ? (
        <StateActions testID="empty-state-actions">{renderedActions}</StateActions>
      ) : null}
    </StateIsland>
  );
}

export type ErrorStateProps = Readonly<{
  title?: ReactNode;
  body?: ReactNode;
  details?: ReactNode;
  retryLabel?: string;
  onRetry?: () => void;
  children?: ReactNode;
  fullPage?: boolean;
  chromePadding?: boolean;
  contentWidth?: StateContentWidth;
  reveal?: boolean;
}>;

export function ErrorState({
  title = null,
  body = null,
  details = null,
  retryLabel,
  onRetry,
  children,
  fullPage = true,
  chromePadding = false,
  contentWidth,
  reveal = true,
}: ErrorStateProps) {
  return (
    <StateIsland
      chromePadding={chromePadding}
      contentTestID="error-state-content"
      contentWidth={contentWidth}
      fullPage={fullPage}
      icon={<CircleAlert size={28} strokeWidth={1.5} />}
      iconClassName="border-[color-mix(in_srgb,var(--color-error)_35%,transparent)] text-[var(--color-error)]"
      reveal={reveal}
      testID="error-state"
      title={title}
      titleClassName="text-[var(--color-error)]"
    >
      {body !== null ? <p className="m-0 max-w-[52ch] text-[var(--color-on-island)]">{body}</p> : null}
      {details !== null ? (
        <pre
          className="m-0 max-h-[220px] max-w-full overflow-auto whitespace-pre-wrap rounded-[var(--radius-m)] border border-[var(--color-outline)] bg-[var(--color-island-1)] p-[var(--space-3)] text-left font-mono text-sm text-[var(--color-on-island)]"
          data-testid="error-state-details"
        >
          {details}
        </pre>
      ) : null}
      {children !== undefined ? (
        <div className="max-w-full text-[var(--color-on-island)]">{children}</div>
      ) : null}
      {retryLabel !== undefined && onRetry !== undefined ? (
        <StateActions testID="error-state-actions">
          <Button onClick={onRetry} variant="primary">
            {retryLabel}
          </Button>
        </StateActions>
      ) : null}
    </StateIsland>
  );
}

type StateIslandProps = Readonly<{
  children: ReactNode;
  chromePadding: boolean;
  contentTestID: string;
  contentWidth: StateContentWidth | undefined;
  fullPage: boolean;
  icon: ReactNode;
  iconClassName?: string;
  reveal: boolean;
  testID: string;
  title: ReactNode;
  titleClassName: string;
  tone?: "primary" | "secondary" | "floating";
}>;

type StateContentWidth = "default" | "full";

function StateIsland({
  children,
  chromePadding,
  contentTestID,
  contentWidth,
  fullPage,
  icon,
  iconClassName,
  reveal,
  testID,
  title,
  titleClassName,
  tone = "floating",
}: StateIslandProps) {
  const content = (
    <div
      className={cx(
        "mx-auto grid justify-items-center gap-[var(--space-3)] text-center",
        contentWidth === "full" ? "w-full max-w-none" : "max-w-[560px]",
      )}
      data-testid={contentTestID}
    >
      <div
        aria-hidden="true"
        className={cx(
          "grid h-14 w-14 place-items-center rounded-full border border-[var(--color-outline)] bg-[var(--color-island-1)] text-[var(--color-muted)]",
          iconClassName,
        )}
        data-testid={`${testID}-icon`}
      >
        {icon}
      </div>
      {title !== null ? (
        <h2 className={cx("m-0 text-[1.25rem] font-bold", titleClassName)}>{title}</h2>
      ) : null}
      <div className="grid max-w-full justify-items-center gap-[var(--space-2)]">{children}</div>
    </div>
  );
  if (fullPage) {
    return (
      <div
        className={cx(
          "grid h-full min-h-0 overflow-hidden place-items-center",
          chromePadding && chromeContentPaddingClassName,
        )}
        data-testid={testID}
      >
        <Island
          className={cx(
            "grid h-full min-h-0 w-full place-items-center overflow-hidden",
            reveal && "animate-[surface-reveal_var(--motion-normal)]",
          )}
          data-testid={`${testID}-island`}
          floatingWidth={contentWidth === "full" ? "full" : "default"}
          tone={tone}
        >
          {content}
        </Island>
      </div>
    );
  }
  return (
    <Island
      className={cx("grid place-items-center", reveal && "animate-[surface-reveal_var(--motion-normal)]")}
      data-testid={testID}
      tone={tone}
    >
      {content}
    </Island>
  );
}

function StateActions({ children, testID }: Readonly<{ children: ReactNode; testID: string }>) {
  return (
    <div
      className="flex flex-wrap items-center justify-center gap-[var(--space-2)] pt-[var(--space-1)]"
      data-testid={testID}
    >
      {children}
    </div>
  );
}
