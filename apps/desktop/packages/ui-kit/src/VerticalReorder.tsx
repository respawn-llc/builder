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
  activeRowHeight: number;
  overID: UniqueIdentifier | null;
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

  const clearSession = useCallback(() => {
    edgeScroll.stop();
    setSession(null);
  }, [edgeScroll]);

  const onDragStart = useCallback(
    (event: DragStartEvent) => {
      const activeID = event.active.id;
      const activeRowHeight = event.active.rect.current.initial?.height ?? 0;
      const source = event.activatorEvent instanceof KeyboardEvent ? "keyboard" : "pointer";
      setSession({
        activeID,
        activeRowHeight,
        overID: activeID,
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
      const overID = event.over?.id ?? null;
      setSession((current) => {
        if (current === null || current.overID === overID) {
          return current;
        }
        if (current.source === "keyboard" && overID !== null) {
          const destination = rowNodes.current.get(overID);
          if (destination !== undefined) {
            destination.scrollIntoView({ block: "nearest" });
          }
        }
        return { ...current, overID };
      });
    },
    [],
  );

  const onDragMove = useCallback(
    (event: DragMoveEvent) => {
      if (session?.source !== "pointer") {
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
      const overID = event.over?.id;
      const source = session?.source;
      const orderedIDs = reorderIDs(items.map(getItemID), activeID, overID);
      clearSession();
      if (orderedIDs === null || source === undefined) {
        return;
      }
      onCommit(orderedIDs);
    },
    [clearSession, getItemID, items, onCommit, session?.source],
  );

  const onDragCancel = useCallback(
    () => {
      clearSession();
    },
    [clearSession],
  );

  useEffect(() => clearSession, [clearSession]);

  const activeItem = session === null ? undefined : itemByID.get(session.activeID);
  const insertionIndex = session === null ? undefined : insertionGapIndex(items, getItemID, session);

  return (
    <DndContext
      autoScroll={false}
      collisionDetection={closestCenter}
      onDragCancel={onDragCancel}
      onDragEnd={onDragEnd}
      onDragMove={onDragMove}
      onDragOver={onDragOver}
      onDragStart={onDragStart}
      sensors={sensors}
    >
      <SortableContext items={items.map(getItemID)} strategy={verticalListSortingStrategy}>
        <div ref={listRef} style={{ display: "grid", gap: "var(--space-3)" }}>
          {items.map((item, index) => {
            const id = getItemID(item);
            return (
              <VerticalReorderItem
                id={id}
                isInsertionGap={insertionIndex === index}
                item={item}
                key={id}
                onRowNodeChange={registerRow}
                renderActivator={renderActivator}
                renderItem={renderItem}
                session={session}
              />
            );
          })}
          {insertionIndex === items.length ? (
            <VerticalReorderGap height={session?.activeRowHeight ?? 0} />
          ) : null}
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
  isInsertionGap,
  item,
  onRowNodeChange,
  renderActivator,
  renderItem,
  session,
}: Readonly<{
  id: ID;
  isInsertionGap: boolean;
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
    ? { height: 0, overflow: "hidden", pointerEvents: "none" }
    : {
        transform:
          transform === null
            ? undefined
            : `translate3d(${transform.x.toString()}px, ${transform.y.toString()}px, 0)`,
        transition: reducedMotion ? undefined : transition,
      };

  return (
    <>
      {isInsertionGap ? <VerticalReorderGap height={session?.activeRowHeight ?? 0} /> : null}
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
    </>
  );
}

function VerticalReorderGap({ height }: Readonly<{ height: number }>) {
  const reducedMotion = useReducedMotion();
  return (
    <div
      aria-hidden="true"
      style={{
        height,
        transition: reducedMotion ? undefined : "height var(--motion-fast) ease-out",
      }}
    />
  );
}

function insertionGapIndex<Item>(
  items: readonly Item[],
  getItemID: (item: Item) => UniqueIdentifier,
  session: VerticalReorderDragSession,
): number | undefined {
  if (session.overID === null || session.activeID === session.overID) {
    return undefined;
  }
  const activeIndex = items.findIndex((item) => getItemID(item) === session.activeID);
  const overIndex = items.findIndex((item) => getItemID(item) === session.overID);
  if (activeIndex < 0 || overIndex < 0) {
    return undefined;
  }
  return activeIndex < overIndex ? overIndex + 1 : overIndex;
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
  return document.scrollingElement instanceof HTMLElement ? document.scrollingElement : null;
}
