import { getBezierPath, type EdgeProps } from "@xyflow/react";

import type { WorkflowGraphEdge } from "./workflowGraphLayout";

export function workflowEdgePath(props: EdgeProps<WorkflowGraphEdge>): Readonly<{
  labelPoint: Readonly<{ x: number; y: number }>;
  path: string;
  tailPoint: Readonly<{ x: number; y: number }>;
}> {
  const routedPath = routedEdgePath(props.data?.routePoints ?? []);
  if (routedPath !== null) {
    return routedPath;
  }
  const [path, labelX, labelY] = getBezierPath(props);
  return { labelPoint: { x: labelX, y: labelY }, path, tailPoint: { x: props.sourceX, y: props.sourceY } };
}

function routedEdgePath(points: readonly Readonly<{ x: number; y: number }>[]): Readonly<{
  labelPoint: Readonly<{ x: number; y: number }>;
  path: string;
  tailPoint: Readonly<{ x: number; y: number }>;
}> | null {
  if (points.length < 2) {
    return null;
  }
  const tailPoint = points[0] ?? { x: 0, y: 0 };
  return {
    labelPoint: midpointOnPolyline(points),
    path: roundedPolylinePath(points, 14),
    tailPoint,
  };
}

function roundedPolylinePath(points: readonly Readonly<{ x: number; y: number }>[], radius: number): string {
  const first = points[0];
  if (first === undefined) {
    return "";
  }
  const commands = [`M ${first.x.toString()} ${first.y.toString()}`];
  for (let index = 1; index < points.length; index += 1) {
    const previous = points[index - 1];
    const current = points[index];
    const next = points[index + 1];
    if (previous === undefined || current === undefined) {
      continue;
    }
    if (next === undefined || isCollinear(previous, current, next)) {
      commands.push(`L ${current.x.toString()} ${current.y.toString()}`);
      continue;
    }
    const corner = roundedCorner(previous, current, next, radius);
    commands.push(`L ${corner.entry.x.toString()} ${corner.entry.y.toString()}`);
    commands.push(
      `Q ${current.x.toString()} ${current.y.toString()} ${corner.exit.x.toString()} ${corner.exit.y.toString()}`,
    );
  }
  return commands.join(" ");
}

function roundedCorner(
  previous: Readonly<{ x: number; y: number }>,
  current: Readonly<{ x: number; y: number }>,
  next: Readonly<{ x: number; y: number }>,
  radius: number,
): Readonly<{ entry: Readonly<{ x: number; y: number }>; exit: Readonly<{ x: number; y: number }> }> {
  const incomingLength = distance(previous, current);
  const outgoingLength = distance(current, next);
  const offset = Math.min(radius, incomingLength / 2, outgoingLength / 2);
  return {
    entry: pointToward(current, previous, offset),
    exit: pointToward(current, next, offset),
  };
}

function pointToward(
  from: Readonly<{ x: number; y: number }>,
  to: Readonly<{ x: number; y: number }>,
  distanceValue: number,
): Readonly<{ x: number; y: number }> {
  const length = distance(from, to);
  if (length === 0) {
    return from;
  }
  const ratio = distanceValue / length;
  return {
    x: from.x + (to.x - from.x) * ratio,
    y: from.y + (to.y - from.y) * ratio,
  };
}

function isCollinear(
  a: Readonly<{ x: number; y: number }>,
  b: Readonly<{ x: number; y: number }>,
  c: Readonly<{ x: number; y: number }>,
): boolean {
  return (b.x - a.x) * (c.y - b.y) === (b.y - a.y) * (c.x - b.x);
}

function midpointOnPolyline(
  points: readonly Readonly<{ x: number; y: number }>[],
): Readonly<{ x: number; y: number }> {
  const segments = polylineSegments(points);
  const targetDistance = segments.reduce((total, segment) => total + segment.length, 0) / 2;
  let traversedDistance = 0;
  for (const segment of segments) {
    if (traversedDistance + segment.length >= targetDistance) {
      const ratio = segment.length === 0 ? 0 : (targetDistance - traversedDistance) / segment.length;
      return {
        x: segment.from.x + (segment.to.x - segment.from.x) * ratio,
        y: segment.from.y + (segment.to.y - segment.from.y) * ratio,
      };
    }
    traversedDistance += segment.length;
  }
  return points[Math.max(0, points.length - 1)] ?? { x: 0, y: 0 };
}

function polylineSegments(points: readonly Readonly<{ x: number; y: number }>[]): readonly Readonly<{
  from: Readonly<{ x: number; y: number }>;
  length: number;
  to: Readonly<{ x: number; y: number }>;
}>[] {
  return points.slice(1).flatMap((point, index) => {
    const from = points[index];
    if (from === undefined) {
      return [];
    }
    return [{ from, length: distance(from, point), to: point }];
  });
}

function distance(from: Readonly<{ x: number; y: number }>, to: Readonly<{ x: number; y: number }>): number {
  return Math.hypot(to.x - from.x, to.y - from.y);
}
