import {
  closestCenter,
  DragOverlay,
  DndContext,
  KeyboardSensor,
  PointerSensor,
  pointerWithin,
  useDndContext,
  useSensor,
  useSensors,
  type ClientRect,
  type Collision,
  type CollisionDetection,
  type DraggableAttributes,
  type DraggableSyntheticListeners,
  type DragEndEvent,
  type KeyboardCoordinateGetter,
  type UniqueIdentifier,
} from "@dnd-kit/core";
import {
  SortableContext,
  arrayMove,
  sortableKeyboardCoordinates,
  useSortable,
  type SortingStrategy,
} from "@dnd-kit/sortable";
import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type CSSProperties,
  type ReactElement,
  type RefObject,
} from "react";
import { createPortal } from "react-dom";

export type ReorderableListItemRenderProps = Readonly<{
  activatorAttributes: Partial<DraggableAttributes>;
  activatorListeners: DraggableSyntheticListeners;
  activatorRef: (element: HTMLElement | null) => void;
  itemRef: (element: HTMLElement | null) => void;
  style: CSSProperties;
}>;

export type ReorderableListCommit<Item, ID extends UniqueIdentifier = UniqueIdentifier> = Readonly<{
  activeID: ID;
  destinationID: ID;
  items: readonly Item[];
}>;

export type ReorderableListProps<Item, ID extends UniqueIdentifier = UniqueIdentifier> = Readonly<{
  disabled?: boolean;
  getItemID: (item: Item) => ID;
  items: readonly Item[];
  onCommit: (commit: ReorderableListCommit<Item, ID>) => void;
  renderItem: (item: Item, props: ReorderableListItemRenderProps) => ReactElement | null;
}>;

export function ReorderableList<Item, ID extends UniqueIdentifier = UniqueIdentifier>({
  disabled = false,
  getItemID,
  items,
  onCommit,
  renderItem,
}: ReorderableListProps<Item, ID>) {
  const itemIDs = items.map(getItemID);
  const itemNodes = useRef(new Map<ID, HTMLElement>());
  const reducedMotion = useReducedMotion();
  const [dragMode, setDragMode] = useState<"keyboard" | "pointer" | null>(null);
  const [overlayItem, setOverlayItem] = useState<Item | null>(null);
  const clearOverlayItem = useCallback(() => {
    setOverlayItem(null);
  }, []);
  const keyboardCoordinates = useCallback<KeyboardCoordinateGetter>(
    (event, args) => {
      const coordinates = sortableKeyboardCoordinates(event, args);
      if (coordinates !== undefined) {
        const activeIndex = itemIDs.findIndex((id) => id === args.active);
        const destinationIndex =
          event.code === "ArrowDown"
            ? activeIndex + 1
            : event.code === "ArrowUp"
              ? activeIndex - 1
              : activeIndex;
        const destinationID = itemIDs[destinationIndex];
        if (destinationID !== undefined) {
          itemNodes.current.get(destinationID)?.scrollIntoView({ block: "nearest" });
        }
      }
      return coordinates;
    },
    [itemIDs],
  );
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
    useSensor(KeyboardSensor, { coordinateGetter: keyboardCoordinates }),
  );
  const handleDragEnd = useCallback(
    (event: DragEndEvent) => {
      const overID = event.over?.id;
      if (overID === undefined || event.active.id === overID) {
        return;
      }
      const activeIndex = itemIDs.findIndex((id) => id === event.active.id);
      const overIndex = itemIDs.findIndex((id) => id === overID);
      if (activeIndex < 0 || overIndex < 0) {
        return;
      }
      const activeID = itemIDs[activeIndex];
      const destinationID = itemIDs[overIndex];
      if (activeID === undefined || destinationID === undefined) {
        return;
      }
      const nextItems = arrayMove([...items], activeIndex, overIndex);
      if (nextItems.every((item, index) => getItemID(item) === itemIDs[index])) {
        return;
      }
      onCommit({
        activeID,
        destinationID,
        items: nextItems,
      });
    },
    [getItemID, itemIDs, items, onCommit],
  );
  return (
    <DndContext
      autoScroll
      collisionDetection={verticalReorderCollisionDetection}
      onDragCancel={() => {
        setDragMode(null);
        setOverlayItem(null);
      }}
      onDragEnd={(event) => {
        handleDragEnd(event);
        setDragMode(null);
      }}
      onDragStart={({ active, activatorEvent }) => {
        if (activatorEvent instanceof KeyboardEvent) {
          setDragMode("keyboard");
          setOverlayItem(null);
          return;
        }
        setDragMode("pointer");
        const activeItem = items.find((item) => getItemID(item) === active.id);
        setOverlayItem(activeItem ?? null);
      }}
      sensors={sensors}
    >
      <ReorderableListItems
        dragMode={dragMode}
        disabled={disabled}
        getItemID={getItemID}
        itemNodes={itemNodes}
        items={items}
        reducedMotion={reducedMotion}
        renderItem={renderItem}
      />
      {dragMode === "pointer" || overlayItem !== null ? (
        <ReorderableListDragOverlay
          dragMode={dragMode}
          item={overlayItem}
          onDropAnimationEnd={clearOverlayItem}
          reducedMotion={reducedMotion}
          renderItem={renderItem}
        />
      ) : null}
    </DndContext>
  );
}

function ReorderableListDragOverlay<Item>({
  dragMode,
  item,
  onDropAnimationEnd,
  reducedMotion,
  renderItem,
}: Readonly<{
  dragMode: "keyboard" | "pointer" | null;
  item: Item | null;
  onDropAnimationEnd(): void;
  reducedMotion: boolean;
  renderItem: (item: Item, props: ReorderableListItemRenderProps) => ReactElement | null;
}>) {
  const { active } = useDndContext();
  useEffect(() => {
    if (item === null || dragMode !== null || active !== null) {
      return undefined;
    }
    if (reducedMotion) {
      onDropAnimationEnd();
      return undefined;
    }
    const timeoutID = window.setTimeout(onDropAnimationEnd, DROP_ANIMATION_DURATION_MS);
    return () => {
      window.clearTimeout(timeoutID);
    };
  }, [active, dragMode, item, onDropAnimationEnd, reducedMotion]);
  return createPortal(
    <DragOverlay
      dropAnimation={
        reducedMotion
          ? null
          : {
              duration: DROP_ANIMATION_DURATION_MS,
            }
      }
    >
      <div aria-hidden="true" className="pointer-events-none" inert>
        {item === null
          ? null
          : renderItem(item, {
              activatorAttributes: {},
              activatorListeners: undefined,
              activatorRef: noopRef,
              itemRef: noopRef,
              style: {},
            })}
      </div>
    </DragOverlay>,
    document.body,
  );
}

function ReorderableListItems<Item, ID extends UniqueIdentifier>({
  dragMode,
  disabled,
  getItemID,
  itemNodes,
  items,
  reducedMotion,
  renderItem,
}: Readonly<{
  dragMode: "keyboard" | "pointer" | null;
  disabled: boolean;
  getItemID: (item: Item) => ID;
  itemNodes: RefObject<Map<ID, HTMLElement>>;
  items: readonly Item[];
  reducedMotion: boolean;
  renderItem: (item: Item, props: ReorderableListItemRenderProps) => ReactElement | null;
}>) {
  const { active, activeNodeRect, over } = useDndContext();
  const itemIDs = items.map(getItemID);
  const projection =
    active === null ? { insertionIndex: undefined } : projectVerticalReorder(itemIDs, active.id, over?.id);
  const activeRowHeight = activeNodeRect?.height;
  const canCollapseActive = projection.insertionIndex !== undefined && activeRowHeight !== undefined;
  return (
    <SortableContext items={itemIDs} strategy={verticalReorderProjectionStrategy}>
      {items.map((item, index) => {
        const itemID = getItemID(item);
        return (
          <ReorderableListItem
            activeRowHeight={activeRowHeight}
            collapseActive={canCollapseActive && active?.id === itemID}
            disabled={disabled}
            hideActiveItem={dragMode === "pointer" || canCollapseActive}
            insertionGap={projection.insertionIndex === index}
            isDragging={active?.id === itemID}
            item={item}
            itemID={itemID}
            itemNodes={itemNodes}
            key={itemID}
            reducedMotion={reducedMotion}
            renderItem={renderItem}
          />
        );
      })}
      {projection.insertionIndex === itemIDs.length ? (
        <VerticalReorderGap height={activeRowHeight} reducedMotion={reducedMotion} />
      ) : null}
    </SortableContext>
  );
}

function ReorderableListItem<Item, ID extends UniqueIdentifier>({
  activeRowHeight,
  collapseActive,
  disabled,
  hideActiveItem,
  item,
  itemID,
  insertionGap,
  itemNodes,
  isDragging,
  reducedMotion,
  renderItem,
}: Readonly<{
  activeRowHeight: number | undefined;
  collapseActive: boolean;
  disabled: boolean;
  hideActiveItem: boolean;
  item: Item;
  itemID: ID;
  insertionGap: boolean;
  itemNodes: RefObject<Map<ID, HTMLElement>>;
  isDragging: boolean | undefined;
  reducedMotion: boolean;
  renderItem: (item: Item, props: ReorderableListItemRenderProps) => ReactElement | null;
}>) {
  const { attributes, listeners, setActivatorNodeRef, setNodeRef, transform, transition } = useSortable({
    disabled,
    id: itemID,
  });
  const setTrackedNodeRef = useCallback(
    (element: HTMLElement | null) => {
      setNodeRef(element);
      if (element === null) {
        itemNodes.current.delete(itemID);
      } else {
        itemNodes.current.set(itemID, element);
      }
    },
    [itemID, itemNodes, setNodeRef],
  );
  const style: CSSProperties = {
    transform:
      transform === null
        ? undefined
        : `translate3d(${transform.x.toString()}px, ${transform.y.toString()}px, 0)`,
    transition: reducedMotion ? undefined : transition,
    ...(collapseActive
      ? { height: 0, opacity: 0, overflow: "hidden", pointerEvents: "none" }
      : isDragging && hideActiveItem
        ? { opacity: 0, pointerEvents: "none" }
        : {}),
  };
  return (
    <>
      {insertionGap ? <VerticalReorderGap height={activeRowHeight} reducedMotion={reducedMotion} /> : null}
      {renderItem(item, {
        activatorAttributes: attributes,
        activatorListeners: listeners,
        activatorRef: setActivatorNodeRef,
        itemRef: setTrackedNodeRef,
        style,
      })}
    </>
  );
}

function VerticalReorderGap({
  height,
  reducedMotion,
}: Readonly<{ height: number | undefined; reducedMotion: boolean }>) {
  if (height === undefined) {
    return null;
  }
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

const verticalReorderProjectionStrategy: SortingStrategy = () => null;

const DROP_ANIMATION_DURATION_MS = 250;

const verticalReorderCollisionDetection: CollisionDetection = ({
  active,
  collisionRect,
  pointerCoordinates,
  ...args
}) => {
  if (pointerCoordinates === null) {
    return closestCenter({ active, collisionRect, pointerCoordinates, ...args });
  }
  const pointerCollisions = pointerWithin({ active, collisionRect, pointerCoordinates, ...args });
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
  if (initialRect === null) {
    return collisions;
  }
  if (initialRect.top !== collisionRect.top || initialRect.left !== collisionRect.left) {
    return collisions;
  }
  const activeCollision = collisions.find(({ id }) => id === active.id);
  if (activeCollision === undefined) {
    return collisions;
  }
  return [activeCollision, ...collisions.filter(({ id }) => id !== active.id)];
}

function projectVerticalReorder(
  itemIDs: readonly UniqueIdentifier[],
  activeID: UniqueIdentifier,
  overID: UniqueIdentifier | undefined,
): Readonly<{ insertionIndex: number | undefined }> {
  if (overID === undefined || activeID === overID) {
    return { insertionIndex: undefined };
  }
  const activeIndex = itemIDs.indexOf(activeID);
  const overIndex = itemIDs.indexOf(overID);
  if (activeIndex < 0 || overIndex < 0) {
    return { insertionIndex: undefined };
  }
  return {
    insertionIndex: activeIndex < overIndex ? overIndex + 1 : overIndex,
  };
}

const noopRef = (): void => undefined;

function useReducedMotion(): boolean {
  const [reducedMotion, setReducedMotion] = useState(
    () => window.matchMedia("(prefers-reduced-motion: reduce)").matches,
  );
  useEffect(() => {
    const media = window.matchMedia("(prefers-reduced-motion: reduce)");
    const handleChange = () => {
      setReducedMotion(media.matches);
    };
    media.addEventListener("change", handleChange);
    return () => {
      media.removeEventListener("change", handleChange);
    };
  }, []);
  return reducedMotion;
}
