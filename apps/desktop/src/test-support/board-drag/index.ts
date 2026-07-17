import { type Mock, vi } from "vitest";

export type BoardDragEventInit = Readonly<{
  clientX?: number;
  clientY?: number;
  dataTransfer?: TestDataTransfer;
  relatedTarget?: EventTarget | null;
}>;

export class TestDataTransfer {
  readonly #values = new Map<string, string>();
  effectAllowed = "all";
  dropEffect = "none";
  readonly setDragImage: Mock<(image: Element, x: number, y: number) => void> = vi.fn();

  get types(): readonly string[] {
    return [...this.#values.keys()];
  }

  setData(type: string, value: string): void {
    this.#values.set(type, value);
  }
}

export function createBoardDragEvent(type: string, init: BoardDragEventInit = {}): Event {
  const event = new Event(type, { bubbles: true, cancelable: true });
  const properties: PropertyDescriptorMap = {
    clientX: { value: init.clientX ?? 0 },
    clientY: { value: init.clientY ?? 0 },
    relatedTarget: { value: init.relatedTarget ?? null },
  };
  if (init.dataTransfer !== undefined) {
    properties.dataTransfer = { value: init.dataTransfer };
  }
  Object.defineProperties(event, properties);
  return event;
}

export function dispatchBoardDrag(target: EventTarget, type: string, init: BoardDragEventInit = {}): void {
  target.dispatchEvent(createBoardDragEvent(type, init));
}

export function setScrollportGeometry(
  element: HTMLElement,
  input: Readonly<{
    left: number;
    top: number;
    width: number;
    height: number;
    scrollWidth: number;
    scrollHeight: number;
  }>,
): void {
  Object.defineProperties(element, {
    clientHeight: { configurable: true, value: input.height },
    clientWidth: { configurable: true, value: input.width },
    scrollHeight: { configurable: true, value: input.scrollHeight },
    scrollLeft: { configurable: true, value: 0, writable: true },
    scrollTop: { configurable: true, value: 0, writable: true },
    scrollWidth: { configurable: true, value: input.scrollWidth },
  });
  Object.defineProperty(element, "getBoundingClientRect", {
    configurable: true,
    value: () => new DOMRect(input.left, input.top, input.width, input.height),
  });
}

export class FakeAnimationFrames {
  #callbacks = new Map<number, FrameRequestCallback>();
  #nextID = 1;

  readonly request = (callback: FrameRequestCallback): number => {
    const id = this.#nextID;
    this.#nextID += 1;
    this.#callbacks.set(id, callback);
    return id;
  };

  readonly cancel = (id: number): void => {
    this.#callbacks.delete(id);
  };

  get pending(): number {
    return this.#callbacks.size;
  }

  step(timestamp: number): void {
    const callbacks = [...this.#callbacks.values()];
    this.#callbacks.clear();
    for (const callback of callbacks) {
      callback(timestamp);
    }
  }
}
