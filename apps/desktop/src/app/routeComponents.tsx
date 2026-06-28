import { getRouteApi, Outlet, useMatch } from "@tanstack/react-router";
import { lazy, Suspense, useEffect } from "react";
import { useTranslation } from "react-i18next";

import { BoardRoute } from "../features/board/BoardRoute";
import { HomeRoute } from "../features/home/HomeRoute";
import { StandaloneTaskRoute } from "../features/task-detail/StandaloneTaskRoute";
import { StartupGate } from "../features/startup/StartupGate";
import { LoadingState } from "../ui";
import { AppChrome } from "./AppChrome";
import { readLastProjectRoute, writeLastProjectRoute } from "./projectRoutePersistence";
import { RouteTransitionFrame } from "./RouteTransitionFrame";
import { shouldSkipNativeDialogStartupGate } from "./routes";
import { useWindowChromeTitle } from "./windowChromeTitle";

const LazyWorkflowEditorRoute = lazy(async () => {
  const module = await import("../features/workflow-editor/WorkflowEditorRoute");
  return { default: module.WorkflowEditorRoute };
});

const LazyWorkflowLibraryRoute = lazy(async () => {
  const module = await import("../features/workflows/WorkflowLibraryRoute");
  return { default: module.WorkflowLibraryRoute };
});

const rootRouteApi = getRouteApi("__root__");
const projectRouteApi = getRouteApi("/projects/$projectId");
const workflowEditorRouteApi = getRouteApi("/workflows/$workflowId/editor");
const legacyWorkflowEditorRouteApi = getRouteApi(
  "/projects/$projectId/workflows/$workflowId/editor",
);
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
  const isHomeRoute = useMatch({ from: "/", shouldThrow: false }) !== undefined;
  const projectMatch = useMatch({ from: "/projects/$projectId", shouldThrow: false });
  const projectId = projectMatch?.params.projectId ?? null;
  const workflowId = projectMatch?.search.workflowId ?? "";

  useEffect(() => {
    if (claimRouteRestoreCheck()) {
      const restored = readLastProjectRoute();
      if (isHomeRoute && restored !== null) {
        // Session restore is startup state hydration, not a user-initiated destination change, so it
        // intentionally bypasses the animated app navigation API.
        void navigate({
          to: "/projects/$projectId",
          params: { projectId: restored.projectId },
          search: { workflowId: restored.workflowId, taskId: "" },
          replace: true,
        });
      }
    }
    if (projectId !== null) {
      writeLastProjectRoute({ projectId, workflowId });
    }
  }, [isHomeRoute, projectId, workflowId, navigate]);

  return null;
}

function claimRouteRestoreCheck(): boolean {
  const storage = safeStorage("session");
  if (storage === null) {
    const shouldRestore = !routeRestoreCheckedFallback;
    routeRestoreCheckedFallback = true;
    return shouldRestore;
  }
  if (storage.getItem(routeRestoreSessionKey) !== null) {
    return false;
  }
  storage.setItem(routeRestoreSessionKey, "1");
  return true;
}

function safeStorage(kind: "local" | "session"): Storage | null {
  try {
    if (kind === "local") {
      return globalThis.localStorage;
    }
    return globalThis.sessionStorage;
  } catch {
    return null;
  }
}

export function ProjectRoute() {
  const params = projectRouteApi.useParams();
  const search = projectRouteApi.useSearch();
  useWindowChromeTitle(null);
  return (
    <BoardRoute
      projectId={params.projectId}
      selectedTaskId={search.taskId}
      workflowId={search.workflowId}
    />
  );
}

export function HomeShellRoute() {
  const { t } = useTranslation();
  useWindowChromeTitle(t("home.projectsPane"));
  return <HomeRoute />;
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

export function LegacyWorkflowEditorRedirectRoute() {
  const navigate = rootRouteApi.useNavigate();
  const params = legacyWorkflowEditorRouteApi.useParams();

  useEffect(() => {
    // Canonical route redirects are not user-initiated destination changes, so they intentionally
    // bypass the animated app navigation API.
    void navigate({
      to: "/workflows/$workflowId/editor",
      params: { workflowId: params.workflowId },
      search: { projectId: params.projectId },
      replace: true,
    });
  }, [navigate, params.projectId, params.workflowId]);

  return null;
}

export function TaskRoute() {
  const { t } = useTranslation();
  const params = taskRouteApi.useParams();
  useWindowChromeTitle(t("task.title"));
  return <StandaloneTaskRoute taskId={params.taskId} />;
}
