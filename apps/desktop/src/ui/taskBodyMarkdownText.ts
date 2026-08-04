import { unified } from "unified";
import remarkGfm from "remark-gfm";
import remarkParse from "remark-parse";
type HtmlState =
  | { kind: "comment" }
  | { kind: "cdata" }
  | { kind: "declaration" | "processing" | "tag"; name?: string; quote?: string }
  | { kind: "rawText"; name: string };
interface Projection {
  html: HtmlState | undefined;
  output: string[];
  sourceCursor: number;
}
const parser = unified().use(remarkParse).use(remarkGfm);
const blockTypes = new Set([
  "paragraph",
  "heading",
  "blockquote",
  "list",
  "listItem",
  "table",
  "tableRow",
  "code",
  "thematicBreak",
]);
const whitespace = new Set([" ", "\t", "\n", "\r"]);
type MarkdownNode = ReturnType<typeof parser.parse>["children"][number];
export function projectMarkdownText(source: string): string {
  const projection: Projection = { html: undefined, output: [], sourceCursor: 0 };
  walk(parser.parse(source), source, projection);
  return projection.output.join("").trim();
}
function walk(
  node: ReturnType<typeof parser.parse> | MarkdownNode,
  source: string,
  projection: Projection,
): void {
  syncCursor(node, source, projection);
  projectNode(node, source, projection);
  if (blockTypes.has(node.type)) space(projection);
}
function syncCursor(
  node: ReturnType<typeof parser.parse> | MarkdownNode,
  source: string,
  projection: Projection,
): void {
  const start = node.position?.start.offset;
  const end = node.position?.end.offset;
  if (start === undefined || end === undefined) return;
  if (projection.html !== undefined && start > projection.sourceCursor)
    feed(source.slice(projection.sourceCursor, start), projection, false);
  projection.sourceCursor = end;
}
function projectNode(
  node: ReturnType<typeof parser.parse> | MarkdownNode,
  source: string,
  projection: Projection,
): void {
  if (node.type === "text") {
    visitText(node.value, node.position, source, projection);
    return;
  }
  if (node.type === "html") {
    feed(node.value, projection, true);
    return;
  }
  if (node.type === "inlineCode" || node.type === "code") emit(node.value, projection);
  else if (node.type === "image" || node.type === "imageReference") emit(node.alt, projection);
  else if (node.type === "break") space(projection);
  else if (node.type === "definition") return;
  else if ("children" in node) for (const child of node.children) walk(child, source, projection);
}
function visitText(
  value: string,
  position: MarkdownNode["position"],
  source: string,
  projection: Projection,
): void {
  const start = position?.start.offset;
  const end = position?.end.offset;
  if (start === undefined || end === undefined) {
    emit(value, projection);
    return;
  }
  const original = source.slice(start, end);
  feed(original, projection, original === value);
  if (original !== value && projection.html === undefined) emit(value, projection);
}
function feed(value: string, projection: Projection, visible: boolean): void {
  for (let index = 0; index < value.length;) {
    const state = projection.html;
    if (state === undefined) {
      const html = htmlStart(value, index);
      if (html === undefined) {
        if (visible) emitCharacter(value.charAt(index), projection);
        index += 1;
        continue;
      }
      space(projection);
      projection.html = html.state;
      index = html.index;
      continue;
    }
    const next = htmlAdvance(value, index, state);
    projection.html = next.state;
    index = next.index;
  }
}
function htmlStart(value: string, start: number): { index: number; state: HtmlState } | undefined {
  if (value.charAt(start) !== "<") return;
  const special = specialHtmlStart(value, start);
  if (special !== undefined) return special;
  return ordinaryHtmlStart(value, start);
}
function specialHtmlStart(value: string, start: number): { index: number; state: HtmlState } | undefined {
  if (value.charAt(start + 1) !== "!") {
    return value.charAt(start + 1) === "?" ? { index: start + 2, state: { kind: "processing" } } : undefined;
  }
  if (value.charAt(start + 2) === "-" && value.charAt(start + 3) === "-")
    return { index: start + 4, state: { kind: "comment" } };
  if (cdataStart(value, start)) return { index: start + 9, state: { kind: "cdata" } };
  return { index: start + 2, state: { kind: "declaration" } };
}
function cdataStart(value: string, start: number): boolean {
  return (
    value.charAt(start + 2) === "[" &&
    value.charAt(start + 3) === "C" &&
    value.charAt(start + 4) === "D" &&
    value.charAt(start + 5) === "A" &&
    value.charAt(start + 6) === "T" &&
    value.charAt(start + 7) === "A" &&
    value.charAt(start + 8) === "["
  );
}
function ordinaryHtmlStart(value: string, start: number): { index: number; state: HtmlState } | undefined {
  const closing = value.charAt(start + 1) === "/";
  let index = start + (closing ? 2 : 1);
  const nameStart = index;
  if (!isTagNameStartChar(value.charAt(index))) return;
  while (isNameChar(value.charAt(index))) index += 1;
  if (index === nameStart) return;
  return {
    index,
    state: closing ? { kind: "tag" } : { kind: "tag", name: value.slice(nameStart, index).toLowerCase() },
  };
}
type TagState = Extract<HtmlState, { kind: "declaration" | "processing" | "tag" }>;
function htmlAdvance(
  value: string,
  start: number,
  state: HtmlState,
): { index: number; state: HtmlState | undefined } {
  if (state.kind === "rawText") {
    const close = rawTextClose(value, start, state.name);
    return close === undefined ? { index: start + 1, state } : { index: close, state: undefined };
  }
  if (state.kind === "comment") return closeDelimited(value, start, "-", state);
  if (state.kind === "cdata") return closeDelimited(value, start, "]", state);
  return advanceTag(value, start, state);
}
function advanceTag(
  value: string,
  start: number,
  state: TagState,
): { index: number; state: HtmlState | undefined } {
  if (state.quote !== undefined)
    return value.charAt(start) === state.quote
      ? { index: start + 1, state: clearQuote(state) }
      : { index: start + 1, state };
  if (value.charAt(start) === "'" || value.charAt(start) === '"')
    return { index: start + 1, state: { ...state, quote: value.charAt(start) } };
  const terminator = tagTerminator(value, start, state);
  if (terminator === undefined) return { index: start + 1, state };
  const nextState: HtmlState | undefined =
    state.kind === "tag" && state.name !== undefined && hiddenRawText(state.name)
      ? { kind: "rawText", name: state.name }
      : undefined;
  return { index: start + terminator, state: nextState };
}
function tagTerminator(value: string, start: number, state: TagState): number | undefined {
  if (state.kind === "processing")
    return value.charAt(start) === "?" && value.charAt(start + 1) === ">" ? 2 : undefined;
  return value.charAt(start) === ">" ? 1 : undefined;
}
function clearQuote(state: TagState): TagState {
  if (state.kind === "tag")
    return state.name === undefined ? { kind: "tag" } : { kind: "tag", name: state.name };
  return { kind: state.kind };
}
function closeDelimited(
  value: string,
  start: number,
  character: "-" | "]",
  state: HtmlState,
): { index: number; state: HtmlState | undefined } {
  return value.charAt(start) === character &&
    value.charAt(start + 1) === character &&
    value.charAt(start + 2) === ">"
    ? { index: start + 3, state: undefined }
    : { index: start + 1, state };
}
function rawTextClose(value: string, start: number, name: string): number | undefined {
  if (value.charAt(start) !== "<" || value.charAt(start + 1) !== "/") return;
  let index = start + 2;
  for (let offset = 0; offset < name.length; offset += 1)
    if (value.charAt(index + offset).toLowerCase() !== name.charAt(offset)) return;
  index += name.length;
  while (
    value.charAt(index) === " " ||
    value.charAt(index) === "\t" ||
    value.charAt(index) === "\n" ||
    value.charAt(index) === "\r"
  )
    index += 1;
  return value.charAt(index) === ">" ? index + 1 : undefined;
}
function emit(value: string | null | undefined, projection: Projection): void {
  if (value !== null && value !== undefined)
    for (const character of value) emitCharacter(character, projection);
}
function emitCharacter(character: string, projection: Projection): void {
  if (whitespace.has(character)) {
    space(projection);
    return;
  }
  projection.output.push(character);
}
function space(projection: Projection): void {
  if (projection.output.length > 0 && projection.output.at(-1) !== " ") projection.output.push(" ");
}
function hiddenRawText(name: string): boolean {
  return name === "script" || name === "style" || name === "template";
}
function isNameChar(value: string): boolean {
  const code = value.charCodeAt(0);
  return (
    (code >= 48 && code <= 57) || (code >= 65 && code <= 90) || (code >= 97 && code <= 122) || value === "-"
  );
}
function isTagNameStartChar(value: string): boolean {
  const code = value.charCodeAt(0);
  return (code >= 65 && code <= 90) || (code >= 97 && code <= 122);
}
