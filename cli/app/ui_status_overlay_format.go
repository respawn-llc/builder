package app

import (
	sharedtheme "core/shared/theme"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

const statusSubscriptionBarMaxWidth = 18

func statusSubscriptionRemaining(usedPercent float64) float64 {
	remaining := 100 - statusClampPercent(usedPercent)
	if remaining < 0 {
		return 0
	}
	if remaining > 100 {
		return 100
	}
	return remaining
}

func statusClampPercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func statusSubscriptionBarWidth(width int) int {
	barWidth := width / 5
	if barWidth > statusSubscriptionBarMaxWidth {
		barWidth = statusSubscriptionBarMaxWidth
	}
	if barWidth < 4 {
		barWidth = 4
	}
	return barWidth
}

func statusSubscriptionBarWidthForLine(width int, label, leftText, metaText string) int {
	reserved := lipgloss.Width(label) + lipgloss.Width(leftText) + 6
	if strings.TrimSpace(metaText) != "" {
		reserved += lipgloss.Width("• "+metaText) + 1
	}
	available := width - reserved
	if available < 4 {
		available = 4
	}
	if available > statusSubscriptionBarMaxWidth {
		available = statusSubscriptionBarMaxWidth
	}
	return available
}

func statusSubscriptionLabelWidth(windows []uiStatusSubscriptionWindow) int {
	width := 0
	for _, window := range windows {
		label := strings.TrimSpace(window.Label)
		if label == "" {
			label = "limit"
		}
		labelWidth := lipgloss.Width(label)
		if labelWidth > width {
			width = labelWidth
		}
	}
	if width < 1 {
		return 1
	}
	return width
}

func statusLocalTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.In(time.Local).Format("Jan 2, 3:04 PM MST")
}

func statusSubscriptionResetMeta(resetAt, now time.Time) string {
	if resetAt.IsZero() {
		return ""
	}
	local := statusLocalTime(resetAt)
	relative := statusRelativeDuration(resetAt.Sub(now))
	if relative == "" {
		return local
	}
	if local == "" {
		return "in " + relative
	}
	return "in " + relative + " at " + local
}

func statusRelativeDuration(value time.Duration) string {
	if value <= 0 {
		return "0m"
	}
	rounded := value.Round(time.Minute)
	if rounded < time.Minute {
		rounded = time.Minute
	}
	totalMinutes := int(rounded / time.Minute)
	days := totalMinutes / (24 * 60)
	totalMinutes -= days * 24 * 60
	hours := totalMinutes / 60
	minutes := totalMinutes - (hours * 60)
	parts := make([]string, 0, 3)
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if minutes > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	return strings.Join(parts, "")
}

func statusVisibleAuthSummary(auth uiStatusAuthInfo, subscription uiStatusSubscriptionInfo) string {
	if !auth.Visible {
		return ""
	}
	summary := strings.TrimSpace(auth.Summary)
	if summary == "" {
		return ""
	}
	if strings.EqualFold(summary, "subscription") && strings.TrimSpace(subscription.Summary) != "" {
		return ""
	}
	return summary
}

func statusSessionProvenanceSummaries(snapshot uiStatusSnapshot) []string {
	summaries := make([]string, 0, 2)
	if snapshot.PreviousSessionID != nil {
		previousID := snapshot.PreviousSessionID.String()
		if previousName := strings.TrimSpace(snapshot.PreviousSessionName); previousName != "" {
			summaries = append(summaries, fmt.Sprintf("Previous session: %s <%s>", previousName, previousID))
		} else {
			summaries = append(summaries, fmt.Sprintf("Previous session: %s", previousID))
		}
	}
	if snapshot.ParentAgentSessionID != nil {
		parentAgentID := snapshot.ParentAgentSessionID.String()
		if parentAgentName := strings.TrimSpace(snapshot.ParentAgentSessionName); parentAgentName != "" {
			summaries = append(summaries, fmt.Sprintf("Parent agent session: %s <%s>", parentAgentName, parentAgentID))
		} else {
			summaries = append(summaries, fmt.Sprintf("Parent agent session: %s", parentAgentID))
		}
	}
	return summaries
}

func statusPartitionSkills(skills []uiStatusSkillInspection) ([]uiStatusSkillInspection, []uiStatusSkillInspection) {
	loaded := make([]uiStatusSkillInspection, 0, len(skills))
	unloaded := make([]uiStatusSkillInspection, 0, len(skills))
	for _, skill := range skills {
		if skill.Loaded {
			loaded = append(loaded, skill)
			continue
		}
		unloaded = append(unloaded, skill)
	}
	return loaded, unloaded
}

func statusDisplayPath(path, workdir string) string {
	trimmed := filepath.ToSlash(strings.TrimSpace(path))
	if trimmed == "" {
		return "<unknown>"
	}
	if work := filepath.ToSlash(strings.TrimSpace(workdir)); work != "" {
		if trimmed == work {
			return trimmed
		}
		if strings.HasPrefix(trimmed, work+"/") {
			return "." + strings.TrimPrefix(trimmed, work)
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		home = filepath.ToSlash(strings.TrimSpace(home))
		if home != "" {
			if trimmed == home {
				return "~"
			}
			if strings.HasPrefix(trimmed, home+"/") {
				return "~" + strings.TrimPrefix(trimmed, home)
			}
		}
	}
	return trimmed
}

func statusValueOrFallback(value, fallback string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fallback
	}
	return trimmed
}

func statusTokenShort(tokens int) string {
	if tokens <= 0 {
		return "0k"
	}
	whole := tokens / 1000
	remainder := tokens % 1000
	if whole >= 10 || remainder == 0 {
		return fmt.Sprintf("%dk", whole)
	}
	return fmt.Sprintf("%d.%dk", whole, remainder/100)
}

func statusPercent(value, total int) string {
	if total <= 0 || value <= 0 {
		return "0%"
	}
	pct := (value * 100) / total
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return fmt.Sprintf("%d%%", pct)
}

func statusSkillLineStyled(skill uiStatusSkillInspection, tokenCounts map[string]int, generatedStyle lipgloss.Style) string {
	name := strings.TrimSpace(skill.Name)
	if name == "" {
		name = filepath.Base(filepath.Dir(skill.Path))
	}
	labels := make([]string, 0, 3)
	if skill.SourceKind == "generated" {
		labels = append(labels, generatedStyle.Render("generated"))
	}
	if skill.Disabled {
		labels = append(labels, lipgloss.NewStyle().Foreground(sharedtheme.DefaultPalette().Status.Error.Adaptive()).Bold(true).Render("disabled"))
	}
	if skill.Shadowed {
		labels = append(labels, "shadowed")
	}
	if skill.Disabled || skill.Shadowed {
		return fmt.Sprintf("%s %s", name, strings.Join(labels, " "))
	}
	summary := fmt.Sprintf("%s (%s)", name, statusTokenShort(tokenCounts[strings.TrimSpace(skill.Path)]))
	if len(labels) > 0 {
		return summary + " " + strings.Join(labels, " ")
	}
	return summary
}

func statusSkillFailureLine(skill uiStatusSkillInspection) string {
	name := strings.TrimSpace(skill.Name)
	if name == "" {
		name = filepath.Base(filepath.Dir(skill.Path))
	}
	reason := strings.TrimSpace(skill.Reason)
	if reason == "" {
		return name
	}
	return fmt.Sprintf("%s (%s)", name, reason)
}

type statusSkillDirectoryGroup struct {
	Directory string
	Skills    []uiStatusSkillInspection
}

func statusGroupSkillsByDirectory(skills []uiStatusSkillInspection) []statusSkillDirectoryGroup {
	groups := make([]statusSkillDirectoryGroup, 0, len(skills))
	indexByDirectory := map[string]int{}
	for _, skill := range skills {
		directory := statusSkillDirectory(skill.Path)
		idx, ok := indexByDirectory[directory]
		if !ok {
			idx = len(groups)
			indexByDirectory[directory] = idx
			groups = append(groups, statusSkillDirectoryGroup{Directory: directory})
		}
		groups[idx].Skills = append(groups[idx].Skills, skill)
	}
	return groups
}

func statusSkillDirectory(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	directory := filepath.Dir(trimmed)
	parent := filepath.Dir(directory)
	if parent == "." || parent == string(filepath.Separator) || parent == directory {
		return directory
	}
	return parent
}

func statusPadRight(text string, width int) string {
	padding := width - lipgloss.Width(text)
	if padding <= 0 {
		return text
	}
	return text + strings.Repeat(" ", padding)
}
