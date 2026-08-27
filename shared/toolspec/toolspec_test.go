package toolspec

import "testing"

func TestParseID(t *testing.T) {
	tests := []struct {
		in   string
		want ID
		ok   bool
	}{
		{in: "shell", want: ToolExecCommand, ok: true},
		{in: "bash", want: ToolExecCommand, ok: true},
		{in: "bash_command", want: ToolExecCommand, ok: true},
		{in: "shell_command", want: ToolExecCommand, ok: true},
		{in: "exec_command", want: ToolExecCommand, ok: true},
		{in: "write_stdin", want: ToolWriteStdin, ok: true},
		{in: "view_image", want: ToolViewImage, ok: true},
		{in: "read_image", want: ToolViewImage, ok: true},
		{in: "patch", want: ToolPatch, ok: true},
		{in: "edit", want: ToolEdit, ok: true},
		{in: "replace", want: ToolEdit, ok: true},
		{in: "write", want: ToolEdit, ok: true},
		{in: "ask_question", want: ToolAskQuestion, ok: true},
		{in: "trigger_handoff", want: ToolTriggerHandoff, ok: true},
		{in: "web_search", want: ToolWebSearch, ok: true},
		{in: "unknown", ok: false},
	}

	for _, tt := range tests {
		got, ok := ParseID(tt.in)
		if ok != tt.ok {
			t.Fatalf("ParseID(%q) ok=%t want %t", tt.in, ok, tt.ok)
		}
		if ok && got != tt.want {
			t.Fatalf("ParseID(%q)=%q want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseConfigIDAndConfigName(t *testing.T) {
	if got, ok := ParseConfigID("shell"); !ok || got != ToolExecCommand {
		t.Fatalf("ParseConfigID(shell) = %q, %t", got, ok)
	}
	if got, ok := ParseConfigID("bash"); ok {
		t.Fatalf("ParseConfigID(bash) unexpectedly resolved to %q", got)
	}
	if got, ok := ParseConfigID("edit"); !ok || got != ToolEdit {
		t.Fatalf("ParseConfigID(edit) = %q, %t", got, ok)
	}
	for _, alias := range []string{"replace", "write"} {
		if got, ok := ParseConfigID(alias); ok {
			t.Fatalf("ParseConfigID(%s) unexpectedly resolved to %q", alias, got)
		}
	}
	for _, alias := range []string{"run_command", "exec", "open_image", "ask", "edit_file", "handoff"} {
		if got, ok := ParseID(alias); ok {
			t.Fatalf("ParseID(%s) unexpectedly resolved to %q", alias, got)
		}
		if got, ok := ParseConfigID(alias); ok {
			t.Fatalf("ParseConfigID(%s) unexpectedly resolved to %q", alias, got)
		}
	}
	if got := ConfigName(ToolExecCommand); got != "shell" {
		t.Fatalf("ConfigName(exec_command) = %q, want shell", got)
	}
}

func TestResolveModelToolNameAcceptsApprovedAliasesAndGeneratedForms(t *testing.T) {
	tests := []struct {
		name string
		id   ID
	}{
		{"exec_command", ToolExecCommand},
		{"shell", ToolExecCommand},
		{"bash", ToolExecCommand},
		{"exec", ToolExecCommand},
		{"run_command", ToolExecCommand},
		{"run-command", ToolExecCommand},
		{"shell_command", ToolExecCommand},
		{"shell-command", ToolExecCommand},
		{"run_shell", ToolExecCommand},
		{"runShell", ToolExecCommand},
		{"bash_command", ToolExecCommand},
		{"bash-command", ToolExecCommand},
		{"execCommand", ToolExecCommand},
		{"EXEC-COMMAND", ToolExecCommand},
		{"write_stdin", ToolWriteStdin},
		{"writeStdin", ToolWriteStdin},
		{"WRITE-STDIN", ToolWriteStdin},
		{"view_image", ToolViewImage},
		{"read_image", ToolViewImage},
		{"read-image", ToolViewImage},
		{"openImage", ToolViewImage},
		{"inspect_image", ToolViewImage},
		{"vision", ToolViewImage},
		{"read_pdf", ToolViewImage},
		{"open-pdf", ToolViewImage},
		{"inspectPdf", ToolViewImage},
		{"patch", ToolPatch},
		{"apply_patch", ToolPatch},
		{"apply-patch", ToolPatch},
		{"edit", ToolEdit},
		{"ask_question", ToolAskQuestion},
		{"question", ToolAskQuestion},
		{"ask-user-question", ToolAskQuestion},
		{"requestUserInput", ToolAskQuestion},
		{"ask", ToolAskQuestion},
		{"ask_user", ToolAskQuestion},
		{"ask-human", ToolAskQuestion},
		{"help", ToolAskQuestion},
		{"say", ToolAskQuestion},
		{"trigger_handoff", ToolTriggerHandoff},
		{"handoff", ToolTriggerHandoff},
		{"trigger-handoff", ToolTriggerHandoff},
		{"compact", ToolTriggerHandoff},
		{"requestHandoff", ToolTriggerHandoff},
		{"edit_file", ToolEdit},
		{"edit-file", ToolEdit},
		{"strReplaceEditor", ToolEdit},
		{"replace", ToolEdit},
		{"string-replace", ToolEdit},
		{"replaceText", ToolEdit},
		{"write", ToolEdit},
		{"complete_node", ToolCompleteNode},
		{"web_search", ToolWebSearch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := ResolveModelToolName(test.name, nil)
			if !ok || got != test.id {
				t.Fatalf("ResolveModelToolName(%q) = %q, %t; want %q, true", test.name, got, ok, test.id)
			}
		})
	}
}

func TestResolveModelToolNameUsesMatchingStageOrder(t *testing.T) {
	tests := []struct {
		name  string
		stage modelToolNameMatchStage
	}{
		{"exec_command", modelToolNameMatchCanonical},
		{"run_command", modelToolNameMatchSemanticAlias},
		{"execCommand", modelToolNameMatchCanonicalCamelCase},
		{"runCommand", modelToolNameMatchSemanticAliasCamelCase},
		{"EXEC-COMMAND", modelToolNameMatchCaseInsensitive},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, stage, ok := resolveModelToolName(test.name, nil)
			if !ok || stage != test.stage {
				t.Fatalf("resolveModelToolName(%q) = stage %d, %t; want stage %d, true", test.name, stage, ok, test.stage)
			}
		})
	}
}

func TestResolveModelToolNameUsesPublishedToolWhenAliasCollides(t *testing.T) {
	if got, ok := ResolveModelToolName("edit", []ID{ToolPatch}); !ok || got != ToolPatch {
		t.Fatalf("patch-only edit resolution = %q, %t; want patch, true", got, ok)
	}
	if got, ok := ResolveModelToolName("edit", []ID{ToolEdit}); !ok || got != ToolEdit {
		t.Fatalf("edit-only edit resolution = %q, %t; want edit, true", got, ok)
	}
}

func TestResolveModelToolNameDoesNotExpandRetainedHostedOrWorkflowNames(t *testing.T) {
	for _, name := range []string{"COMPLETE_NODE", "completeNode", "complete-node", "WEB_SEARCH", "WEB-SEARCH", "webSearch"} {
		if got, ok := ResolveModelToolName(name, nil); ok {
			t.Fatalf("ResolveModelToolName(%q) = %q, true; retained names must remain exact", name, got)
		}
	}
}

func TestResolveModelToolNameDoesNotTrimWhitespace(t *testing.T) {
	for _, name := range []string{" exec_command ", " run_command ", " shell "} {
		if got, ok := ResolveModelToolName(name, nil); ok {
			t.Fatalf("ResolveModelToolName(%q) = %q, true; surrounding whitespace is not an alias", name, got)
		}
	}
}

func TestResolveModelParameterNameAcceptsApprovedAliasesAndGeneratedForms(t *testing.T) {
	tests := []struct {
		tool      ID
		name      string
		canonical string
	}{
		{ToolExecCommand, "command", "cmd"},
		{ToolExecCommand, "script", "cmd"},
		{ToolExecCommand, "cwd", "workdir"},
		{ToolExecCommand, "working_directory", "workdir"},
		{ToolExecCommand, "working_dir", "workdir"},
		{ToolExecCommand, "working-directory", "workdir"},
		{ToolExecCommand, "shellPath", "shell"},
		{ToolExecCommand, "interpreter", "shell"},
		{ToolExecCommand, "login_shell", "login"},
		{ToolExecCommand, "pty", "tty"},
		{ToolExecCommand, "use-tty", "tty"},
		{ToolExecCommand, "raw_output", "raw"},
		{ToolExecCommand, "rawOutput", "raw"},
		{ToolExecCommand, "yield_ms", "yield_time_ms"},
		{ToolExecCommand, "wait_ms", "yield_time_ms"},
		{ToolExecCommand, "yield-time_ms", "yield_time_ms"},
		{ToolExecCommand, "yield-time-ms", "yield_time_ms"},
		{ToolExecCommand, "yieldTimeMs", "yield_time_ms"},
		{ToolExecCommand, "max-tokens", "max_output_tokens"},
		{ToolExecCommand, "max_tokens", "max_output_tokens"},
		{ToolExecCommand, "output_token_limit", "max_output_tokens"},
		{ToolWriteStdin, "process_id", "session_id"},
		{ToolWriteStdin, "shellId", "session_id"},
		{ToolWriteStdin, "stdin", "chars"},
		{ToolWriteStdin, "input", "chars"},
		{ToolWriteStdin, "text", "chars"},
		{ToolWriteStdin, "yield_ms", "yield_time_ms"},
		{ToolWriteStdin, "yield-time_ms", "yield_time_ms"},
		{ToolWriteStdin, "wait_ms", "yield_time_ms"},
		{ToolWriteStdin, "max_tokens", "max_output_tokens"},
		{ToolWriteStdin, "output_token_limit", "max_output_tokens"},
		{ToolViewImage, "file_path", "path"},
		{ToolViewImage, "image_path", "path"},
		{ToolViewImage, "file", "path"},
		{ToolViewImage, "pdf-path", "path"},
		{ToolViewImage, "filename", "path"},
		{ToolViewImage, "raw_output", "raw"},
		{ToolViewImage, "unoptimized", "raw"},
		{ToolViewImage, "disableOptimization", "raw"},
		{ToolViewImage, "original_quality", "raw"},
		{ToolPatch, "diff", "patch"},
		{ToolPatch, "patch_text", "patch"},
		{ToolPatch, "patch-content", "patch"},
		{ToolPatch, "content", "patch"},
		{ToolPatch, "patch_content", "patch"},
		{ToolPatch, "input", "patch"},
		{ToolEdit, "filePath", "path"},
		{ToolEdit, "file", "path"},
		{ToolEdit, "oldText", "old_string"},
		{ToolEdit, "old_text", "old_string"},
		{ToolEdit, "find", "old_string"},
		{ToolEdit, "search", "old_string"},
		{ToolEdit, "new-text", "new_string"},
		{ToolEdit, "new_text", "new_string"},
		{ToolEdit, "replacement", "new_string"},
		{ToolEdit, "replace", "new_string"},
		{ToolEdit, "replaceAll", "replace_all"},
		{ToolEdit, "all", "replace_all"},
		{ToolEdit, "global", "replace_all"},
		{ToolAskQuestion, "prompt", "question"},
		{ToolAskQuestion, "message", "question"},
		{ToolAskQuestion, "text", "question"},
		{ToolAskQuestion, "choices", "suggestions"},
		{ToolAskQuestion, "options", "suggestions"},
		{ToolAskQuestion, "answers", "suggestions"},
		{ToolAskQuestion, "suggested-option-index", "recommended_option_index"},
		{ToolAskQuestion, "recommended_index", "recommended_option_index"},
		{ToolAskQuestion, "default_index", "recommended_option_index"},
		{ToolTriggerHandoff, "summaryPrompt", "summarizer_prompt"},
		{ToolTriggerHandoff, "handoff_prompt", "summarizer_prompt"},
		{ToolTriggerHandoff, "compaction_prompt", "summarizer_prompt"},
		{ToolTriggerHandoff, "handoff-message", "future_agent_message"},
		{ToolTriggerHandoff, "next_agent_message", "future_agent_message"},
		{ToolTriggerHandoff, "continuation_message", "future_agent_message"},
	}
	for _, test := range tests {
		t.Run(string(test.tool)+"/"+test.name, func(t *testing.T) {
			got, ok := ResolveModelParameterName(test.tool, test.name)
			if !ok || got != test.canonical {
				t.Fatalf("ResolveModelParameterName(%q, %q) = %q, %t; want %q, true", test.tool, test.name, got, ok, test.canonical)
			}
		})
	}
}

func TestResolveModelParameterNameCanonicalDerivedFormsTakePrecedence(t *testing.T) {
	tests := []struct {
		tool      ID
		name      string
		canonical string
	}{
		{ToolExecCommand, "CMD", "cmd"},
		{ToolExecCommand, "yield-time_ms", "yield_time_ms"},
		{ToolEdit, "NEW-STRING", "new_string"},
		{ToolAskQuestion, "recommendedOptionIndex", "recommended_option_index"},
	}
	for _, test := range tests {
		got, ok := ResolveModelParameterName(test.tool, test.name)
		if !ok || got != test.canonical {
			t.Fatalf("ResolveModelParameterName(%q, %q) = %q, %t; want %q, true", test.tool, test.name, got, ok, test.canonical)
		}
	}
	for _, name := range []string{" cmd ", " workdir "} {
		if got, ok := ResolveModelParameterName(ToolExecCommand, name); ok {
			t.Fatalf("ResolveModelParameterName(%q) = %q, true; whitespace is not an alias", name, got)
		}
	}
}

func TestAliasCatalogRejectsConflictingToolAndParameterAliases(t *testing.T) {
	t.Run("semantic alias camel case", func(t *testing.T) {
		catalog := newToolAliasCatalog([]toolAliasSpec{
			{id: ToolExecCommand, aliases: []string{"foo_bar"}, variations: true},
			{id: ToolEdit, aliases: []string{"fooBar"}, variations: true},
		})
		assertPanics(t, func() {
			validateToolAliasCatalog(catalog, []ID{ToolExecCommand, ToolEdit})
		})
	})
	t.Run("parameters", func(t *testing.T) {
		assertPanics(t, func() {
			newParameterAliasCatalog(ToolPatch, []parameterAliasSpec{
				{canonical: "patch", aliases: []string{"same"}},
				{canonical: "other", aliases: []string{"same"}},
			})
		})
	})
}

func assertPanics(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("function did not panic")
		}
	}()
	fn()
}
