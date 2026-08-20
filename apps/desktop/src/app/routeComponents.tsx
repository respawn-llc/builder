import { getRouteApi, Outlet, useMatch } from "@tanstack/react-router";
import { lazy, Suspense, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import { BoardRoute } from "@/features/board";
import { createProjectTasksViewMemory, HomeRoute, ProjectTasksSurface } from "@/features/home";
import { StartupGate } from "@/features/startup";
import { StandaloneTaskRoute } from "@/features/task-detail";
import { LoadingState } from "@/ui";
import { AppChrome } from "./AppChrome";
import {
  readBrowserStorage,
  readLastProjectRoute,
  SidebarRootOwner,
  writeBrowserStorage,
  writeLastProjectRoute,
} from "@/app-facade";
import { RouteTransitionFrame } from "./RouteTransitionFrame";
import { shouldSkipNativeDialogStartupGate } from "./routes";
import { useWindowChromeTitle } from "@/app-facade";

const LazyWorkflowEditorRoute = lazy(async () => {
  const module = await import("@/features/workflow-editor");
  return { default: module.WorkflowEditorRoute };
});

const LazyWorkflowLibraryRoute = lazy(async () => {
  const module = await import("@/features/workflows");
  return { default: module.WorkflowLibraryRoute };
});

const rootRouteApi = getRouteApi("__root__");
const homeRouteApi = getRouteApi("/");
const projectRouteApi = getRouteApi("/projects/$projectId");
const projectTasksRouteApi = getRouteApi("/projects/$projectId/tasks");
const workflowEditorRouteApi = getRouteApi("/workflows/$workflowId/editor");
const taskRouteApi = getRouteApi("/tasks/$taskId");

const routeRestoreSessionKey = "desktop.routeRestoreChecked";
let routeRestoreCheckedFallback = false;

export function RootRoute() {
  const isNativeDialogWindow =
    typeof window !== "undefined" && window.location.pathname.startsWith("/native-dialog/");
  if (isNativeDialogWindow) {
    if (shouldSkipNativeDialogStartupGate(window.location.pathname)) {
      return <Outlet />;
    }
    return (
      <StartupGate>
        <Outlet />
      </StartupGate>
    );
  }

  return (
    <AppChrome>
      <RoutePersistence />
      <StartupGate>
        <RouteTransitionFrame />
      </StartupGate>
    </AppChrome>
  );
}

function RoutePersistence() {
  const navigate = rootRouteApi.useNavigate();
  const homeMatch = useMatch({ from: "/", shouldThrow: false });
  const homeProjectId = homeMatch?.search.projectId ?? null;
  const isUnselectedHomeRoute = homeMatch !== undefined && homeMatch.search.projectId === undefined;
  const projectMatch = useMatch({ from: "/projects/$projectId", shouldThrow: false });
  const projectId = projectMatch?.params.projectId ?? null;
  const workflowId = projectMatch?.search.workflowId;

  useEffect(() => {
    if (claimRouteRestoreCheck()) {
      const restored = readLastProjectRoute();
      if (isUnselectedHomeRoute && restored !== null) {
        // Session restore is startup state hydration, not a user-initiated destination change, so it
        // intentionally bypasses the animated app navigation API.
        void (restored.kind === "home_project"
          ? navigate({
              to: "/",
              search: { projectId: restored.projectId },
              replace: true,
            })
          : navigate({
              to: "/projects/$projectId",
              params: { projectId: restored.projectId },
              search: { workflowId: restored.workflowId, taskId: "" },
              replace: true,
            }));
      }
    }
    if (homeProjectId !== null) {
      writeLastProjectRoute({ kind: "home_project", projectId: homeProjectId });
    } else if (projectId !== null) {
      writeLastProjectRoute({ kind: "workflow_board", projectId, workflowId });
    }
  }, [homeProjectId, isUnselectedHomeRoute, projectId, workflowId, navigate]);

  return null;
}

function claimRouteRestoreCheck(): boolean {
  const stored = readBrowserStorage("session", routeRestoreSessionKey);
  if (!stored.ok) {
    const shouldRestore = !routeRestoreCheckedFallback;
    routeRestoreCheckedFallback = true;
    return shouldRestore;
  }
  if (stored.value !== null) {
    return false;
  }
  const written = writeBrowserStorage("session", routeRestoreSessionKey, "1");
  if (!written.ok) {
    const shouldRestore = !routeRestoreCheckedFallback;
    routeRestoreCheckedFallback = true;
    return shouldRestore;
  }
  return true;
}

export function ProjectRoute() {
  const params = projectRouteApi.useParams();
  const search = projectRouteApi.useSearch();
  useWindowChromeTitle(null);
  return (
    <BoardRoute projectId={params.projectId} selectedTaskId={search.taskId} workflowId={search.workflowId} />
  );
}

export function ProjectTasksRoute() {
  const { t } = useTranslation();
  const params = projectTasksRouteApi.useParams();
  useWindowChromeTitle(t("home.prototype.tasks"));
  const [viewMemory] = useState(createProjectTasksViewMemory);
  return (
    <SidebarRootOwner>
      <section className="island-glass h-full min-h-0 overflow-hidden rounded-[var(--radius-xl)]">
        <ProjectTasksSurface projectID={params.projectId} sidebarMode="shift" viewMemory={viewMemory} />
      </section>
    </SidebarRootOwner>
  );
}

export function HomeShellRoute() {
  const search = homeRouteApi.useSearch();
  useWindowChromeTitle(null);
  return <HomeRoute selectedProjectID={search.projectId ?? null} />;
}

export function WorkflowEditorShellRoute() {
  const { t } = useTranslation();
  const params = workflowEditorRouteApi.useParams();
  const search = workflowEditorRouteApi.useSearch();
  useWindowChromeTitle(null);
  return (
    <Suspense
      fallback={
        <LoadingState
          appearanceDelayMs={0}
          chromePadding
          contentWidth="full"
          title={t("workflowEditor.loadingTitle")}
        />
      }
    >
      <LazyWorkflowEditorRoute projectID={search.projectId} workflowID={params.workflowId} />
    </Suspense>
  );
}

export function WorkflowLibraryShellRoute() {
  const { t } = useTranslation();
  useWindowChromeTitle(t("workflowLibrary.title"));
  return (
    <Suspense fallback={<LoadingState appearanceDelayMs={0} title={t("workflowLibrary.title")} />}>
      <LazyWorkflowLibraryRoute />
    </Suspense>
  );
}

export function TaskRoute() {
  const { t } = useTranslation();
  const params = taskRouteApi.useParams();
  useWindowChromeTitle(t("task.title"));
  return <StandaloneTaskRoute taskId={params.taskId} />;
}
