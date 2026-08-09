import {
  useEffect,
  useId,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent,
  type PointerEvent,
  type ReactNode,
} from "react";

import { ChevronDown } from "lucide-react";

import { StaticMarkdown } from "./MarkdownText";
import { cx } from "./classes";
import { fieldIslandInputClassName } from "./fieldInputStyles";
import { useOpacityExit } from "./motion";
import {
  consumeTextFieldSubmitShortcut,
  type TextFieldSubmitShortcutPolicy,
} from "./textFieldSubmitShortcut";

export type MarkdownFieldTaskListInteraction = Readonly<{
  checkedLabel: string;
  uncheckedLabel: string;
}>;

export type MarkdownFieldSubmitIntent = Readonly<{
  available: boolean;
  onSubmitIntent: () => void;
  policy: TextFieldSubmitShortcutPolicy;
}>;

export type MarkdownFieldHeightClamp = Readonly<{
  maximumLines: number;
  minimumLines: number;
  viewportPercent: number;
}>;

type MarkdownFieldCommonProps = Readonly<{
  disabled: boolean;
  editorMinHeight: number;
  error?: string | undefined;
  label: string;
  onChange: (value: string) => void;
  onEdit: () => void;
  onEditingChange: (editing: boolean) => void;
  placeholder: string;
  submitIntent?: MarkdownFieldSubmitIntent | undefined;
  taskListInteraction?: MarkdownFieldTaskListInteraction | undefined;
  value: string;
  editing: boolean;
}>;

export type MarkdownFieldProps = MarkdownFieldCommonProps;

export type CollapsibleMarkdownFieldProps = MarkdownFieldCommonProps &
  Readonly<{
    collapsedHeightClamp: MarkdownFieldHeightClamp;
    expanded: boolean;
    expandLabel: string;
    onExpand: () => void;
  }>;

type MarkdownFieldReadPresentation =
  | Readonly<{ kind: "plain" }>
  | Readonly<{
      collapsedHeightClamp: MarkdownFieldHeightClamp;
      expanded: boolean;
      expandLabel: string;
      kind: "collapsible";
      onExpand: () => void;
    }>;

type MarkdownFieldCoreProps = MarkdownFieldCommonProps &
  Readonly<{
    readPresentation: MarkdownFieldReadPresentation;
  }>;

export function MarkdownField(props: MarkdownFieldProps) {
  return <MarkdownFieldCore {...props} readPresentation={{ kind: "plain" }} />;
}

export function CollapsibleMarkdownField({
  collapsedHeightClamp,
  expanded,
  expandLabel,
  onExpand,
  ...props
}: CollapsibleMarkdownFieldProps) {
  return (
    <MarkdownFieldCore
      {...props}
      readPresentation={{
        collapsedHeightClamp,
        expanded,
        expandLabel,
        kind: "collapsible",
        onExpand,
      }}
    />
  );
}

function MarkdownFieldCore({
  disabled,
  editorMinHeight,
  error,
  label,
  onChange,
  onEdit,
  onEditingChange,
  placeholder,
  readPresentation,
  submitIntent,
  taskListInteraction,
  value,
  editing,
}: MarkdownFieldCoreProps) {
  const fieldID = useId();
  const errorID = `${fieldID}-error`;
  const errorText = error === undefined || error.length === 0 ? undefined : error;
  const showEditor = editing && !disabled;

  return (
    <div className="grid h-full min-h-0 min-w-0 grid-rows-[minmax(0,1fr)_auto] gap-[var(--space-2)]">
      <div className="min-h-0 min-w-0">
        {showEditor ? (
          <MarkdownFieldEditor
            describedBy={errorText === undefined ? undefined : errorID}
            editorMinHeight={editorMinHeight}
            error={errorText !== undefined}
            fieldID={fieldID}
            label={label}
            onBlur={() => {
              onEditingChange(false);
            }}
            onChange={onChange}
            onKeyDown={(event) => {
              if (submitIntent === undefined) {
                return;
              }
              const matched = consumeTextFieldSubmitShortcut(event, submitIntent.policy);
              if (matched && !event.repeat && submitIntent.available) {
                submitIntent.onSubmitIntent();
              }
            }}
            placeholder={placeholder}
            value={value}
          />
        ) : (
          <MarkdownFieldReadViewport
            disabled={disabled}
            label={label}
            onChange={onChange}
            onEdit={onEdit}
            onExpand={readPresentation.kind === "collapsible" ? readPresentation.onExpand : undefined}
            placeholder={placeholder}
            readPresentation={readPresentation}
            taskListInteraction={taskListInteraction}
            value={value}
          />
        )}
      </div>
      {errorText === undefined ? null : (
        <span className="text-[var(--color-error)]" id={errorID}>
          {errorText}
        </span>
      )}
    </div>
  );
}

function MarkdownFieldEditor({
  describedBy,
  editorMinHeight,
  error,
  fieldID,
  label,
  onBlur,
  onChange,
  onKeyDown,
  placeholder,
  value,
}: Readonly<{
  describedBy?: string | undefined;
  editorMinHeight: number;
  error: boolean;
  fieldID: string;
  label: string;
  onBlur: () => void;
  onChange: (value: string) => void;
  onKeyDown: (event: KeyboardEvent<HTMLTextAreaElement>) => void;
  placeholder: string;
  value: string;
}>) {
  const editorRef = useRef<HTMLTextAreaElement | null>(null);
  const onBlurRef = useRef(onBlur);

  // Attach to the editor node so browser-native focus loss always returns the
  // controlled field to its rendered Markdown presentation.
  useEffect(() => {
    onBlurRef.current = onBlur;
  }, [onBlur]);

  useEffect(() => {
    const editor = editorRef.current;
    if (editor === null) {
      return;
    }
    const handleBlur = () => {
      onBlurRef.current();
    };
    editor.addEventListener("blur", handleBlur);
    return () => {
      editor.removeEventListener("blur", handleBlur);
    };
  }, []);

  return (
    <textarea
      aria-describedby={describedBy}
      aria-invalid={error ? true : undefined}
      aria-label={label}
      autoFocus
      className={cx(
        fieldIslandInputClassName(1),
        "block h-full min-h-0 min-w-0 resize-none p-[var(--space-2)] font-mono",
      )}
      id={fieldID}
      ref={editorRef}
      onChange={(event) => {
        onChange(event.target.value);
      }}
      onKeyDown={onKeyDown}
      placeholder={placeholder}
      style={{ minHeight: `${editorMinHeight.toString()}px` }}
      value={value}
    />
  );
}

function MarkdownFieldReadViewport({
  disabled,
  label,
  onChange,
  onEdit,
  onExpand,
  placeholder,
  readPresentation,
  taskListInteraction,
  value,
}: Readonly<{
  disabled: boolean;
  label: string;
  onChange: (value: string) => void;
  onEdit: () => void;
  onExpand: (() => void) | undefined;
  placeholder: string;
  readPresentation: MarkdownFieldReadPresentation;
  taskListInteraction: MarkdownFieldTaskListInteraction | undefined;
  value: string;
}>) {
  const viewportRef = useRef<HTMLDivElement | null>(null);
  const contentRef = useRef<HTMLDivElement | null>(null);
  const collapsible = readPresentation.kind === "collapsible";
  const collapsed = collapsible && !readPresentation.expanded;
  const overflows = useMarkdownFieldOverflow({
    contentRef,
    enabled: collapsed,
    viewportRef,
  });
  const affordancePhase = useOpacityExit(collapsible && !readPresentation.expanded && overflows);

  const surfaceStyle: CSSProperties = collapsed
    ? { maxHeight: heightClamp(readPresentation.collapsedHeightClamp) }
    : {};
  const taskListProps = markdownTaskListProps(disabled, taskListInteraction, onChange);
  const expandLabel = readPresentation.kind === "collapsible" ? readPresentation.expandLabel : undefined;

  return (
    <div className="relative h-full min-h-0 min-w-0">
      <div
        aria-label={label}
        aria-readonly
        className={cx(
          fieldIslandInputClassName(1),
          "block h-full min-h-0 min-w-0 overflow-visible p-[var(--space-2)]",
          !disabled && "cursor-text",
          collapsed && "overflow-hidden",
        )}
        onKeyDown={(event) => {
          activateFromKeyboard(event, disabled, onEdit);
        }}
        onPointerUp={(event) => {
          activateFromPointer(event, disabled, onEdit);
        }}
        ref={viewportRef}
        role="textbox"
        style={surfaceStyle}
        tabIndex={disabled ? -1 : 0}
      >
        <div ref={contentRef}>
          {value.trim().length > 0 ? (
            <StaticMarkdown disabled={disabled} {...(taskListProps ?? {})} value={value} />
          ) : (
            <span className="text-[var(--color-muted)]">{placeholder}</span>
          )}
        </div>
      </div>
      {renderMarkdownFieldAffordance(affordancePhase, expandLabel, onExpand)}
    </div>
  );
}

function useMarkdownFieldOverflow({
  contentRef,
  enabled,
  viewportRef,
}: Readonly<{
  contentRef: Readonly<{ current: HTMLDivElement | null }>;
  enabled: boolean;
  viewportRef: Readonly<{ current: HTMLDivElement | null }>;
}>): boolean {
  const [overflows, setOverflows] = useState(false);
  useEffect(() => {
    if (!enabled) {
      return;
    }
    const measureOverflow = () => {
      const viewport = viewportRef.current;
      if (viewport !== null) {
        setOverflows(viewport.scrollHeight > viewport.clientHeight);
      }
    };
    const frame = window.requestAnimationFrame(measureOverflow);
    window.addEventListener("resize", measureOverflow);
    if (typeof ResizeObserver === "undefined") {
      return () => {
        window.cancelAnimationFrame(frame);
        window.removeEventListener("resize", measureOverflow);
      };
    }
    const observer = new ResizeObserver(measureOverflow);
    if (viewportRef.current !== null) {
      observer.observe(viewportRef.current);
    }
    if (contentRef.current !== null) {
      observer.observe(contentRef.current);
    }
    return () => {
      observer.disconnect();
      window.cancelAnimationFrame(frame);
      window.removeEventListener("resize", measureOverflow);
    };
  }, [contentRef, enabled, viewportRef]);
  return enabled && overflows;
}

function activateFromPointer(
  event: PointerEvent<HTMLDivElement>,
  disabled: boolean,
  onEdit: () => void,
): void {
  if (disabled || !isPlainMarkdownActivation(event.target)) {
    return;
  }
  const selection = window.getSelection();
  if (selection !== null && !selection.isCollapsed) {
    return;
  }
  onEdit();
}

function activateFromKeyboard(
  event: KeyboardEvent<HTMLDivElement>,
  disabled: boolean,
  onEdit: () => void,
): void {
  if (disabled || event.target !== event.currentTarget) {
    return;
  }
  if (event.key !== "Enter" && event.key !== " ") {
    return;
  }
  event.preventDefault();
  onEdit();
}

function isPlainMarkdownActivation(target: EventTarget | null): boolean {
  return !(target instanceof Element && target.closest("a,button,input,[role='button']") !== null);
}

function markdownTaskListProps(
  disabled: boolean,
  interaction: MarkdownFieldTaskListInteraction | undefined,
  onChange: (value: string) => void,
): Readonly<{
  onTaskListChange: (value: string) => void;
  taskListItemToggleLabel: (checked: boolean) => string;
}> | undefined {
  if (disabled || interaction === undefined) {
    return undefined;
  }
  return {
    onTaskListChange: onChange,
    taskListItemToggleLabel: (checked) =>
      checked ? interaction.checkedLabel : interaction.uncheckedLabel,
  };
}

function renderMarkdownFieldAffordance(
  phase: ReturnType<typeof useOpacityExit>,
  expandLabel: string | undefined,
  onExpand: (() => void) | undefined,
): ReactNode {
  if (phase === "hidden" || onExpand === undefined || expandLabel === undefined) {
    return null;
  }
  return (
    <>
      <div
        aria-hidden="true"
        className={cx(
          "pointer-events-none absolute inset-x-[var(--space-2)] bottom-[var(--space-2)] h-12 bg-gradient-to-b from-transparent to-[var(--color-island-1)] transition-opacity motion-reduce:transition-none",
          phase === "visible" ? "opacity-100" : "opacity-0",
        )}
        data-state={phase}
      />
      <button
        aria-label={expandLabel}
        className={cx(
          "app-region-no-drag absolute inset-x-0 bottom-0 grid h-10 place-items-center text-[var(--color-on-island)] transition-opacity motion-reduce:transition-none",
          phase === "visible"
            ? "pointer-events-auto opacity-100"
            : "pointer-events-none opacity-0",
        )}
        data-state={phase}
        onClick={onExpand}
        type="button"
      >
        <ChevronDown aria-hidden="true" size={20} strokeWidth={1.5} />
      </button>
    </>
  );
}

function heightClamp(clamp: MarkdownFieldHeightClamp): string {
  return `clamp(${clamp.minimumLines.toString()}lh,${clamp.viewportPercent.toString()}dvh,${clamp.maximumLines.toString()}lh)`;
}
