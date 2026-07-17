package commands

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClientPromptRootsUseOnlyClientHomeDefaultKentDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	roots, err := NewClientPromptRoots()
	if err != nil {
		t.Fatalf("NewClientPromptRoots: %v", err)
	}
	if want := filepath.Join(home, configDirName); roots.GlobalRoot != want {
		t.Fatalf("GlobalRoot = %q, want %q", roots.GlobalRoot, want)
	}
	writeFilePrompt(t, filepath.Join(roots.GlobalRoot, promptsDirName, "global.md"), "global command")
	registry, err := NewDefaultRegistryWithClientPromptRoots(roots)
	if err != nil {
		t.Fatalf("NewDefaultRegistryWithClientPromptRoots: %v", err)
	}
	if _, ok := registry.Command("/prompt:global"); !ok {
		t.Fatal("expected client-home prompt command")
	}
}

func TestLoadFilePromptCommandsPrecedenceAndFallbacks(t *testing.T) {
	workspace := t.TempDir()
	globalRoot := t.TempDir()
	localRoot := filepath.Join(workspace, configDirName)

	for _, prompt := range []struct {
		path    string
		content string
	}{
		{filepath.Join(localRoot, promptsDirName, "demo.md"), "local-prompts"},
		{filepath.Join(localRoot, commandsDirName, "demo.md"), "local-commands"},
		{filepath.Join(globalRoot, promptsDirName, "demo.md"), "global-prompts"},
		{filepath.Join(globalRoot, commandsDirName, "demo.md"), "global-commands"},
		{filepath.Join(globalRoot, generatedDirName, promptsDirName, "demo.md"), "generated-prompts"},
		{filepath.Join(globalRoot, generatedDirName, commandsDirName, "demo.md"), "generated-commands"},
		{filepath.Join(localRoot, promptsDirName, "Bad!Name.md"), "local-normalized"},
		{filepath.Join(localRoot, commandsDirName, "Bad-Name.md"), "local-command-normalized"},
		{filepath.Join(globalRoot, promptsDirName, "Bad#Name.md"), "global-normalized"},
		{filepath.Join(localRoot, promptsDirName, "Blank Fallback.md"), " \n\t"},
		{filepath.Join(globalRoot, promptsDirName, "Blank_Fallback.md"), "valid-fallback"},
		{filepath.Join(globalRoot, commandsDirName, "generated.md"), "global-generated"},
		{filepath.Join(globalRoot, generatedDirName, promptsDirName, "generated.md"), "generated-duplicate"},
		{filepath.Join(globalRoot, generatedDirName, commandsDirName, "generated-only.md"), "generated-only"},
	} {
		writeFilePrompt(t, prompt.path, prompt.content)
	}

	loaded, err := loadFilePromptCommands(workspace, globalRoot)
	if err != nil {
		t.Fatalf("load file prompts: %v", err)
	}
	got := make(map[string]string, len(loaded))
	for _, command := range loaded {
		got[command.Name] = command.Content
	}
	want := map[string]string{
		"prompt:demo":           "local-prompts",
		"prompt:badname":        "local-normalized",
		"prompt:blank_fallback": "valid-fallback",
		"prompt:generated":      "global-generated",
		"prompt:generatedonly":  "generated-only",
	}
	if len(got) != len(want) {
		t.Fatalf("loaded commands = %+v, want %+v", got, want)
	}
	for name, content := range want {
		if got[name] != content {
			t.Fatalf("command %q content = %q, want %q", name, got[name], content)
		}
	}
}

func TestLoadFilePromptCommandsFiltersUnsupportedEntries(t *testing.T) {
	workspace := t.TempDir()
	globalRoot := t.TempDir()
	localPrompts := filepath.Join(workspace, configDirName, promptsDirName)

	writeFilePrompt(t, filepath.Join(localPrompts, "ok.md"), "ok")
	writeFilePrompt(t, filepath.Join(localPrompts, "skip.txt"), "wrong extension")
	writeFilePrompt(t, filepath.Join(localPrompts, "nested", "deep.md"), "nested")
	writeFilePrompt(t, filepath.Join(localPrompts, "!!!.md"), "invalid name")
	writeFilePrompt(t, filepath.Join(localPrompts, "blank.md"), " \n\t")

	loaded, err := loadFilePromptCommands(workspace, globalRoot)
	if err != nil {
		t.Fatalf("load file prompts: %v", err)
	}
	if len(loaded) != 1 || loaded[0].Name != "prompt:ok" || loaded[0].Content != "ok" {
		t.Fatalf("loaded commands = %+v, want only prompt:ok", loaded)
	}
}

func TestDefaultRegistryWithFilePromptsUsesWorkspaceAndSymlinkedGlobalCommands(t *testing.T) {
	workspace := t.TempDir()
	globalRoot := t.TempDir()
	agentsRoot := t.TempDir()
	workspaceConfigRoot := filepath.Join(workspace, configDirName)

	writeFilePrompt(t, filepath.Join(workspaceConfigRoot, promptsDirName, "review.md"), "# custom\nexact content\n")
	writeFilePrompt(t, filepath.Join(workspaceConfigRoot, "config.toml"), "model = \"local\"\n")
	agentsCommandsRoot := filepath.Join(agentsRoot, commandsDirName)
	writeFilePrompt(t, filepath.Join(agentsCommandsRoot, "agents.md"), "from global symlink target")
	if err := os.Symlink(agentsCommandsRoot, filepath.Join(globalRoot, commandsDirName)); err != nil {
		t.Fatalf("symlink global commands: %v", err)
	}

	registry, err := NewDefaultRegistryWithFilePrompts(workspace, globalRoot)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	review := registry.Execute("/prompt:review")
	if !review.Handled || !review.SubmitUser || review.Action != ActionNone || review.FreshConversation {
		t.Fatalf("workspace prompt result = %+v", review)
	}
	if review.User != "# custom\nexact content\n" {
		t.Fatalf("workspace prompt payload = %q", review.User)
	}
	if agents := registry.Execute("/prompt:agents"); !agents.Handled || agents.User != "from global symlink target" {
		t.Fatalf("global prompt result = %+v", agents)
	}
}

func TestNormalizeFilePromptCommandID(t *testing.T) {
	for input, want := range map[string]string{
		"  Bad - Name !!  ": "bad_name",
		"Already_OK":        "already_ok",
		"!!!":               "",
		"   ":               "",
	} {
		if got := normalizeFilePromptCommandID(input); got != want {
			t.Fatalf("normalizeFilePromptCommandID(%q) = %q, want %q", input, got, want)
		}
	}
}

func writeFilePrompt(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
