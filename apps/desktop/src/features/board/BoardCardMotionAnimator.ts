import type { RefObject } from "react";

import type { PendingBoardCardMove } from "./BoardCardMotionModel";

export type BoardCardMotionTransitionOptions = Readonly<{
  cardElementsRef: RefObject<ReadonlyMap<string, HTMLElement>>;
  columnElementsRef: RefObject<ReadonlyMap<string, HTMLElement>>;
  namesByCardID: ReadonlyMap<string, string>;
  pendingCardMove: PendingBoardCardMove | null;
  update: () => void;
}>;

type BoardCardMotionSnapshot = Readonly<{
  clone: HTMLElement;
  rect: DOMRectReadOnly;
}>;

export async function runBoardCardMotionTransition(options: BoardCardMotionTransitionOptions): Promise<void> {
  if (prefersReducedMotion() || options.namesByCardID.size === 0) {
    options.update();
    return;
  }
  const oldSnapshots = snapshotBoardCardElements(options.cardElementsRef.current, options.namesByCardID);
  options.update();
  const animations = Array.from(options.namesByCardID.keys()).flatMap((cardID) => {
    const oldSnapshot = oldSnapshots.get(cardID);
    if (oldSnapshot === undefined) {
      return [];
    }
    const newElement = options.cardElementsRef.current.get(cardID);
    if (newElement !== undefined) {
      return [
        animateMovedBoardCard(newElement, oldSnapshot.rect).finally(() => {
          oldSnapshot.clone.remove();
        }),
      ];
    }
    const targetRect =
      options.pendingCardMove?.taskID === cardID
        ? options.columnElementsRef.current.get(options.pendingCardMove.targetColumnID)?.getBoundingClientRect()
        : undefined;
    return [animateDepartingBoardCard(oldSnapshot, targetRect)];
  });
  await Promise.allSettled(animations);
}

function snapshotBoardCardElements(
  cardElements: ReadonlyMap<string, HTMLElement>,
  namesByCardID: ReadonlyMap<string, string>,
): ReadonlyMap<string, BoardCardMotionSnapshot> {
  const snapshots = new Map<string, BoardCardMotionSnapshot>();
  for (const cardID of namesByCardID.keys()) {
    const element = cardElements.get(cardID);
    if (element === undefined) {
      continue;
    }
    const rect = element.getBoundingClientRect();
    snapshots.set(cardID, { clone: cloneBoardCardForMotion(element, rect), rect });
  }
  return snapshots;
}

async function animateMovedBoardCard(element: HTMLElement, oldRect: DOMRectReadOnly): Promise<void> {
  const newRect = element.getBoundingClientRect();
  const deltaX = oldRect.left - newRect.left;
  const deltaY = oldRect.top - newRect.top;
  if (deltaX === 0 && deltaY === 0) {
    return;
  }
  await animateElement(
    element,
    [
      { transform: `translate(${deltaX.toString()}px, ${deltaY.toString()}px)` },
      { transform: "translate(0, 0)" },
    ],
    boardCardMotionTiming(),
  );
}

async function animateDepartingBoardCard(
  snapshot: BoardCardMotionSnapshot,
  targetRect: DOMRectReadOnly | undefined,
): Promise<void> {
  const clone = snapshot.clone;
  document.body.append(clone);
  const targetTransform =
    targetRect === undefined ? "translateY(-8px) scale(0.98)" : boardCardDepartureTransform(snapshot.rect, targetRect);
  try {
    await animateElement(
      clone,
      [
        { opacity: 1, transform: "translate(0, 0) scale(1)" },
        { opacity: targetRect === undefined ? 0 : 0.72, transform: targetTransform },
      ],
      boardCardMotionTiming(),
    );
  } finally {
    clone.remove();
  }
}

async function animateElement(
  element: HTMLElement,
  keyframes: Keyframe[] | PropertyIndexedKeyframes,
  options: KeyframeAnimationOptions,
): Promise<void> {
  if (!(element.animate instanceof Function)) {
    return;
  }
  await element.animate(keyframes, options).finished;
}

function cloneBoardCardForMotion(element: HTMLElement, rect: DOMRectReadOnly): HTMLElement {
  const clone = element.cloneNode(true);
  if (!(clone instanceof HTMLElement)) {
    throw new TypeError("Board card motion clone must be an HTMLElement");
  }
  clone.classList.add("board-card-motion-clone");
  const style = clone.style;
  style.left = `${rect.left.toString()}px`;
  style.top = `${rect.top.toString()}px`;
  style.width = `${rect.width.toString()}px`;
  style.height = `${rect.height.toString()}px`;
  return clone;
}

function boardCardDepartureTransform(cardRect: DOMRectReadOnly, targetRect: DOMRectReadOnly): string {
  const targetLeft = targetRect.left + (targetRect.width - cardRect.width) / 2;
  const targetTop = targetRect.top + Math.min(cardRect.height, targetRect.height * 0.18);
  return `translate(${(targetLeft - cardRect.left).toString()}px, ${(targetTop - cardRect.top).toString()}px) scale(0.96)`;
}

function boardCardMotionTiming(): KeyframeAnimationOptions {
  return {
    duration: motionDurationMs("--motion-normal", 400),
    easing: "cubic-bezier(0.16, 1, 0.3, 1)",
    fill: "both",
  };
}

function motionDurationMs(tokenName: string, fallbackMs: number): number {
  const tokenValue = getComputedStyle(document.documentElement).getPropertyValue(tokenName).trim();
  const durationToken = tokenValue.split(" ").find((part) => part.length > 0) ?? "";
  if (durationToken.endsWith("ms")) {
    return Number.parseFloat(durationToken);
  }
  if (durationToken.endsWith("s")) {
    return Number.parseFloat(durationToken) * 1000;
  }
  return fallbackMs;
}

function prefersReducedMotion(): boolean {
  return (
    globalThis.matchMedia instanceof Function &&
    globalThis.matchMedia("(prefers-reduced-motion: reduce)").matches
  );
}
