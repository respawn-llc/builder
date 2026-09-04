export type ChatToolIdentityKind =
  "ask-question" | "edit" | "patch" | "shell-command" | "shell-input" | "view-image" | "web-search";

export type ChatToolIdentity = Readonly<{
  kind: ChatToolIdentityKind;
  ownsPatchPresentation: boolean;
}>;

const chatToolIdentities = new Map<string, ChatToolIdentity>([
  ...aliases("shell-command", false, [
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
  ...aliases("shell-input", false, ["write_stdin", "write-stdin", "writeStdin"]),
  ...aliases("view-image", false, [
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
  ...aliases("patch", true, ["patch"]),
  ...aliases("patch", false, ["apply_patch", "apply-patch", "applyPatch"]),
  ...aliases("edit", true, ["edit", "replace", "write"]),
  ...aliases("edit", false, [
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
  ...aliases("web-search", false, [
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
  ...aliases("ask-question", false, [
    "ask_question",
    "ask-question",
    "askQuestion",
    "question",
    "ask_user_question",
    "ask-user-question",
    "askUserQuestion",
    "request_user_input",
    "request-user-input",
    "requestUserInput",
    "ask",
    "ask_user",
    "ask-user",
    "askUser",
    "ask_human",
    "ask-human",
    "askHuman",
    "help",
    "say",
  ]),
]);

function aliases(
  kind: ChatToolIdentityKind,
  ownsPatchPresentation: boolean,
  names: readonly string[],
): readonly (readonly [string, ChatToolIdentity])[] {
  return names.map((name) => [name, { kind, ownsPatchPresentation }] as const);
}

export function resolveChatToolIdentity(name: string): ChatToolIdentity | undefined {
  return chatToolIdentities.get(name);
}
