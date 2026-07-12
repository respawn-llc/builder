import { createBrowserNativeBridge } from "@app/native-bridge";
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { AlertTriangle, Pin } from "lucide-react";
import { useMemo, useRef, useState, type ReactNode } from "react";
import { I18nextProvider } from "react-i18next";

import { ApiClient, type WorkflowNodeKind } from "../api";
import { FakeRpcTransport } from "../api/fakeTransport";
import { applyConfiguredTheme, readEffectiveTheme, type AppTheme } from "../appEnvironment";
import { createGuiLogger } from "../app/logging";
import { AppServicesProvider } from "../app/servicesContext";
import { appI18n, initializeI18n } from "../i18n/setup";
import { Badge, Button, showStatusToast, Toaster } from "../ui";
import { HomeProjectBoard, HoverMenuBoard, KanbanBoard, PrimitiveBoard } from "./ShowcaseBoards";
import { TaskDetailBoard } from "./TaskDetailShowcase";
import { inventoryCards, mockNotices, mockWorkspaces } from "./mockData";
import { WorkflowGraphCanvas } from "../features/workflow-editor/WorkflowGraphCanvas";
import type { WorkflowGraphLayout } from "../features/workflow-editor/workflowGraphLayout";

void initializeI18n();

export function DevShowcaseApp() {
  const services = useMemo(() => {
    const nativeBridge = createBrowserNativeBridge();
    return {
      api: new ApiClient(new FakeRpcTransport([])),
      debugThemeOverrideEnabled: true,
      endpoint: "mock://ui-showcase",
      homePath: "",
      logger: createGuiLogger(nativeBridge),
      nativeBridge,
    };
  }, []);
  const router = useMemo(() => {
    const rootRoute = createRootRoute({ component: DevShowcaseBoard });
    const indexRoute = createRoute({ getParentRoute: () => rootRoute, path: "/", component: DevShowcaseBoard });
    return createRouter({
      history: createMemoryHistory({ initialEntries: ["/"] }),
      routeTree: rootRoute.addChildren([indexRoute]),
    });
  }, []);

  return (
    <I18nextProvider i18n={appI18n}>
      <AppServicesProvider services={services}>
        <RouterProvider router={router} />
        <Toaster />
      </AppServicesProvider>
    </I18nextProvider>
  );
}

function DevShowcaseBoard() {
  const [theme, setTheme] = useState<AppTheme>(() => readEffectiveTheme());
  const [dialogOpen, setDialogOpen] = useState(false);
  const [unlinkWorkspace, setUnlinkWorkspace] = useState(mockWorkspaces[1] ?? null);
  const [cancelExpanded, setCancelExpanded] = useState(false);

  function setPreviewTheme(nextTheme: AppTheme): void {
    applyConfiguredTheme(nextTheme);
    setTheme(nextTheme);
  }

  return (
    <main
      className="window-glass-fill h-screen overflow-y-auto overflow-x-hidden p-[var(--space-4)] text-[var(--color-on-island)]"
      data-testid="dev-showcase-scroll-root"
    >
      <div className="mx-auto grid max-w-[1800px] gap-[var(--space-5)]">
        <HeroSection theme={theme} onThemeChange={setPreviewTheme} />
        <ShowcaseSection
          description="Toast and floating-notice styling shown in-flow so it never hides the board underneath."
          eyebrow="00"
          title="Overlay Surfaces"
        >
          <NoticeSurfaceExamples />
        </ShowcaseSection>
        <ShowcaseSection
          description="Buttons, badges, form fields, Markdown, state cards, dialogs, and notice surfaces."
          eyebrow="01"
          title="UI Primitives"
        >
          <PrimitiveBoard dialogOpen={dialogOpen} onDialogOpenChange={setDialogOpen} />
        </ShowcaseSection>
        <ShowcaseSection
          description="Static workflow graph with regular and join-node creation handles for browser visual QA."
          eyebrow="02"
          title="Workflow Graph"
        >
          <WorkflowGraphPreview />
        </ShowcaseSection>
        <ShowcaseSection
          description="Project list rows, inbox rows, workspace cards, project edit status, unlink dialog."
          eyebrow="03"
          title="Home And Project Layouts"
        >
          <HomeProjectBoard unlinkWorkspace={unlinkWorkspace} onUnlinkWorkspaceChange={setUnlinkWorkspace} />
        </ShowcaseSection>
        <ShowcaseSection
          description="Real Kanban columns/cards with backlog, running, waiting, interrupted, done, load-more state."
          eyebrow="04"
          title="Kanban Board"
        >
          <KanbanBoard />
        </ShowcaseSection>
        <ShowcaseSection
          description="Real board hover menu. Hover/focus expands with workflow linking in the header."
          eyebrow="05"
          title="Hover Menu States"
        >
          <HoverMenuBoard />
        </ShowcaseSection>
        <ShowcaseSection
          description="Description editor, inbox question/approval/cancel flows, comments, activity, and runs."
          eyebrow="06"
          title="Task Detail"
        >
          <TaskDetailBoard cancelExpanded={cancelExpanded} onCancelExpandedChange={setCancelExpanded} />
        </ShowcaseSection>
      </div>
    </main>
  );
}

function WorkflowGraphPreview() {
  return (
    <section
      className="island-glass h-[420px] overflow-hidden rounded-[var(--radius-xl)] p-[var(--space-3)]"
      data-testid="dev-showcase-workflow-graph"
    >
      <WorkflowGraphCanvas
        graph={workflowGraphPreviewLayout}
        keyboardScope="focused"
        onConnectNodes={() => undefined}
        onEdgeInspect={() => undefined}
        onGroupInspect={() => undefined}
        onNodeInspect={() => undefined}
        onWorkflowInspect={() => undefined}
        toolbarPositionStrategy="absolute"
      />
    </section>
  );
}

const workflowGraphPreviewLayout = {
  edges: [],
  nodes: [
    workflowGraphPreviewNode({
      id: "showcase-start",
      kind: "start",
      label: "Backlog",
      role: "",
      x: 0,
    }),
    workflowGraphPreviewNode({
      id: "showcase-agent",
      kind: "agent",
      label: "Agent",
      role: "agent",
      x: 280,
    }),
    workflowGraphPreviewNode({
      id: "showcase-join",
      kind: "join",
      label: "Join",
      role: "",
      type: "workflowJoin",
      x: 560,
      y: 21,
      size: 50,
    }),
    workflowGraphPreviewNode({
      id: "showcase-done",
      kind: "terminal",
      label: "Done",
      role: "",
      x: 760,
    }),
  ],
} satisfies WorkflowGraphLayout;

function workflowGraphPreviewNode({
  id,
  kind,
  label,
  role,
  size,
  type = "workflowNode",
  x,
  y = 0,
}: Readonly<{
  id: string;
  kind: WorkflowNodeKind;
  label: string;
  role: string;
  size?: number | undefined;
  type?: string;
  x: number;
  y?: number | undefined;
}>): WorkflowGraphLayout["nodes"][number] {
  return {
    data: {
      entityID: id,
      entityKind: "node",
      groupID: "",
      hasError: false,
      key: id,
      kind,
      label,
      role,
    },
    draggable: kind === "agent",
    id,
    position: { x, y },
    style: size === undefined ? { height: 92, width: 220 } : { height: size, width: size },
    type,
  };
}

function HeroSection({
  theme,
  onThemeChange,
}: Readonly<{ theme: AppTheme; onThemeChange: (theme: AppTheme) => void }>) {
  return (
    <header className="island-glass grid gap-[var(--space-4)] rounded-[var(--radius-xl)] p-[var(--space-5)]">
      <div className="flex flex-wrap items-start justify-between gap-[var(--space-4)]">
        <div className="max-w-[960px]">
          <p className="m-0 text-sm font-bold text-[var(--color-primary)]">
            Dev-only browser preview
          </p>
          <h1 className="m-0 mt-[var(--space-2)] text-[clamp(2rem,6vw,5rem)] leading-none">
            Kent UI Showcase
          </h1>
          <p className="m-0 mt-[var(--space-3)] max-w-[780px] text-lg text-[var(--color-muted)]">
            Single scrollable board for reviewing hard-to-reach widgets and layout states with static mock data.
          </p>
        </div>
        <div className="flex flex-wrap gap-[var(--space-2)]">
          <Button
            onClick={() => {
              onThemeChange(theme === "dark" ? "light" : "dark");
            }}
            variant="primary"
          >
            Toggle {theme === "dark" ? "light" : "dark"} theme
          </Button>
          <Badge tone="info">No Kent server</Badge>
          <Badge tone="success">Static mocks</Badge>
        </div>
      </div>
      <div className="grid gap-[var(--space-3)] md:grid-cols-4">
        {inventoryCards.map((item) => (
          <article
            className="rounded-[var(--radius-l)] border border-[var(--color-outline)] bg-[var(--color-island-1)] p-[var(--space-3)]"
            key={item.title}
          >
            <strong>{item.title}</strong>
            <p className="m-0 mt-[var(--space-1)] text-sm text-[var(--color-muted)]">{item.body}</p>
          </article>
        ))}
      </div>
    </header>
  );
}

function NoticeSurfaceExamples() {
  const toastSequence = useRef(0);

  function triggerToast(notice: (typeof mockNotices)[number]): void {
    toastSequence.current += 1;
    const action = "actionLabel" in notice ? { actionLabel: notice.actionLabel, onAction: notice.onAction } : {};
    showStatusToast({
      ...action,
      body: notice.body,
      id: `dev-showcase-${notice.id}-${toastSequence.current.toString()}`,
      title: notice.title,
      tone: notice.tone,
    });
  }

  return (
    <div className="grid gap-[var(--space-3)] lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
      <section className="island-glass grid gap-[var(--space-3)] rounded-[var(--radius-xl)] p-[var(--space-4)]">
        <h3 className="m-0">Sonner Toasts</h3>
        <p className="m-0 text-sm text-[var(--color-muted)]">
          Trigger real global toasts. No static legacy toast stack is rendered here.
        </p>
        <div className="flex flex-wrap gap-[var(--space-2)]" data-testid="dev-showcase-toast-triggers">
          {mockNotices.map((notice) => (
            <Button
              data-testid={`dev-showcase-sonner-${notice.tone}`}
              key={notice.id}
              onClick={() => {
                triggerToast(notice);
              }}
              variant="ghost"
            >
              {notice.title}
            </Button>
          ))}
        </div>
      </section>
      <section className="island-glass grid gap-[var(--space-3)] rounded-[var(--radius-xl)] p-[var(--space-4)]">
        <h3 className="m-0">Floating Notice Shapes</h3>
        <div className="grid gap-[var(--space-3)] md:grid-cols-[minmax(0,1fr)_auto_auto]">
          <article
            className="grid h-[176px] gap-[6px] rounded-[var(--radius-xl)] border border-[var(--color-error)] bg-[var(--color-island-0)] p-[var(--space-3)]"
            data-testid="dev-showcase-floating-example"
          >
            <header className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-[var(--space-2)] leading-none">
              <h4 className="m-0 text-lg font-bold leading-none text-[var(--color-error)]">Runtime blocked</h4>
              <span className="grid h-[18px] w-[18px] place-items-center rounded-full">−</span>
            </header>
            <p className="m-0 text-sm">Expanded floating notice, rendered in-flow for review.</p>
          </article>
          <div className="grid h-12 w-12 place-items-center rounded-[var(--radius-m)] bg-[var(--color-error)] text-[var(--color-on-error)]">
            <AlertTriangle aria-hidden="true" size={24} strokeWidth={1.7} />
          </div>
          <div className="grid h-12 w-12 place-items-center rounded-[var(--radius-m)] border border-[var(--color-outline)] bg-[var(--color-island-2)]">
            <Pin aria-hidden="true" size={22} strokeWidth={1.7} />
          </div>
        </div>
      </section>
    </div>
  );
}

function ShowcaseSection({
  children,
  description,
  eyebrow,
  title,
}: Readonly<{ children: ReactNode; description: string; eyebrow: string; title: string }>) {
  return (
    <section className="grid gap-[var(--space-3)]" data-testid={`showcase-section-${eyebrow}`}>
      <header className="grid gap-[var(--space-1)]">
        <span className="font-mono text-sm text-[var(--color-muted)]">{eyebrow}</span>
        <h2 className="m-0 text-[clamp(1.5rem,3vw,2.8rem)]">{title}</h2>
        <p className="m-0 max-w-[900px] text-[var(--color-muted)]">{description}</p>
      </header>
      {children}
    </section>
  );
}
