package status

import (
	"slices"
	"testing"
	"time"

	"core/shared/config"
)

func TestEnvironmentCacheRejectsMismatchedSkillPolicies(t *testing.T) {
	tests := []struct {
		name    string
		cached  config.Settings
		request config.Settings
	}{
		{
			name:    "disabled name changes",
			cached:  config.Settings{SkillToggles: map[string]bool{"apiresult": false}},
			request: config.Settings{SkillToggles: map[string]bool{"other": false}},
		},
		{
			name:    "disabled name removed",
			cached:  config.Settings{SkillToggles: map[string]bool{"apiresult": false}},
			request: config.Settings{},
		},
		{
			name:    "skill named enabled changes",
			cached:  config.Settings{SkillToggles: map[string]bool{"enabled": false}},
			request: config.Settings{SkillToggles: map[string]bool{"enabled": true}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
			repository := NewMemoryRepository()
			cachedPolicy := config.ResolveSkillPolicy(tt.cached)
			repository.StoreEnvironment("shared-workspace", EnvironmentStageResult{
				SkillPolicy: cachedPolicy,
				Skills:      []SkillInspection{{Name: "stale", Path: "/stale/SKILL.md", Loaded: true}},
			}, now)

			seed := repository.SeedSnapshot(Request{
				CacheKeys: CacheKeys{Environment: "shared-workspace"},
				Settings:  tt.request,
			}, Snapshot{}, now)

			if len(seed.Snapshot.Skills) != 0 {
				t.Fatalf("mismatched policy seeded stale skills: %+v", seed.Snapshot.Skills)
			}
			if !slices.Contains(seed.PendingSections, SectionEnvironment) {
				t.Fatalf("mismatched policy did not schedule environment refresh: %+v", seed.PendingSections)
			}
		})
	}
}

func TestEnvironmentCacheSeedsEquivalentSkillPolicy(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	repository := NewMemoryRepository()
	settings := config.Settings{SkillToggles: map[string]bool{" APIResult ": false}}
	policy := config.ResolveSkillPolicy(settings)
	repository.StoreEnvironment("workspace", EnvironmentStageResult{
		SkillPolicy: policy,
		Skills:      []SkillInspection{{Name: "cached", Loaded: true}},
	}, now)

	seed := repository.SeedSnapshot(Request{
		CacheKeys: CacheKeys{Environment: "workspace"},
		Settings:  config.Settings{SkillToggles: map[string]bool{"apiresult": false}},
	}, Snapshot{}, now)

	if len(seed.Snapshot.Skills) != 1 || seed.Snapshot.Skills[0].Name != "cached" {
		t.Fatalf("equivalent policy did not seed cached skills: %+v", seed.Snapshot.Skills)
	}
	if slices.Contains(seed.PendingSections, SectionEnvironment) {
		t.Fatalf("fresh equivalent policy scheduled unnecessary refresh: %+v", seed.PendingSections)
	}
}
