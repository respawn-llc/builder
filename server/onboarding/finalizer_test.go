package onboarding_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"core/server/onboarding"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/toolspec"

	"github.com/google/uuid"
)

func TestFinalizerDefaultEqualChoicesRenderLikeDefaults(t *testing.T) {
	nullRoot := t.TempDir()
	nullHome := t.TempDir()
	defaultRoot := t.TempDir()
	defaultHome := t.TempDir()
	defaultModel := serverapi.OnboardingModelChoice{Kind: serverapi.OnboardingModelKnown, ModelID: "gpt-5.5"}
	defaultTheme := serverapi.OnboardingThemeAuto

	if _, err := newTestFinalizer(t, nullRoot, nullHome).FinalizeOnboarding(context.Background(), serverapi.OnboardingFinalizeRequest{}); err != nil {
		t.Fatalf("null finalize: %v", err)
	}
	if _, err := newTestFinalizer(t, defaultRoot, defaultHome).FinalizeOnboarding(context.Background(), serverapi.OnboardingFinalizeRequest{
		Model: &defaultModel,
		Theme: &defaultTheme,
	}); err != nil {
		t.Fatalf("default-equal finalize: %v", err)
	}
	nullConfig := readSettingsFile(t, nullRoot)
	defaultConfig := readSettingsFile(t, defaultRoot)
	if string(nullConfig) != string(defaultConfig) {
		t.Fatalf("default-equal choices should render exactly like omitted choices")
	}
}

func TestFinalizerProjectsModelContextThinkingVerbosityAskQuestionSupervisorAndCompaction(t *testing.T) {
	type want struct {
		model      string
		window     int
		threshold  int
		thinking   string
		verbosity  config.ModelVerbosity
		ask        *bool
		supervisor string
		compaction config.CompactionMode
	}
	trueValue := true
	falseValue := false
	tests := []struct {
		name string
		req  serverapi.OnboardingFinalizeRequest
		want want
	}{
		{
			name: "known model large context level thinking verbosity true ask supervisor override native compaction",
			req: serverapi.OnboardingFinalizeRequest{
				Model:         &serverapi.OnboardingModelChoice{Kind: serverapi.OnboardingModelKnown, ModelID: "gpt-5.4-mini"},
				ContextWindow: &serverapi.OnboardingContextWindowChoice{Kind: serverapi.OnboardingContextWindowLarge},
				Thinking:      &serverapi.OnboardingThinkingChoice{Kind: serverapi.OnboardingThinkingLevel, Level: "high"},
				Verbosity:     ptr(serverapi.OnboardingVerbosityHigh),
				AskQuestion:   &trueValue,
				Supervisor: &serverapi.OnboardingSupervisorChoice{
					Frequency: serverapi.OnboardingSupervisorAll,
					Model:     &serverapi.OnboardingModelChoice{Kind: serverapi.OnboardingModelKnown, ModelID: "gpt-5.4"},
					Thinking:  &serverapi.OnboardingThinkingChoice{Kind: serverapi.OnboardingThinkingCustom, Value: "xhigh"},
				},
				Compaction: ptr(serverapi.OnboardingCompactionNative),
			},
			want: want{
				model:      "gpt-5.4-mini",
				window:     400_000,
				threshold:  380_000,
				thinking:   "high",
				verbosity:  config.ModelVerbosityHigh,
				ask:        &trueValue,
				supervisor: "all",
				compaction: config.CompactionModeNative,
			},
		},
		{
			name: "custom model custom context disabled thinking false ask supervisor inheritance none compaction",
			req: serverapi.OnboardingFinalizeRequest{
				Model:         &serverapi.OnboardingModelChoice{Kind: serverapi.OnboardingModelCustom, Alias: "custom-openai-model"},
				ContextWindow: &serverapi.OnboardingContextWindowChoice{Kind: serverapi.OnboardingContextWindowCustom, Tokens: 123_456},
				Thinking:      &serverapi.OnboardingThinkingChoice{Kind: serverapi.OnboardingThinkingDisabled},
				AskQuestion:   &falseValue,
				Supervisor:    &serverapi.OnboardingSupervisorChoice{Frequency: serverapi.OnboardingSupervisorOff},
				Compaction:    ptr(serverapi.OnboardingCompactionNone),
			},
			want: want{
				model:      "custom-openai-model",
				window:     123_456,
				threshold:  117_283,
				thinking:   "",
				verbosity:  config.ModelVerbosityMedium,
				ask:        &falseValue,
				supervisor: "off",
				compaction: config.CompactionModeNone,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			home := t.TempDir()
			if _, err := newTestFinalizer(t, root, home).FinalizeOnboarding(context.Background(), tc.req); err != nil {
				t.Fatalf("FinalizeOnboarding: %v", err)
			}
			cfg := loadFinalizedConfig(t, root)
			if cfg.Settings.Model != tc.want.model {
				t.Fatalf("model = %q, want %q", cfg.Settings.Model, tc.want.model)
			}
			if cfg.Settings.ModelContextWindow != tc.want.window || cfg.Settings.ContextCompactionThresholdTokens != tc.want.threshold {
				t.Fatalf("window/threshold = %d/%d, want %d/%d", cfg.Settings.ModelContextWindow, cfg.Settings.ContextCompactionThresholdTokens, tc.want.window, tc.want.threshold)
			}
			if cfg.Settings.ThinkingLevel != tc.want.thinking || cfg.Settings.ModelVerbosity != tc.want.verbosity {
				t.Fatalf("thinking/verbosity = %q/%q, want %q/%q", cfg.Settings.ThinkingLevel, cfg.Settings.ModelVerbosity, tc.want.thinking, tc.want.verbosity)
			}
			if tc.want.ask != nil && cfg.Settings.EnabledTools[toolspec.ToolAskQuestion] != *tc.want.ask {
				t.Fatalf("ask question = %t, want %t", cfg.Settings.EnabledTools[toolspec.ToolAskQuestion], *tc.want.ask)
			}
			if cfg.Settings.Reviewer.Frequency != tc.want.supervisor || cfg.Settings.CompactionMode != tc.want.compaction {
				t.Fatalf("supervisor/compaction = %q/%q, want %q/%q", cfg.Settings.Reviewer.Frequency, cfg.Settings.CompactionMode, tc.want.supervisor, tc.want.compaction)
			}
		})
	}
}

func TestFinalizerAcceptsMinimumCustomContextWindow(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	if _, err := newTestFinalizer(t, root, home).FinalizeOnboarding(context.Background(), serverapi.OnboardingFinalizeRequest{
		ContextWindow: &serverapi.OnboardingContextWindowChoice{Kind: serverapi.OnboardingContextWindowCustom, Tokens: 50_000},
	}); err != nil {
		t.Fatalf("FinalizeOnboarding: %v", err)
	}
	cfg := loadFinalizedConfig(t, root)
	if cfg.Settings.ModelContextWindow != 50_000 {
		t.Fatalf("model context window = %d, want 50000", cfg.Settings.ModelContextWindow)
	}
	if effective := config.EffectivePreSubmitThresholdTokens(cfg.Settings.ContextCompactionThresholdTokens, cfg.Settings.PreSubmitCompactionLeadTokens); effective < config.MinimumThresholdTokens(cfg.Settings.ModelContextWindow) {
		t.Fatalf("effective pre-submit threshold = %d below minimum", effective)
	}
}

func TestFinalizerPreservesDisabledSupervisorThinking(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	if _, err := newTestFinalizer(t, root, home).FinalizeOnboarding(context.Background(), serverapi.OnboardingFinalizeRequest{
		Thinking: &serverapi.OnboardingThinkingChoice{Kind: serverapi.OnboardingThinkingLevel, Level: "high"},
		Supervisor: &serverapi.OnboardingSupervisorChoice{
			Frequency: serverapi.OnboardingSupervisorAll,
			Thinking:  &serverapi.OnboardingThinkingChoice{Kind: serverapi.OnboardingThinkingDisabled},
		},
	}); err != nil {
		t.Fatalf("FinalizeOnboarding: %v", err)
	}
	cfg := loadFinalizedConfig(t, root)
	if cfg.Settings.ThinkingLevel != "high" {
		t.Fatalf("main thinking = %q, want high", cfg.Settings.ThinkingLevel)
	}
	if cfg.Settings.Reviewer.ThinkingLevel != "" {
		t.Fatalf("reviewer thinking = %q, want disabled", cfg.Settings.Reviewer.ThinkingLevel)
	}
}

func TestFinalizerRejectsUnsupportedVerbosityForSelectedModel(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	_, err := newTestFinalizer(t, root, home).FinalizeOnboarding(context.Background(), serverapi.OnboardingFinalizeRequest{
		Model:     &serverapi.OnboardingModelChoice{Kind: serverapi.OnboardingModelCustom, Alias: "claude-3-7-sonnet"},
		Verbosity: ptr(serverapi.OnboardingVerbosityHigh),
	})
	if !errors.Is(err, serverapi.ErrOnboardingFinalizeInvalidRequest) {
		t.Fatalf("error = %v, want invalid_request", err)
	}
}

func TestFinalizerImportsSkillsAndCommandsBeforeConfigWrite(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	skillUUID := createProviderSkillSource(t, home, ".claude")
	commandUUID := createProviderCommandSource(t, home, ".claude")

	resp, err := newTestFinalizer(t, root, home).FinalizeOnboarding(context.Background(), serverapi.OnboardingFinalizeRequest{
		SkillsImport:   &serverapi.OnboardingImportSelection{Mode: serverapi.OnboardingImportModeSymlinkSource, ProviderUUID: &skillUUID},
		CommandsImport: &serverapi.OnboardingImportSelection{Mode: serverapi.OnboardingImportModeSymlinkSource, ProviderUUID: &commandUUID},
	})
	if err != nil {
		t.Fatalf("FinalizeOnboarding: %v", err)
	}
	if !resp.Completed {
		t.Fatal("expected finalize completion")
	}
	assertSymlink(t, filepath.Join(root, "skills"))
	assertSymlink(t, filepath.Join(root, "prompts"))
}

func TestFinalizerRollsBackImportsWhenConfigWriteFails(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	providerUUID := createProviderSkillSource(t, home, ".claude")
	blocker := filepath.Join(root, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	finalizer, err := onboarding.NewFinalizer(onboarding.Options{
		PersistenceRoot: root,
		HomeDir:         home,
		SettingsPath:    filepath.Join(blocker, "config.toml"),
	})
	if err != nil {
		t.Fatalf("NewFinalizer: %v", err)
	}

	_, err = finalizer.FinalizeOnboarding(context.Background(), serverapi.OnboardingFinalizeRequest{
		SkillsImport: &serverapi.OnboardingImportSelection{Mode: serverapi.OnboardingImportModeSymlinkSource, ProviderUUID: &providerUUID},
	})
	if !errors.Is(err, serverapi.ErrOnboardingFinalizeConfigWriteFailed) {
		t.Fatalf("error = %v, want config_write_failed", err)
	}
	if _, statErr := os.Lstat(filepath.Join(root, "skills")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("skills import should be rolled back, stat err=%v", statErr)
	}
}

func TestFinalizerExistingConfigWinsBeforeValidationAndSideEffects(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	if _, _, err := config.WriteDefaultSettingsFileAt(filepath.Join(root, "config.toml")); err != nil {
		t.Fatalf("write existing config: %v", err)
	}
	badTheme := serverapi.OnboardingTheme("blue")
	providerUUID := createProviderSkillSource(t, home, ".claude")

	_, err := newTestFinalizer(t, root, home).FinalizeOnboarding(context.Background(), serverapi.OnboardingFinalizeRequest{
		Theme:        &badTheme,
		SkillsImport: &serverapi.OnboardingImportSelection{Mode: serverapi.OnboardingImportModeSymlinkSource, ProviderUUID: &providerUUID},
	})
	if !errors.Is(err, serverapi.ErrOnboardingFinalizeConfigAlreadyExists) {
		t.Fatalf("error = %v, want config_already_exists", err)
	}
	if _, statErr := os.Lstat(filepath.Join(root, "skills")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("imports should not run when config already exists, stat err=%v", statErr)
	}
}

func TestFinalizerConcurrentFinalizeSerializesAndRechecksConfig(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	finalizer := newTestFinalizer(t, root, home)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := finalizer.FinalizeOnboarding(context.Background(), serverapi.OnboardingFinalizeRequest{})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	var successes, conflicts int
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		if errors.Is(err, serverapi.ErrOnboardingFinalizeConfigAlreadyExists) {
			conflicts++
			continue
		}
		t.Fatalf("unexpected concurrent finalize error: %v", err)
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("success/conflict counts = %d/%d, want 1/1", successes, conflicts)
	}
}

func TestFinalizerSkipsImportDiscoveryWhenNoImportsRequested(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".claude"), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write provider blocker: %v", err)
	}
	finalizer := newTestFinalizer(t, root, home)

	resp, err := finalizer.FinalizeOnboarding(context.Background(), serverapi.OnboardingFinalizeRequest{})
	if err != nil {
		t.Fatalf("FinalizeOnboarding: %v", err)
	}
	if !resp.Completed {
		t.Fatal("expected finalize completion")
	}
	if _, err := os.Stat(filepath.Join(root, "config.toml")); err != nil {
		t.Fatalf("expected config file: %v", err)
	}
}

func ptr[T any](value T) *T {
	return &value
}

func readSettingsFile(t *testing.T, root string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "config.toml"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	return data
}

func loadFinalizedConfig(t *testing.T, root string) config.App {
	t.Helper()
	cfg, err := config.LoadGlobal(config.LoadOptions{ConfigRoot: root})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

func assertSymlink(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is not a symlink", path)
	}
}

func TestFinalizerReturnsTargetExistsForRequestedImportBlockedByTarget(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	providerUUID := createProviderSkillSource(t, home, ".claude")
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "skills", "existing"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write target marker: %v", err)
	}
	finalizer := newTestFinalizer(t, root, home)

	_, err := finalizer.FinalizeOnboarding(context.Background(), serverapi.OnboardingFinalizeRequest{
		SkillsImport: &serverapi.OnboardingImportSelection{Mode: serverapi.OnboardingImportModeSymlinkSource, ProviderUUID: &providerUUID},
	})
	var finalizeErr *serverapi.OnboardingFinalizeError
	if !errors.As(err, &finalizeErr) {
		t.Fatalf("error = %T %v, want OnboardingFinalizeError", err, err)
	}
	if finalizeErr.Code != serverapi.OnboardingFinalizeImportUnavailable {
		t.Fatalf("code = %q, want import_unavailable", finalizeErr.Code)
	}
	details := finalizeErr.Details.(serverapi.OnboardingImportUnavailableDetails)
	if details.ReasonCode != serverapi.OnboardingImportReasonTargetExists {
		t.Fatalf("reason = %q, want target_exists", details.ReasonCode)
	}
	if _, err := os.Stat(filepath.Join(root, "config.toml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config should remain absent, stat err=%v", err)
	}
}

func TestFinalizerRollsBackEarlierImportWhenLaterImportSelectionFails(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	providerUUID := createProviderSkillSource(t, home, ".claude")
	missingCommandsUUID := providerUUID
	finalizer := newTestFinalizer(t, root, home)

	_, err := finalizer.FinalizeOnboarding(context.Background(), serverapi.OnboardingFinalizeRequest{
		SkillsImport:   &serverapi.OnboardingImportSelection{Mode: serverapi.OnboardingImportModeSymlinkSource, ProviderUUID: &providerUUID},
		CommandsImport: &serverapi.OnboardingImportSelection{Mode: serverapi.OnboardingImportModeSymlinkSource, ProviderUUID: &missingCommandsUUID},
	})
	var finalizeErr *serverapi.OnboardingFinalizeError
	if !errors.As(err, &finalizeErr) {
		t.Fatalf("error = %T %v, want OnboardingFinalizeError", err, err)
	}
	if finalizeErr.Code != serverapi.OnboardingFinalizeImportUnavailable {
		t.Fatalf("code = %q, want import_unavailable", finalizeErr.Code)
	}
	if _, err := os.Lstat(filepath.Join(root, "skills")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("skills import should be rolled back, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "config.toml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config should remain absent, stat err=%v", err)
	}
}

func TestFinalizerRestoresPreexistingEmptyTargetDirectoryOnRollback(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	providerUUID := createProviderSkillSource(t, home, ".claude")
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o755); err != nil {
		t.Fatalf("mkdir empty target: %v", err)
	}

	_, err := newTestFinalizer(t, root, home).FinalizeOnboarding(context.Background(), serverapi.OnboardingFinalizeRequest{
		SkillsImport:   &serverapi.OnboardingImportSelection{Mode: serverapi.OnboardingImportModeSymlinkSource, ProviderUUID: &providerUUID},
		CommandsImport: &serverapi.OnboardingImportSelection{Mode: serverapi.OnboardingImportModeSymlinkSource, ProviderUUID: &providerUUID},
	})
	if !errors.Is(err, serverapi.ErrOnboardingFinalizeImportUnavailable) {
		t.Fatalf("error = %v, want import_unavailable", err)
	}
	info, statErr := os.Lstat(filepath.Join(root, "skills"))
	if statErr != nil {
		t.Fatalf("skills target should be restored: %v", statErr)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		t.Fatalf("skills target should be restored as directory, mode=%s", info.Mode())
	}
	entries, readErr := os.ReadDir(filepath.Join(root, "skills"))
	if readErr != nil {
		t.Fatalf("read restored skills dir: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("restored skills dir entries = %d, want empty", len(entries))
	}
}

func TestFinalizerRejectsNonV4ProviderUUID(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	v1UUID := uuid.MustParse("6ba7b810-9dad-11d1-80b4-00c04fd430c8")

	_, err := newTestFinalizer(t, root, home).FinalizeOnboarding(context.Background(), serverapi.OnboardingFinalizeRequest{
		SkillsImport: &serverapi.OnboardingImportSelection{Mode: serverapi.OnboardingImportModeSymlinkSource, ProviderUUID: &v1UUID},
	})
	if !errors.Is(err, serverapi.ErrOnboardingFinalizeInvalidRequest) {
		t.Fatalf("error = %v, want invalid_request", err)
	}
}

func TestFinalizerWritesDisabledSkillNamesWithoutDiscovery(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	finalizer := newTestFinalizer(t, root, home)

	if _, err := finalizer.FinalizeOnboarding(context.Background(), serverapi.OnboardingFinalizeRequest{DisabledSkillNames: []string{"apiresult"}}); err != nil {
		t.Fatalf("FinalizeOnboarding: %v", err)
	}
	cfg, err := config.LoadGlobal(config.LoadOptions{ConfigRoot: root})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	enabled, ok := cfg.Settings.SkillToggles["apiresult"]
	if !ok || enabled {
		t.Fatalf("skill toggle should be disabled: %+v", cfg.Settings.SkillToggles)
	}
}

func TestFinalizerRejectsNativeCompactionForSelectedNonOpenAIModel(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	finalizer := newTestFinalizer(t, root, home)
	native := serverapi.OnboardingCompactionNative

	_, err := finalizer.FinalizeOnboarding(context.Background(), serverapi.OnboardingFinalizeRequest{
		Model:      &serverapi.OnboardingModelChoice{Kind: serverapi.OnboardingModelCustom, Alias: "claude-3-7-sonnet"},
		Compaction: &native,
	})
	if !errors.Is(err, serverapi.ErrOnboardingFinalizeInvalidRequest) {
		t.Fatalf("error = %v, want invalid_request", err)
	}
	var finalizeErr *serverapi.OnboardingFinalizeError
	if !errors.As(err, &finalizeErr) {
		t.Fatalf("error = %T %v, want OnboardingFinalizeError", err, err)
	}
	details := finalizeErr.Details.(serverapi.OnboardingInvalidRequestDetails)
	if len(details.FieldErrors) != 1 || details.FieldErrors[0].Field != "compaction" || details.FieldErrors[0].Code != "unsupported_for_provider" {
		t.Fatalf("field errors = %+v", details.FieldErrors)
	}
}

func TestFinalizerRejectsNativeCompactionForUnknownCustomModel(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	finalizer := newTestFinalizer(t, root, home)
	native := serverapi.OnboardingCompactionNative

	_, err := finalizer.FinalizeOnboarding(context.Background(), serverapi.OnboardingFinalizeRequest{
		Model:      &serverapi.OnboardingModelChoice{Kind: serverapi.OnboardingModelCustom, Alias: "unknown-custom-model"},
		Compaction: &native,
	})
	if !errors.Is(err, serverapi.ErrOnboardingFinalizeInvalidRequest) {
		t.Fatalf("error = %v, want invalid_request", err)
	}
}

func TestFinalizerCommandsImportBlocksLegacyCommandsDirectory(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	providerUUID := createProviderCommandSource(t, home, ".claude")
	if err := os.MkdirAll(filepath.Join(root, "commands", "legacy"), 0o755); err != nil {
		t.Fatalf("mkdir legacy commands: %v", err)
	}

	_, err := newTestFinalizer(t, root, home).FinalizeOnboarding(context.Background(), serverapi.OnboardingFinalizeRequest{
		CommandsImport: &serverapi.OnboardingImportSelection{Mode: serverapi.OnboardingImportModeSymlinkSource, ProviderUUID: &providerUUID},
	})
	if !errors.Is(err, serverapi.ErrOnboardingFinalizeImportUnavailable) {
		t.Fatalf("FinalizeOnboarding error = %v, want import_unavailable", err)
	}
	if _, statErr := os.Lstat(filepath.Join(root, "prompts")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("prompts import should not run when legacy commands exist, stat err=%v", statErr)
	}
}

func TestProductionProviderCatalogUUIDsAreStableV4Values(t *testing.T) {
	first := onboarding.ProductionProviderCatalog()
	second := onboarding.ProductionProviderCatalog()
	if len(first) == 0 || len(first) != len(second) {
		t.Fatalf("catalog lengths = %d/%d", len(first), len(second))
	}
	seen := map[uuid.UUID]bool{}
	for index := range first {
		if first[index].UUID == uuid.Nil || first[index].UUID.Version() != 4 {
			t.Fatalf("provider %d uuid = %s, want v4", index, first[index].UUID)
		}
		if first[index].UUID != second[index].UUID {
			t.Fatalf("provider %d uuid changed across calls: %s vs %s", index, first[index].UUID, second[index].UUID)
		}
		if seen[first[index].UUID] {
			t.Fatalf("duplicate provider uuid %s", first[index].UUID)
		}
		seen[first[index].UUID] = true
	}
}

func TestFinalizeErrorSentinelsSurviveRemoteDecode(t *testing.T) {
	encoded := serverapi.NewOnboardingFinalizeError(serverapi.OnboardingFinalizeConfigAlreadyExists, serverapi.OnboardingConfigAlreadyExistsDetails{SettingsPath: "/tmp/config.toml"}, nil)
	decoded := serverapi.DecodeOnboardingFinalizeError(encoded.RPCErrorData(), encoded.Error())

	if !errors.Is(decoded, serverapi.ErrOnboardingFinalizeConfigAlreadyExists) {
		t.Fatalf("decoded error = %v, want config_already_exists sentinel", decoded)
	}
	var finalizeErr *serverapi.OnboardingFinalizeError
	if !errors.As(decoded, &finalizeErr) {
		t.Fatalf("decoded error = %T %v, want OnboardingFinalizeError", decoded, decoded)
	}
	if _, ok := finalizeErr.Details.(serverapi.OnboardingConfigAlreadyExistsDetails); !ok {
		t.Fatalf("decoded details = %T, want OnboardingConfigAlreadyExistsDetails", finalizeErr.Details)
	}
}

func newTestFinalizer(t *testing.T, root string, home string) *onboarding.Finalizer {
	t.Helper()
	finalizer, err := onboarding.NewFinalizer(onboarding.Options{PersistenceRoot: root, HomeDir: home})
	if err != nil {
		t.Fatalf("NewFinalizer: %v", err)
	}
	return finalizer
}

func createProviderSkillSource(t *testing.T, home string, entry string) uuid.UUID {
	t.Helper()
	providerUUID := providerUUIDForHomeEntry(t, entry)
	skillDir := filepath.Join(home, entry, "skills", "example")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: Example\ndescription: Example skill\n---\nBody\n"), 0o644); err != nil {
		t.Fatalf("write skill metadata: %v", err)
	}
	return providerUUID
}

func createProviderCommandSource(t *testing.T, home string, entry string) uuid.UUID {
	t.Helper()
	providerUUID := providerUUIDForHomeEntry(t, entry)
	commandDir := filepath.Join(home, entry, "commands")
	if err := os.MkdirAll(commandDir, 0o755); err != nil {
		t.Fatalf("mkdir command source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(commandDir, "example.md"), []byte("command"), 0o644); err != nil {
		t.Fatalf("write command: %v", err)
	}
	return providerUUID
}

func providerUUIDForHomeEntry(t *testing.T, entry string) uuid.UUID {
	t.Helper()
	var providerUUID uuid.UUID
	for _, provider := range onboarding.ProductionProviderCatalog() {
		if provider.HomeEntry == entry {
			providerUUID = provider.UUID
			break
		}
	}
	if providerUUID == uuid.Nil {
		t.Fatalf("provider %s not found", entry)
	}
	return providerUUID
}
