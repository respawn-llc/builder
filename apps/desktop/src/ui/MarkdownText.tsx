import { createContext, useContext } from "react";
import type { Components } from "react-markdown";
import ReactMarkdown from "react-markdown";
import rehypeSanitize from "rehype-sanitize";
import remarkGfm from "remark-gfm";

import { Checkbox } from "./radix/checkbox";
import { safeExternalUrl } from "./externalLinks";
import "./MarkdownText.css";

export type MarkdownTextProps = Readonly<{
  value: string;
  onChange?: (value: string) => void;
  onOpenLink?: (url: string) => void;
  taskListItemToggleLabel?: (checked: boolean) => string;
  inline?: boolean;
}>;

type MarkdownTaskListItemContextValue = Readonly<{
  onChange: MarkdownTextProps["onChange"];
  sourceOffset: number | undefined;
  taskListItemToggleLabel: MarkdownTextProps["taskListItemToggleLabel"];
  value: string;
}>;

const MarkdownTaskListItemContext = createContext<MarkdownTaskListItemContextValue | null>(null);

export function MarkdownText({
  value,
  onChange,
  onOpenLink,
  taskListItemToggleLabel,
  inline = false,
}: MarkdownTextProps) {
  const rendered = (
    <ReactMarkdown
      components={markdownComponents({
        inline,
        onChange,
        onOpenLink,
        taskListItemToggleLabel,
        value,
      })}
      rehypePlugins={[rehypeSanitize]}
      remarkPlugins={[remarkGfm]}
      skipHtml
    >
      {value}
    </ReactMarkdown>
  );
  if (inline) {
    return (
      <span className="markdown-text markdown-text-inline" data-testid="markdown-text-inline">
        {rendered}
      </span>
    );
  }
  return (
    <div className="markdown-text" data-testid="markdown-text">
      {rendered}
    </div>
  );
}

function markdownComponents({
  inline,
  onChange,
  onOpenLink,
  taskListItemToggleLabel,
  value,
}: Readonly<{
  inline: boolean;
  onChange: MarkdownTextProps["onChange"];
  onOpenLink: MarkdownTextProps["onOpenLink"];
  taskListItemToggleLabel: MarkdownTextProps["taskListItemToggleLabel"];
  value: string;
}>): Components {
  return {
    ...(inline
      ? {
          p({ children }) {
            return <span>{children}</span>;
          },
        }
      : {}),
    a({ children, href }) {
      const safeHref = safeExternalUrl(href);
      if (safeHref === undefined) {
        return <span>{children}</span>;
      }
      return (
        <a
          href={safeHref}
          onClick={(event) => {
            if (onOpenLink === undefined) {
              return;
            }
            event.preventDefault();
            event.stopPropagation();
            onOpenLink(safeHref);
          }}
          rel="noreferrer"
          target="_blank"
        >
          {children}
        </a>
      );
    },
    code({ children }) {
      return <code>{children}</code>;
    },
    pre({ children }) {
      return <pre>{children}</pre>;
    },
    li({ children, node, ...props }) {
      const className = node?.properties.className;
      const taskListItem =
        className === "task-list-item" ||
        (Array.isArray(className) && className.some((value) => value === "task-list-item"));
      if (!taskListItem) {
        return <li {...props}>{children}</li>;
      }
      return (
        <MarkdownTaskListItemContext.Provider
          value={{
            onChange,
            sourceOffset: node?.position?.start.offset,
            taskListItemToggleLabel,
            value,
          }}
        >
          <li {...props}>{children}</li>
        </MarkdownTaskListItemContext.Provider>
      );
    },
    input({ checked, type }) {
      if (type !== "checkbox") {
        return null;
      }
      return <MarkdownTaskListCheckbox checked={checked === true} />;
    },
  };
}

function MarkdownTaskListCheckbox({ checked }: Readonly<{ checked: boolean }>) {
  const taskListItem = useContext(MarkdownTaskListItemContext);
  const onChange = taskListItem?.onChange;
  const sourceOffset = taskListItem?.sourceOffset;
  const editable = onChange !== undefined && sourceOffset !== undefined;
  const ariaLabel = taskListItem?.taskListItemToggleLabel?.(checked);
  return (
    <Checkbox
      checked={checked}
      className="markdown-task-list-checkbox"
      disabled={!editable}
      onCheckedChange={(nextChecked) => {
        if (
          nextChecked === "indeterminate" ||
          onChange === undefined ||
          sourceOffset === undefined ||
          taskListItem === null
        ) {
          return;
        }
        onChange(toggleTaskListMarker(taskListItem.value, sourceOffset, nextChecked));
      }}
      {...(ariaLabel === undefined ? {} : { "aria-label": ariaLabel })}
    />
  );
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
