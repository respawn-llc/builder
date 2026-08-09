package status

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"core/prompts"
	"core/server/runtime"
	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
)

type Collector struct {
	RequestTimeout         time.Duration
	GitTimeout             time.Duration
	SessionNameReadTimeout time.Duration
	EnvSanitizer           func([]string) []string
}

func (c Collector) Collect(ctx context.Context, req Request) (Snapshot, error) {
	snapshot := c.EnrichBase(ctx, req, c.CollectBase(req))
	authResult := c.CollectAuth(ctx, req, snapshot)
	gitResult := c.CollectGit(ctx, req, snapshot)
	envResult := c.CollectEnvironment(ctx, req, snapshot)
	snapshot.Auth = authResult.Auth
	snapshot.Subscription = authResult.Subscription
	snapshot.Git = gitResult.Git
	snapshot.SkillPolicy = envResult.SkillPolicy
	snapshot.Skills = envResult.Skills
	snapshot.SkillTokenCounts = envResult.SkillTokenCounts
	snapshot.AgentsPaths = envResult.AgentsPaths
	snapshot.AgentTokenCounts = envResult.AgentTokenCounts
	snapshot.CollectorWarning = JoinWarnings(snapshot.CollectorWarning, authResult.Warning)
	snapshot.CollectorWarning = JoinWarnings(snapshot.CollectorWarning, envResult.CollectorWarning)
	return snapshot, nil
}

func (c Collector) CollectBase(req Request) Snapshot {
	collectedAt := req.CurrentTime
	if collectedAt.IsZero() {
		collectedAt = time.Now()
	}
	target := ExecutionTarget(req)
	workdir := Workdir(req.WorkspaceRoot, target)
	contextInfo := ContextInfo{ThresholdTokens: req.Settings.ContextCompactionThresholdTokens}
	var previousSessionID *runtimeids.SessionID
	var parentAgentSessionID *runtimeids.SessionID
	compactionCount := 0
	if req.Runtime != nil {
		status := req.Runtime.Status()
		usage := status.ContextUsage
		contextInfo.UsedTokens = usage.UsedTokens
		contextInfo.WindowTokens = usage.WindowTokens
		contextInfo.AvailableTokens = usage.WindowTokens - usage.UsedTokens
		if contextInfo.AvailableTokens < 0 {
			contextInfo.AvailableTokens = 0
		}
		if status.PreviousSessionID != nil {
			id := *status.PreviousSessionID
			previousSessionID = &id
		}
		if status.ParentAgentSessionID != nil {
			id := *status.ParentAgentSessionID
			parentAgentSessionID = &id
		}
		compactionCount = status.CompactionCount
	}
	return Snapshot{
		CollectedAt:          collectedAt,
		Workdir:              filepath.ToSlash(strings.TrimSpace(workdir)),
		SessionName:          strings.TrimSpace(req.SessionName),
		SessionID:            strings.TrimSpace(req.SessionID),
		AgentRole:            textutil.Pointer(req.AgentRole),
		PreviousSessionID:    previousSessionID,
		ParentAgentSessionID: parentAgentSessionID,
		Context:              contextInfo,
		Model:                ModelInfo{Summary: ModelSummary(req)},
		Config: ConfigInfo{
			SettingsPath:    filepath.ToSlash(strings.TrimSpace(req.Source.SettingsPath)),
			OverrideSources: ConfigOverrideSources(req.Source),
			Supervisor:      SupervisorLabel(req.ReviewerEnabled, strings.TrimSpace(req.ReviewerMode)),
			AutoCompaction:  req.AutoCompactionEnabled,
			Questions:       req.QuestionsEnabled,
			Debug:           req.Settings.Debug,
		},
		CompactionCount: compactionCount,
	}
}

func (c Collector) EnrichBase(ctx context.Context, req Request, snapshot Snapshot) Snapshot {
	if sessionID := strings.TrimSpace(snapshot.SessionID); sessionID != "" && req.SessionViews != nil {
		currentSession, err := c.resolveSessionView(ctx, req.SessionViews, sessionID)
		if err != nil {
			snapshot.CollectorWarning = JoinWarnings(snapshot.CollectorWarning, "current session: "+err.Error())
		} else {
			snapshot.AgentRole = textutil.Pointer(currentSession.AgentRole)
		}
	}
	var warning string
	snapshot.PreviousSessionName, warning = c.relatedSessionName(ctx, req.SessionViews, snapshot.PreviousSessionID, "previous session")
	snapshot.CollectorWarning = JoinWarnings(snapshot.CollectorWarning, warning)
	snapshot.ParentAgentSessionName, warning = c.relatedSessionName(ctx, req.SessionViews, snapshot.ParentAgentSessionID, "parent agent session")
	snapshot.CollectorWarning = JoinWarnings(snapshot.CollectorWarning, warning)
	return snapshot
}

func JoinWarnings(existing string, warning string) string {
	parts := []string{strings.TrimSpace(existing), strings.TrimSpace(warning)}
	return strings.Join(slices.DeleteFunc(parts, func(value string) bool {
		return value == ""
	}), " | ")
}

func (c Collector) relatedSessionName(ctx context.Context, sessionViews apicontract.SessionViewService, sessionID *runtimeids.SessionID, label string) (string, string) {
	if sessionID == nil {
		return "", ""
	}
	sessionView, err := c.resolveSessionView(ctx, sessionViews, sessionID.String())
	if err != nil {
		return "", label + ": " + err.Error()
	}
	return strings.TrimSpace(sessionView.SessionName), ""
}

func (c Collector) resolveSessionView(ctx context.Context, sessionViews apicontract.SessionViewService, sessionID string) (clientui.RuntimeSessionView, error) {
	id := strings.TrimSpace(sessionID)
	if sessionViews == nil || id == "" {
		return clientui.RuntimeSessionView{}, nil
	}
	readTimeout := c.SessionNameReadTimeout
	if readTimeout <= 0 {
		readTimeout = c.RequestTimeout
		if readTimeout <= 0 {
			readTimeout = 10 * time.Second
		}
	}
	readCtx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	resp, err := sessionViews.GetSessionMainView(readCtx, serverapi.SessionMainViewRequest{SessionID: id})
	if err != nil {
		return clientui.RuntimeSessionView{}, err
	}
	return resp.MainView.Session, nil
}

func (c Collector) CollectAuth(ctx context.Context, req Request, _ Snapshot) AuthStageResult {
	if req.AuthStatus == nil {
		return AuthStageResult{}
	}
	response, err := req.AuthStatus.GetAuthStatus(ctx, serverapi.AuthStatusRequest{Provider: req.AuthProvider})
	if err != nil {
		return UnavailableAuthStage(err, req.AuthProvider)
	}
	return AuthStageFromResponse(response, req.AuthProvider)
}

func (c Collector) CollectGit(ctx context.Context, req Request, _ Snapshot) GitStageResult {
	gitTimeout := c.GitTimeout
	if gitTimeout <= 0 {
		gitTimeout = 4 * time.Second
	}
	return GitStageResult{Git: CollectGitStatus(ctx, GitRoot(req), gitTimeout, c.EnvSanitizer)}
}

func (Collector) CollectEnvironment(_ context.Context, req Request, _ Snapshot) EnvironmentStageResult {
	result := EnvironmentStageResult{SkillPolicy: config.ResolveSkillPolicy(req.Settings)}
	warnings := make([]string, 0, 3)
	workspaceRoot := EnvironmentRoot(req.WorkspaceRoot, ExecutionTarget(req))
	if recovered, err := prompts.RecoveredRootNonEmptyFor(req.PersistenceRoot); err != nil {
		warnings = append(warnings, "generated: "+err.Error())
	} else if recovered {
		if warning, warnErr := prompts.RecoveredWarningFor(req.PersistenceRoot); warnErr != nil {
			warnings = append(warnings, "generated: "+warnErr.Error())
		} else {
			warnings = append(warnings, warning)
		}
	}
	inspectedSkills, skillsErr := runtime.InspectSkills(workspaceRoot, req.PersistenceRoot, result.SkillPolicy)
	if skillsErr != nil {
		warnings = append(warnings, "skills: "+skillsErr.Error())
	} else {
		skills := SkillInspectionsFromRuntime(inspectedSkills)
		result.Skills = skills
		result.SkillTokenCounts = EstimateSkillTokens(skills)
	}
	agentsPaths, agentsErr := runtime.InstalledAgentsPaths(workspaceRoot, req.PersistenceRoot)
	if agentsErr != nil {
		warnings = append(warnings, "agents: "+agentsErr.Error())
	} else {
		result.AgentsPaths = agentsPaths
		result.AgentTokenCounts = EstimatePathTokens(agentsPaths)
	}
	result.CollectorWarning = strings.Join(warnings, " | ")
	return result
}
