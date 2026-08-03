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
        setDragMode(null);
      }}
      onDragEnd={(event) => {
        handleDragEnd(event);
        if (dragMode === "keyboard") {
          setDragMode(null);
        }
      }}
      onDragStart={({ activatorEvent }) => {
        setDragMode(activatorEvent instanceof KeyboardEvent ? "keyboard" : "pointer");
      }}
      sensors={sensors}
    >
      <SortableContext items={itemIDs} strategy={verticalListSortingStrategy}>
        {items.map((item) => (
          <ReorderableListItem
            disabled={disabled}
            hideActiveItem={dragMode === "pointer"}
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
        getItemID={getItemID}
        items={items}
        reducedMotion={reducedMotion}
        renderItem={renderItem}
      />
    </DndContext>
  );
}

function ReorderableListDragOverlay<Item>({
  dragMode,
  getItemID,
  items,
  reducedMotion,
  renderItem,
}: Readonly<{
  dragMode: "keyboard" | "pointer" | null;
  getItemID: (item: Item) => UniqueIdentifier;
  items: readonly Item[];
  reducedMotion: boolean;
  renderItem: (item: Item, props: ReorderableListItemRenderProps) => ReactElement | null;
}>) {
  const { active } = useDndContext();
  if (dragMode !== "pointer") {
    return null;
  }
  const activeItem = active === null ? undefined : items.find((item) => getItemID(item) === active.id);
  return createPortal(
    <DragOverlay dropAnimation={reducedMotion ? null : undefined}>
      <div aria-hidden="true" className="pointer-events-none" inert>
        {activeItem === undefined
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
  item,
  itemID,
  itemNodes,
  reducedMotion,
  renderItem,
}: Readonly<{
  disabled: boolean;
  hideActiveItem: boolean;
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
    opacity: hideActiveItem && isDragging ? 0 : undefined,
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
