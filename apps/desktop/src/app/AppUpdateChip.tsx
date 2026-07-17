import { Loader2, X } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { DesktopUpdateState } from "./useDesktopUpdate";

export type AppUpdateChipProps = Readonly<{
  state: DesktopUpdateState;
}>;

// Small accent pill in the app chrome that appears only when an update is
// available. The pill body downloads, installs, and relaunches into the new
// version in one click; the trailing × dismisses the chip until the next launch.
// The × is hidden while installing since an in-flight install can't be cancelled.
export function AppUpdateChip({ state }: AppUpdateChipProps) {
  const { t } = useTranslation();
  if (state.phase === "none") {
    return null;
  }
  const installing = state.phase === "installing";
  const percent = state.progressRatio === null ? null : Math.round(state.progressRatio * 100);
  const label = installing
    ? percent === null
      ? t("app.update.installing")
      : t("app.update.installingPercent", { percent })
    : state.phase === "error"
      ? t("app.update.retry")
      : t("app.update.available");
  return (
    <div
      className="app-region-no-drag inline-flex h-[22px] items-center rounded-full bg-primary text-primary-foreground"
      data-phase={state.phase}
      data-testid="app-update-chip"
    >
      <button
        aria-label={t("app.update.installAria")}
        className="inline-flex h-[22px] items-center gap-1 rounded-full py-0 pr-1.5 pl-3 text-[12px] font-medium transition-opacity duration-[var(--motion-fast)] hover:opacity-90 disabled:cursor-default disabled:opacity-80"
        data-testid="app-update-chip-install"
        disabled={installing}
        onClick={state.install}
        type="button"
      >
        {installing ? (
          <Loader2 aria-hidden="true" className="animate-spin" size={12} strokeWidth={1.75} />
        ) : null}
        {label}
      </button>
      {installing ? null : (
        <button
          aria-label={t("app.update.dismiss")}
          className="grid h-[22px] w-[22px] place-items-center rounded-full text-primary-foreground/80 transition-[opacity,background-color] duration-[var(--motion-fast)] hover:bg-[color-mix(in_srgb,var(--color-on-primary)_18%,transparent)] hover:text-primary-foreground"
          data-testid="app-update-chip-dismiss"
          onClick={state.dismiss}
          type="button"
        >
          <X aria-hidden="true" size={12} strokeWidth={2} />
        </button>
      )}
    </div>
  );
}
