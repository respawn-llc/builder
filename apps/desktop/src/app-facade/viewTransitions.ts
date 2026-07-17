export type ViewTransitionScope = "route" | "board-card";

export type ViewTransitionOptions = Readonly<{
  scope: ViewTransitionScope;
  update: () => void | Promise<void>;
}>;

export type ViewTransitionResult = Readonly<{
  mode: "transition" | "immediate";
  finished: Promise<void>;
  updateCallbackDone: Promise<void>;
}>;

type DocumentViewTransition = Readonly<{
  finished: Promise<void>;
  ready: Promise<void>;
  updateCallbackDone: Promise<void>;
}>;

let activeTransition: DocumentViewTransition | null = null;

export async function runViewTransition(options: ViewTransitionOptions): Promise<ViewTransitionResult> {
  if (!canStartViewTransition()) {
    await options.update();
    return immediateViewTransitionResult();
  }

  const documentElement = document.documentElement;
  const scopeClass = viewTransitionScopeClass(options.scope);
  documentElement.classList.add(scopeClass);
  const transition = document.startViewTransition(options.update);

  activeTransition = transition;
  void transition.finished.finally(() => {
    if (activeTransition === transition) {
      activeTransition = null;
    }
    documentElement.classList.remove(scopeClass);
  });

  return {
    mode: "transition",
    finished: transition.finished,
    updateCallbackDone: transition.updateCallbackDone,
  };
}

export function viewTransitionScopeClass(scope: ViewTransitionScope): string {
  return `view-transition-${scope}`;
}

function canStartViewTransition(): boolean {
  if (typeof document === "undefined" || !(document.startViewTransition instanceof Function)) {
    return false;
  }
  if (document.visibilityState === "hidden" || activeTransition !== null) {
    return false;
  }
  return !prefersReducedMotion();
}

function prefersReducedMotion(): boolean {
  return (
    globalThis.matchMedia instanceof Function &&
    globalThis.matchMedia("(prefers-reduced-motion: reduce)").matches
  );
}

function immediateViewTransitionResult(): ViewTransitionResult {
  const resolved = Promise.resolve();
  return { mode: "immediate", finished: resolved, updateCallbackDone: resolved };
}
