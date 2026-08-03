import {
  closestCenter,
  DragOverlay,
  DndContext,
  KeyboardSensor,
  PointerSensor,
  useDndContext,
  useSensor,
  useSensors,
  type DraggableAttributes,
  type DraggableSyntheticListeners,
  type DragEndEvent,
  type KeyboardCoordinateGetter,
  type DropAnimationFunction,
  type UniqueIdentifier,
} from "@dnd-kit/core";
import {
  SortableContext,
  arrayMove,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
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

type DropDestination = Readonly<{
  left: number;
  top: number;
}>;

interface DropAnimationToken {
  readonly marker: symbol;
}

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
  const [pointerActiveID, setPointerActiveID] = useState<ID | null>(null);
  const [overlayItem, setOverlayItem] = useState<Item | null>(null);
  const [dropDestination, setDropDestination] = useState<DropDestination | null>(null);
  const [dropAnimationToken, setDropAnimationToken] = useState<DropAnimationToken | null>(null);
  const dropAnimationTokenRef = useRef<DropAnimationToken | null>(null);
  const dropAnimationRef = useRef<Animation | null>(null);
  const clearDropState = useCallback((token?: DropAnimationToken) => {
    if (token !== undefined && token !== dropAnimationTokenRef.current) {
      return;
    }
    dropAnimationTokenRef.current = null;
    const currentAnimation = dropAnimationRef.current;
    dropAnimationRef.current = null;
    if (token === undefined) {
      currentAnimation?.cancel();
    }
    setDragMode(null);
    setPointerActiveID(null);
    setOverlayItem(null);
    setDropDestination(null);
    setDropAnimationToken(null);
  }, []);
  const recordDropAnimation = useCallback((token: DropAnimationToken, animation: Animation) => {
    if (token !== dropAnimationTokenRef.current) {
      animation.cancel();
      return;
    }
    dropAnimationRef.current = animation;
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
      collisionDetection={closestCenter}
      onDragCancel={() => {
        clearDropState();
      }}
      onDragEnd={(event) => {
        const activeIndex = itemIDs.findIndex((id) => id === event.active.id);
        const activeID = itemIDs[activeIndex];
        const activeNode = activeID === undefined ? undefined : itemNodes.current.get(activeID);
        const destination =
          event.over?.id === undefined || event.over.id === event.active.id || activeNode === undefined
            ? null
            : {
                left: activeNode.getBoundingClientRect().left,
                top: activeNode.getBoundingClientRect().top,
              };
        setDropDestination(destination);
        handleDragEnd(event);
        if (event.activatorEvent instanceof KeyboardEvent || destination === null) {
          clearDropState();
        } else {
          const token: DropAnimationToken = { marker: Symbol("drop-animation") };
          dropAnimationTokenRef.current = token;
          setDropAnimationToken(token);
        }
      }}
      onDragStart={({ active, activatorEvent }) => {
        clearDropState();
        if (activatorEvent instanceof KeyboardEvent) {
          setDragMode("keyboard");
          setPointerActiveID(null);
          setOverlayItem(null);
          setDropDestination(null);
          return;
        }
        setDragMode("pointer");
        setPointerActiveID(itemIDs.find((id) => id === active.id) ?? null);
        setOverlayItem(items.find((item) => getItemID(item) === active.id) ?? null);
        setDropDestination(null);
      }}
      sensors={sensors}
    >
      <SortableContext items={itemIDs} strategy={verticalListSortingStrategy}>
        {items.map((item) => (
          <ReorderableListItem
            disabled={disabled}
            hideActiveItem={dragMode === "pointer"}
            hiddenItemID={pointerActiveID}
            item={item}
            itemID={getItemID(item)}
            itemNodes={itemNodes}
            key={getItemID(item)}
            reducedMotion={reducedMotion}
            renderItem={renderItem}
          />
        ))}
      </SortableContext>
      <ReorderableListDragOverlay
        dragMode={dragMode}
        dropDestination={dropDestination}
        getItemID={getItemID}
        items={items}
        dropAnimationToken={dropAnimationToken}
        onDropAnimationEnd={clearDropState}
        onDropAnimationCreated={recordDropAnimation}
        overlayItem={overlayItem}
        reducedMotion={reducedMotion}
        renderItem={renderItem}
      />
    </DndContext>
  );
}

function ReorderableListDragOverlay<Item>({
  dragMode,
  dropDestination,
  dropAnimationToken,
  getItemID,
  items,
  onDropAnimationCreated,
  onDropAnimationEnd,
  overlayItem,
  reducedMotion,
  renderItem,
}: Readonly<{
  dragMode: "keyboard" | "pointer" | null;
  dropDestination: DropDestination | null;
  dropAnimationToken: DropAnimationToken | null;
  getItemID: (item: Item) => UniqueIdentifier;
  items: readonly Item[];
  onDropAnimationCreated: (token: DropAnimationToken, animation: Animation) => void;
  onDropAnimationEnd: (token: DropAnimationToken) => void;
  overlayItem: Item | null;
  reducedMotion: boolean;
  renderItem: (item: Item, props: ReorderableListItemRenderProps) => ReactElement | null;
}>) {
  const { active } = useDndContext();
  const activeItem = active === null ? undefined : items.find((item) => getItemID(item) === active.id);
  const dropAnimation = useCallback<DropAnimationFunction>(
    async ({ dragOverlay, transform }) => {
      if (dropDestination === null || dropAnimationToken === null || reducedMotion) {
        if (dropAnimationToken !== null) {
          onDropAnimationEnd(dropAnimationToken);
        }
        return;
      }
      const overlayRect = dragOverlay.node.getBoundingClientRect();
      const overlayBaseLeft = overlayRect.left - transform.x;
      const overlayBaseTop = overlayRect.top - transform.y;
      const animation = dragOverlay.node.animate(
        [
          { transform: formatTransform(transform) },
          {
            transform: formatTransform({
              x: dropDestination.left - overlayBaseLeft,
              y: dropDestination.top - overlayBaseTop,
              scaleX: 1,
              scaleY: 1,
            }),
          },
        ],
        {
          duration: DROP_ANIMATION_DURATION_MS,
          easing: "ease-out",
          fill: "forwards",
        },
      );
      onDropAnimationCreated(dropAnimationToken, animation);
      let settled = false;
      await new Promise<void>((resolve) => {
        const finish = () => {
          if (settled) {
            return;
          }
          settled = true;
          onDropAnimationEnd(dropAnimationToken);
          resolve();
        };
        animation.onfinish = finish;
        animation.oncancel = finish;
      });
    },
    [dropAnimationToken, dropDestination, onDropAnimationCreated, onDropAnimationEnd, reducedMotion],
  );
  if (dragMode !== "pointer" && overlayItem === null) {
    return null;
  }
  return createPortal(
    <DragOverlay dropAnimation={dropAnimationToken === null ? null : dropAnimation}>
      <div aria-hidden="true" className="pointer-events-none" inert>
        {activeItem == null
          ? null
          : renderItem(activeItem, {
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

function ReorderableListItem<Item, ID extends UniqueIdentifier>({
  disabled,
  hideActiveItem,
  hiddenItemID,
  item,
  itemID,
  itemNodes,
  reducedMotion,
  renderItem,
}: Readonly<{
  disabled: boolean;
  hideActiveItem: boolean;
  hiddenItemID: ID | null;
  item: Item;
  itemID: ID;
  itemNodes: RefObject<Map<ID, HTMLElement>>;
  reducedMotion: boolean;
  renderItem: (item: Item, props: ReorderableListItemRenderProps) => ReactElement | null;
}>) {
  const { attributes, isDragging, listeners, setActivatorNodeRef, setNodeRef, transform, transition } =
    useSortable({
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
    opacity: hideActiveItem && (isDragging || hiddenItemID === itemID) ? 0 : undefined,
    pointerEvents: hideActiveItem && (isDragging || hiddenItemID === itemID) ? "none" : undefined,
    transition: reducedMotion ? undefined : transition,
  };
  return renderItem(item, {
    activatorAttributes: attributes,
    activatorListeners: listeners,
    activatorRef: setActivatorNodeRef,
    itemRef: setTrackedNodeRef,
    style,
  });
}

const DROP_ANIMATION_DURATION_MS = 250;

const noopRef = (): void => undefined;

function formatTransform(transform: Readonly<{ x: number; y: number; scaleX: number; scaleY: number }>): string {
  return `translate3d(${transform.x.toString()}px, ${transform.y.toString()}px, 0) scale(${transform.scaleX.toString()}, ${transform.scaleY.toString()})`;
}

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
