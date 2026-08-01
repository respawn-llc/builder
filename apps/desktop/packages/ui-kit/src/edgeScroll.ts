const defaultFrameDeltaMs = 16;
const maxFrameDeltaMs = 48;
const maxVelocityPixelsPerSecond = 900;
const edgeActivationDistancePixels = 72;

export type EdgeScrollAxis = "x" | "y";

export type EdgeScrollMotion = Readonly<{
  axis: EdgeScrollAxis;
  element: HTMLElement;
  velocity: number;
}>;

export type EdgeScrollDriver = Readonly<{
  refresh(): void;
  stop(): void;
}>;

export function edgeScrollVelocity(position: number, start: number, end: number): number {
  if (start >= end) {
    return 0;
  }
  const distanceFromStart = position - start;
  if (distanceFromStart >= 0 && distanceFromStart < edgeActivationDistancePixels) {
    return -edgeScrollVelocityMagnitude(distanceFromStart);
  }
  const distanceFromEnd = end - position;
  if (distanceFromEnd >= 0 && distanceFromEnd < edgeActivationDistancePixels) {
    return edgeScrollVelocityMagnitude(distanceFromEnd);
  }
  return 0;
}

export function canScrollEdge(element: HTMLElement, axis: EdgeScrollAxis, velocity: number): boolean {
  if (velocity === 0) {
    return false;
  }
  const position = axis === "x" ? element.scrollLeft : element.scrollTop;
  const maximum =
    axis === "x"
      ? Math.max(0, element.scrollWidth - element.clientWidth)
      : Math.max(0, element.scrollHeight - element.clientHeight);
  return velocity < 0 ? position > 0 : position < maximum;
}

export function createEdgeScrollDriver(
  getMotion: () => readonly EdgeScrollMotion[] | null,
): EdgeScrollDriver {
  let frameID: number | null = null;
  let lastFrameTimestamp: number | null = null;

  function stop(): void {
    if (frameID !== null) {
      window.cancelAnimationFrame(frameID);
      frameID = null;
    }
    lastFrameTimestamp = null;
  }

  function onFrame(timestamp: number): void {
    frameID = null;
    const motion = getMotion();
    if (motion === null || motion.length === 0) {
      stop();
      return;
    }
    const elapsedMs =
      lastFrameTimestamp === null
        ? defaultFrameDeltaMs
        : Math.min(Math.max(timestamp - lastFrameTimestamp, 0), maxFrameDeltaMs);
    lastFrameTimestamp = timestamp;
    let moved = false;
    for (const item of motion) {
      moved ||= applyScroll(item, elapsedMs);
    }
    if (!moved) {
      stop();
      return;
    }
    refresh();
  }

  function refresh(): void {
    const motion = getMotion();
    if (motion === null || motion.length === 0) {
      stop();
      return;
    }
    frameID ??= window.requestAnimationFrame(onFrame);
  }

  return { refresh, stop };
}

function applyScroll(motion: EdgeScrollMotion, elapsedMs: number): boolean {
  const velocity = Math.min(Math.max(motion.velocity, -maxVelocityPixelsPerSecond), maxVelocityPixelsPerSecond);
  if (velocity === 0) {
    return false;
  }
  const position = motion.axis === "x" ? motion.element.scrollLeft : motion.element.scrollTop;
  const maximum =
    motion.axis === "x"
      ? Math.max(0, motion.element.scrollWidth - motion.element.clientWidth)
      : Math.max(0, motion.element.scrollHeight - motion.element.clientHeight);
  const next = Math.min(Math.max(position + (velocity * elapsedMs) / 1_000, 0), maximum);
  if (motion.axis === "x") {
    motion.element.scrollLeft = next;
  } else {
    motion.element.scrollTop = next;
  }
  return next !== position;
}

function edgeScrollVelocityMagnitude(distanceFromEdge: number): number {
  const proximity = 1 - distanceFromEdge / edgeActivationDistancePixels;
  return maxVelocityPixelsPerSecond * proximity * proximity;
}
