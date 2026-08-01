import {
  closestCenter,
  DndContext,
  DragOverlay,
  KeyboardSensor,
  PointerSensor,
  pointerWithin,
  useDndContext,
  useSensor,
  useSensors,
  type DragEndEvent,
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
  type RefObject,
  type ReactElement,
  type ReactNode,
  type RefAttributes,
} from "react";
import { createPortal } from "react-dom";
import { createEdgeScrollDriver } from "./edgeScroll";
import { indexesByID, projectVerticalReorder } from "./reorderProjection";

export type VerticalReorderDragSource = "keyboard" | "pointer";

export type VerticalReorderDragSession = Readonly<{
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
  const rowNodes = useRef(new Map<UniqueIdentifier, HTMLElement>());
  const listRef = useRef<HTMLDivElement | null>(null);
  const pointerListenerCleanup = useRef<(() => void) | null>(null);
  const edgeScroll = useBoundedVerticalEdgeScroll();
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
  const stopPointerListener = useCallback(() => {
    pointerListenerCleanup.current?.();
    pointerListenerCleanup.current = null;
  }, []);
  const startPointerListener = useCallback(() => {
    stopPointerListener();
    const handlePointerMove = (event: PointerEvent) => {
      recordPointerPosition({ x: event.clientX, y: event.clientY });
    };
    window.addEventListener("pointermove", handlePointerMove, { passive: true });
    pointerListenerCleanup.current = () => {
      window.removeEventListener("pointermove", handlePointerMove);
    };
  }, [recordPointerPosition, stopPointerListener]);
  const clearSession = useCallback(() => {
    edgeScroll.stop();
    stopPointerListener();
    setSession(null);
  }, [edgeScroll, stopPointerListener]);

  const onDragStart = useCallback(
    (event: DragStartEvent) => {
      const source = event.activatorEvent instanceof KeyboardEvent ? "keyboard" : "pointer";
      setSession({
        source,
      });
      if (source === "pointer") {
        edgeScroll.start(listRef.current);
        startPointerListener();
        const pointer = pointerActivationLocation(event.activatorEvent);
        if (pointer !== null) {
          recordPointerPosition(pointer);
        }
      }
    },
    [edgeScroll, recordPointerPosition, startPointerListener],
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

  const onDragEnd = useCallback(
    (event: DragEndEvent) => {
      const activeID = event.active.id;
      const orderedIDs = projectVerticalReorder(items.map(getItemID), activeID, event.over?.id);
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

  return (
    <DndContext
      autoScroll={false}
      collisionDetection={verticalReorderCollisionDetection}
      onDragCancel={onDragCancel}
      onDragEnd={onDragEnd}
      onDragOver={onDragOver}
      onDragStart={onDragStart}
      sensors={sensors}
    >
      <VerticalReorderList
        getItemID={getItemID}
        items={items}
        listRef={listRef}
        onRowNodeChange={registerRow}
        renderActivator={renderActivator}
        renderItem={renderItem}
      />
      <VerticalReorderOverlay
        getItemID={getItemID}
        items={items}
        renderActivator={renderActivator}
        renderItem={renderItem}
      />
    </DndContext>
  );
}

function VerticalReorderOverlay<Item>({
  getItemID,
  items,
  renderActivator,
  renderItem,
}: Readonly<{
  getItemID: (item: Item) => UniqueIdentifier;
  items: readonly Item[];
  renderActivator: (item: Item) => VerticalReorderActivator;
  renderItem: (item: Item, row: VerticalReorderRow) => ReactNode;
}>) {
  const { active } = useDndContext();
  const activeItem =
    active === null ? undefined : items.find((item) => getItemID(item) === active.id);
  return createPortal(
    <DragOverlay dropAnimation={useReducedMotion() ? null : undefined}>
      {activeItem === undefined ? null : (
        <div aria-hidden="true" style={{ pointerEvents: "none" }}>
          {renderItem(activeItem, {
            activator: renderActivator(activeItem),
            isDragging: true,
            isOverlay: true,
          })}
        </div>
      )}
    </DragOverlay>,
    document.body,
  );
}

function VerticalReorderList<Item, ID extends UniqueIdentifier>({
  getItemID,
  items,
  listRef,
  onRowNodeChange,
  renderActivator,
  renderItem,
}: Readonly<{
  getItemID: (item: Item) => ID;
  items: readonly Item[];
  listRef: RefObject<HTMLDivElement | null>;
  onRowNodeChange: (id: ID, element: HTMLElement | null) => void;
  renderActivator: (item: Item) => VerticalReorderActivator;
  renderItem: (item: Item, row: VerticalReorderRow) => ReactNode;
}>) {
  return (
    <SortableContext items={items.map(getItemID)} strategy={verticalListSortingStrategy}>
      <div ref={listRef} style={{ display: "grid", gap: "var(--space-3)" }}>
        {items.map((item) => {
          const id = getItemID(item);
          return (
            <VerticalReorderItem
              id={id}
              item={item}
              key={id}
              onRowNodeChange={onRowNodeChange}
              renderActivator={renderActivator}
              renderItem={renderItem}
            />
          );
        })}
      </div>
    </SortableContext>
  );
}

function VerticalReorderItem<Item, ID extends UniqueIdentifier>({
  id,
  item,
  onRowNodeChange,
  renderActivator,
  renderItem,
}: Readonly<{
  id: ID;
  item: Item;
  onRowNodeChange: (id: ID, element: HTMLElement | null) => void;
  renderActivator: (item: Item) => VerticalReorderActivator;
  renderItem: (item: Item, row: VerticalReorderRow) => ReactNode;
}>) {
  const reducedMotion = useReducedMotion();
  const {
    attributes,
    isDragging,
    listeners,
    setActivatorNodeRef,
    setNodeRef,
    transform,
    transition,
  } = useSortable({ id });
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
    <div ref={setRowNodeRef} style={style}>
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

const verticalReorderCollisionDetection: CollisionDetection = ({
  active,
  collisionRect,
  pointerCoordinates,
  ...args
}) => {
  const pointerCollisions =
    pointerCoordinates === null
      ? []
      : pointerWithin({ active, collisionRect, pointerCoordinates, ...args });
  const collisions =
    pointerCollisions.length === 0
      ? closestCenter({ active, collisionRect, pointerCoordinates, ...args })
      : pointerCollisions;
  return activationCollisions(active, collisionRect, collisions);
};

function activationCollisions(
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

function pointerActivationLocation(event: Event): Readonly<{ x: number; y: number }> | null {
  if (!(event instanceof PointerEvent)) {
    return null;
  }
  return { x: event.clientX, y: event.clientY };
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
