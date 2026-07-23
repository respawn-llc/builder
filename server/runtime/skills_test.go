package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/server/llm"
	"core/server/skillcatalog"
	brand "core/shared/config"
	"core/shared/textutil"
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
	if err := os.WriteFile(filepath.Join(invalidSkillDir, skillcatalog.SkillFileName), []byte("---\nname: invalid\n---\n"), 0o644); err != nil {
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

func TestSkillsContextMessageFailsOnUnreadableSkillsDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspace := t.TempDir()
	skillsRoot := filepath.Join(workspace, brand.ConfigDirName, skillcatalog.SkillsDirName)
	if err := os.MkdirAll(filepath.Dir(skillsRoot), 0o755); err != nil {
		t.Fatalf("mkdir skills parent: %v", err)
	}
	if err := os.WriteFile(skillsRoot, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write non-directory skills root: %v", err)
	}

	_, _, err := skillsContextMessage(workspace, brand.SkillPolicy{})
	if !errors.Is(err, skillcatalog.ErrReadSkillsDirectory) {
		t.Fatalf("expected ErrReadSkillsDirectory, got %v", err)
	}
}

func TestSplitMetaContextMessagesSeparatesMetaContextWithoutDeduplication(t *testing.T) {
	skillsMessage := llm.Message{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeSkills), Content: textutil.Value("## Skills\n### Available skills")}
	messages := []llm.Message{
		skillsMessage,
		skillsMessage,
		{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeEnvironment), Content: textutil.Value(environmentInjectedHeader + "\nOS: darwin")},
		{Role: llm.RoleUser, Content: textutil.Value("request")},
	}

	meta, transcript := splitMetaContextMessages(messages)
	if len(meta) != 3 {
		t.Fatalf("expected split to preserve duplicate meta candidates, got %d", len(meta))
	}
	if meta[0].MessageType == nil || *meta[0].MessageType != llm.MessageTypeSkills {
		t.Fatalf("expected first meta message to be skills context, got %+v", meta[0])
	}
	if meta[1].MessageType == nil || *meta[1].MessageType != llm.MessageTypeSkills {
		t.Fatalf("expected second meta message to remain duplicate skills context, got %+v", meta[1])
	}
	if meta[2].MessageType == nil || *meta[2].MessageType != llm.MessageTypeEnvironment {
		t.Fatalf("expected third meta message to be environment context, got %+v", meta[2])
	}
	if len(transcript) != 1 || transcript[0].Role != llm.RoleUser || messageContent(transcript[0]) != "request" {
		t.Fatalf("expected transcript to contain only user request, got %+v", transcript)
	}
}

func TestSplitMetaContextMessagesTreatsHeadlessContextAsMeta(t *testing.T) {
	headless := llm.Message{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeHeadlessMode), Content: textutil.Value("headless mode instructions")}
	messages := []llm.Message{
		headless,
		headless,
		{Role: llm.RoleUser, Content: textutil.Value("request")},
	}

	meta, transcript := splitMetaContextMessages(messages)
	if len(meta) != 2 {
		t.Fatalf("expected split to preserve duplicate headless meta messages, got %d", len(meta))
	}
	if meta[0].MessageType == nil || *meta[0].MessageType != llm.MessageTypeHeadlessMode {
		t.Fatalf("expected headless meta message, got %+v", meta[0])
	}
	if len(transcript) != 1 || transcript[0].Role != llm.RoleUser || messageContent(transcript[0]) != "request" {
		t.Fatalf("expected transcript to contain only user request, got %+v", transcript)
	}
}

func TestSplitMetaContextMessagesTreatsHeadlessExitContextAsMeta(t *testing.T) {
	headlessExit := llm.Message{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeHeadlessModeExit), Content: textutil.Value("interactive mode instructions")}
	messages := []llm.Message{
		headlessExit,
		headlessExit,
		{Role: llm.RoleUser, Content: textutil.Value("request")},
	}

	meta, transcript := splitMetaContextMessages(messages)
	if len(meta) != 2 {
		t.Fatalf("expected split to preserve duplicate headless exit meta messages, got %d", len(meta))
	}
	if meta[0].MessageType == nil || *meta[0].MessageType != llm.MessageTypeHeadlessModeExit {
		t.Fatalf("expected headless exit meta message, got %+v", meta[0])
	}
	if len(transcript) != 1 || transcript[0].Role != llm.RoleUser || messageContent(transcript[0]) != "request" {
		t.Fatalf("expected transcript to contain only user request, got %+v", transcript)
	}
}

func TestBuildReviewerTranscriptMessagesSkipsSkillsContextEntries(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeSkills), Content: textutil.Value("## Skills\n### Available skills\n- demo: desc")},
		{Role: llm.RoleUser, Content: textutil.Value("request")},
	}

	transcript := buildReviewerTranscriptMessages(messages)
	if len(transcript) != 1 {
		t.Fatalf("expected one transcript entry, got %d", len(transcript))
	}
	if !strings.Contains(messageContent(transcript[0]), "User:") || !strings.Contains(messageContent(transcript[0]), "request") {
		t.Fatalf("expected transcript entry to include user request, got %q", messageContent(transcript[0]))
	}
	if strings.Contains(messageContent(transcript[0]), "## Skills") {
		t.Fatalf("did not expect skills context in transcript entry, got %q", messageContent(transcript[0]))
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

func TestBuildReviewerRequestMessagesDoesNotRediscoverMissingSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspace := t.TempDir()
	writeTestSkill(t, filepath.Join(home, brand.ConfigDirName, "skills", "home-skill"), "Home Skill", "from home")
	writeTestSkill(t, filepath.Join(workspace, brand.ConfigDirName, "skills", "workspace-skill"), "Workspace Skill", "from workspace")

	messages := []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("request")}}
	got, err := buildReviewerRequestMessagesWithBuilder(
		messages,
		newMetaContextBuilder(workspace, "gpt-5", "high", skillPolicyWithDisabled("workspace skill"), time.Now()),
		false,
	)
	if err != nil {
		t.Fatalf("buildReviewerRequestMessages: %v", err)
	}
	for _, msg := range got {
		if msg.Role == llm.RoleDeveloper && msg.MessageType != nil && *msg.MessageType == llm.MessageTypeSkills {
			t.Fatalf("reviewer rediscovered skills outside generation reconstruction: %+v", got)
		}
	}
}

func writeTestSkill(t *testing.T, dir string, name string, description string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	skillPath := filepath.Join(dir, skillcatalog.SkillFileName)
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n# Body\n"
	if err := os.WriteFile(skillPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}
	if canonical, err := filepath.EvalSymlinks(skillPath); err == nil {
		return canonical
	}
	return skillPath
}

func skillsContextMessage(workspaceRoot string, policy brand.SkillPolicy) (string, bool, error) {
	builder := newMetaContextBuilder(workspaceRoot, "", "", policy, time.Now())
	metaResult, err := builder.Build(metaContextBuildOptions{IncludeSkills: true})
	if err != nil {
		return "", false, err
	}
	if len(metaResult.Skills) == 0 {
		return "", false, nil
	}
	return messageContent(metaResult.Skills[0]), true, nil
}
