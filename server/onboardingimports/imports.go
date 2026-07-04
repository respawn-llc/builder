package onboardingimports

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"core/server/skillcatalog"
	brand "core/shared/config"
)

type ProviderID string
type SourceKind string
type ItemKind string
type ChoiceMode string
type ErrorScope string

const (
	ProviderClaudeCode ProviderID = "claude_code"
	ProviderCodex      ProviderID = "codex"
	ProviderAgents     ProviderID = "agents"

	SourceKindExternalProvider SourceKind = "external_provider"
	SourceKindGenerated        SourceKind = "generated"
	SourceKindGlobal           SourceKind = "global"
	SourceKindWorkspace        SourceKind = "workspace"

	ItemKindSkill   ItemKind = "skill"
	ItemKindCommand ItemKind = "command"

	ChoiceModeNone          ChoiceMode = "none"
	ChoiceModeSymlinkSource ChoiceMode = "symlink_source"

	ErrorScopeWorkspace ErrorScope = "workspace"
	ErrorScopeProvider  ErrorScope = "provider"
	ErrorScopeGenerated ErrorScope = "generated"
	ErrorScopeTarget    ErrorScope = "target"
)

type Options struct {
	ConfigRoot     string
	WorkspaceRoot  *string
	HomeDir        string
	DisabledSkills map[string]bool
}

type Provider struct {
	ID                    ProviderID
	HomeEntry             string
	SkillSourceCandidates []string
	SupportsCommandImport bool
}

type Result struct {
	Workspace       WorkspaceScope
	Skills          ItemGroup
	Commands        ItemGroup
	SkillEnablement []SkillEnablementProjection
	Errors          []Error
	Recommendations Recommendations
}

type WorkspaceScope struct {
	Root *string
}

type ItemGroup struct {
	Choices []Choice
	Roots   []Root
	Items   []Item
	Target  Target
}

type Target struct {
	Skip      bool
	Conflicts []Conflict
}

type Root struct {
	SourceKind SourceKind
	ProviderID *ProviderID
	Path       string
	Exists     bool
}

type Choice struct {
	Ref        ChoiceRef
	ProviderID *ProviderID
	SourceRoot *string
	ItemCount  int
}

type ChoiceRef struct {
	Mode       ChoiceMode
	SourceKind *SourceKind
	ProviderID *ProviderID
	SourceRoot *string
}

type ItemRef struct {
	ItemKind       ItemKind
	SourceKind     SourceKind
	ProviderID     *ProviderID
	SourceRoot     *string
	SourcePath     *string
	TargetName     string
	Name           *string
	ModifiedUnixMs *int64
}

type Item struct {
	Ref            ItemRef
	Conflicts      []Conflict
	DefaultEnabled *bool
}

type Conflict struct {
	SourceKind SourceKind
	ProviderID *ProviderID
	Path       *string
}

type SkillEnablementProjection struct {
	ChoiceRef  ChoiceRef
	Candidates []Item
}

type Error struct {
	Code       string
	Scope      ErrorScope
	ProviderID *ProviderID
	Path       *string
	Operation  string
	Message    string
}

type Recommendations struct {
	Skills   *ModeRecommendation
	Commands *ModeRecommendation
}

type ModeRecommendation struct {
	ChoiceRef   ChoiceRef
	ItemCount   int
	SourcePaths []string
}

func Providers() []Provider {
	return []Provider{
		{ID: ProviderClaudeCode, HomeEntry: ".claude", SkillSourceCandidates: []string{"skills"}, SupportsCommandImport: true},
		{ID: ProviderCodex, HomeEntry: ".codex", SkillSourceCandidates: []string{filepath.Join("skills", "local"), "skills"}, SupportsCommandImport: true},
		{ID: ProviderAgents, HomeEntry: ".agents", SkillSourceCandidates: []string{"skills"}, SupportsCommandImport: true},
	}
}

func Discover(opts Options) (Result, error) {
	result := Result{
		Skills:   ItemGroup{Choices: []Choice{{Ref: ChoiceRef{Mode: ChoiceModeNone}}}},
		Commands: ItemGroup{Choices: []Choice{{Ref: ChoiceRef{Mode: ChoiceModeNone}}}},
	}
	workspaceRoot, workspaceOK, workspaceErr := resolveWorkspaceRoot(opts.WorkspaceRoot)
	if workspaceErr != nil {
		result.Errors = append(result.Errors, Error{Code: "invalid_workspace", Scope: ErrorScopeWorkspace, Operation: "resolve_workspace_root", Message: workspaceErr.Error()})
	} else if workspaceOK {
		result.Workspace.Root = &workspaceRoot
	}

	homeDir := strings.TrimSpace(opts.HomeDir)
	if homeDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			result.Errors = append(result.Errors, Error{Code: "home_dir_resolution_failed", Scope: ErrorScopeProvider, Operation: "resolve_home_dir", Message: err.Error()})
		} else {
			homeDir = home
		}
	}
	existingNames, existingErrs := existingSkillNames(opts.ConfigRoot, result.Workspace.Root)
	result.Errors = append(result.Errors, existingErrs...)

	generated, err := generatedSkillItems(opts.ConfigRoot, opts.DisabledSkills)
	if err != nil {
		result.Errors = append(result.Errors, Error{Code: "generated_discovery_failed", Scope: ErrorScopeGenerated, Operation: "discover_generated_skills", Message: err.Error()})
	}
	result.Skills.Items = append(result.Skills.Items, generated...)

	if homeDir != "" {
		for _, provider := range Providers() {
			base := filepath.Join(homeDir, provider.HomeEntry)
			discoverProviderSkills(&result, provider, base)
			discoverProviderCommands(&result, provider, base)
		}
	}
	result.Skills.Choices = append(result.Skills.Choices, choicesForItems(result.Skills.Items, ItemKindSkill)...)
	result.Commands.Choices = append(result.Commands.Choices, choicesForItems(result.Commands.Items, ItemKindCommand)...)
	skillTarget, skillTargetErrors := targetState([]string{filepath.Join(opts.ConfigRoot, skillcatalog.SkillsDirName)})
	commandTarget, commandTargetErrors := targetState([]string{filepath.Join(opts.ConfigRoot, "commands"), filepath.Join(opts.ConfigRoot, "prompts")})
	result.Skills.Target = skillTarget
	result.Commands.Target = commandTarget
	result.Errors = append(result.Errors, skillTargetErrors...)
	result.Errors = append(result.Errors, commandTargetErrors...)
	result.Recommendations.Skills = recommendationForChoices(result.Skills.Choices, result.Skills.Items, result.Skills.Target)
	result.Recommendations.Commands = recommendationForChoices(result.Commands.Choices, result.Commands.Items, result.Commands.Target)
	result.SkillEnablement = skillEnablementProjections(result.Skills.Choices, result.Skills.Items, generated, existingNames, opts.DisabledSkills)
	_ = workspaceOK
	return result, nil
}

func ValidateChoice(result Result, ref ChoiceRef, itemKind ItemKind) error {
	choices := result.Skills.Choices
	if itemKind == ItemKindCommand {
		choices = result.Commands.Choices
	}
	for _, choice := range choices {
		if choiceRefEqual(choice.Ref, ref) {
			return nil
		}
	}
	return ErrInvalidChoice
}

func resolveWorkspaceRoot(root *string) (string, bool, error) {
	if root == nil {
		return "", false, nil
	}
	trimmed := strings.TrimSpace(*root)
	if trimmed == "" {
		return "", false, errors.New("workspace root is blank")
	}
	resolved, err := brand.ResolveExistingPathRealPath(trimmed)
	if err != nil {
		return "", false, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", false, err
	}
	if !info.IsDir() {
		return "", false, fmt.Errorf("workspace root is not a directory: %s", resolved)
	}
	return resolved, true, nil
}

func discoverProviderSkills(result *Result, provider Provider, base string) {
	for _, candidate := range provider.SkillSourceCandidates {
		root := filepath.Join(base, candidate)
		exists, err := pathExists(root)
		if err != nil {
			addProviderError(result, provider.ID, root, "inspect_skill_root", err)
			continue
		}
		result.Skills.Roots = append(result.Skills.Roots, Root{SourceKind: SourceKindExternalProvider, ProviderID: &provider.ID, Path: root, Exists: exists})
		if !exists {
			continue
		}
		items, err := discoverDirectSkills(provider.ID, root)
		if err != nil {
			addProviderError(result, provider.ID, root, "discover_skills", err)
			continue
		}
		if len(items) > 0 {
			result.Skills.Items = append(result.Skills.Items, items...)
			return
		}
	}
}

func discoverProviderCommands(result *Result, provider Provider, base string) {
	if !provider.SupportsCommandImport {
		return
	}
	for _, root := range []string{filepath.Join(base, "commands"), filepath.Join(base, "prompts")} {
		exists, err := pathExists(root)
		if err != nil {
			addProviderError(result, provider.ID, root, "inspect_command_root", err)
			continue
		}
		result.Commands.Roots = append(result.Commands.Roots, Root{SourceKind: SourceKindExternalProvider, ProviderID: &provider.ID, Path: root, Exists: exists})
		if !exists {
			continue
		}
		items, err := discoverDirectCommands(provider.ID, root)
		if err != nil {
			addProviderError(result, provider.ID, root, "discover_commands", err)
			continue
		}
		if len(items) > 0 {
			result.Commands.Items = append(result.Commands.Items, items...)
			return
		}
	}
}

func discoverDirectSkills(providerID ProviderID, root string) ([]Item, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	items := make([]Item, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillPath := filepath.Join(root, entry.Name(), skillcatalog.SkillFileName)
		skill, ok := skillcatalog.ParseSkillMetadata(skillPath)
		if !ok {
			continue
		}
		info, err := os.Stat(skillPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		sourceRoot, sourcePath, target, name := root, filepath.Join(root, entry.Name()), entry.Name(), skill.Name
		items = append(items, Item{Ref: ItemRef{ItemKind: ItemKindSkill, SourceKind: SourceKindExternalProvider, ProviderID: &providerID, SourceRoot: &sourceRoot, SourcePath: &sourcePath, TargetName: target, Name: &name, ModifiedUnixMs: unixMs(info.ModTime())}})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Ref.TargetName < items[j].Ref.TargetName })
	return items, nil
}

func discoverDirectCommands(providerID ProviderID, root string) ([]Item, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	items := make([]Item, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		sourceRoot, sourcePath, target := root, filepath.Join(root, entry.Name()), entry.Name()
		name := strings.TrimSuffix(target, filepath.Ext(target))
		items = append(items, Item{Ref: ItemRef{ItemKind: ItemKindCommand, SourceKind: SourceKindExternalProvider, ProviderID: &providerID, SourceRoot: &sourceRoot, SourcePath: &sourcePath, TargetName: target, Name: &name}})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Ref.TargetName < items[j].Ref.TargetName })
	return items, nil
}

func generatedSkillItems(configRoot string, disabled map[string]bool) ([]Item, error) {
	inspections, err := skillcatalog.DiscoverGenerated(configRoot, disabled)
	if err != nil {
		return nil, err
	}
	items := make([]Item, 0)
	for _, skill := range inspections {
		if skill.SourceKind != skillcatalog.SourceKindGenerated || !skill.Loaded {
			continue
		}
		sourcePath := filepath.Dir(filepath.FromSlash(skill.Path))
		target, name := filepath.Base(sourcePath), skill.Name
		enabled := !skill.Disabled
		items = append(items, Item{Ref: ItemRef{ItemKind: ItemKindSkill, SourceKind: SourceKindGenerated, SourcePath: &sourcePath, TargetName: target, Name: &name}, DefaultEnabled: &enabled})
	}
	return items, nil
}

func choicesForItems(items []Item, kind ItemKind) []Choice {
	grouped := map[string][]Item{}
	refs := map[string]ChoiceRef{}
	for _, item := range items {
		if item.Ref.ItemKind != kind || item.Ref.SourceKind != SourceKindExternalProvider || item.Ref.ProviderID == nil || item.Ref.SourceRoot == nil {
			continue
		}
		key := string(*item.Ref.ProviderID) + "\x00" + *item.Ref.SourceRoot
		grouped[key] = append(grouped[key], item)
		sourceKind := SourceKindExternalProvider
		refs[key] = ChoiceRef{Mode: ChoiceModeSymlinkSource, SourceKind: &sourceKind, ProviderID: item.Ref.ProviderID, SourceRoot: item.Ref.SourceRoot}
	}
	keys := make([]string, 0, len(grouped))
	for key := range grouped {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left, right := refs[keys[i]], refs[keys[j]]
		if providerRank(*left.ProviderID) != providerRank(*right.ProviderID) {
			return providerRank(*left.ProviderID) < providerRank(*right.ProviderID)
		}
		return keys[i] < keys[j]
	})
	choices := make([]Choice, 0, len(keys))
	for _, key := range keys {
		ref := refs[key]
		choices = append(choices, Choice{Ref: ref, ProviderID: ref.ProviderID, SourceRoot: ref.SourceRoot, ItemCount: len(grouped[key])})
	}
	return choices
}

func recommendationForChoices(choices []Choice, items []Item, target Target) *ModeRecommendation {
	if target.Skip {
		return &ModeRecommendation{ChoiceRef: ChoiceRef{Mode: ChoiceModeNone}}
	}
	best := Choice{Ref: ChoiceRef{Mode: ChoiceModeNone}}
	for _, choice := range choices {
		if choice.Ref.Mode != ChoiceModeSymlinkSource || choice.ItemCount == 0 {
			continue
		}
		if best.Ref.Mode != ChoiceModeSymlinkSource || choice.ItemCount > best.ItemCount || choice.ItemCount == best.ItemCount && providerRank(*choice.ProviderID) < providerRank(*best.ProviderID) {
			best = choice
		}
	}
	paths := make([]string, 0)
	for _, item := range items {
		if best.Ref.Mode == ChoiceModeSymlinkSource && item.Ref.ProviderID != nil && best.ProviderID != nil && *item.Ref.ProviderID == *best.ProviderID && item.Ref.SourcePath != nil {
			paths = append(paths, *item.Ref.SourcePath)
		}
	}
	return &ModeRecommendation{ChoiceRef: best.Ref, ItemCount: best.ItemCount, SourcePaths: paths}
}

func skillEnablementProjections(choices []Choice, allItems []Item, generated []Item, existing map[string]bool, disabled map[string]bool) []SkillEnablementProjection {
	projections := make([]SkillEnablementProjection, 0, len(choices))
	for _, choice := range choices {
		imported := itemsForChoice(allItems, choice.Ref)
		candidates := append([]Item(nil), imported...)
		shadowing := cloneBoolMap(existing)
		for _, item := range imported {
			if item.Ref.Name != nil {
				shadowing[normalizeName(*item.Ref.Name)] = true
			}
		}
		for _, item := range generated {
			if item.Ref.Name != nil && shadowing[normalizeName(*item.Ref.Name)] {
				continue
			}
			candidates = append(candidates, item)
		}
		candidates = annotateConflicts(candidates)
		for idx := range candidates {
			enabled := true
			if candidates[idx].Ref.Name != nil && normalizedDisabled(disabled)[normalizeName(*candidates[idx].Ref.Name)] {
				enabled = false
			}
			candidates[idx].DefaultEnabled = &enabled
		}
		projections = append(projections, SkillEnablementProjection{ChoiceRef: choice.Ref, Candidates: candidates})
	}
	return projections
}

func itemsForChoice(items []Item, ref ChoiceRef) []Item {
	out := make([]Item, 0)
	for _, item := range items {
		if ref.Mode == ChoiceModeNone {
			continue
		}
		if item.Ref.SourceKind == SourceKindExternalProvider && item.Ref.ProviderID != nil && item.Ref.SourceRoot != nil && ref.ProviderID != nil && ref.SourceRoot != nil && *item.Ref.ProviderID == *ref.ProviderID && *item.Ref.SourceRoot == *ref.SourceRoot {
			out = append(out, item)
		}
	}
	return out
}

func annotateConflicts(items []Item) []Item {
	groups := map[string][]Item{}
	for _, item := range items {
		groups[strings.ToLower(strings.TrimSpace(item.Ref.TargetName))] = append(groups[strings.ToLower(strings.TrimSpace(item.Ref.TargetName))], item)
	}
	out := append([]Item(nil), items...)
	for idx := range out {
		group := groups[strings.ToLower(strings.TrimSpace(out[idx].Ref.TargetName))]
		if len(group) < 2 {
			continue
		}
		for _, opponent := range group {
			if choiceItemEqual(out[idx].Ref, opponent.Ref) {
				continue
			}
			out[idx].Conflicts = append(out[idx].Conflicts, Conflict{SourceKind: opponent.Ref.SourceKind, ProviderID: opponent.Ref.ProviderID, Path: opponent.Ref.SourcePath})
		}
	}
	return out
}

func existingSkillNames(configRoot string, workspaceRoot *string) (map[string]bool, []Error) {
	names := map[string]bool{}
	errs := make([]Error, 0)
	roots := []string{filepath.Join(configRoot, skillcatalog.SkillsDirName)}
	if workspaceRoot != nil {
		roots = append(roots, filepath.Join(*workspaceRoot, brand.ConfigDirName, skillcatalog.SkillsDirName))
	}
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			path := root
			errs = append(errs, Error{Code: "existing_skill_read_failed", Scope: ErrorScopeWorkspace, Path: &path, Operation: "read_existing_skills", Message: err.Error()})
			continue
		}
		for _, entry := range entries {
			skill, ok := skillcatalog.ParseSkillMetadata(filepath.Join(root, entry.Name(), skillcatalog.SkillFileName))
			if ok {
				names[normalizeName(skill.Name)] = true
			}
		}
	}
	return names, errs
}

func targetState(globalPaths []string) (Target, []Error) {
	conflicts := make([]Conflict, 0)
	errs := make([]Error, 0)
	for _, path := range globalPaths {
		hasContent, probeErr := targetHasExistingContent(path)
		cleaned := filepath.Clean(path)
		if probeErr != nil {
			errs = append(errs, *probeErr)
		}
		if hasContent {
			conflicts = append(conflicts, Conflict{SourceKind: SourceKindGlobal, Path: &cleaned})
		}
	}
	return Target{Skip: len(conflicts) > 0, Conflicts: conflicts}, errs
}

func targetHasExistingContent(path string) (bool, *Error) {
	cleaned := filepath.Clean(path)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, &Error{Code: "target_stat_failed", Scope: ErrorScopeTarget, Path: &cleaned, Operation: "inspect_import_target", Message: err.Error()}
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return true, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return true, &Error{Code: "target_read_failed", Scope: ErrorScopeTarget, Path: &cleaned, Operation: "read_import_target", Message: err.Error()}
	}
	return len(entries) > 0, nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

func addProviderError(result *Result, providerID ProviderID, path string, operation string, err error) {
	result.Errors = append(result.Errors, Error{Code: "provider_discovery_failed", Scope: ErrorScopeProvider, ProviderID: &providerID, Path: &path, Operation: operation, Message: err.Error()})
}

func choiceRefEqual(left, right ChoiceRef) bool {
	return left.Mode == right.Mode && ptrEqual(left.ProviderID, right.ProviderID) && ptrStringEqual(left.SourceRoot, right.SourceRoot)
}

func choiceItemEqual(left, right ItemRef) bool {
	return left.ItemKind == right.ItemKind && left.SourceKind == right.SourceKind && ptrEqual(left.ProviderID, right.ProviderID) && ptrStringEqual(left.SourcePath, right.SourcePath)
}

func ptrEqual[T comparable](left, right *T) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func ptrStringEqual(left, right *string) bool {
	return ptrEqual(left, right)
}

func providerRank(provider ProviderID) int {
	for idx, supported := range Providers() {
		if supported.ID == provider {
			return idx
		}
	}
	return len(Providers())
}

func normalizedDisabled(values map[string]bool) map[string]bool {
	out := map[string]bool{}
	for key, value := range values {
		if value {
			out[normalizeName(key)] = true
		}
	}
	return out
}

func cloneBoolMap(values map[string]bool) map[string]bool {
	out := make(map[string]bool, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func normalizeName(raw string) string {
	return brand.NormalizeSkillName(raw)
}

func unixMs(t time.Time) *int64 {
	v := t.UnixMilli()
	return &v
}
