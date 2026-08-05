import { Link, useLocation } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { ChevronLeft, ChevronRight, Home, SunMoon } from "lucide-react";
import { useCallback, type MouseEvent, type PointerEvent, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { WorkflowEditorDraftBridgeProvider } from "@/features/workflow-editor";
import { toggleInMemoryThemeOverride } from "./startup/appEnvironment";
import { AttentionNotificationController } from "./AttentionNotificationController";
import { AppUpdateChip } from "./AppUpdateChip";
import { useDesktopUpdate, type DesktopUpdateState } from "./useDesktopUpdate";
import {
  appChromeInlineTitleClassNames,
  appChromeTopTreatmentForPlatform,
  appChromeTitleClassNames,
  appChromeTitlePlacementClassNames,
  appChromeUsesMacOSLayout,
} from "./appChromeStyles";
import { useAppNavigation, useNavigationStackState } from "@/app-facade";
import { completeProjectDeletion, useProjectDeletedEvents } from "@/app-facade";
import { SidebarHost, SidebarRouteChangeCloser } from "./sidebar";
import { useSidebar, type SidebarDestination } from "@/app-facade";
import { SidebarProvider } from "./sidebarProvider";
import { TaskSearchGlobalTrigger, TaskSearchHost, TaskSearchProvider } from "@/shared/task-search";
import { useStatusController } from "@/app-facade";
import { useAppServices } from "@/app-facade";
import { useCurrentWindowChromeTitle } from "@/app-facade";

export type AppChromeProps = Readonly<{
  children: ReactNode;
}>;

export function AppChrome({ children }: AppChromeProps) {
  const { debugThemeOverrideEnabled, logger, nativeBridge } = useAppServices();
  const navigation = useAppNavigation();
  const stack = useNavigationStackState();
  const macOS = appChromeUsesMacOSLayout(nativeBridge.capabilities.platform);
  const topTreatment = appChromeTopTreatmentForPlatform(nativeBridge.capabilities.platform);
  const title = useCurrentWindowChromeTitle();
  const update = useDesktopUpdate(nativeBridge, logger);

  return (
    <TaskSearchProvider>
      <main className="window-glass-fill grid h-screen w-screen overflow-hidden pt-[var(--native-titlebar-height)]">
      <div
        aria-hidden="true"
        className={topTreatment.classNames.join(" ")}
        data-effect={topTreatment.effect}
        data-testid="app-chrome-top-treatment"
        style={topTreatment.style}
      />
      <div
        className="app-region-drag fixed inset-x-0 top-0 z-20 h-[var(--native-titlebar-height)]"
        data-tauri-drag-region
        onPointerDown={(event) => {
          void startNativeWindowDrag(event, nativeBridge.window.startDragging);
        }}
      />
      <AppChromeNavigation
        debugThemeOverrideEnabled={debugThemeOverrideEnabled}
        macOS={macOS}
        navigation={navigation}
        stack={stack}
        title={title}
        update={update}
      />
      <AppChromeFloatingUpdateChip state={update} visible={macOS} />
      {title !== null && !macOS ? (
        <div
          className={[...appChromeTitleClassNames, ...appChromeTitlePlacementClassNames(macOS)].join(" ")}
          data-testid="app-chrome-title"
        >
          {title}
        </div>
      ) : null}
      <SidebarProvider>
        <TaskSearchHost />
        <WorkflowEditorDraftBridgeProvider>
          <ProjectDeletionEventHandler />
          <AttentionNotificationController />
          <div
            className="app-region-no-drag relative flex min-h-0 min-w-0 w-full overflow-hidden"
            data-testid="app-shell-content"
          >
            <div className="min-h-0 min-w-0 flex-1 overflow-visible" data-testid="app-main-content">
              {children}
            </div>
            <SidebarHost />
          </div>
          <SidebarRouteChangeCloser />
        </WorkflowEditorDraftBridgeProvider>
      </SidebarProvider>
      </main>
    </TaskSearchProvider>
  );
}

function AppChromeNavigation({
  debugThemeOverrideEnabled,
  macOS,
  navigation,
  stack,
  title,
  update,
}: Readonly<{
  debugThemeOverrideEnabled: boolean;
  macOS: boolean;
  navigation: ReturnType<typeof useAppNavigation>;
  stack: ReturnType<typeof useNavigationStackState>;
  title: string | null;
  update: DesktopUpdateState;
}>) {
  const { t } = useTranslation();
  return (
    <div
      className={`app-region-no-drag fixed top-[8px] z-30 flex h-6 items-center ${macOS ? "left-[var(--native-home-link-left-macos)]" : "right-[var(--space-4)]"}`}
      data-testid="app-chrome-navigation"
    >
      {!macOS ? <TaskSearchGlobalTrigger /> : null}
      {stack.hasHistory && !macOS ? (
        <HistoryButtons
          backLabel={t("app.back")}
          forwardLabel={t("app.forward")}
          navigation={navigation}
          placement="before-home"
          stack={stack}
        />
      ) : null}
      {!macOS ? <AppUpdateChip state={update} /> : null}
      <Link
        aria-label={t("app.home")}
        className="grid h-6 w-6 place-items-center rounded-full border border-transparent text-[var(--color-on-island)]"
        data-testid="app-chrome-home"
        onClick={(event) => {
          if (isPlainPrimaryClick(event)) {
            event.preventDefault();
            void navigation.openHome();
          }
        }}
        to="/"
      >
        <Home aria-hidden="true" size={16} strokeWidth={1.125} />
      </Link>
      {stack.hasHistory && macOS ? (
        <HistoryButtons
          backLabel={t("app.back")}
          forwardLabel={t("app.forward")}
          navigation={navigation}
          placement="after-home"
          stack={stack}
        />
      ) : null}
      {macOS ? <TaskSearchGlobalTrigger /> : null}
      {debugThemeOverrideEnabled ? <DebugThemeToggle label={t("app.toggleTheme")} /> : null}
      {title !== null && macOS ? (
        <div className={appChromeInlineTitleClassNames.join(" ")} data-testid="app-chrome-title">
          {title}
        </div>
      ) : null}
    </div>
  );
}

// macOS keeps the traffic lights and nav cluster on the left, so the update chip
// gets its own fixed slot in the free top-right corner. On other platforms the
// chip rides inside the right-aligned nav cluster (see AppChrome) to avoid
// overlapping the window controls.
function AppChromeFloatingUpdateChip({
  state,
  visible,
}: Readonly<{ state: DesktopUpdateState; visible: boolean }>) {
  if (!visible) {
    return null;
  }
  return (
    <div
      className="app-region-no-drag fixed top-[8px] right-[var(--space-4)] z-30 flex h-[22px] items-center"
      data-testid="app-chrome-update-slot"
    >
      <AppUpdateChip state={state} />
    </div>
  );
}

function ProjectDeletionEventHandler() {
  const { t } = useTranslation();
  const location = useLocation();
  const queryClient = useQueryClient();
  const { nativeBridge } = useAppServices();
  const navigation = useAppNavigation();
  const { activeDestination, closeSidebar } = useSidebar();
  const { push } = useStatusController();
  useProjectDeletedEvents(
    nativeBridge,
    useCallback(
      (event) => {
        const routeMatches = routeReferencesProject(location.pathname, event.projectID);
        const sidebarMatches = sidebarReferencesProject(activeDestination, event.projectID);
        void completeProjectDeletion({
          closeSidebar: routeMatches || sidebarMatches ? closeSidebar : noopCloseSidebar,
          navigateHome: routeMatches ? navigation.openHome : noopNavigation,
          projectID: event.projectID,
          pushDeletedToast: () => {
            push({
              id: "project-delete-deleted",
              tone: "success",
              title: t("projectEdit.deleteDeleted"),
            });
          },
          queryClient,
        });
      },
      [activeDestination, closeSidebar, location.pathname, navigation.openHome, push, queryClient, t],
    ),
  );
  return null;
}

function routeReferencesProject(pathname: string, projectID: string): boolean {
  const segments = pathname.split("/").filter((segment) => segment.length > 0);
  return segments[0] === "projects" && segments[1] === projectID;
}

function sidebarReferencesProject(destination: SidebarDestination | null, projectID: string): boolean {
  if (destination === null) {
    return false;
  }
  if ("projectID" in destination && destination.projectID === projectID) {
    return true;
  }
  return destination.kind === "linkWorkflow" && destination.projectID === projectID;
}

function noopCloseSidebar(): void {
  return;
}

async function noopNavigation(): Promise<void> {
  return;
}

function isPlainPrimaryClick(event: MouseEvent): boolean {
  return event.button === 0 && !event.altKey && !event.ctrlKey && !event.metaKey && !event.shiftKey;
}

function DebugThemeToggle({ label }: Readonly<{ label: string }>) {
  return (
    <button
      aria-label={label}
      className="grid h-6 w-6 place-items-center rounded-full border border-transparent bg-transparent text-[var(--color-on-island)]"
      data-testid="app-chrome-debug-theme-toggle"
      onClick={() => {
        toggleInMemoryThemeOverride();
      }}
      type="button"
    >
      <SunMoon aria-hidden="true" size={16} strokeWidth={1.25} />
    </button>
  );
}

function HistoryButtons({
  navigation,
  stack,
  backLabel,
  forwardLabel,
  placement,
}: Readonly<{
  backLabel: string;
  forwardLabel: string;
  navigation: ReturnType<typeof useAppNavigation>;
  placement: "before-home" | "after-home";
  stack: ReturnType<typeof useNavigationStackState>;
}>) {
  return (
    <div className="grid grid-cols-2" data-placement={placement} data-testid="app-chrome-history-buttons">
      <button
        aria-label={backLabel}
        className="grid h-6 w-6 place-items-center rounded-full border border-transparent bg-transparent text-[var(--color-on-island)] disabled:opacity-35"
        disabled={!stack.canGoBack}
        onClick={() => void navigation.back()}
        type="button"
      >
        <ChevronLeft aria-hidden="true" size={16} strokeWidth={1.25} />
      </button>
      <button
        aria-label={forwardLabel}
        className="grid h-6 w-6 place-items-center rounded-full border border-transparent bg-transparent text-[var(--color-on-island)] disabled:opacity-35"
        disabled={!stack.canGoForward}
        onClick={() => void navigation.forward()}
        type="button"
      >
        <ChevronRight aria-hidden="true" size={16} strokeWidth={1.25} />
      </button>
    </div>
  );
}

async function startNativeWindowDrag(
  event: PointerEvent<HTMLDivElement>,
  startDragging: () => Promise<void>,
): Promise<void> {
  if (event.button !== 0) {
    return;
  }
  event.preventDefault();
  await startDragging();
}
