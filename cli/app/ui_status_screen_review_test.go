package app

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"core/shared/config"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestStatusOverlayScreenReview(t *testing.T) {
	scenarios := []struct {
		name   string
		width  int
		height int
		setup  func(*uiModel)
	}{
		{
			name: "normal-disabled", width: 80, height: 24,
			setup: func(model *uiModel) {
				model.status.snapshot = statusScreenReviewSnapshot()
				model.status.snapshot.SkillDiscoveryState = config.SkillSubsystemDisabled
			},
		},
		{
			name: "narrow-disabled", width: 32, height: 12,
			setup: func(model *uiModel) {
				model.status.snapshot = statusScreenReviewSnapshot()
				model.status.snapshot.SkillDiscoveryState = config.SkillSubsystemDisabled
			},
		},
		{
			name: "loading-with-cached-base", width: 48, height: 14,
			setup: func(model *uiModel) {
				model.status.snapshot = statusScreenReviewSnapshot()
				model.status.snapshot.Skills = nil
				model.status.snapshot.SkillTokenCounts = nil
				model.status.pendingSections = map[uiStatusSection]bool{uiStatusSectionEnvironment: true}
			},
		},
		{
			name: "scrolled-lower-sections", width: 44, height: 10,
			setup: func(model *uiModel) {
				model.status.snapshot = statusScreenReviewSnapshot()
				for index := 0; index < 12; index++ {
					path := fmt.Sprintf("/workspace/docs/agents/%02d/AGENTS.md", index)
					model.status.snapshot.AgentsPaths = append(model.status.snapshot.AgentsPaths, path)
					model.status.snapshot.AgentTokenCounts[path] = 100 + index
				}
				model.status.scroll = 1 << 30
			},
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			model := newProjectedStaticUIModel()
			model.status.open = true
			scenario.setup(model)
			lines := model.layout().renderStatusOverlay(scenario.width, scenario.height, uiThemeStyles("dark"))
			if len(lines) != scenario.height {
				t.Fatalf("rendered line count = %d, want %d", len(lines), scenario.height)
			}
			for index, line := range lines {
				if width := lipgloss.Width(line); width > scenario.width {
					t.Fatalf("line %d width = %d, max %d", index, width, scenario.width)
				}
			}
			plain := make([]string, len(lines))
			for index, line := range lines {
				plain[index] = strings.TrimRight(ansi.Strip(line), " ")
			}
			t.Log("\n" + strings.Join(plain, "\n"))
		})
	}
}

func statusScreenReviewSnapshot() uiStatusSnapshot {
	skillPath := "/workspace/.kent/skills/apiresult/SKILL.md"
	return uiStatusSnapshot{
		CollectedAt: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
		Workdir:     "/workspace",
		SessionName: "screen-review",
		SessionID:   "session-screen-review",
		Model:       uiStatusModelInfo{Summary: "gpt-5.6-sol"},
		Context: uiStatusContextInfo{
			AvailableTokens: 250_000,
			WindowTokens:    372_000,
			ThresholdTokens: 353_400,
		},
		Config: uiStatusConfigInfo{
			SettingsPath:   "/workspace/.kent/config.toml",
			Supervisor:     "edits",
			AutoCompaction: true,
			Questions:      true,
		},
		Skills: []uiStatusSkillInspection{{
			Name:        "apiresult",
			Description: "API result guidance",
			Path:        skillPath,
			SourceKind:  "workspace",
			Loaded:      true,
		}},
		SkillTokenCounts: map[string]int{skillPath: 240},
		AgentsPaths:      []string{"/workspace/AGENTS.md"},
		AgentTokenCounts: map[string]int{"/workspace/AGENTS.md": 320},
	}
}
