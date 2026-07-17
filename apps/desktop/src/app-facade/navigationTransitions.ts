import { runViewTransition } from "./viewTransitions";

export async function runNavigationTransition(update: () => void | Promise<void>): Promise<void> {
  const transition = await runViewTransition({ scope: "route", update });
  await transition.updateCallbackDone;
}
