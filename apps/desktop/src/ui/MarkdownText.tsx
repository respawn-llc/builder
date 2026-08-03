import { createContext, useContext, useEffect, useState, type ComponentProps, type CSSProperties } from "react";
import { bundledLanguages, bundledLanguagesInfo, getSingletonHighlighter, type BundledLanguage, type Highlighter, type ThemedTokenWithVariants } from "shiki";
import { Streamdown, type Components, type CustomRendererProps, type ExtraProps } from "streamdown";

import { Checkbox } from "./radix/checkbox";
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

type MarkdownTaskListItemContextValue = Readonly<{ onChange: StaticMarkdownProps["onTaskListChange"]; sourceOffset: number | undefined; taskListItemToggleLabel: StaticMarkdownProps["taskListItemToggleLabel"]; value: string; disabled: boolean }>;

const MarkdownTaskListItemContext = createContext<MarkdownTaskListItemContextValue | null>(null);
let highlighterPromise: Promise<Highlighter> | undefined;
const languageLookup = languageMetadata();
const languageKeys = [...languageLookup.keys()];
const richComponents = {
  input: MarkdownTaskListCheckbox,
  li: MarkdownTaskListItem,
  p: "div",
} satisfies Pick<Components, "input" | "li" | "p">;

export function StaticMarkdown({ disabled = false, onTaskListChange, taskListItemToggleLabel, value }: StaticMarkdownProps) {
  return <MarkdownCore animated={false} disabled={disabled} onChange={onTaskListChange} taskListItemToggleLabel={taskListItemToggleLabel} value={value} />;
}

export function StreamingMarkdown({ value }: StreamingMarkdownProps) { return <MarkdownCore animated value={value} />; }

export function TaskBodyMarkdown({ value }: TaskBodyMarkdownProps) {
  return <span className="markdown-plain-text">{projectMarkdownText(value)}</span>;
}

function MarkdownCore({ animated, disabled = false, onChange, taskListItemToggleLabel, value }: Readonly<{ animated: boolean; disabled?: boolean; onChange?: StaticMarkdownProps["onTaskListChange"]; taskListItemToggleLabel?: StaticMarkdownProps["taskListItemToggleLabel"]; value: string }>) {
  const interaction = { disabled, onChange, taskListItemToggleLabel, value };
  return (
    <MarkdownTaskListItemContext.Provider value={{ ...interaction, sourceOffset: undefined }}>
      <Streamdown
        animated={animated ? { animation: "blurIn", duration: 50, easing: "ease", sep: "char", stagger: 13 } : false}
        className={`markdown-text${animated ? " markdown-text-streaming" : ""}`}
        components={richComponents}
        controls={false}
        isAnimating={animated}
        mode="streaming"
        plugins={{ renderers: [{ component: HighlightedCode, language: languageKeys }] }}
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
  const isTaskItem = className === "task-list-item" || (Array.isArray(className) && className.includes("task-list-item"));
  if (!isTaskItem) return <li {...props}>{children}</li>;
  if (taskListItem === null) return <li {...props}>{children}</li>;
  return <MarkdownTaskListItemContext.Provider value={{ ...taskListItem, sourceOffset: node?.position?.start.offset }}><li {...props}>{children}</li></MarkdownTaskListItemContext.Provider>;
}

function MarkdownTaskListCheckbox({ checked, type }: ComponentProps<"input"> & ExtraProps) {
  const checkedValue = checked === true;
  const taskListItem = useContext(MarkdownTaskListItemContext);
  if (type !== "checkbox") return null;
  const sourceOffset = taskListItem?.sourceOffset;
  const editable = taskListItem?.onChange !== undefined && sourceOffset !== undefined;
  const checkedFromSource = editable ? taskListChecked(taskListItem.value, sourceOffset) : checkedValue;
  return <Checkbox checked={checkedFromSource} className="markdown-task-list-checkbox" disabled={taskListItem?.disabled !== false || !editable}
    onCheckedChange={(nextChecked) => { if (nextChecked === "indeterminate" || !editable) return; taskListItem.onChange(toggleTaskListMarker(taskListItem.value, sourceOffset, nextChecked)); }}
    {...(taskListItem?.taskListItemToggleLabel === undefined ? {} : { "aria-label": taskListItem.taskListItemToggleLabel(checkedValue) })} />;
}

function taskListChecked(value: string, sourceOffset: number): boolean {
  const markerOffset = taskListMarkerOffset(value, sourceOffset);
  return value[markerOffset] === "x" || value[markerOffset] === "X";
}

function HighlightedCode({ code, language, isIncomplete }: CustomRendererProps) {
  const canonicalLanguage = languageLookup.get(language);
  const [highlighted, setHighlighted] = useState<{ language: BundledLanguage; source: string; tokens: readonly (readonly ThemedTokenWithVariants[])[] } | null>(null);
  const [failure, setFailure] = useState<{ language: BundledLanguage; source: string; error: unknown } | null>(null);
  useEffect(() => {
    let active = true;
    if (isIncomplete || canonicalLanguage === undefined) return () => void (active = false);
    void highlightCode(code, canonicalLanguage).then((tokens) => { if (active) setHighlighted({ language: canonicalLanguage, source: code, tokens }); })
      .catch((error: unknown) => { if (active) setFailure({ language: canonicalLanguage, source: code, error }); });
    return () => void (active = false);
  }, [canonicalLanguage, code, isIncomplete]);
  const currentFailure = failure;
  if (!isIncomplete && currentFailure !== null && currentFailure.language === canonicalLanguage && currentFailure.source === code) throw currentFailure.error;
  const current = highlighted;
  const tokens = current !== null && current.language === canonicalLanguage && current.source === code ? current.tokens : null;
  if (tokens === null || isIncomplete) return <PlainCode code={code} />;
  return (
    <pre>
      <code>
        {tokens.map((line, lineIndex) => (
          <span key={lineIndex}>
            {line.map((token, tokenIndex) => <span key={tokenIndex} className="streamdown-code-token" style={tokenStyle(token)}>{token.content}</span>)}
            {lineIndex < tokens.length - 1 ? "\n" : null}
          </span>
        ))}
      </code>
    </pre>
  );
}

async function highlightCode(code: string, language: BundledLanguage): Promise<readonly (readonly ThemedTokenWithVariants[])[]> {
  const highlighter = await getHighlighter();
  await highlighter.loadLanguage(language);
  return highlighter.codeToTokensWithThemes(code, { lang: language, themes: { dark: "github-dark", light: "github-light" } });
}

async function getHighlighter(): Promise<Highlighter> {
  highlighterPromise ??= getSingletonHighlighter({ langs: [], themes: ["github-light", "github-dark"] }).catch((error: unknown) => { highlighterPromise = undefined; throw error; });
  return highlighterPromise;
}

function PlainCode({ code }: Readonly<{ code: string }>) {
  return (
    <pre>
      <code>{code}</code>
    </pre>
  );
}

interface TokenStyle extends CSSProperties {
  "--shiki-light"?: string;
  "--shiki-dark"?: string;
}
function tokenStyle(token: ThemedTokenWithVariants): TokenStyle {
  const style: TokenStyle = {};
  if (token.variants.light?.color !== undefined) style["--shiki-light"] = token.variants.light.color;
  if (token.variants.dark?.color !== undefined) style["--shiki-dark"] = token.variants.dark.color;
  return style;
}

function languageMetadata(): Map<string, BundledLanguage> {
  const result = new Map<string, BundledLanguage>();
  for (const info of bundledLanguagesInfo) {
    if (!isBundledLanguage(info.id)) continue;
    addLanguageForms(result, info.id, info.id);
    addLanguageForms(result, info.name, info.id);
    for (const alias of info.aliases ?? []) addLanguageForms(result, alias, info.id);
  }
  return result;
}

function addLanguageForms(lookup: Map<string, BundledLanguage>, display: string, canonical: BundledLanguage): void {
  lookup.set(display, canonical);
  lookup.set(display.toLowerCase(), canonical);
  lookup.set(display.toUpperCase(), canonical);
}

function singleMarkdownBlock(markdown: string): string[] { return [markdown]; }

function isBundledLanguage(value: string): value is BundledLanguage { return value in bundledLanguages; }

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
