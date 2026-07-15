package status

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"core/shared/apicontract"
	"core/shared/auth"
	"core/shared/clientui"
	"core/shared/config"
)

const (
	authCacheFreshness        = time.Minute
	gitCacheFreshness         = 5 * time.Second
	environmentCacheFreshness = 5 * time.Minute
)

type Repository interface {
	SeedSnapshot(req Request, base Snapshot, now time.Time) SeedResult
	StoreAuth(cacheKey string, result AuthStageResult, now time.Time)
	StoreGit(cacheKey string, result GitStageResult, now time.Time)
	StoreEnvironment(cacheKey string, result EnvironmentStageResult, now time.Time)
}

type CacheKeys struct {
	Auth        string
	Git         string
	Environment string
}

type Request struct {
	Runtime               clientui.RuntimeClient
	WorkspaceRoot         string
	PersistenceRoot       string
	ExecutionTarget       clientui.SessionExecutionTarget
	SessionViews          apicontract.SessionViewService
	Settings              config.Settings
	Source                config.SourceReport
	AuthCacheIdentity     string
	AuthCacheUnseedable   bool
	CacheKeys             CacheKeys
	AuthStatus            apicontract.AuthStatusService
	AuthStatePath         string
	SessionName           string
	SessionID             string
	ConfiguredModelName   string
	ModelName             string
	ThinkingLevel         string
	FastModeAvailable     bool
	FastModeEnabled       bool
	ReviewerEnabled       bool
	ReviewerMode          string
	AutoCompactionEnabled bool
	QuestionsEnabled      bool
	OwnsServer            bool
	CurrentTime           time.Time
}

type Snapshot struct {
	CollectedAt       time.Time
	Workdir           string
	SessionName       string
	SessionID         string
	ParentSessionID   string
	ParentSessionName string
	OwnsServer        bool
	Git               GitInfo
	Auth              AuthInfo
	Context           ContextInfo
	Model             ModelInfo
	Update            UpdateInfo
	Config            ConfigInfo
	Subscription      SubscriptionInfo
	Skills            []SkillInspection
	SkillTokenCounts  map[string]int
	AgentsPaths       []string
	AgentTokenCounts  map[string]int
	CompactionCount   int
	CollectorWarning  string
}

type AuthInfo struct {
	Summary     string
	Details     []string
	Visible     bool
	Method      auth.MethodType
	Provider    string
	Unavailable bool
}

type GitInfo struct {
	Visible bool
	Branch  string
	Dirty   bool
	Ahead   int
	Behind  int
	Error   string
}

type ContextInfo struct {
	UsedTokens      int
	AvailableTokens int
	WindowTokens    int
	ThresholdTokens int
}

type ModelInfo struct {
	Summary string
}

type UpdateInfo struct {
	Checked       bool
	Available     bool
	LatestVersion string
}

type ConfigInfo struct {
	SettingsPath    string
	OverrideSources []string
	Supervisor      string
	AutoCompaction  bool
	Questions       bool
	Debug           bool
}

type SubscriptionInfo struct {
	Applicable bool
	Summary    string
	Error      string
	Windows    []SubscriptionWindow
}

type SubscriptionWindow struct {
	Label       string
	Qualifier   string
	UsedPercent float64
	ResetAt     time.Time
}

type SkillInspection struct {
	Name        string
	Description string
	Path        string
	SourceKind  string
	Loaded      bool
	Disabled    bool
	Shadowed    bool
	Reason      string
}

type Section string

const (
	SectionBase        Section = "base"
	SectionAuth        Section = "account"
	SectionGit         Section = "git"
	SectionEnvironment Section = "environment"
)

type SeedResult struct {
	Snapshot        Snapshot
	PendingSections []Section
	Warnings        map[Section]string
}

type AuthStageResult struct {
	Auth         AuthInfo
	Subscription SubscriptionInfo
	Warning      string
}

type GitStageResult struct {
	Git GitInfo
}

type EnvironmentStageResult struct {
	Skills           []SkillInspection
	SkillTokenCounts map[string]int
	AgentsPaths      []string
	AgentTokenCounts map[string]int
	CollectorWarning string
}

type memoryRepository struct {
	mu        sync.Mutex
	authByKey map[string]authCacheEntry
	gitByKey  map[string]gitCacheEntry
	envByKey  map[string]environmentCacheEntry
}

type authCacheEntry struct {
	fetchedAt time.Time
	result    AuthStageResult
}

type gitCacheEntry struct {
	fetchedAt time.Time
	result    GitStageResult
}

type environmentCacheEntry struct {
	fetchedAt time.Time
	result    EnvironmentStageResult
}

func NewMemoryRepository() Repository {
	return &memoryRepository{
		authByKey: map[string]authCacheEntry{},
		gitByKey:  map[string]gitCacheEntry{},
		envByKey:  map[string]environmentCacheEntry{},
	}
}

func (r *memoryRepository) SeedSnapshot(req Request, base Snapshot, now time.Time) SeedResult {
	r.mu.Lock()
	defer r.mu.Unlock()

	seed := SeedResult{Snapshot: base, Warnings: map[Section]string{}}

	authEntry, authCached := r.authByKey[strings.TrimSpace(req.CacheKeys.Auth)]
	if req.AuthCacheUnseedable {
		authCached = false
	}
	if authCached {
		seed.Snapshot.Auth = authEntry.result.Auth
		seed.Snapshot.Subscription = authEntry.result.Subscription
		if warning := strings.TrimSpace(authEntry.result.Warning); warning != "" {
			seed.Warnings[SectionAuth] = warning
		}
	}
	if !authCached || now.Sub(authEntry.fetchedAt) > authCacheFreshness {
		seed.PendingSections = append(seed.PendingSections, SectionAuth)
	}

	gitEntry, gitCached := r.gitByKey[strings.TrimSpace(req.CacheKeys.Git)]
	if gitCached {
		seed.Snapshot.Git = gitEntry.result.Git
	}
	if !gitCached || !gitEntry.result.Git.Visible || now.Sub(gitEntry.fetchedAt) > gitCacheFreshness {
		seed.PendingSections = append(seed.PendingSections, SectionGit)
	}

	envEntry, envCached := r.envByKey[strings.TrimSpace(req.CacheKeys.Environment)]
	if envCached {
		seed.Snapshot.Skills = append([]SkillInspection(nil), envEntry.result.Skills...)
		seed.Snapshot.SkillTokenCounts = CloneTokenMap(envEntry.result.SkillTokenCounts)
		seed.Snapshot.AgentsPaths = append([]string(nil), envEntry.result.AgentsPaths...)
		seed.Snapshot.AgentTokenCounts = CloneTokenMap(envEntry.result.AgentTokenCounts)
		if warning := strings.TrimSpace(envEntry.result.CollectorWarning); warning != "" {
			seed.Warnings[SectionEnvironment] = warning
		}
	}
	if !envCached || now.Sub(envEntry.fetchedAt) > environmentCacheFreshness {
		seed.PendingSections = append(seed.PendingSections, SectionEnvironment)
	}

	if len(seed.Warnings) == 0 {
		seed.Warnings = nil
	}
	return seed
}

func (r *memoryRepository) StoreAuth(cacheKey string, result AuthStageResult, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if strings.TrimSpace(cacheKey) == "" {
		return
	}
	r.authByKey[cacheKey] = authCacheEntry{fetchedAt: repositoryTime(now), result: result}
}

func (r *memoryRepository) StoreGit(cacheKey string, result GitStageResult, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if strings.TrimSpace(cacheKey) == "" {
		return
	}
	r.gitByKey[cacheKey] = gitCacheEntry{fetchedAt: repositoryTime(now), result: result}
}

func (r *memoryRepository) StoreEnvironment(cacheKey string, result EnvironmentStageResult, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if strings.TrimSpace(cacheKey) == "" {
		return
	}
	r.envByKey[cacheKey] = environmentCacheEntry{fetchedAt: repositoryTime(now), result: result}
}

func repositoryTime(now time.Time) time.Time {
	if now.IsZero() {
		return time.Now()
	}
	return now
}

func AuthCacheKey(req Request) string {
	identity := strings.TrimSpace(req.AuthCacheIdentity)
	if identity == "" {
		identity = "auth:none"
	}
	return strings.Join([]string{
		strings.TrimSpace(req.Settings.OpenAIBaseURL),
		strings.TrimSpace(req.Settings.ProviderOverride),
		identity,
	}, "|")
}

func GitCacheKey(workdir string) string {
	trimmed := strings.TrimSpace(workdir)
	if trimmed == "" {
		return ""
	}
	normalized := strings.ReplaceAll(trimmed, "\\", "/")
	return path.Clean(normalized)
}

func ExecutionTarget(req Request) clientui.SessionExecutionTarget {
	if !clientui.SessionExecutionTargetIsZero(req.ExecutionTarget) {
		return req.ExecutionTarget
	}
	if req.Runtime == nil {
		return clientui.SessionExecutionTarget{}
	}
	return req.Runtime.SessionView().ExecutionTarget
}

func EnvironmentRoot(workspaceRoot string, target clientui.SessionExecutionTarget) string {
	if target.Worktree != nil {
		if worktreeRoot := strings.TrimSpace(target.Worktree.Root); worktreeRoot != "" {
			return worktreeRoot
		}
	}
	if registeredWorkspaceRoot := strings.TrimSpace(target.WorkspaceRoot); registeredWorkspaceRoot != "" {
		return registeredWorkspaceRoot
	}
	return strings.TrimSpace(workspaceRoot)
}

func Workdir(workspaceRoot string, target clientui.SessionExecutionTarget) string {
	if workdir := strings.TrimSpace(target.EffectiveWorkdir); workdir != "" {
		return workdir
	}
	workdir := strings.TrimSpace(workspaceRoot)
	if workdir != "" {
		return workdir
	}
	if cwd, err := os.Getwd(); err == nil {
		return strings.TrimSpace(cwd)
	}
	return ""
}

func GitRoot(req Request) string {
	target := ExecutionTarget(req)
	if target.Worktree != nil {
		worktreeRoot := strings.TrimSpace(target.Worktree.Root)
		if worktreeRoot != "" {
			return worktreeRoot
		}
	}
	if workspaceRoot := strings.TrimSpace(req.WorkspaceRoot); workspaceRoot != "" {
		return workspaceRoot
	}
	if workspaceRoot := strings.TrimSpace(target.WorkspaceRoot); workspaceRoot != "" {
		return workspaceRoot
	}
	return ""
}

func CloneTokenMap(input map[string]int) map[string]int {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]int, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func CollectGitStatus(ctx context.Context, workdir string, timeout time.Duration, envSanitizer func([]string) []string) GitInfo {
	trimmedWorkdir := strings.TrimSpace(workdir)
	if trimmedWorkdir == "" {
		return GitInfo{}
	}
	if _, err := exec.LookPath("git"); err != nil {
		return GitInfo{}
	}
	isRepo, probeErr := GitRepositoryProbe(trimmedWorkdir)
	if probeErr != nil {
		return GitInfo{Visible: true, Error: GitError(probeErr, "")}
	}
	if !isRepo {
		return GitInfo{}
	}
	gitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(gitCtx, "git", "-C", trimmedWorkdir, "status", "--porcelain=v2", "--branch")
	cmd.Env = os.Environ()
	if envSanitizer != nil {
		cmd.Env = envSanitizer(cmd.Env)
	}
	out, err := cmd.CombinedOutput()
	if gitCtx.Err() == context.DeadlineExceeded || err != nil {
		return GitInfo{Visible: true, Error: GitError(err, string(out))}
	}
	gitInfo := GitInfo{Visible: true}
	for _, line := range splitPlainLines(strings.TrimSpace(string(out))) {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "# branch.head ") {
			gitInfo.Branch = strings.TrimSpace(strings.TrimPrefix(trimmed, "# branch.head "))
			if gitInfo.Branch == "(detached)" {
				gitInfo.Branch = "detached"
			}
			continue
		}
		if strings.HasPrefix(trimmed, "# branch.ab ") {
			fields := strings.Fields(strings.TrimPrefix(trimmed, "# branch.ab "))
			for _, field := range fields {
				if strings.HasPrefix(field, "+") {
					fmt.Sscanf(strings.TrimPrefix(field, "+"), "%d", &gitInfo.Ahead)
				}
				if strings.HasPrefix(field, "-") {
					fmt.Sscanf(strings.TrimPrefix(field, "-"), "%d", &gitInfo.Behind)
				}
			}
			continue
		}
		if !strings.HasPrefix(trimmed, "#") {
			gitInfo.Dirty = true
		}
	}
	if gitInfo.Branch == "" {
		gitInfo.Branch = "unknown"
	}
	return gitInfo
}

func GitRepositoryProbe(workdir string) (bool, error) {
	info, err := os.Stat(workdir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("inspect git workdir: %w", err)
	}
	if !info.IsDir() {
		return false, nil
	}
	current := filepath.Clean(workdir)
	if resolved, err := filepath.EvalSymlinks(current); err == nil {
		current = filepath.Clean(resolved)
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("resolve git workdir: %w", err)
	}
	for {
		gitMetadataPath := filepath.Join(current, ".git")
		if _, err := os.Lstat(gitMetadataPath); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("inspect git metadata: %w", err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false, nil
		}
		current = parent
	}
}

func GitError(err error, output string) string {
	message := strings.TrimSpace(output)
	if message == "" && err != nil {
		message = strings.TrimSpace(err.Error())
	}
	if message == "" {
		return "git status failed"
	}
	return "git status failed: " + message
}

func splitPlainLines(v string) []string {
	if strings.TrimSpace(v) == "" {
		return []string{""}
	}
	return strings.Split(v, "\n")
}
