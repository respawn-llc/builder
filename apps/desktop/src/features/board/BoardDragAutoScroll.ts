import { useCallback, useEffect, useState, type DragEvent as ReactDragEvent, type RefObject } from "react";

const BOARD_DRAG_AUTOSCROLL_EDGE_ZONE_PX = 72;
const BOARD_DRAG_AUTOSCROLL_MAX_SPEED_PX_PER_SECOND = 900;
const BOARD_DRAG_AUTOSCROLL_MAX_FRAME_DELTA_MS = 48;

type DragPointer = Readonly<{ clientX: number; clientY: number }>;

type ScrollAxisMotion = Readonly<{
  element: HTMLElement;
  axis: "x" | "y";
  velocity: number;
}>;

type ScrollMotion = Readonly<{
  horizontal: ScrollAxisMotion | null;
  vertical: ScrollAxisMotion | null;
}>;

class BoardDragAutoScrollController {
  #active = false;
  #columns = new Map<string, HTMLElement>();
  #frameID: number | null = null;
  #lastFrameTimestamp: number | null = null;
  #pointer: DragPointer | null = null;
  #root: HTMLElement | null;

  constructor(root: HTMLElement | null) {
    this.#root = root;
  }

  setRoot(root: HTMLElement | null): void {
    this.#root = root;
    this.#refreshLoop();
  }

  setActive(active: boolean): void {
    this.#active = active;
    if (!active) {
      this.stop();
      return;
    }
    this.#refreshLoop();
  }

  registerColumnScrollport(columnID: string, element: HTMLElement | null): void {
    if (element === null) {
      this.#columns.delete(columnID);
    } else {
      this.#columns.set(columnID, element);
    }
    this.#refreshLoop();
  }

  updatePointer(pointer: DragPointer): void {
    if (!this.#active) {
      return;
    }
    this.#pointer = pointer;
    this.#refreshLoop();
  }

  stop(): void {
    this.#pointer = null;
    this.#lastFrameTimestamp = null;
    if (this.#frameID !== null) {
      cancelAnimationFrame(this.#frameID);
      this.#frameID = null;
    }
  }

  destroy(): void {
    this.stop();
    this.#columns.clear();
    this.#root = null;
  }

  #refreshLoop(): void {
    if (this.#motion() === null) {
      this.stop();
      return;
    }
    if (this.#frameID !== null) {
      return;
    }
    this.#frameID = requestAnimationFrame((timestamp) => {
      this.#frameID = null;
      this.#onFrame(timestamp);
    });
  }

  #onFrame(timestamp: number): void {
    const motion = this.#motion();
    if (motion === null) {
      this.stop();
      return;
    }
    const elapsedMs =
      this.#lastFrameTimestamp === null
        ? 0
        : normalizedBoardDragFrameDeltaMs(timestamp - this.#lastFrameTimestamp);
    this.#lastFrameTimestamp = timestamp;
    if (elapsedMs > 0) {
      if (motion.horizontal !== null) {
        applyScroll(motion.horizontal.element, motion.horizontal.axis, motion.horizontal.velocity, elapsedMs);
      }
      if (motion.vertical !== null) {
        applyScroll(motion.vertical.element, motion.vertical.axis, motion.vertical.velocity, elapsedMs);
      }
    }
    this.#refreshLoop();
  }

  #motion(): ScrollMotion | null {
    if (!this.#active || this.#pointer === null || this.#root === null) {
      return null;
    }
    const rootRect = this.#root.getBoundingClientRect();
    const horizontalVelocity = boardDragEdgeVelocity(this.#pointer.clientX, rootRect.left, rootRect.right);
    const verticalTarget = this.#verticalTarget(this.#pointer);
    const verticalVelocity =
      verticalTarget === null
        ? 0
        : boardDragEdgeVelocity(
            this.#pointer.clientY,
            verticalTarget.getBoundingClientRect().top,
            verticalTarget.getBoundingClientRect().bottom,
          );
    const horizontalCanMove = canScroll(this.#root, "x", horizontalVelocity);
    const horizontal = horizontalCanMove
      ? { element: this.#root, axis: "x" as const, velocity: horizontalVelocity }
      : null;
    const vertical =
      verticalTarget !== null && canScroll(verticalTarget, "y", verticalVelocity)
        ? { element: verticalTarget, axis: "y" as const, velocity: verticalVelocity }
        : null;
    if (horizontal === null && vertical === null) {
      return null;
    }
    return { horizontal, vertical };
  }

  #verticalTarget(pointer: DragPointer): HTMLElement | null {
    for (const element of this.#columns.values()) {
      if (pointIsInsideElement(pointer, element)) {
        return element;
      }
    }
    return null;
  }
}

function boardDragEdgeVelocity(position: number, start: number, end: number): number {
  if (start >= end) {
    return 0;
  }
  const distanceFromStart = position - start;
  if (distanceFromStart >= 0 && distanceFromStart < BOARD_DRAG_AUTOSCROLL_EDGE_ZONE_PX) {
    return -edgeVelocityMagnitude(distanceFromStart);
  }
  const distanceFromEnd = end - position;
  if (distanceFromEnd >= 0 && distanceFromEnd < BOARD_DRAG_AUTOSCROLL_EDGE_ZONE_PX) {
    return edgeVelocityMagnitude(distanceFromEnd);
  }
  return 0;
}

function normalizedBoardDragFrameDeltaMs(deltaMs: number): number {
  return Math.min(Math.max(deltaMs, 0), BOARD_DRAG_AUTOSCROLL_MAX_FRAME_DELTA_MS);
}

export function useBoardDragAutoScroll({
  active,
  rootRef,
}: Readonly<{
  active: boolean;
  rootRef: RefObject<HTMLElement | null>;
}>) {
  const [controller] = useState(() => new BoardDragAutoScrollController(null));

  useEffect(() => {
    controller.setRoot(rootRef.current);
    controller.setActive(active);
  }, [active, controller, rootRef]);

  useEffect(() => {
    const stopOutsideBoard = (event: Event) => {
      const root = rootRef.current;
      if (root === null || !(event.target instanceof Node) || !root.contains(event.target)) {
        controller.stop();
      }
    };
    const stopDocumentExit = (event: globalThis.DragEvent) => {
      if (event.relatedTarget === null) {
        const root = rootRef.current;
        if (
          root !== null &&
          event.target instanceof Node &&
          root.contains(event.target) &&
          pointIsInsideElement(event, root)
        ) {
          return;
        }
        controller.stop();
      }
    };
    document.addEventListener("dragover", stopOutsideBoard, true);
    document.addEventListener("dragleave", stopDocumentExit, true);
    return () => {
      document.removeEventListener("dragover", stopOutsideBoard, true);
      document.removeEventListener("dragleave", stopDocumentExit, true);
    };
  }, [controller, rootRef]);

  useEffect(
    () => () => {
      controller.destroy();
    },
    [controller],
  );

  const onBoardDragOver = useCallback(
    (event: ReactDragEvent<HTMLElement>) => {
      controller.setRoot(rootRef.current);
      controller.updatePointer({ clientX: event.clientX, clientY: event.clientY });
    },
    [controller, rootRef],
  );
  const onBoardDragLeave = useCallback(
    (event: ReactDragEvent<HTMLElement>) => {
      const root = rootRef.current;
      if (root !== null && event.relatedTarget instanceof Node && root.contains(event.relatedTarget)) {
        return;
      }
      if (root !== null && event.relatedTarget === null && pointIsInsideElement(event, root)) {
        return;
      }
      controller.stop();
    },
    [controller, rootRef],
  );
  const registerColumnScrollport = useCallback(
    (columnID: string, element: HTMLElement | null) => {
      controller.registerColumnScrollport(columnID, element);
    },
    [controller],
  );
  const stop = useCallback(() => {
    controller.stop();
  }, [controller]);

  return { onBoardDragLeave, onBoardDragOver, registerColumnScrollport, stop };
}

function pointIsInsideElement(point: DragPointer, element: HTMLElement): boolean {
  const rect = element.getBoundingClientRect();
  return (
    point.clientX >= rect.left &&
    point.clientX < rect.right &&
    point.clientY >= rect.top &&
    point.clientY < rect.bottom
  );
}

function edgeVelocityMagnitude(distanceFromEdge: number): number {
  const proximity = 1 - distanceFromEdge / BOARD_DRAG_AUTOSCROLL_EDGE_ZONE_PX;
  return BOARD_DRAG_AUTOSCROLL_MAX_SPEED_PX_PER_SECOND * proximity * proximity;
}

function canScroll(element: HTMLElement, axis: "x" | "y", velocity: number): boolean {
  if (velocity === 0) {
    return false;
  }
  const position = axis === "x" ? element.scrollLeft : element.scrollTop;
  const max =
    axis === "x" ? element.scrollWidth - element.clientWidth : element.scrollHeight - element.clientHeight;
  return velocity < 0 ? position > 0 : position < max;
}

function applyScroll(element: HTMLElement, axis: "x" | "y", velocity: number, elapsedMs: number): boolean {
  if (velocity === 0) {
    return false;
  }
  const position = axis === "x" ? element.scrollLeft : element.scrollTop;
  const max = Math.max(
    0,
    axis === "x" ? element.scrollWidth - element.clientWidth : element.scrollHeight - element.clientHeight,
  );
  const next = Math.min(Math.max(position + (velocity * elapsedMs) / 1_000, 0), max);
  if (axis === "x") {
    element.scrollLeft = next;
  } else {
    element.scrollTop = next;
  }
  return next !== position;
}
