import { createContext, useContext, type ComponentProps } from "react";
import remarkBreaks from "remark-breaks";
import remarkGfm from "remark-gfm";
import { Streamdown, type Components, type CustomRendererProps, type ExtraProps } from "streamdown";

import { Checkbox } from "./radix/checkbox";
import { syntaxHighlightingLanguageHints } from "./syntaxHighlighting";
import { SyntaxHighlightedCode } from "./SyntaxHighlightedCode";
import { projectMarkdownText } from "./taskBodyMarkdownText";
import "streamdown/styles.css";
import "./MarkdownText.css";
import "./StreamdownMarkdown.css";

// @streamdown/code@1.1.1 has a lossy unbounded token-result cache; Chat can
// re-evaluate a newer official highlighter when it adopts this seam.
export type StaticMarkdownProps = Readonly<{
  disabled?: boolean;
  onTaskListChange?: (value: string) => void;
  taskListItemToggleLabel?: (checked: boolean) => string;
  value: string;
}>;
export type StreamingMarkdownProps = Readonly<{ value: string }>;
export type TaskBodyMarkdownProps = Readonly<{ value: string }>;

type MarkdownTaskListItemContextValue = Readonly<{
  onChange: StaticMarkdownProps["onTaskListChange"];
  sourceOffset: number | undefined;
  taskListItemToggleLabel: StaticMarkdownProps["taskListItemToggleLabel"];
  value: string;
  disabled: boolean;
}>;

const MarkdownTaskListItemContext = createContext<MarkdownTaskListItemContextValue | null>(null);
const languageHints = syntaxHighlightingLanguageHints();
const richComponents = {
  input: MarkdownTaskListCheckbox,
  li: MarkdownTaskListItem,
  p: "div",
} satisfies Pick<Components, "input" | "li" | "p">;

export function StaticMarkdown({
  disabled = false,
  onTaskListChange,
  taskListItemToggleLabel,
  value,
}: StaticMarkdownProps) {
  return (
    <MarkdownCore
      animated={false}
      disabled={disabled}
      onChange={onTaskListChange}
      taskListItemToggleLabel={taskListItemToggleLabel}
      value={value}
    />
  );
}

export function StreamingMarkdown({ value }: StreamingMarkdownProps) {
  return <MarkdownCore animated value={value} />;
}

export function TaskBodyMarkdown({ value }: TaskBodyMarkdownProps) {
  return <span className="markdown-plain-text">{projectMarkdownText(value)}</span>;
}

function MarkdownCore({
  animated,
  disabled = false,
  onChange,
  taskListItemToggleLabel,
  value,
}: Readonly<{
  animated: boolean;
  disabled?: boolean;
  onChange?: StaticMarkdownProps["onTaskListChange"];
  taskListItemToggleLabel?: StaticMarkdownProps["taskListItemToggleLabel"];
  value: string;
}>) {
  const interaction = { disabled, onChange, taskListItemToggleLabel, value };
  return (
    <MarkdownTaskListItemContext.Provider value={{ ...interaction, sourceOffset: undefined }}>
      <Streamdown
        animated={
          animated ? { animation: "blurIn", duration: 50, easing: "ease", sep: "char", stagger: 13 } : false
        }
        className={`markdown-text${animated ? " markdown-text-streaming" : ""}`}
        components={richComponents}
        controls={false}
        isAnimating={animated}
        mode={animated ? "streaming" : "static"}
        plugins={{ renderers: [{ component: MarkdownHighlightedCode, language: languageHints }] }}
        remarkPlugins={[[remarkGfm, {}], remarkBreaks]}
        {...(onChange === undefined ? {} : { parseMarkdownIntoBlocksFn: singleMarkdownBlock })}
      >
        {value}
      </Streamdown>
    </MarkdownTaskListItemContext.Provider>
  );
}

function MarkdownTaskListItem({ children, node, ...props }: ComponentProps<"li"> & ExtraProps) {
  const taskListItem = useContext(MarkdownTaskListItemContext);
  const className = node?.properties.className;
  const isTaskItem = className?.some((value) => value === "task-list-item") === true;
  if (!isTaskItem) return <li {...props}>{children}</li>;
  if (taskListItem === null) return <li {...props}>{children}</li>;
  return (
    <MarkdownTaskListItemContext.Provider
      value={{ ...taskListItem, sourceOffset: node?.position?.start.offset }}
    >
      <li {...props}>{children}</li>
    </MarkdownTaskListItemContext.Provider>
  );
}

function MarkdownTaskListCheckbox({ checked, type }: ComponentProps<"input"> & ExtraProps) {
  const checkedValue = checked === true;
  const taskListItem = useContext(MarkdownTaskListItemContext);
  if (type !== "checkbox") return null;
  const sourceOffset = taskListItem?.sourceOffset;
  const editable = taskListItem?.onChange !== undefined && sourceOffset !== undefined;
  const checkedFromSource = editable ? taskListChecked(taskListItem.value, sourceOffset) : checkedValue;
  return (
    <Checkbox
      checked={checkedFromSource}
      className="markdown-task-list-checkbox"
      disabled={taskListItem?.disabled !== false || !editable}
      onCheckedChange={(nextChecked) => {
        if (nextChecked === "indeterminate" || !editable) return;
        taskListItem.onChange(toggleTaskListMarker(taskListItem.value, sourceOffset, nextChecked));
      }}
      {...(taskListItem?.taskListItemToggleLabel === undefined
        ? {}
        : { "aria-label": taskListItem.taskListItemToggleLabel(checkedFromSource) })}
    />
  );
}

function taskListChecked(value: string, sourceOffset: number): boolean {
  const markerOffset = taskListMarkerOffset(value, sourceOffset);
  return value[markerOffset] === "x" || value[markerOffset] === "X";
}

function MarkdownHighlightedCode({ code, language, isIncomplete }: CustomRendererProps) {
  return <SyntaxHighlightedCode code={code} languageHint={isIncomplete ? undefined : language} />;
}

function singleMarkdownBlock(markdown: string): string[] {
  return [markdown];
}

function toggleTaskListMarker(value: string, sourceOffset: number, checked: boolean): string {
  const markerOffset = taskListMarkerOffset(value, sourceOffset);
  return `${value.slice(0, markerOffset)}${checked ? "x" : " "}${value.slice(markerOffset + 1)}`;
}

function taskListMarkerOffset(value: string, sourceOffset: number): number {
  let cursor = listMarkerEndOffset(value, sourceOffset);
  if (!isHorizontalWhitespace(value[cursor])) {
    throw invalidTaskListMarker(value, sourceOffset);
  }
  while (isHorizontalWhitespace(value[cursor])) {
    cursor += 1;
  }
  return taskListStateOffset(value, sourceOffset, cursor);
}

function listMarkerEndOffset(value: string, sourceOffset: number): number {
  const first = value[sourceOffset];
  if (first === "-" || first === "+" || first === "*") {
    return sourceOffset + 1;
  }
  return orderedListMarkerEndOffset(value, sourceOffset);
}

function orderedListMarkerEndOffset(value: string, sourceOffset: number): number {
  let cursor = sourceOffset;
  const first = value[cursor];
  if (!isAsciiDigit(first)) {
    throw invalidTaskListMarker(value, sourceOffset);
  }
  let digitCount = 0;
  while (isAsciiDigit(value[cursor]) && digitCount < 9) {
    cursor += 1;
    digitCount += 1;
  }
  if (value[cursor] !== "." && value[cursor] !== ")") {
    throw invalidTaskListMarker(value, sourceOffset);
  }
  return cursor + 1;
}

function taskListStateOffset(value: string, sourceOffset: number, checkboxOffset: number): number {
  const stateOffset = checkboxOffset + 1;
  const state = value[stateOffset];
  if (
    value[checkboxOffset] !== "[" ||
    (state !== " " && state !== "x" && state !== "X") ||
    value[checkboxOffset + 2] !== "]"
  ) {
    throw invalidTaskListMarker(value, sourceOffset);
  }
  return stateOffset;
}

function isAsciiDigit(value: string | undefined): boolean {
  return value !== undefined && value >= "0" && value <= "9";
}

function isHorizontalWhitespace(value: string | undefined): boolean {
  return value === " " || value === "\t";
}

function invalidTaskListMarker(value: string, sourceOffset: number): Error {
  return new Error(
    `Markdown task-list invariant failed at source offset ${String(sourceOffset)} in ${String(value.length)} characters.`,
  );
}
