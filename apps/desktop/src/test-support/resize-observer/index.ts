export type ResizeObserverGeometry = Readonly<{
  clientHeight?: number | undefined;
  rect?: DOMRect | undefined;
  scrollHeight?: number | undefined;
}>;

export type ResizeObserverGeometryHarness = Readonly<{
  notify(): void;
  restore(): void;
  setGeometry(element: HTMLElement, geometry: ResizeObserverGeometry): void;
}>;

export function installResizeObserverGeometry(): ResizeObserverGeometryHarness {
  const originalResizeObserver = globalThis.ResizeObserver;
  const originalClientHeight = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "clientHeight");
  const originalScrollHeight = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollHeight");
  const originalGetBoundingClientRect = Object.getOwnPropertyDescriptor(
    HTMLElement.prototype,
    "getBoundingClientRect",
  );
  const geometries = new WeakMap<HTMLElement, ResizeObserverGeometry>();
  const observers: ControlledResizeObserver[] = [];

  globalThis.ResizeObserver = class extends ControlledResizeObserver {
    constructor(callback: ResizeObserverCallback) {
      super(callback);
      observers.push(this);
    }
  };
  Object.defineProperty(HTMLElement.prototype, "clientHeight", {
    configurable: true,
    get(this: HTMLElement) {
      return geometries.get(this)?.clientHeight ?? 0;
    },
  });
  Object.defineProperty(HTMLElement.prototype, "scrollHeight", {
    configurable: true,
    get(this: HTMLElement) {
      return geometries.get(this)?.scrollHeight ?? 0;
    },
  });
  Object.defineProperty(HTMLElement.prototype, "getBoundingClientRect", {
    configurable: true,
    value(this: HTMLElement): DOMRect {
      const geometry = geometries.get(this);
      if (geometry?.rect !== undefined) {
        return geometry.rect;
      }
      return emptyRect();
    },
  });

  return {
    notify() {
      for (const observer of observers) {
        observer.notify();
      }
    },
    restore() {
      globalThis.ResizeObserver = originalResizeObserver;
      restoreDescriptor(HTMLElement.prototype, "clientHeight", originalClientHeight);
      restoreDescriptor(HTMLElement.prototype, "scrollHeight", originalScrollHeight);
      restoreDescriptor(HTMLElement.prototype, "getBoundingClientRect", originalGetBoundingClientRect);
    },
    setGeometry(element, geometry) {
      geometries.set(element, geometry);
    },
  };
}

class ControlledResizeObserver implements ResizeObserver {
  readonly #callback: ResizeObserverCallback;

  constructor(callback: ResizeObserverCallback) {
    this.#callback = callback;
  }

  disconnect(): void {
    return;
  }

  observe(): void {
    return;
  }

  unobserve(): void {
    return;
  }

  notify(): void {
    this.#callback([], this);
  }
}

function restoreDescriptor(
  prototype: typeof HTMLElement.prototype,
  property: "clientHeight" | "getBoundingClientRect" | "scrollHeight",
  descriptor: PropertyDescriptor | undefined,
): void {
  if (descriptor === undefined) {
    Reflect.deleteProperty(prototype, property);
  } else {
    Object.defineProperty(prototype, property, descriptor);
  }
}

function emptyRect(): DOMRect {
  return {
    bottom: 0,
    height: 0,
    left: 0,
    right: 0,
    top: 0,
    width: 0,
    x: 0,
    y: 0,
    toJSON: () => ({}),
  };
}
