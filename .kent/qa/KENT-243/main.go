package main

import (
	"fmt"
	"os"
	"path/filepath"

	"core/internal/testharness/pty/blackbox"

	"github.com/google/uuid"
)

const (
	skillProbe = "3ba7d449-d50b-42fc-a486-dcd63f2b6fc0"
)

var fixtureConfig = []byte(`
model = "gpt-5"
provider_override = "openai"
openai_base_url = "http://127.0.0.1:1/v1"
theme = "dark"

[reviewer]
frequency = "off"

[skills]
enabled = false

[subagents.qa.skills]
enabled = true
`)

var fixtureSkill = []byte(`---
name: qa-live-skill
description: 3ba7d449-d50b-42fc-a486-dcd63f2b6fc0
---

Live KENT-243 QA fixture.
`)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./.kent/qa/KENT-243 /path/to/kent")
		os.Exit(2)
	}
	binary, err := filepath.Abs(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fixture := blackbox.RunFixture{
		Config: fixtureConfig,
		WorkspaceFiles: []blackbox.WorkspaceFixtureFile{{
			Path:    filepath.Join(".kent", "skills", "qa-live-skill", "SKILL.md"),
			Content: fixtureSkill,
		}},
	}

	if err := runModelScenario(binary, fixture, "global disable", nil, 1); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := runModelScenario(binary, fixture, "role re-enablement", []string{"--agent", "qa"}, 2); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	statusScreen, err := runStatusScenario(binary, fixture)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Println("global disable: PASS (parent request has no skills developer-message slot)")
	fmt.Println("role re-enablement: PASS (qa-role request restores the skills developer-message slot)")
	fmt.Println("/status: PASS (live disabled-policy overlay opened in alternate screen)")
	fmt.Println("--- /status screen for manual neutral-state review ---")
	fmt.Print(statusScreen)
}

func runModelScenario(binary string, fixture blackbox.RunFixture, name string, clientArgs []string, developerMessageCount int) error {
	operation := blackbox.RequiredOperation{
		ID:                    uuid.New(),
		Route:                 blackbox.RouteResponses,
		DeveloperMessageCount: &developerMessageCount,
		Outcome:               blackbox.OutcomeStream,
		ResponsePhase:         blackbox.NewResponsePhase(blackbox.ResponsePhaseFinal),
	}
	output := "ok"
	operation.Output = &output
	scenario := blackbox.Scenario{
		Version:         1,
		ID:              uuid.New(),
		Dimensions:      blackbox.Dimensions{Rows: 32, Cols: 100},
		ModelOperations: []blackbox.RequiredOperation{operation},
		Actions: []blackbox.Action{
			waitAction(blackbox.PredicatePromptReady),
			inputAction("verify " + name),
			{ID: uuid.New(), Kind: blackbox.ActionSubmitPrompt},
			waitAction(blackbox.PredicateModelConsumed),
		},
	}
	result := (blackbox.Runner{}).Run(blackbox.RunRequest{
		Scenario: scenario, Profile: blackbox.GoProfile, ClientBinary: binary, ServerBinary: binary,
		ClientArgs: clientArgs, Fixture: fixture,
	})
	if result.Err != nil {
		return fmt.Errorf("%s scenario: %w; run_root=%s artifacts=%s", name, result.Err, result.RunRoot, result.ArtifactDir)
	}
	if result.Cleanup != nil {
		return fmt.Errorf("%s scenario cleanup: %w", name, result.Cleanup)
	}
	return nil
}

func runStatusScenario(binary string, fixture blackbox.RunFixture) (string, error) {
	enabled := true
	mode := 1049
	scenario := blackbox.Scenario{
		Version:    1,
		ID:         uuid.New(),
		Dimensions: blackbox.Dimensions{Rows: 32, Cols: 80},
		Actions: []blackbox.Action{
			waitAction(blackbox.PredicatePromptReady),
			inputAction("/status"),
			{ID: uuid.New(), Kind: blackbox.ActionSubmitPrompt},
			{
				ID:   uuid.New(),
				Kind: blackbox.ActionWait,
				Predicate: &blackbox.Predicate{
					Kind: blackbox.PredicateAll,
					Children: []blackbox.Predicate{
						{Kind: blackbox.PredicatePrivateMode, Mode: &mode, Enabled: &enabled},
						{Kind: blackbox.PredicateNonBlank},
					},
				},
			},
			{ID: uuid.New(), Kind: blackbox.ActionPressKey, Key: key(blackbox.KeyEnd)},
			waitAction(blackbox.PredicateNonBlank),
		},
	}
	result := (blackbox.Runner{}).Run(blackbox.RunRequest{
		Scenario: scenario, Profile: blackbox.GoProfile, ClientBinary: binary, ServerBinary: binary, Fixture: fixture,
	})
	if result.Err != nil {
		return "", fmt.Errorf("/status scenario: %w; run_root=%s artifacts=%s", result.Err, result.RunRoot, result.ArtifactDir)
	}
	if result.Cleanup != nil {
		return "", fmt.Errorf("/status scenario cleanup: %w", result.Cleanup)
	}
	if result.Observation.Analysis == nil {
		return "", fmt.Errorf("/status scenario produced no terminal analysis")
	}
	return result.Observation.Analysis.Screen.RenderText(), nil
}

func waitAction(kind blackbox.PredicateKind) blackbox.Action {
	return blackbox.Action{
		ID:        uuid.New(),
		Kind:      blackbox.ActionWait,
		Predicate: &blackbox.Predicate{Kind: kind},
	}
}

func inputAction(input string) blackbox.Action {
	return blackbox.Action{ID: uuid.New(), Kind: blackbox.ActionEnterInput, Input: &input}
}

func key(value blackbox.Key) *blackbox.Key {
	return &value
}
