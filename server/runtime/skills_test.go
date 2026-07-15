package runtime

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/server/llm"
	"core/server/skillcatalog"
	brand "core/shared/config"
)

func skillPolicyWithDisabled(names ...string) brand.SkillPolicy {
	toggles := make(map[string]bool, len(names))
	for _, name := range names {
		toggles[name] = false
	}
	return brand.ResolveSkillPolicy(brand.Settings{SkillToggles: toggles})
}

func TestSkillsContextMessageIncludesCodexPromptAndSkillEntries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspace := t.TempDir()
	homeSkillPath := writeTestSkill(t, filepath.Join(home, brand.ConfigDirName, "skills", "home-skill"), "home-skill", "from home")
	workspaceSkillPath := writeTestSkill(t, filepath.Join(workspace, brand.ConfigDirName, "skills", "workspace-skill"), "workspace-skill", "from workspace")

	content, found, err := skillsContextMessage(workspace, brand.SkillPolicy{})
	if err != nil {
		t.Fatalf("skillsContextMessage: %v", err)
	}
	if !found {
		t.Fatal("expected skills context to be found")
	}

	for _, required := range []string{
		skillsAvailableHeader,
		"- home-skill: " + filepath.ToSlash(homeSkillPath) + " . from home",
		"- workspace-skill: " + filepath.ToSlash(workspaceSkillPath) + " . from workspace",
		"For each skill, `SKILL.md` is the main index file to start with.",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("expected skills context to include %q, got %q", required, content)
		}
	}
	if strings.Contains(content, "## Skills") {
		t.Fatalf("expected skills context to omit skills header, got %q", content)
	}
	if !strings.HasPrefix(content, skillsPrompt+"\n"+skillsAvailableHeader+"\n") {
		t.Fatalf("expected skills context to start with usage rules then available skills, got %q", content)
	}
}

func TestSkillsContextMessageSkipsInvalidSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspace := t.TempDir()
	invalidSkillDir := filepath.Join(workspace, brand.ConfigDirName, "skills", "invalid")
	if err := os.MkdirAll(invalidSkillDir, 0o755); err != nil {
		t.Fatalf("mkdir invalid skill dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(invalidSkillDir, skillFileName), []byte("---\nname: invalid\n---\n"), 0o644); err != nil {
		t.Fatalf("write invalid skill: %v", err)
	}

	content, found, err := skillsContextMessage(workspace, brand.SkillPolicy{})
	if err != nil {
		t.Fatalf("skillsContextMessage: %v", err)
	}
	if found {
		t.Fatalf("expected no skills context for invalid skill, got %q", content)
	}
}

func TestSkillsContextMessageLoadsSymlinkedSkillDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspace := t.TempDir()
	targetSkillPath := writeTestSkill(t, filepath.Join(t.TempDir(), "shared-skills", "linked-skill"), "linked-skill", "from symlink")
	linkPath := filepath.Join(workspace, brand.ConfigDirName, "skills", "linked-skill")
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		t.Fatalf("mkdir symlink parent: %v", err)
	}
	if err := os.Symlink(filepath.Dir(targetSkillPath), linkPath); err != nil {
		t.Fatalf("symlink skill dir: %v", err)
	}

	content, found, err := skillsContextMessage(workspace, brand.SkillPolicy{})
	if err != nil {
		t.Fatalf("skillsContextMessage: %v", err)
	}
	if !found {
		t.Fatal("expected symlinked skill to be discovered")
	}
	want := "- linked-skill: " + filepath.ToSlash(targetSkillPath) + " . from symlink"
	if !strings.Contains(content, want) {
		t.Fatalf("expected symlinked skill entry %q, got %q", want, content)
	}
}

func TestSkillsContextMessageLoadsSkillFromSymlinkedGlobalSkillsRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspace := t.TempDir()
	sharedSkillsRoot := filepath.Join(t.TempDir(), "shared-skills")
	targetSkillPath := writeTestSkill(t, filepath.Join(t.TempDir(), "external-skills", "linked-skill"), "linked-skill", "from symlinked global root")
	if err := os.MkdirAll(sharedSkillsRoot, 0o755); err != nil {
		t.Fatalf("mkdir shared skills root: %v", err)
	}
	if err := os.Symlink(filepath.Dir(targetSkillPath), filepath.Join(sharedSkillsRoot, "linked-skill")); err != nil {
		t.Fatalf("symlink skill dir in global root: %v", err)
	}
	globalSkillsRoot := filepath.Join(home, brand.ConfigDirName, "skills")
	if err := os.MkdirAll(filepath.Dir(globalSkillsRoot), 0o755); err != nil {
		t.Fatalf("mkdir global skills parent: %v", err)
	}
	if err := os.Symlink(sharedSkillsRoot, globalSkillsRoot); err != nil {
		t.Fatalf("symlink global skills root: %v", err)
	}

	content, found, err := skillsContextMessage(workspace, brand.SkillPolicy{})
	if err != nil {
		t.Fatalf("skillsContextMessage: %v", err)
	}
	if !found {
		t.Fatal("expected skill from symlinked global skills root to be discovered")
	}
	want := "- linked-skill: " + filepath.ToSlash(targetSkillPath) + " . from symlinked global root"
	if !strings.Contains(content, want) {
		t.Fatalf("expected symlinked global skill entry %q, got %q", want, content)
	}
}

func TestResolveSkillDirUsesLstatWhenDirEntryTypeIsUnknown(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspace := t.TempDir()
	brokenLinkPath := filepath.Join(workspace, brand.ConfigDirName, "skills", "broken-skill")
	if err := os.MkdirAll(filepath.Dir(brokenLinkPath), 0o755); err != nil {
		t.Fatalf("mkdir broken symlink parent: %v", err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing-skill-dir"), brokenLinkPath); err != nil {
		t.Fatalf("symlink broken skill dir: %v", err)
	}

	resolution := resolveSkillDir(filepath.Dir(brokenLinkPath), fakeDirEntry{name: filepath.Base(brokenLinkPath)})
	if resolution.Discoverable {
		t.Fatalf("expected broken symlink with unknown entry type to stay undiscoverable, got %+v", resolution)
	}
	if resolution.Issue == nil {
		t.Fatalf("expected broken symlink with unknown entry type to surface an issue, got %+v", resolution)
	}
	if resolution.Issue.Path != filepath.ToSlash(brokenLinkPath) {
		t.Fatalf("expected issue path %q, got %+v", filepath.ToSlash(brokenLinkPath), resolution)
	}
	if resolution.Issue.Reason != "symlink target does not exist" {
		t.Fatalf("expected stable missing-target reason, got %+v", resolution)
	}
}

func TestSkillsContextMessageSkipsBrokenSymlinkedSkillDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspace := t.TempDir()
	validSkillPath := writeTestSkill(t, filepath.Join(workspace, brand.ConfigDirName, "skills", "valid-skill"), "valid-skill", "from workspace")
	brokenLinkPath := filepath.Join(workspace, brand.ConfigDirName, "skills", "broken-skill")
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing-skill-dir"), brokenLinkPath); err != nil {
		t.Fatalf("symlink broken skill dir: %v", err)
	}

	content, found, err := skillsContextMessage(workspace, brand.SkillPolicy{})
	if err != nil {
		t.Fatalf("skillsContextMessage: %v", err)
	}
	if !found {
		t.Fatal("expected valid skill to remain discoverable")
	}
	if !strings.Contains(content, "- valid-skill: "+filepath.ToSlash(validSkillPath)+" . from workspace") {
		t.Fatalf("expected valid skill entry to remain, got %q", content)
	}
	if strings.Contains(content, "broken-skill") {
		t.Fatalf("did not expect broken symlinked skill in context, got %q", content)
	}
}

func TestAppendMissingReviewerMetaContextPrependsSkillsWhenMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspace := t.TempDir()
	writeTestSkill(t, filepath.Join(workspace, brand.ConfigDirName, "skills", "workspace-skill"), "workspace-skill", "from workspace")

	in := []llm.Message{{Role: llm.RoleUser, Content: "request"}}
	got, err := appendMissingReviewerMetaContext(in, workspace, "gpt-5", "high", "", false, brand.SkillPolicy{})
	if err != nil {
		t.Fatalf("appendMissingReviewerMetaContext: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected skills+environment prepended plus original, got %d", len(got))
	}
	if got[0].Role != llm.RoleDeveloper || got[0].MessageType != llm.MessageTypeEnvironment || !strings.Contains(got[0].Content, environmentInjectedHeader) {
		t.Fatalf("expected first prepended message to be environment context, got %+v", got[0])
	}
	if got[1].Role != llm.RoleDeveloper || got[1].MessageType != llm.MessageTypeSkills || !strings.Contains(got[1].Content, skillsAvailableHeader) {
		t.Fatalf("expected second prepended message to be skills context, got %+v", got[1])
	}
	if got[2].Role != llm.RoleUser || got[2].Content != "request" {
		t.Fatalf("expected original message at tail, got %+v", got[2])
	}
}

func TestSkillsContextMessageFailsOnUnreadableSkillsDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspace := t.TempDir()
	prev := readSkillsDir
	readSkillsDir = func(path string) ([]os.DirEntry, error) {
		if path == filepath.Join(workspace, brand.ConfigDirName, "skills") {
			return nil, os.ErrPermission
		}
		return prev(path)
	}
	t.Cleanup(func() {
		readSkillsDir = prev
	})

	_, _, err := skillsContextMessage(workspace, brand.SkillPolicy{})
	if !errors.Is(err, errReadSkillsDirectory) {
		t.Fatalf("expected errReadSkillsDirectory, got %v", err)
	}
}

func TestDisabledSkillPolicySkipsDiscoveryAndWarnings(t *testing.T) {
	previous := readSkillsDir
	readSkillsDir = func(string) ([]os.DirEntry, error) {
		t.Fatal("disabled skills policy must not read directories")
		return nil, os.ErrPermission
	}
	t.Cleanup(func() {
		readSkillsDir = previous
	})

	policy := brand.ResolveSkillPolicy(brand.Settings{SkillSubsystem: brand.SkillSubsystemDisabled})
	discovery, err := discoverInjectedSkills(t.TempDir(), "\x00invalid", policy)
	if err != nil {
		t.Fatalf("discover disabled skills: %v", err)
	}
	if discovery.State != skillcatalog.DiscoveryStateDisabled {
		t.Fatalf("discovery state = %q, want disabled", discovery.State)
	}
	if len(discovery.Skills) != 0 || len(discovery.Issues) != 0 {
		t.Fatalf("disabled discovery must not expose skills or warnings: %+v", discovery)
	}
	content, found, err := skillsContextMessage(t.TempDir(), policy)
	if err != nil {
		t.Fatalf("disabled skills context: %v", err)
	}
	if found || content != "" {
		t.Fatalf("disabled skills context = (%q, %t), want absent", content, found)
	}
}

func TestSplitMetaContextMessagesSeparatesMetaContextWithoutDeduplication(t *testing.T) {
	skillsMessage := llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeSkills, Content: "## Skills\n### Available skills"}
	messages := []llm.Message{
		skillsMessage,
		skillsMessage,
		{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeEnvironment, Content: environmentInjectedHeader + "\nOS: darwin"},
		{Role: llm.RoleUser, Content: "request"},
	}

	meta, transcript := splitMetaContextMessages(messages)
	if len(meta) != 3 {
		t.Fatalf("expected split to preserve duplicate meta candidates, got %d", len(meta))
	}
	if meta[0].MessageType != llm.MessageTypeSkills {
		t.Fatalf("expected first meta message to be skills context, got %+v", meta[0])
	}
	if meta[1].MessageType != llm.MessageTypeSkills {
		t.Fatalf("expected second meta message to remain duplicate skills context, got %+v", meta[1])
	}
	if meta[2].MessageType != llm.MessageTypeEnvironment {
		t.Fatalf("expected third meta message to be environment context, got %+v", meta[2])
	}
	if len(transcript) != 1 || transcript[0].Role != llm.RoleUser || transcript[0].Content != "request" {
		t.Fatalf("expected transcript to contain only user request, got %+v", transcript)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	rebuilt, err := appendMissingReviewerMetaContext(messages, t.TempDir(), "gpt-5", "high", "", false, brand.SkillPolicy{})
	if err != nil {
		t.Fatalf("appendMissingReviewerMetaContext: %v", err)
	}
	if len(rebuilt) != 2 {
		t.Fatalf("expected reviewer builder to replace persisted skills with current discovery, got %d", len(rebuilt))
	}
	if rebuilt[0].MessageType != llm.MessageTypeEnvironment || rebuilt[1].Role != llm.RoleUser {
		t.Fatalf("expected environment followed by transcript when current discovery is empty, got %+v", rebuilt)
	}
}

func TestSplitMetaContextMessagesTreatsHeadlessContextAsMeta(t *testing.T) {
	headless := llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeHeadlessMode, Content: "headless mode instructions"}
	messages := []llm.Message{
		headless,
		headless,
		{Role: llm.RoleUser, Content: "request"},
	}

	meta, transcript := splitMetaContextMessages(messages)
	if len(meta) != 2 {
		t.Fatalf("expected split to preserve duplicate headless meta messages, got %d", len(meta))
	}
	if meta[0].MessageType != llm.MessageTypeHeadlessMode {
		t.Fatalf("expected headless meta message, got %+v", meta[0])
	}
	if len(transcript) != 1 || transcript[0].Role != llm.RoleUser || transcript[0].Content != "request" {
		t.Fatalf("expected transcript to contain only user request, got %+v", transcript)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	rebuilt, err := appendMissingReviewerMetaContext(messages, t.TempDir(), "gpt-5", "high", "", true, brand.SkillPolicy{})
	if err != nil {
		t.Fatalf("appendMissingReviewerMetaContext: %v", err)
	}
	if len(rebuilt) != 3 {
		t.Fatalf("expected builder to reconstruct environment + headless + transcript, got %d", len(rebuilt))
	}
	if rebuilt[0].MessageType != llm.MessageTypeEnvironment || rebuilt[1].MessageType != llm.MessageTypeHeadlessMode {
		t.Fatalf("expected canonical environment -> headless ordering, got %+v", rebuilt)
	}
}

func TestSplitMetaContextMessagesTreatsHeadlessExitContextAsMeta(t *testing.T) {
	headlessExit := llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeHeadlessModeExit, Content: "interactive mode instructions"}
	messages := []llm.Message{
		headlessExit,
		headlessExit,
		{Role: llm.RoleUser, Content: "request"},
	}

	meta, transcript := splitMetaContextMessages(messages)
	if len(meta) != 2 {
		t.Fatalf("expected split to preserve duplicate headless exit meta messages, got %d", len(meta))
	}
	if meta[0].MessageType != llm.MessageTypeHeadlessModeExit {
		t.Fatalf("expected headless exit meta message, got %+v", meta[0])
	}
	if len(transcript) != 1 || transcript[0].Role != llm.RoleUser || transcript[0].Content != "request" {
		t.Fatalf("expected transcript to contain only user request, got %+v", transcript)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	rebuilt, err := appendMissingReviewerMetaContext(messages, t.TempDir(), "gpt-5", "high", "", false, brand.SkillPolicy{})
	if err != nil {
		t.Fatalf("appendMissingReviewerMetaContext: %v", err)
	}
	if len(rebuilt) != 3 {
		t.Fatalf("expected builder to reconstruct environment + headless exit + transcript, got %d", len(rebuilt))
	}
	if rebuilt[0].MessageType != llm.MessageTypeEnvironment || rebuilt[1].MessageType != llm.MessageTypeHeadlessModeExit {
		t.Fatalf("expected canonical environment -> headless exit ordering, got %+v", rebuilt)
	}
}

func TestBuildReviewerTranscriptMessagesSkipsSkillsContextEntries(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeSkills, Content: "## Skills\n### Available skills\n- demo: desc"},
		{Role: llm.RoleUser, Content: "request"},
	}

	transcript := buildReviewerTranscriptMessages(messages)
	if len(transcript) != 1 {
		t.Fatalf("expected one transcript entry, got %d", len(transcript))
	}
	if !strings.Contains(transcript[0].Content, "User:") || !strings.Contains(transcript[0].Content, "request") {
		t.Fatalf("expected transcript entry to include user request, got %q", transcript[0].Content)
	}
	if strings.Contains(transcript[0].Content, "## Skills") {
		t.Fatalf("did not expect skills context in transcript entry, got %q", transcript[0].Content)
	}
}

func TestSkillsContextMessageSkipsConfigDisabledSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspace := t.TempDir()
	writeTestSkill(t, filepath.Join(home, brand.ConfigDirName, "skills", "home-skill"), "Home Skill", "from home")
	writeTestSkill(t, filepath.Join(workspace, brand.ConfigDirName, "skills", "workspace-skill"), "Workspace Skill", "from workspace")

	content, found, err := skillsContextMessage(workspace, skillPolicyWithDisabled("workspace skill"))
	if err != nil {
		t.Fatalf("skillsContextMessageWithDisabled: %v", err)
	}
	if !found {
		t.Fatal("expected skills context to be found")
	}
	if strings.Contains(content, "Workspace Skill") {
		t.Fatalf("expected disabled workspace skill to be omitted, got %q", content)
	}
	if !strings.Contains(content, "Home Skill") {
		t.Fatalf("expected enabled home skill to remain, got %q", content)
	}
}

func TestGeneratedSkillsAreInjectedAfterUserSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspace := t.TempDir()
	homeSkillPath := writeTestSkill(t, filepath.Join(home, brand.ConfigDirName, "skills", "home-skill"), "Home Skill", "from home")
	workspaceSkillPath := writeTestSkill(t, filepath.Join(workspace, brand.ConfigDirName, "skills", "workspace-skill"), "Workspace Skill", "from workspace")
	generatedSkillPath := writeTestSkill(t, filepath.Join(home, brand.ConfigDirName, ".generated", "skills", "skill-creator"), "skill-creator", "generated")

	content, found, err := skillsContextMessage(workspace, brand.SkillPolicy{})
	if err != nil {
		t.Fatalf("skillsContextMessage: %v", err)
	}
	if !found {
		t.Fatal("expected skills context")
	}
	expected := []string{
		"- Home Skill: " + filepath.ToSlash(homeSkillPath) + " . from home",
		"- Workspace Skill: " + filepath.ToSlash(workspaceSkillPath) + " . from workspace",
		"- skill-creator: " + filepath.ToSlash(generatedSkillPath) + " . generated",
	}
	previous := -1
	for _, text := range expected {
		idx := strings.Index(content, text)
		if idx < 0 {
			t.Fatalf("expected %q in skills context %q", text, content)
		}
		if idx <= previous {
			t.Fatalf("expected generated skill after user skills, got %q", content)
		}
		previous = idx
	}
}

func TestUserSkillDuplicateNameBehaviorIsUnchanged(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspace := t.TempDir()
	homeSkillPath := writeTestSkill(t, filepath.Join(home, brand.ConfigDirName, "skills", "same-skill-global"), "same-skill", "from home")
	workspaceSkillPath := writeTestSkill(t, filepath.Join(workspace, brand.ConfigDirName, "skills", "same-skill-workspace"), "same-skill", "from workspace")

	content, found, err := skillsContextMessage(workspace, brand.SkillPolicy{})
	if err != nil {
		t.Fatalf("skillsContextMessage: %v", err)
	}
	if !found {
		t.Fatal("expected skills context")
	}
	homeEntry := "- same-skill: " + filepath.ToSlash(homeSkillPath) + " . from home"
	workspaceEntry := "- same-skill: " + filepath.ToSlash(workspaceSkillPath) + " . from workspace"
	homeIdx := strings.Index(content, homeEntry)
	workspaceIdx := strings.Index(content, workspaceEntry)
	if homeIdx < 0 || workspaceIdx < 0 {
		t.Fatalf("expected both same-name user skills to remain, got %q", content)
	}
	if homeIdx >= workspaceIdx {
		t.Fatalf("expected existing global-before-workspace order to remain, got %q", content)
	}
}

func TestGeneratedSkillIsShadowedByUserSkillName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspace := t.TempDir()
	userSkillPath := writeTestSkill(t, filepath.Join(workspace, brand.ConfigDirName, "skills", "skill-creator"), "skill-creator", "workspace")
	writeTestSkill(t, filepath.Join(home, brand.ConfigDirName, ".generated", "skills", "skill-creator"), "skill-creator", "generated")

	content, found, err := skillsContextMessage(workspace, brand.SkillPolicy{})
	if err != nil {
		t.Fatalf("skillsContextMessage: %v", err)
	}
	if !found {
		t.Fatal("expected skills context")
	}
	if !strings.Contains(content, "- skill-creator: "+filepath.ToSlash(userSkillPath)+" . workspace") {
		t.Fatalf("expected user skill to remain, got %q", content)
	}
	if strings.Contains(content, "generated") {
		t.Fatalf("expected generated skill to be shadowed, got %q", content)
	}
}

func TestGeneratedSkillIsDisabledBySkillToggle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspace := t.TempDir()
	writeTestSkill(t, filepath.Join(home, brand.ConfigDirName, ".generated", "skills", "skill-creator"), "skill-creator", "generated")

	content, found, err := skillsContextMessage(workspace, skillPolicyWithDisabled("skill-creator"))
	if err != nil {
		t.Fatalf("skillsContextMessageWithDisabled: %v", err)
	}
	if found {
		t.Fatalf("expected disabled generated skill to be omitted, got %q", content)
	}
}

func TestInspectSkillsMarksConfigDisabledSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspace := t.TempDir()
	writeTestSkill(t, filepath.Join(workspace, brand.ConfigDirName, "skills", "workspace-skill"), "Workspace Skill", "from workspace")

	result, err := InspectSkills(workspace, "", skillPolicyWithDisabled("workspace skill"))
	if err != nil {
		t.Fatalf("InspectSkills: %v", err)
	}
	inspections := result.Inspections
	if len(inspections) != 1 {
		t.Fatalf("expected one inspection, got %d", len(inspections))
	}
	if !inspections[0].Loaded {
		t.Fatalf("expected skill to stay loadable, got %+v", inspections[0])
	}
	if !inspections[0].Disabled {
		t.Fatalf("expected skill to be marked disabled, got %+v", inspections[0])
	}
}

func TestInspectSkillsDistinguishesDisabledFromEnabledEmpty(t *testing.T) {
	disabledPolicy := brand.ResolveSkillPolicy(brand.Settings{SkillSubsystem: brand.SkillSubsystemDisabled})
	disabled, err := InspectSkills(t.TempDir(), "\x00invalid", disabledPolicy)
	if err != nil {
		t.Fatalf("InspectSkills disabled: %v", err)
	}
	if disabled.State != skillcatalog.DiscoveryStateDisabled || len(disabled.Inspections) != 0 {
		t.Fatalf("disabled inspection result = %+v", disabled)
	}

	enabled, err := InspectSkills(t.TempDir(), t.TempDir(), brand.SkillPolicy{})
	if err != nil {
		t.Fatalf("InspectSkills enabled empty: %v", err)
	}
	if enabled.State != skillcatalog.DiscoveryStateEnabled {
		t.Fatalf("enabled empty state = %q, want enabled", enabled.State)
	}
}

func TestInspectSkillsMarksGeneratedShadowedAndDisabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspace := t.TempDir()
	writeTestSkill(t, filepath.Join(workspace, brand.ConfigDirName, "skills", "skill-creator"), "skill-creator", "workspace")
	generatedPath := writeTestSkill(t, filepath.Join(home, brand.ConfigDirName, ".generated", "skills", "skill-creator"), "skill-creator", "generated")

	result, err := InspectSkills(workspace, "", skillPolicyWithDisabled("skill-creator"))
	if err != nil {
		t.Fatalf("InspectSkills: %v", err)
	}
	inspections := result.Inspections
	var generatedInspection *SkillInspection
	for idx := range inspections {
		if inspections[idx].Path == filepath.ToSlash(generatedPath) {
			generatedInspection = &inspections[idx]
			break
		}
	}
	if generatedInspection == nil {
		t.Fatalf("expected generated inspection, got %+v", inspections)
	}
	if generatedInspection.SourceKind != string(skillSourceGenerated) || !generatedInspection.Shadowed || !generatedInspection.Disabled {
		t.Fatalf("expected generated skill to be shadowed and disabled, got %+v", *generatedInspection)
	}
}

func TestInspectSkillsLoadsSymlinkedSkillDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspace := t.TempDir()
	targetSkillPath := writeTestSkill(t, filepath.Join(t.TempDir(), "shared-skills", "linked-skill"), "linked-skill", "from symlink")
	linkPath := filepath.Join(workspace, brand.ConfigDirName, "skills", "linked-skill")
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		t.Fatalf("mkdir symlink parent: %v", err)
	}
	if err := os.Symlink(filepath.Dir(targetSkillPath), linkPath); err != nil {
		t.Fatalf("symlink skill dir: %v", err)
	}

	result, err := InspectSkills(workspace, "", brand.SkillPolicy{})
	if err != nil {
		t.Fatalf("InspectSkills: %v", err)
	}
	inspections := result.Inspections
	if len(inspections) != 1 {
		t.Fatalf("expected one inspection, got %d", len(inspections))
	}
	if !inspections[0].Loaded {
		t.Fatalf("expected symlinked skill inspection to load, got %+v", inspections[0])
	}
	if inspections[0].Path != filepath.ToSlash(targetSkillPath) {
		t.Fatalf("expected inspection path %q, got %+v", filepath.ToSlash(targetSkillPath), inspections[0])
	}
}

func TestInspectSkillsReportsBrokenSymlinkedSkillDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspace := t.TempDir()
	brokenLinkPath := filepath.Join(workspace, brand.ConfigDirName, "skills", "broken-skill")
	if err := os.MkdirAll(filepath.Dir(brokenLinkPath), 0o755); err != nil {
		t.Fatalf("mkdir broken symlink parent: %v", err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing-skill-dir"), brokenLinkPath); err != nil {
		t.Fatalf("symlink broken skill dir: %v", err)
	}

	result, err := InspectSkills(workspace, "", brand.SkillPolicy{})
	if err != nil {
		t.Fatalf("InspectSkills: %v", err)
	}
	inspections := result.Inspections
	if len(inspections) != 1 {
		t.Fatalf("expected one inspection, got %d", len(inspections))
	}
	if inspections[0].Loaded {
		t.Fatalf("expected broken symlinked skill inspection to fail, got %+v", inspections[0])
	}
	brokenSkillPath := filepath.ToSlash(filepath.Join(brokenLinkPath, skillFileName))
	if inspections[0].Path != brokenSkillPath {
		t.Fatalf("expected inspection path %q, got %+v", brokenSkillPath, inspections[0])
	}
	if inspections[0].Reason != "symlink target does not exist" {
		t.Fatalf("expected missing target reason, got %+v", inspections[0])
	}
}

func TestBuildReviewerRequestMessagesSkipsDisabledSkillsWhenBackfillingMeta(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspace := t.TempDir()
	writeTestSkill(t, filepath.Join(home, brand.ConfigDirName, "skills", "home-skill"), "Home Skill", "from home")
	writeTestSkill(t, filepath.Join(workspace, brand.ConfigDirName, "skills", "workspace-skill"), "Workspace Skill", "from workspace")

	messages := []llm.Message{{Role: llm.RoleUser, Content: "request"}}
	got, err := buildReviewerRequestMessagesWithBuilder(
		messages,
		newMetaContextBuilder(workspace, "gpt-5", "high", skillPolicyWithDisabled("workspace skill"), time.Now()),
		false,
	)
	if err != nil {
		t.Fatalf("buildReviewerRequestMessages: %v", err)
	}
	foundSkills := false
	for _, msg := range got {
		if msg.Role != llm.RoleDeveloper || msg.MessageType != llm.MessageTypeSkills {
			continue
		}
		foundSkills = true
		if strings.Contains(msg.Content, "Workspace Skill") {
			t.Fatalf("expected disabled workspace skill to be omitted from reviewer meta, got %q", msg.Content)
		}
		if !strings.Contains(msg.Content, "Home Skill") {
			t.Fatalf("expected enabled home skill in reviewer meta, got %q", msg.Content)
		}
	}
	if !foundSkills {
		t.Fatalf("expected backfilled reviewer skills meta message, got %+v", got)
	}
}

func writeTestSkill(t *testing.T, dir string, name string, description string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	skillPath := filepath.Join(dir, skillFileName)
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n# Body\n"
	if err := os.WriteFile(skillPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}
	if canonical, err := filepath.EvalSymlinks(skillPath); err == nil {
		return canonical
	}
	return skillPath
}

type fakeDirEntry struct {
	name string
}

func (f fakeDirEntry) Name() string             { return f.name }
func (fakeDirEntry) IsDir() bool                { return false }
func (fakeDirEntry) Type() fs.FileMode          { return 0 }
func (fakeDirEntry) Info() (fs.FileInfo, error) { return nil, fs.ErrNotExist }
