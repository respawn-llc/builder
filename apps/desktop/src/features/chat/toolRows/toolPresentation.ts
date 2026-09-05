import type { TranscriptDisclosureIconTone } from "@/ui";
import type {
  TranscriptCommittedToolRow,
  TranscriptLiveToolRow,
  TranscriptNonQuestionToolPresentation,
  TranscriptToolSlotItem,
} from "./toolSlotTypes";

type ToolMeta = TranscriptNonQuestionToolPresentation;
type PatchPresentation = NonNullable<ToolMeta["PatchPresentation"]>;
type ChatToolIdentityKind = "edit" | "patch" | "shell-command" | "shell-input" | "view-image" | "web-search";
type ChatToolIdentity = Readonly<{
  kind: ChatToolIdentityKind;
}>;
export type PatchChangesPresentation = Extract<PatchPresentation, Readonly<{ Variant: "changes" }>>;
export type PatchChangedFile = PatchChangesPresentation["Files"][number];
type SlotTool = TranscriptLiveToolRow | TranscriptCommittedToolRow["Tool"];
type CommittedTool = TranscriptCommittedToolRow["Tool"];
type ResolutionContext = Readonly<{
  committed: CommittedTool | undefined;
  input: string | undefined;
  item: TranscriptToolSlotItem;
  meta: ToolMeta | null | undefined;
  output: string | undefined;
  tool: SlotTool;
}>;

export type ToolPresentation =
  | Readonly<{
      kind: "generic";
      body: readonly ToolTextSection[];
      compact: string;
      copyPayload?: string | undefined;
      icon: "wrench";
      iconTone: TranscriptDisclosureIconTone;
      running: boolean;
      status?: string | undefined;
    }>
  | Readonly<{
      kind: "shell-command";
      command: string;
      commandLanguage?: string | undefined;
      compact: string;
      copyPayload?: string | undefined;
      exitCode?: number | undefined;
      icon: "terminal";
      iconTone: TranscriptDisclosureIconTone;
      output?: string | undefined;
      outputLanguage?: string | undefined;
      running: boolean;
      status?: string | undefined;
    }>
  | Readonly<{
      kind: "shell-input";
      body: readonly ToolTextSection[];
      compact: string;
      copyPayload?: string | undefined;
      icon: "terminal";
      iconTone: TranscriptDisclosureIconTone;
      running: boolean;
      status?: string | undefined;
    }>
  | Readonly<{
      kind: "view-image";
      body: readonly ToolTextSection[];
      compact: string;
      copyPayload?: string | undefined;
      icon: "wrench";
      iconTone: TranscriptDisclosureIconTone;
      running: boolean;
      status?: string | undefined;
    }>
  | Readonly<{
      kind: "web-search";
      compact: string;
      icon: "globe";
      iconTone: TranscriptDisclosureIconTone;
      running: boolean;
    }>
  | Readonly<{
      kind: "patch-changes";
      diagnostic?: string | undefined;
      files: readonly PatchChangedFile[];
      icon: "file-diff";
      iconTone: TranscriptDisclosureIconTone;
      running: boolean;
    }>
  | Readonly<{
      kind: "patch-invalid-input";
      compact: string;
      detail?: string | undefined;
      icon: "file-diff";
      iconTone: TranscriptDisclosureIconTone;
      running: boolean;
    }>;

export type ToolTextSection = Readonly<{
  content: string;
  id: "input" | "output";
}> &
  (Readonly<{ kind: "plain" }> | Readonly<{ kind: "source"; languageHint: string }>);

export type ToolPresentationStrings = Readonly<{
  backgrounded: string;
  editFailed: string;
  exitCode(code: number): string;
  moreLines(count: number): string;
  patchFailed: string;
  searchedWeb(query: string): string;
  viewedImage(path: string): string;
}>;

const chatToolIdentities = new Map<string, ChatToolIdentity>([
  ...aliases("shell-command", [
    "exec_command",
    "shell",
    "bash",
    "exec",
    "run_command",
    "run-command",
    "runCommand",
    "shell_command",
    "shell-command",
    "shellCommand",
    "run_shell",
    "run-shell",
    "runShell",
    "bash_command",
    "bash-command",
    "bashCommand",
    "exec-command",
    "execCommand",
  ]),
  ...aliases("shell-input", ["write_stdin", "write-stdin", "writeStdin"]),
  ...aliases("view-image", [
    "view_image",
    "view-image",
    "viewImage",
    "read_image",
    "read-image",
    "readImage",
    "open_image",
    "open-image",
    "openImage",
    "inspect_image",
    "inspect-image",
    "inspectImage",
    "vision",
    "read_pdf",
    "read-pdf",
    "readPdf",
    "open_pdf",
    "open-pdf",
    "openPdf",
    "inspect_pdf",
    "inspect-pdf",
    "inspectPdf",
  ]),
  ...aliases("patch", ["patch", "apply_patch", "apply-patch", "applyPatch"]),
  ...aliases("edit", [
    "edit",
    "replace",
    "write",
    "edit_file",
    "edit-file",
    "editFile",
    "str_replace_editor",
    "str-replace-editor",
    "strReplaceEditor",
    "string_replace",
    "string-replace",
    "stringReplace",
    "replace_text",
    "replace-text",
    "replaceText",
  ]),
  ...aliases("web-search", [
    "web_search",
    "web-search",
    "webSearch",
    "web_search_preview",
    "web-search-preview",
    "webSearchPreview",
    "web_search_call",
    "web-search-call",
    "webSearchCall",
    "search_web",
    "search-web",
    "searchWeb",
  ]),
]);

function aliases(
  kind: ChatToolIdentityKind,
  names: readonly string[],
): readonly (readonly [string, ChatToolIdentity])[] {
  return names.map((name) => [name, { kind }] as const);
}

function resolveChatToolIdentity(name: string): ChatToolIdentity | undefined {
  return chatToolIdentities.get(name);
}

export function resolveToolPresentation(
  item: TranscriptToolSlotItem,
  strings: ToolPresentationStrings,
): ToolPresentation {
  const context = resolutionContext(item);
  const identity = resolveChatToolIdentity(context.tool.ToolName);
  if (context.meta?.PatchPresentation != null) {
    return resolvePatch(context, strings, identity);
  }
  return resolveToolWithoutPatchPresentation(context, strings, identity);
}

function resolveToolWithoutPatchPresentation(
  context: ResolutionContext,
  strings: ToolPresentationStrings,
  identity: ChatToolIdentity | undefined,
): ToolPresentation {
  if (identity?.kind === "patch" || identity?.kind === "edit") {
    return resolveGeneric(context);
  }
  return resolveNonPatchTool(context, strings, identity?.kind);
}

function resolveNonPatchTool(
  context: ResolutionContext,
  strings: ToolPresentationStrings,
  kind: Exclude<ChatToolIdentityKind, "edit" | "patch"> | undefined,
): ToolPresentation {
  switch (kind) {
    case "web-search":
      return resolveWebSearch(context, strings);
    case "view-image":
      return resolveViewImage(context, strings) ?? resolveGeneric(context);
    case "shell-input":
      return resolveShellInput(context);
    case "shell-command":
      return resolveShellCommand(context, strings);
    case undefined:
      return usesShellPresentation(context.meta)
        ? resolveShellCommand(context, strings)
        : resolveGeneric(context);
  }
}

function resolvePatch(
  context: ResolutionContext,
  strings: ToolPresentationStrings,
  identity: ChatToolIdentity | undefined,
): Extract<
  ToolPresentation,
  Readonly<{ kind: "patch-changes" }> | Readonly<{ kind: "patch-invalid-input" }>
> {
  const presentation = context.meta?.PatchPresentation;
  if (presentation === null || presentation === undefined) {
    throw new Error("Patch/Edit row requires typed presentation");
  }
  switch (presentation.Variant) {
    case "changes":
      return {
        kind: "patch-changes",
        diagnostic: context.committed?.IsError === true ? context.output : undefined,
        files: presentation.Files,
        icon: "file-diff",
        iconTone: toolIconTone(context.item, context.meta),
        running: context.item.kind === "live",
      };
    case "invalid_input":
      return {
        kind: "patch-invalid-input",
        compact: identity?.kind === "edit" ? strings.editFailed : strings.patchFailed,
        detail: invalidPatchInputDetail(
          presentation.InvalidInput.InputDetail,
          context.committed?.IsError === true ? context.output : undefined,
        ),
        icon: "file-diff",
        iconTone: toolIconTone(context.item, context.meta),
        running: context.item.kind === "live",
      };
  }
}

function invalidPatchInputDetail(input: string, diagnostic: string | undefined): string | undefined {
  const inputSection = rawSection(input);
  if (inputSection === undefined) return diagnostic;
  if (diagnostic === undefined) return inputSection;
  return `${inputSection}\n\n${diagnostic}`;
}

function resolutionContext(item: TranscriptToolSlotItem): ResolutionContext {
  const tool = item.kind === "live" ? item.tool : item.row.Tool;
  const meta = tool.Presentation;
  const committed = item.kind === "committed" ? item.row.Tool : undefined;
  return {
    committed,
    input: meaningful(meta?.Command) ?? meaningful(meta?.CompactText),
    item,
    meta,
    output: rawSection(committed?.Text),
    tool,
  };
}

function resolveWebSearch(
  context: ResolutionContext,
  strings: ToolPresentationStrings,
): Extract<ToolPresentation, Readonly<{ kind: "web-search" }>> {
  const query = meaningful(context.meta?.Command);
  return {
    kind: "web-search",
    compact: query === undefined ? context.tool.ToolName : strings.searchedWeb(query),
    icon: "globe",
    iconTone: toolIconTone(context.item, context.meta),
    running: context.item.kind === "live",
  };
}

function resolveViewImage(
  context: ResolutionContext,
  strings: ToolPresentationStrings,
): Extract<ToolPresentation, Readonly<{ kind: "view-image" }>> | undefined {
  if (context.meta?.RenderHint?.Kind !== "plain") return undefined;
  const imagePath = meaningful(context.meta.RenderHint.Path);
  if (imagePath === undefined) return undefined;
  const imageInput = strings.viewedImage(imagePath);
  return {
    kind: "view-image",
    body: textSections(imageInput, context.output),
    compact: imageInput,
    copyPayload: buildToolCopyPayload("input-output", imageInput, context.committed?.Text),
    icon: "wrench",
    iconTone: toolIconTone(context.item, context.meta),
    running: context.item.kind === "live",
  };
}

function resolveShellInput(
  context: ResolutionContext,
): Extract<ToolPresentation, Readonly<{ kind: "shell-input" }>> {
  return {
    kind: "shell-input",
    body: textSections(context.input, context.output),
    compact: meaningful(context.meta?.CompactText) ?? context.input ?? context.tool.ToolName,
    copyPayload: buildToolCopyPayload("output-only", undefined, context.committed?.Text),
    icon: "terminal",
    iconTone: toolIconTone(context.item, context.meta),
    running: context.item.kind === "live",
    status: meaningful(context.meta?.InlineMeta),
  };
}

function resolveShellCommand(
  context: ResolutionContext,
  strings: ToolPresentationStrings,
): Extract<ToolPresentation, Readonly<{ kind: "shell-command" }>> {
  const shellCommand = meaningful(context.meta?.Command) ?? meaningful(context.meta?.CompactText);
  const shellExitCode = context.meta?.ShellExitCode ?? undefined;
  const shellFailed = shellExitCode !== undefined && shellExitCode !== 0;
  const command = shellCommand ?? context.tool.ToolName;
  return {
    kind: "shell-command",
    command,
    commandLanguage: shellCommandLanguage(context.meta),
    compact: firstAuthoredLine(command),
    copyPayload: buildToolCopyPayload("input-output", shellCommand, context.committed?.Text),
    exitCode: shellFailed ? shellExitCode : undefined,
    icon: "terminal",
    iconTone: toolIconTone(context.item, context.meta, shellFailed),
    output: context.output,
    outputLanguage: sourceResultLanguage(context.meta),
    running: context.item.kind === "live",
    status: shellStatus(command, context.meta, strings),
  };
}

function resolveGeneric(
  context: ResolutionContext,
): Extract<ToolPresentation, Readonly<{ kind: "generic" }>> {
  const compactInput =
    meaningful(context.meta?.CompactText) ?? context.input ?? meaningful(context.committed?.CondensedText);
  const compactResult = meaningful(context.committed?.ResultSummary);
  return {
    kind: "generic",
    body: genericTextSections(context.input, context.output, context.meta),
    compact: compactInput ?? compactResult ?? context.tool.ToolName,
    copyPayload: buildToolCopyPayload("input-output", context.input, context.committed?.Text),
    icon: "wrench",
    iconTone: toolIconTone(context.item, context.meta),
    running: context.item.kind === "live",
    status: genericStatus(context, compactInput, compactResult),
  };
}

function genericStatus(
  context: ResolutionContext,
  compactInput: string | undefined,
  compactResult: string | undefined,
): string | undefined {
  const inlineMeta = meaningful(context.meta?.InlineMeta);
  if (context.item.kind === "live" || compactInput === undefined) return inlineMeta;
  return compactResult ?? inlineMeta;
}

function usesShellPresentation(meta: ToolMeta | null | undefined): boolean {
  return meta?.Presentation === "shell" || meta?.RenderBehavior === "shell" || meta?.IsShell === true;
}

function shellStatus(
  command: string | undefined,
  meta: ToolMeta | null | undefined,
  strings: ToolPresentationStrings,
): string | undefined {
  const parts: string[] = [];
  const continuationCount = authoredLineBreakCount(command);
  if (continuationCount > 0) parts.push(strings.moreLines(continuationCount));
  if (meta?.MovedToBackground === true) parts.push(strings.backgrounded);
  const exitCode = meta?.ShellExitCode ?? undefined;
  if (exitCode !== undefined && exitCode !== 0) parts.push(strings.exitCode(exitCode));
  return parts.length === 0 ? undefined : parts.join(" · ");
}

function firstAuthoredLine(value: string): string {
  const end = firstLineBreakOffset(value);
  return end === undefined ? value : value.slice(0, end);
}

function authoredLineBreakCount(value: string | undefined): number {
  if (value === undefined) return 0;
  let count = 0;
  for (let index = 0; index < value.length; index += 1) {
    if (value[index] === "\n") {
      count += 1;
    } else if (value[index] === "\r") {
      count += 1;
      if (value[index + 1] === "\n") index += 1;
    }
  }
  return count;
}

function firstLineBreakOffset(value: string): number | undefined {
  for (let index = 0; index < value.length; index += 1) {
    if (value[index] === "\n" || value[index] === "\r") return index;
  }
  return undefined;
}

type CopyPolicy = "input-output" | "output-only";

function buildToolCopyPayload(
  policy: CopyPolicy,
  input: string | undefined,
  output: string | null | undefined,
): string | undefined {
  const outputSection = rawSection(output);
  if (policy === "output-only") return outputSection;
  const inputSection = rawSection(input);
  if (inputSection === undefined) return outputSection;
  if (outputSection === undefined) return inputSection;
  return `${inputSection}\n\n${outputSection}`;
}

function rawSection(value: string | null | undefined): string | undefined {
  return value === undefined || value === null || value.length === 0 ? undefined : value;
}

function shellCommandLanguage(meta: ToolMeta | null | undefined): string | undefined {
  if (meta?.RenderHint?.Kind !== "shell") return undefined;
  switch (meta.RenderHint.ShellDialect) {
    case "posix":
      return "bash";
    case "powershell":
      return "powershell";
    case "windows_command":
      return "batch";
    default:
      return undefined;
  }
}

function sourceResultLanguage(meta: ToolMeta | null | undefined): string | undefined {
  return meta?.RenderHint?.Kind === "source" ? meaningful(meta.RenderHint.Path) : undefined;
}

function toolIconTone(
  item: TranscriptToolSlotItem,
  meta: ToolMeta | null | undefined,
  shellFailed = false,
): TranscriptDisclosureIconTone {
  if (item.kind === "live") return "neutral";
  if (item.row.Tool.IsError || shellFailed) return "error";
  if (meta?.MovedToBackground === true) return "neutral";
  if (meta?.RawOutputRequested === true) return "warning";
  return "success";
}

function textSections(...values: readonly (string | undefined)[]): readonly ToolTextSection[] {
  const [input, output] = values;
  const sections: ToolTextSection[] = [];
  if (input !== undefined) sections.push({ content: input, id: "input", kind: "plain" });
  if (output !== undefined) sections.push({ content: output, id: "output", kind: "plain" });
  return sections;
}

function genericTextSections(
  input: string | undefined,
  output: string | undefined,
  meta: ToolMeta | null | undefined,
): readonly ToolTextSection[] {
  const sections = textSections(input);
  if (output === undefined) return sections;
  const path = sourceResultLanguage(meta);
  return [
    ...sections,
    path === undefined
      ? { content: output, id: "output", kind: "plain" }
      : { content: output, id: "output", kind: "source", languageHint: path },
  ];
}

function meaningful(value: string | null | undefined): string | undefined {
  return value === undefined || value === null || value.trim().length === 0 ? undefined : value;
}
