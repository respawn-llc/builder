import {
  closestCenter,
  DndContext,
  DragOverlay,
  KeyboardSensor,
  PointerSensor,
  pointerWithin,
  useSensor,
  useSensors,
  type DragEndEvent,
  type DragMoveEvent,
  type DragOverEvent,
  type DragStartEvent,
  type Collision,
  type KeyboardCoordinateGetter,
  type SensorContext,
  type UniqueIdentifier,
  type CollisionDetection,
  type ClientRect,
} from "@dnd-kit/core";
import {
  SortableContext,
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
  const pointerListenerActive = useRef(false);
  const rowNodes = useRef(new Map<UniqueIdentifier, HTMLElement>());
  const listRef = useRef<HTMLDivElement | null>(null);
  const edgeScroll = useBoundedVerticalEdgeScroll();
  const itemByID = new Map<UniqueIdentifier, Item>(items.map((item) => [getItemID(item), item]));
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
    useSensor(KeyboardSensor, { coordinateGetter: verticalReorderKeyboardCoordinates }),
  );

  const registerRow = useCallback((id: ID, element: HTMLElement | null) => {
    if (element === null) {
      rowNodes.current.delete(id);
      return;
    }
    rowNodes.current.set(id, element);
  }, []);
  const recordPointerPosition = useCallback(
    (pointer: Readonly<{ x: number; y: number }>) => {
      edgeScroll.move(pointer);
    },
    [edgeScroll],
  );
  const clearSession = useCallback(() => {
    edgeScroll.stop();
    pointerListenerActive.current = false;
    setSession(null);
  }, [edgeScroll]);

  const onDragStart = useCallback(
    (event: DragStartEvent) => {
      const activeID = event.active.id;
      const source = event.activatorEvent instanceof KeyboardEvent ? "keyboard" : "pointer";
      setSession({
        activeID,
        source,
      });
      if (source === "pointer") {
        edgeScroll.start(listRef.current);
        const pointer = pointerActivationLocation(event.activatorEvent);
        if (pointer !== null) {
          recordPointerPosition(pointer);
        }
      }
    },
    [edgeScroll, recordPointerPosition],
  );

  const onDragOver = useCallback(
    (event: DragOverEvent) => {
      const overID = event.over?.id;
      if (overID === undefined) {
        return;
      }
      if (session?.source === "keyboard") {
        rowNodes.current.get(overID)?.scrollIntoView({ block: "nearest" });
      }
    },
    [session?.source],
  );

  const onDragMove = useCallback(
    (event: DragMoveEvent) => {
      if (session?.source === "keyboard") {
        return;
      }
      if (pointerListenerActive.current) {
        return;
      }
      const pointer = pointerLocation(event);
      if (pointer !== null) {
        recordPointerPosition(pointer);
      }
    },
    [recordPointerPosition, session?.source],
  );

  const onDragEnd = useCallback(
    (event: DragEndEvent) => {
      const activeID = event.active.id;
      const orderedIDs = reorderIDs(items.map(getItemID), activeID, event.over?.id);
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
  useEffect(() => {
    if (session?.source !== "pointer") {
      pointerListenerActive.current = false;
      return undefined;
    }
    const handlePointerMove = (event: PointerEvent) => {
      recordPointerPosition({ x: event.clientX, y: event.clientY });
    };
    pointerListenerActive.current = true;
    window.addEventListener("pointermove", handlePointerMove, { passive: true });
    return () => {
      pointerListenerActive.current = false;
      window.removeEventListener("pointermove", handlePointerMove);
    };
  }, [recordPointerPosition, session?.source]);

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
  renderActivator,
  renderItem,
  session,
}: Readonly<{
  id: ID;
  item: Item;
  onRowNodeChange: (id: ID, element: HTMLElement | null) => void;
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
  const indexes = indexesByID(ids);
  const activeIndex = indexes.get(activeID);
  const overIndex = indexes.get(overID);
  if (activeIndex === undefined || overIndex === undefined) {
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
  active,
  collisionRect,
  pointerCoordinates,
  ...args
}) => {
  if (pointerCoordinates === null) {
    const collisions = closestCenter({ active, collisionRect, pointerCoordinates, ...args });
    return keyboardActivationCollisions(active, collisionRect, collisions);
  }
  const pointerCollisions = pointerWithin({ active, collisionRect, pointerCoordinates, ...args });
  return pointerCollisions.length === 0
    ? closestCenter({ active, collisionRect, pointerCoordinates, ...args })
    : pointerCollisions;
};

function keyboardActivationCollisions(
  active: { id: UniqueIdentifier; rect: { current: { initial: ClientRect | null } } },
  collisionRect: ClientRect,
  collisions: Collision[],
): Collision[] {
  const initialRect = active.rect.current.initial;
  if (initialRect === null || rectMoved(initialRect, collisionRect)) {
    return collisions;
  }
  const activeCollision = collisions.find(({ id }) => id === active.id);
  if (activeCollision === undefined) {
    return collisions;
  }
  return [activeCollision, ...collisions.filter(({ id }) => id !== active.id)];
}

function rectMoved(initial: ClientRect, current: ClientRect): boolean {
  return initial.top !== current.top || initial.left !== current.left;
}

const verticalReorderKeyboardCoordinates: KeyboardCoordinateGetter = (event, { active, context }) => {
  const direction = verticalKeyboardDirection(event.code);
  if (direction === undefined) {
    return undefined;
  }
  event.preventDefault();
  const ids = context.droppableContainers.getEnabled().map(({ id }) => id);
  const indexes = indexesByID(ids);
  const activeIndex = indexes.get(active);
  if (activeIndex === undefined) {
    return undefined;
  }
  const overIndex = context.over === null ? undefined : indexes.get(context.over.id);
  const currentIndex = keyboardCurrentIndex(context, activeIndex, overIndex);
  const targetID = ids[currentIndex + direction];
  if (targetID === undefined) {
    return undefined;
  }
  const targetRect = context.droppableRects.get(targetID);
  return targetRect === undefined ? undefined : { x: targetRect.left, y: targetRect.top };
};

function indexesByID(ids: readonly UniqueIdentifier[]): ReadonlyMap<UniqueIdentifier, number> {
  return new Map(ids.map((id, index) => [id, index]));
}

function verticalKeyboardDirection(code: string): 1 | -1 | undefined {
  if (code === "ArrowDown") {
    return 1;
  }
  if (code === "ArrowUp") {
    return -1;
  }
  return undefined;
}

function keyboardCurrentIndex(
  context: SensorContext,
  activeIndex: number,
  overIndex: number | undefined,
): number {
  const activeRect = context.active?.rect.current;
  if (activeRect === undefined) {
    return activeIndex;
  }
  const { initial, translated } = activeRect;
  if (initial === null || translated === null) {
    return activeIndex;
  }
  const hasMoved = initial.top !== translated.top || initial.left !== translated.left;
  return hasMoved && overIndex !== undefined ? overIndex : activeIndex;
}

function pointerLocation(event: DragMoveEvent): Readonly<{ x: number; y: number }> | null {
  return pointerEventLocation(event.activatorEvent, event.delta);
}

function pointerActivationLocation(event: Event): Readonly<{ x: number; y: number }> | null {
  return pointerEventLocation(event, { x: 0, y: 0 });
}

function pointerEventLocation(
  event: Event,
  delta: Readonly<{ x: number; y: number }>,
): Readonly<{ x: number; y: number }> | null {
  if (!(event instanceof PointerEvent)) {
    return null;
  }
  return {
    x: event.clientX + delta.x,
    y: event.clientY + delta.y,
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
