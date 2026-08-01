import { useCallback, useEffect, useState, type DragEvent as ReactDragEvent, type RefObject } from "react";

import {
  canScrollEdge,
  createEdgeScrollDriver,
  edgeScrollVelocity,
  type EdgeScrollDriver,
  type EdgeScrollMotion,
} from "@app/ui-kit";

type DragPointer = Readonly<{ clientX: number; clientY: number }>;
type PointContainment = "inclusive" | "strict";

class BoardDragAutoScrollController {
  #active = false;
  #columns = new Map<string, HTMLElement>();
  #driver: EdgeScrollDriver;
  #pointer: DragPointer | null = null;
  #root: HTMLElement | null;

  constructor(root: HTMLElement | null) {
    this.#root = root;
    this.#driver = createEdgeScrollDriver(() => this.#motion());
  }

  setRoot(root: HTMLElement | null): void {
    this.#root = root;
    this.#driver.refresh();
  }

  setActive(active: boolean): void {
    this.#active = active;
    if (!active) {
      this.stop();
      return;
    }
    this.#driver.refresh();
  }

  registerColumnScrollport(columnID: string, element: HTMLElement | null): void {
    if (element === null) {
      this.#columns.delete(columnID);
    } else {
      this.#columns.set(columnID, element);
    }
    this.#driver.refresh();
  }

  updatePointer(pointer: DragPointer): void {
    if (!this.#active) {
      return;
    }
    this.#pointer = pointer;
    this.#driver.refresh();
  }

  stop(): void {
    this.#pointer = null;
    this.#driver.stop();
  }

  destroy(): void {
    this.stop();
    this.#columns.clear();
    this.#root = null;
    this.#driver.stop();
  }

  #motion(): readonly EdgeScrollMotion[] | null {
    if (!this.#active || this.#pointer === null || this.#root === null) {
      return null;
    }
    const rootRect = this.#root.getBoundingClientRect();
    const horizontalVelocity = edgeScrollVelocity(this.#pointer.clientX, rootRect.left, rootRect.right);
    const verticalTarget = this.#verticalTarget(this.#pointer);
    const verticalVelocity =
      verticalTarget === null
        ? 0
        : edgeScrollVelocity(
            this.#pointer.clientY,
            verticalTarget.getBoundingClientRect().top,
            verticalTarget.getBoundingClientRect().bottom,
          );
    const horizontalCanMove = canScrollEdge(this.#root, "x", horizontalVelocity);
    const motion: EdgeScrollMotion[] = [];
    if (horizontalCanMove) {
      motion.push({ element: this.#root, axis: "x", velocity: horizontalVelocity });
    }
    if (verticalTarget !== null && canScrollEdge(verticalTarget, "y", verticalVelocity)) {
      motion.push({ element: verticalTarget, axis: "y", velocity: verticalVelocity });
    }
    return motion.length === 0 ? null : motion;
  }

  #verticalTarget(pointer: DragPointer): HTMLElement | null {
    for (const element of this.#columns.values()) {
      if (pointIsInsideElement(pointer, element, "inclusive")) {
        return element;
      }
    }
    return null;
  }
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
          pointIsInsideElement(event, root, "strict")
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
      if (root !== null && event.relatedTarget === null && pointIsInsideElement(event, root, "strict")) {
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

function pointIsInsideElement(
  point: DragPointer,
  element: HTMLElement,
  containment: PointContainment,
): boolean {
  const rect = element.getBoundingClientRect();
  if (containment === "inclusive") {
    return (
      point.clientX >= rect.left &&
      point.clientX <= rect.right &&
      point.clientY >= rect.top &&
      point.clientY <= rect.bottom
    );
  }
  return (
    point.clientX > rect.left &&
    point.clientX < rect.right &&
    point.clientY > rect.top &&
    point.clientY < rect.bottom
  );
}
