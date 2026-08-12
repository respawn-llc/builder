import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { SearchIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import {
  SidebarRootOwner,
  taskSearchDebounceMs,
  type TaskSearchResult as SearchResult,
  useDebouncedText,
  useOwnedSidebarRoots,
  useTaskSearch,
  useTaskSearchMemory,
} from "@/app-facade";
import { InteractiveChip } from "@/ui";
import { adjacentSearchResult, useTaskSearchSelection } from "./taskSearchSelection";
import { TaskSearchDialog, useTaskSearchShortcuts } from "./BoardTaskSearch";

type TaskSearchInvocation =
  Readonly<{ projectId: string; onOpenTask(taskID: string): void }> | Readonly<{ projectId?: never }>;

type TaskSearchController = Readonly<{
  invocation: TaskSearchInvocation | null;
  open: boolean;
  cancelProjectSearch(projectId: string): void;
  close(): void;
  openSearch(invocation: TaskSearchInvocation): void;
}>;

const TaskSearchControllerContext = createContext<TaskSearchController | null>(null);

export function TaskSearchProvider({ children }: Readonly<{ children: ReactNode }>) {
  const [state, setState] = useState<Pick<TaskSearchController, "invocation" | "open">>({
    invocation: null,
    open: false,
  });
  const cancelProjectSearch = useCallback((projectId: string): void => {
    setState((current) =>
      current.invocation?.projectId === projectId ? { invocation: null, open: false } : current,
    );
  }, []);
  const close = useCallback((): void => {
    setState((current) => ({ ...current, open: false }));
  }, []);
  const openSearch = useCallback((invocation: TaskSearchInvocation): void => {
    setState({ invocation, open: true });
  }, []);
  const value = useMemo(
    () => ({ ...state, cancelProjectSearch, close, openSearch }),
    [cancelProjectSearch, close, openSearch, state],
  );
  return (
    <TaskSearchControllerContext.Provider value={value}>{children}</TaskSearchControllerContext.Provider>
  );
}

function useTaskSearchController(): TaskSearchController {
  const controller = useContext(TaskSearchControllerContext);
  if (controller === null) {
    throw new Error("Task Search requires TaskSearchProvider.");
  }
  return controller;
}

export function TaskSearchProjectTrigger({
  projectID,
  onOpenTask,
}: Readonly<{
  projectID: string;
  onOpenTask(taskID: string): void;
}>) {
  const { cancelProjectSearch, invocation, openSearch, open } = useTaskSearchController();
  const projectIDRef = useRef(projectID);
  useEffect(() => {
    const previousProjectID = projectIDRef.current;
    projectIDRef.current = projectID;
    if (previousProjectID !== projectID && open && invocation?.projectId === previousProjectID) {
      openSearch({ onOpenTask, projectId: projectID });
    }
  }, [invocation?.projectId, onOpenTask, open, openSearch, projectID]);
  useEffect(() => {
    return () => {
      cancelProjectSearch(projectIDRef.current);
    };
  }, [cancelProjectSearch]);
  return (
    <TaskSearchTrigger
      compact={false}
      onClick={() => {
        openSearch({ onOpenTask, projectId: projectID });
      }}
      selected={open && invocation?.projectId === projectID}
    />
  );
}

export function TaskSearchGlobalTrigger() {
  const { t } = useTranslation();
  const { invocation, open, openSearch } = useTaskSearchController();
  return (
    <button
      aria-label={t("taskSearch.open")}
      aria-pressed={open && invocation?.projectId === undefined}
      className="app-region-no-drag grid h-6 w-6 place-items-center rounded-full border border-transparent bg-transparent text-[var(--color-on-island)]"
      onClick={() => {
        openSearch({});
      }}
      type="button"
    >
      <SearchIcon aria-hidden="true" size={16} strokeWidth={1.25} />
    </button>
  );
}

function TaskSearchTrigger({
  compact,
  onClick,
  selected,
}: Readonly<{
  compact: boolean;
  onClick(): void;
  selected: boolean;
}>) {
  const { t } = useTranslation();
  return (
    <InteractiveChip
      aria-label={t("taskSearch.open")}
      className={compact ? "h-6 w-6 justify-center p-0" : undefined}
      onClick={onClick}
      selected={selected}
      size={compact ? "compact" : "default"}
      style={compact ? undefined : { paddingInline: "var(--space-3)" }}
      tone={selected ? "primary" : "neutral"}
    >
      <SearchIcon aria-hidden="true" className="shrink-0" size={14} strokeWidth={1.8} />
      {compact ? null : t("taskSearch.open")}
    </InteractiveChip>
  );
}

export function TaskSearchHost() {
  return (
    <SidebarRootOwner>
      <OwnedTaskSearchHost />
    </SidebarRootOwner>
  );
}

function OwnedTaskSearchHost() {
  const { invocation, close, open, openSearch } = useTaskSearchController();
  const { open: openSidebar } = useOwnedSidebarRoots();
  const memory = useTaskSearchMemory();
  const pendingActivationRef = useRef<Readonly<{ invocation: TaskSearchInvocation; taskID: string }> | null>(
    null,
  );
  const projectID = invocation?.projectId ?? null;
  const query = memory.query;
  const debouncedQuery = useDebouncedText(query, taskSearchDebounceMs);
  const search = useTaskSearch(projectID, open, debouncedQuery);
  const selection = useTaskSearchSelection(projectID, search.displayedQuery, search.results);
  const revealActiveSelection = selection.revealActive;
  const previousOpenRef = useRef(false);
  const previousProjectIDRef = useRef<string | null>(null);
  useEffect(() => {
    const scopeChanged = previousProjectIDRef.current !== projectID;
    const opened = open && !previousOpenRef.current;
    previousProjectIDRef.current = projectID;
    previousOpenRef.current = open;
    if (open && (opened || scopeChanged)) {
      revealActiveSelection();
    }
  }, [open, projectID, revealActiveSelection]);
  const activate = useCallback(
    (result: SearchResult): void => {
      if (invocation === null) {
        throw new Error("Task Search activation requires an active invocation.");
      }
      pendingActivationRef.current = { invocation, taskID: result.group.taskID };
      close();
    },
    [close, invocation],
  );
  const completeClose = useCallback((): void => {
    const pending = pendingActivationRef.current;
    pendingActivationRef.current = null;
    if (pending === null) {
      return;
    }
    if (pending.invocation.projectId !== undefined) {
      pending.invocation.onOpenTask(pending.taskID);
      return;
    }
    const destination = { kind: "taskDetail" as const, mode: "overlay" as const, taskID: pending.taskID };
    openSidebar(destination);
  }, [openSidebar]);
  const openGlobalSearch = useCallback((): void => {
    pendingActivationRef.current = null;
    openSearch({});
  }, [openSearch]);
  useTaskSearchShortcuts(openGlobalSearch);
  const moveSelection = useCallback(
    (direction: -1 | 1): void => {
      const next = adjacentSearchResult(search.results, selection.activeKey, direction);
      if (next !== null) {
        selection.selectAndReveal(next.key);
      }
    },
    [search.results, selection],
  );
  if (invocation === null) {
    return null;
  }
  return (
    <TaskSearchDialog
      onActivate={activate}
      onClose={close}
      onExitComplete={completeClose}
      onMoveSelection={moveSelection}
      onQueryChange={memory.setQuery}
      open={open}
      query={query}
      search={search}
      selection={selection}
    />
  );
}
