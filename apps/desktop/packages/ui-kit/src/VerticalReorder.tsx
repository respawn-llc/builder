import {
  closestCenter,
  DndContext,
  DragOverlay,
  KeyboardSensor,
  PointerSensor,
  useSensor,
  useSensors,
  type DragEndEvent,
  type DragMoveEvent,
  type DragOverEvent,
  type DragStartEvent,
  type UniqueIdentifier,
  type CollisionDetection,
} from "@dnd-kit/core";
import {
  SortableContext,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import {
  cloneElement,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type HTMLAttributes,
  type ReactElement,
  type ReactNode,
  type RefAttributes,
} from "react";
import { createEdgeScrollDriver } from "./edgeScroll";

export type VerticalReorderDragSource = "keyboard" | "pointer";

export type VerticalReorderDragSession = Readonly<{
  activeID: UniqueIdentifier;
  source: VerticalReorderDragSource;
}>;

export type VerticalReorderRow = Readonly<{
  activator: VerticalReorderActivator;
  isDragging: boolean;
  isOverlay: boolean;
}>;

type VerticalReorderActivator = ReactElement<HTMLAttributes<HTMLElement> & RefAttributes<HTMLElement>>;

export function VerticalReorder<Item, ID extends UniqueIdentifier>({
  getItemID,
  items,
  onCommit,
  renderActivator,
  renderItem,
}: Readonly<{
  getItemID: (item: Item) => ID;
  items: readonly Item[];
  onCommit: (orderedIDs: readonly ID[]) => void;
  renderActivator: (item: Item) => VerticalReorderActivator;
  renderItem: (item: Item, row: VerticalReorderRow) => ReactNode;
}>) {
  const [session, setSession] = useState<VerticalReorderDragSession | null>(null);
  const destinationIDRef = useRef<UniqueIdentifier | null>(null);
  const rowNodes = useRef(new Map<UniqueIdentifier, HTMLElement>());
  const listRef = useRef<HTMLDivElement | null>(null);
  const edgeScroll = useBoundedVerticalEdgeScroll();
  const itemByID = new Map<UniqueIdentifier, Item>(items.map((item) => [getItemID(item), item]));
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );

  const registerRow = useCallback((id: ID, element: HTMLElement | null) => {
    if (element === null) {
      rowNodes.current.delete(id);
      return;
    }
    rowNodes.current.set(id, element);
  }, []);
  const moveKeyboardDestination = useCallback(
    (activeID: UniqueIdentifier, direction: 1 | -1) => {
      const ids = items.map(getItemID);
      const currentID = destinationIDRef.current ?? activeID;
      const currentIndex = ids.findIndex((id) => id === currentID);
      destinationIDRef.current = ids[currentIndex + direction] ?? currentID;
    },
    [getItemID, items],
  );

  const clearSession = useCallback(() => {
    edgeScroll.stop();
    destinationIDRef.current = null;
    setSession(null);
  }, [edgeScroll]);

  const onDragStart = useCallback(
    (event: DragStartEvent) => {
      const activeID = event.active.id;
      const source = event.activatorEvent instanceof KeyboardEvent ? "keyboard" : "pointer";
      destinationIDRef.current = activeID;
      setSession({
        activeID,
        source,
      });
      if (source === "pointer") {
        edgeScroll.start(listRef.current);
      }
    },
    [edgeScroll],
  );

  const onDragOver = useCallback(
    (event: DragOverEvent) => {
      const overID = event.over?.id;
      if (overID === undefined) {
        return;
      }
      if (session?.source === "keyboard") {
        rowNodes.current.get(overID)?.scrollIntoView({ block: "nearest" });
        return;
      }
      if (overID !== event.active.id) {
        destinationIDRef.current = overID;
      }
    },
    [session?.source],
  );

  const onDragMove = useCallback(
    (event: DragMoveEvent) => {
      if (session?.source === "keyboard") {
        return;
      }
      const pointer = pointerLocation(event);
      if (pointer !== null) {
        edgeScroll.move(pointer);
      }
    },
    [edgeScroll, session?.source],
  );

  const onDragEnd = useCallback(
    (event: DragEndEvent) => {
      const activeID = event.active.id;
      const overID = destinationIDRef.current ?? event.over?.id;
      const orderedIDs = reorderIDs(items.map(getItemID), activeID, overID);
      clearSession();
      if (orderedIDs === null) {
        return;
      }
      onCommit(orderedIDs);
    },
    [clearSession, getItemID, items, onCommit],
  );

  const onDragCancel = useCallback(
    () => {
      clearSession();
    },
    [clearSession],
  );

  useEffect(() => clearSession, [clearSession]);

  const activeItem = session === null ? undefined : itemByID.get(session.activeID);

  return (
    <DndContext
      autoScroll={false}
      collisionDetection={verticalReorderCollisionDetection}
      onDragCancel={onDragCancel}
      onDragEnd={onDragEnd}
      onDragMove={onDragMove}
      onDragOver={onDragOver}
      onDragStart={onDragStart}
      sensors={sensors}
    >
      <SortableContext items={items.map(getItemID)} strategy={verticalListSortingStrategy}>
        <div ref={listRef} style={{ display: "grid", gap: "var(--space-3)" }}>
          {items.map((item) => {
            const id = getItemID(item);
            return (
              <VerticalReorderItem
                id={id}
                item={item}
                key={id}
                onRowNodeChange={registerRow}
                onKeyboardMove={moveKeyboardDestination}
                renderActivator={renderActivator}
                renderItem={renderItem}
                session={session}
              />
            );
          })}
        </div>
      </SortableContext>
      <DragOverlay dropAnimation={useReducedMotion() ? null : undefined}>
        {activeItem === undefined || session === null ? null : (
          <div aria-hidden="true" style={{ pointerEvents: "none" }}>
            {renderItem(activeItem, {
              activator: renderActivator(activeItem),
              isDragging: true,
              isOverlay: true,
            })}
          </div>
        )}
      </DragOverlay>
    </DndContext>
  );
}

function VerticalReorderItem<Item, ID extends UniqueIdentifier>({
  id,
  item,
  onRowNodeChange,
  onKeyboardMove,
  renderActivator,
  renderItem,
  session,
}: Readonly<{
  id: ID;
  item: Item;
  onRowNodeChange: (id: ID, element: HTMLElement | null) => void;
  onKeyboardMove: (activeID: ID, direction: 1 | -1) => void;
  renderActivator: (item: Item) => VerticalReorderActivator;
  renderItem: (item: Item, row: VerticalReorderRow) => ReactNode;
  session: VerticalReorderDragSession | null;
}>) {
  const reducedMotion = useReducedMotion();
  const { attributes, listeners, setActivatorNodeRef, setNodeRef, transform, transition } = useSortable({ id });
  const isDragging = session?.activeID === id;
  const setRowNodeRef = useCallback(
    (element: HTMLElement | null) => {
      setNodeRef(element);
      onRowNodeChange(id, element);
    },
    [id, onRowNodeChange, setNodeRef],
  );
  const style: CSSProperties = isDragging
    ? { opacity: 0, pointerEvents: "none" }
    : {
        transform:
          transform === null
            ? undefined
            : `translate3d(${transform.x.toString()}px, ${transform.y.toString()}px, 0)`,
        transition: reducedMotion ? undefined : transition,
      };

  return (
    <div
      ref={setRowNodeRef}
      style={style}
    >
      {renderItem(item, {
        activator: cloneElement(renderActivator(item), {
          ...attributes,
          ...listeners,
          onKeyDown: (event) => {
            if (isDragging && event.code === "ArrowDown") {
              onKeyboardMove(id, 1);
            } else if (isDragging && event.code === "ArrowUp") {
              onKeyboardMove(id, -1);
            }
            listeners?.onKeyDown?.(event);
          },
          ref: setActivatorNodeRef,
        }),
        isDragging,
        isOverlay: false,
      })}
    </div>
  );
}

function reorderIDs<ID extends UniqueIdentifier>(
  ids: readonly ID[],
  activeID: UniqueIdentifier,
  overID: UniqueIdentifier | undefined,
): readonly ID[] | null {
  if (overID === undefined || activeID === overID) {
    return null;
  }
  const activeIndex = ids.findIndex((id) => id === activeID);
  const overIndex = ids.findIndex((id) => id === overID);
  if (activeIndex < 0 || overIndex < 0) {
    return null;
  }
  const next = [...ids];
  const [moved] = next.splice(activeIndex, 1);
  if (moved === undefined) {
    return null;
  }
  next.splice(overIndex, 0, moved);
  return next;
}

const verticalReorderCollisionDetection: CollisionDetection = ({
  collisionRect,
  pointerCoordinates,
  ...args
}) => {
  if (pointerCoordinates === null) {
    return closestCenter({ collisionRect, pointerCoordinates, ...args });
  }
  const pointerRect = {
    ...collisionRect,
    bottom: pointerCoordinates.y + collisionRect.height / 2,
    left: pointerCoordinates.x - collisionRect.width / 2,
    right: pointerCoordinates.x + collisionRect.width / 2,
    top: pointerCoordinates.y - collisionRect.height / 2,
  };
  return closestCenter({ collisionRect: pointerRect, pointerCoordinates, ...args });
};

function pointerLocation(event: DragMoveEvent): Readonly<{ x: number; y: number }> | null {
  if (!(event.activatorEvent instanceof PointerEvent)) {
    return null;
  }
  return {
    x: event.activatorEvent.clientX + event.delta.x,
    y: event.activatorEvent.clientY + event.delta.y,
  };
}

function useReducedMotion(): boolean {
  const [reducedMotion, setReducedMotion] = useState(readReducedMotionPreference);
  useEffect(() => {
    if (!(window.matchMedia instanceof Function)) {
      return undefined;
    }
    const query = window.matchMedia("(prefers-reduced-motion: reduce)");
    const update = () => {
      setReducedMotion(query.matches);
    };
    update();
    query.addEventListener("change", update);
    return () => {
      query.removeEventListener("change", update);
    };
  }, []);
  return reducedMotion;
}

function readReducedMotionPreference(): boolean {
  return window.matchMedia instanceof Function && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

function useBoundedVerticalEdgeScroll(): Readonly<{
  move(pointer: Readonly<{ x: number; y: number }>): void;
  start(anchor: HTMLElement | null): void;
  stop(): void;
}> {
  const container = useRef<HTMLElement | null>(null);
  const pointer = useRef<Readonly<{ x: number; y: number }> | null>(null);
  const driver = useRef<ReturnType<typeof createEdgeScrollDriver> | null>(null);
  useEffect(() => {
    const created = createEdgeScrollDriver(() => {
        const current = container.current;
        const position = pointer.current;
        if (current === null || !current.isConnected || position === null) {
          return null;
        }
        const rect = current.getBoundingClientRect();
        if (
          position.x < rect.left ||
          position.x > rect.right ||
          position.y < rect.top ||
          position.y > rect.bottom
        ) {
          return null;
        }
        const velocity = verticalEdgeScrollVelocity(position.y, rect.top, rect.bottom);
        if (velocity === 0 || !canScrollVertically(current, velocity)) {
          return null;
        }
        return [{ axis: "y", element: current, velocity }];
      });
    driver.current = created;
    return () => {
      created.stop();
      driver.current = null;
    };
  }, []);
  const stop = useCallback(() => {
    container.current = null;
    pointer.current = null;
    driver.current?.stop();
  }, []);
  const start = useCallback(
    (anchor: HTMLElement | null) => {
      stop();
      container.current = nearestVerticalScrollContainer(anchor);
    },
    [stop],
  );
  const move = useCallback(
    (nextPointer: Readonly<{ x: number; y: number }>) => {
      pointer.current = nextPointer;
      driver.current?.refresh();
    },
    [],
  );
  useEffect(() => stop, [stop]);
  return useMemo(() => ({ move, start, stop }), [move, start, stop]);
}

function verticalEdgeScrollVelocity(pointerY: number, top: number, bottom: number): number {
  const edgeActivationDistance = 72;
  const maxVelocity = 900;
  const distanceFromTop = pointerY - top;
  const distanceFromBottom = bottom - pointerY;
  if (distanceFromTop >= 0 && distanceFromTop < edgeActivationDistance) {
    const proximity = 1 - distanceFromTop / edgeActivationDistance;
    return -maxVelocity * proximity * proximity;
  }
  if (distanceFromBottom >= 0 && distanceFromBottom < edgeActivationDistance) {
    const proximity = 1 - distanceFromBottom / edgeActivationDistance;
    return maxVelocity * proximity * proximity;
  }
  return 0;
}

function canScrollVertically(element: HTMLElement, velocity: number): boolean {
  const maximum = Math.max(0, element.scrollHeight - element.clientHeight);
  return velocity < 0 ? element.scrollTop > 0 : element.scrollTop < maximum;
}

function nearestVerticalScrollContainer(anchor: HTMLElement | null): HTMLElement | null {
  let element = anchor?.parentElement ?? null;
  while (element !== null) {
    const style = window.getComputedStyle(element);
    if (
      (style.overflowY === "auto" || style.overflowY === "scroll") &&
      element.scrollHeight > element.clientHeight
    ) {
      return element;
    }
    element = element.parentElement;
  }
  return null;
}
