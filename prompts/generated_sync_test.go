package prompts

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
	"time"
)

func TestSyncSeedsMissingGeneratedRoot(t *testing.T) {
	home := t.TempDir()
	result, err := GeneratedSync(context.Background(), GeneratedSyncOptions{HomeDir: home, FS: testGeneratedFS()})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.Recovered {
		t.Fatalf("did not expect recovery: %+v", result)
	}
	assertFile(t, filepath.Join(home, configDirName, ".generated", "README.md"), generatedReadme(filepath.Join(home, configDirName, generatedSkillsDir), filepath.Join(home, configDirName, recoveredDirName)))
	assertFile(t, filepath.Join(home, configDirName, ".generated", "skills", "skill-creator", "SKILL.md"), testSkillMarkdown("skill-creator", "create skills"))
	markerPath := filepath.Join(home, configDirName, ".generated", markerFileName)
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("expected marker: %v", err)
	}
}

func TestGeneratedLayoutForCustomPersistenceRoot(t *testing.T) {
	root := t.TempDir()
	layout, err := GeneratedLayoutFor(root)
	if err != nil {
		t.Fatalf("GeneratedLayoutFor: %v", err)
	}
	if layout.GeneratedRoot != filepath.Join(root, generatedDirName) {
		t.Fatalf("generated root = %q, want %q", layout.GeneratedRoot, filepath.Join(root, generatedDirName))
	}
	if layout.GeneratedSkillsRoot != filepath.Join(root, generatedDirName, generatedSkillsDir) {
		t.Fatalf("generated skills root = %q, want %q", layout.GeneratedSkillsRoot, filepath.Join(root, generatedDirName, generatedSkillsDir))
	}
	if layout.UserSkillsRoot != filepath.Join(root, generatedSkillsDir) {
		t.Fatalf("user skills root = %q, want %q", layout.UserSkillsRoot, filepath.Join(root, generatedSkillsDir))
	}
	if layout.RecoveryRoot != filepath.Join(root, recoveredDirName) {
		t.Fatalf("recovery root = %q, want %q", layout.RecoveryRoot, filepath.Join(root, recoveredDirName))
	}
}

func TestGeneratedLayoutForEmptyRootMatchesDefaultHelpers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	layout, err := GeneratedLayoutFor("")
	if err != nil {
		t.Fatalf("GeneratedLayoutFor empty: %v", err)
	}
	generatedRoot, err := GeneratedRoot()
	if err != nil {
		t.Fatalf("GeneratedRoot: %v", err)
	}
	generatedSkillsRoot, err := GeneratedSkillsRoot()
	if err != nil {
		t.Fatalf("GeneratedSkillsRoot: %v", err)
	}
	recoveryRoot, err := RecoveryRoot()
	if err != nil {
		t.Fatalf("RecoveryRoot: %v", err)
	}
	if layout.GeneratedRoot != generatedRoot || layout.GeneratedSkillsRoot != generatedSkillsRoot || layout.RecoveryRoot != recoveryRoot {
		t.Fatalf("layout = %+v, want generated=%q skills=%q recovery=%q", layout, generatedRoot, generatedSkillsRoot, recoveryRoot)
	}
	if layout.UserSkillsRoot != filepath.Join(home, configDirName, generatedSkillsDir) {
		t.Fatalf("user skills root = %q, want default home skills root", layout.UserSkillsRoot)
	}
}

func TestSyncUpgradesCleanGeneratedRootWithoutRecovery(t *testing.T) {
	home := t.TempDir()
	oldFS := fstest.MapFS{
		"skills/old-skill/SKILL.md": {Data: []byte(testSkillMarkdown("old-skill", "old"))},
	}
	if _, err := GeneratedSync(context.Background(), GeneratedSyncOptions{HomeDir: home, FS: oldFS}); err != nil {
		t.Fatalf("seed old: %v", err)
	}
	newFS := testGeneratedFS()
	result, err := GeneratedSync(context.Background(), GeneratedSyncOptions{HomeDir: home, FS: newFS})
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if result.Recovered {
		t.Fatalf("did not expect recovery for clean upgrade: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(home, configDirName, ".generated", "skills", "old-skill", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("expected old skill removed on clean upgrade, err=%v", err)
	}
	assertFile(t, filepath.Join(home, configDirName, ".generated", "skills", "skill-creator", "SKILL.md"), testSkillMarkdown("skill-creator", "create skills"))
	if nonEmpty, err := recoveredRootNonEmpty(filepath.Join(home, configDirName, "recovered")); err != nil || nonEmpty {
		t.Fatalf("expected no recovered entries, nonEmpty=%t err=%v", nonEmpty, err)
	}
}

func TestSyncRecoversEditedGeneratedRoot(t *testing.T) {
	home := t.TempDir()
	now := fixedNow()
	if _, err := GeneratedSync(context.Background(), GeneratedSyncOptions{HomeDir: home, FS: testGeneratedFS(), Now: now}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, configDirName, ".generated", "skills", "skill-creator", "SKILL.md"), []byte("edited"), 0o644); err != nil {
		t.Fatalf("edit generated skill: %v", err)
	}
	result, err := GeneratedSync(context.Background(), GeneratedSyncOptions{HomeDir: home, FS: testGeneratedFS(), Now: now})
	if err != nil {
		t.Fatalf("recover edited: %v", err)
	}
	if !result.Recovered {
		t.Fatalf("expected recovery: %+v", result)
	}
	wantRecovery := filepath.Join(home, configDirName, "recovered", "2026-05-04T18-43-16Z", ".generated")
	if result.RecoveryPath != wantRecovery {
		t.Fatalf("recovery path = %q, want %q", result.RecoveryPath, wantRecovery)
	}
	assertFile(t, filepath.Join(wantRecovery, "skills", "skill-creator", "SKILL.md"), "edited")
	assertFile(t, filepath.Join(home, configDirName, ".generated", "skills", "skill-creator", "SKILL.md"), testSkillMarkdown("skill-creator", "create skills"))
	wantWarning := recoveredWarning(filepath.Join(home, configDirName, generatedDirName), filepath.Join(home, configDirName, recoveredDirName))
	if !result.RecoveredRootNonEmpty || result.RecoveredWarning != wantWarning {
		t.Fatalf("expected recovered warning state, got %+v", result)
	}
}

func TestSyncRecoversAddedDeletedAndRenamedEntries(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, root string)
	}{
		{
			name: "added",
			mutate: func(t *testing.T, root string) {
				if err := os.WriteFile(filepath.Join(root, "extra.txt"), []byte("extra"), 0o644); err != nil {
					t.Fatalf("add extra: %v", err)
				}
			},
		},
		{
			name: "deleted",
			mutate: func(t *testing.T, root string) {
				if err := os.Remove(filepath.Join(root, "README.md")); err != nil {
					t.Fatalf("delete README: %v", err)
				}
			},
		},
		{
			name: "renamed",
			mutate: func(t *testing.T, root string) {
				if err := os.Rename(filepath.Join(root, "README.md"), filepath.Join(root, "RENAMED.md")); err != nil {
					t.Fatalf("rename README: %v", err)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if _, err := GeneratedSync(context.Background(), GeneratedSyncOptions{HomeDir: home, FS: testGeneratedFS(), Now: fixedNow()}); err != nil {
				t.Fatalf("seed: %v", err)
			}
			root := filepath.Join(home, configDirName, ".generated")
			tc.mutate(t, root)
			result, err := GeneratedSync(context.Background(), GeneratedSyncOptions{HomeDir: home, FS: testGeneratedFS(), Now: fixedNow()})
			if err != nil {
				t.Fatalf("recover: %v", err)
			}
			if !result.Recovered {
				t.Fatalf("expected recovery for %s: %+v", tc.name, result)
			}
		})
	}
}

func TestSyncRecoversEmptiedGeneratedRoot(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, configDirName, ".generated")
	if _, err := GeneratedSync(context.Background(), GeneratedSyncOptions{HomeDir: home, FS: testGeneratedFS(), Now: fixedNow()}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read generated root: %v", err)
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			t.Fatalf("empty generated root: %v", err)
		}
	}

	result, err := GeneratedSync(context.Background(), GeneratedSyncOptions{HomeDir: home, FS: testGeneratedFS(), Now: fixedNow()})
	if err != nil {
		t.Fatalf("recover empty root: %v", err)
	}
	if !result.Recovered {
		t.Fatalf("expected recovery for empty generated root: %+v", result)
	}
	wantRecovery := filepath.Join(home, configDirName, "recovered", "2026-05-04T18-43-16Z", ".generated")
	info, err := os.Stat(wantRecovery)
	if err != nil {
		t.Fatalf("expected recovered empty generated root: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected recovered generated root to be a directory, mode=%s", info.Mode())
	}
	assertFile(t, filepath.Join(root, "skills", "skill-creator", "SKILL.md"), testSkillMarkdown("skill-creator", "create skills"))
}

func TestSyncRecoversPermissionOnlyEdits(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		perm   os.FileMode
		verify func(t *testing.T, path string)
	}{
		{
			name: "file",
			path: filepath.Join("skills", "skill-creator", "SKILL.md"),
			perm: 0o755,
			verify: func(t *testing.T, path string) {
				assertPerm(t, path, generatedFilePerm)
			},
		},
		{
			name: "directory",
			path: filepath.Join("skills", "skill-creator"),
			perm: 0o700,
			verify: func(t *testing.T, path string) {
				assertPerm(t, path, generatedDirPerm)
			},
		},
		{
			name: "root-directory",
			path: ".",
			perm: 0o755,
			verify: func(t *testing.T, path string) {
				assertPerm(t, path, generatedRootPerm)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if _, err := GeneratedSync(context.Background(), GeneratedSyncOptions{HomeDir: home, FS: testGeneratedFS(), Now: fixedNow()}); err != nil {
				t.Fatalf("seed: %v", err)
			}
			target := filepath.Join(home, configDirName, ".generated", tc.path)
			if err := os.Chmod(target, tc.perm); err != nil {
				t.Fatalf("chmod generated %s: %v", tc.path, err)
			}
			result, err := GeneratedSync(context.Background(), GeneratedSyncOptions{HomeDir: home, FS: testGeneratedFS(), Now: fixedNow()})
			if err != nil {
				t.Fatalf("recover mode edit: %v", err)
			}
			if !result.Recovered {
				t.Fatalf("expected recovery for mode edit: %+v", result)
			}
			tc.verify(t, target)
		})
	}
}

func TestSyncMarkerTracksOnDiskModesUnderRestrictiveUmask(t *testing.T) {
	oldUmask := syscall.Umask(0o077)
	t.Cleanup(func() {
		syscall.Umask(oldUmask)
	})

	home := t.TempDir()
	if _, err := GeneratedSync(context.Background(), GeneratedSyncOptions{HomeDir: home, FS: testGeneratedFS(), Now: fixedNow()}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	result, err := GeneratedSync(context.Background(), GeneratedSyncOptions{HomeDir: home, FS: testGeneratedFS(), Now: fixedNow()})
	if err != nil {
		t.Fatalf("sync clean generated root: %v", err)
	}
	if result.Recovered {
		t.Fatalf("did not expect recovery for clean umask-masked tree: %+v", result)
	}
	assertPerm(t, filepath.Join(home, configDirName, ".generated", "skills", "skill-creator", "SKILL.md"), 0o600)
	assertPerm(t, filepath.Join(home, configDirName, ".generated", "skills", "skill-creator"), 0o700)
}

func TestSyncRecoversSymlinkWithoutFollowing(t *testing.T) {
	home := t.TempDir()
	if _, err := GeneratedSync(context.Background(), GeneratedSyncOptions{HomeDir: home, FS: testGeneratedFS(), Now: fixedNow()}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	outside := filepath.Join(home, "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(home, configDirName, ".generated", "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	result, err := GeneratedSync(context.Background(), GeneratedSyncOptions{HomeDir: home, FS: testGeneratedFS(), Now: fixedNow()})
	if err != nil {
		t.Fatalf("recover symlink: %v", err)
	}
	if !result.Recovered {
		t.Fatalf("expected recovery: %+v", result)
	}
	assertFile(t, outside, "outside")
	linkPath := filepath.Join(result.RecoveryPath, "link")
	info, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("expected recovered symlink: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected recovered link to stay symlink, mode=%s", info.Mode())
	}
}

func TestSyncRecoversInvalidOrMissingMarker(t *testing.T) {
	for _, tc := range []struct {
		name   string
		marker string
		remove bool
	}{
		{name: "missing", remove: true},
		{name: "invalid", marker: "not-json"},
		{name: "wrong-schema", marker: `{"schema_version":2,"tree_hash":"sha256:abc"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if _, err := GeneratedSync(context.Background(), GeneratedSyncOptions{HomeDir: home, FS: testGeneratedFS(), Now: fixedNow()}); err != nil {
				t.Fatalf("seed: %v", err)
			}
			markerPath := filepath.Join(home, configDirName, ".generated", markerFileName)
			if tc.remove {
				if err := os.Remove(markerPath); err != nil {
					t.Fatalf("remove marker: %v", err)
				}
			} else if err := os.WriteFile(markerPath, []byte(tc.marker), 0o644); err != nil {
				t.Fatalf("write marker: %v", err)
			}
			result, err := GeneratedSync(context.Background(), GeneratedSyncOptions{HomeDir: home, FS: testGeneratedFS(), Now: fixedNow()})
			if err != nil {
				t.Fatalf("recover marker: %v", err)
			}
			if !result.Recovered {
				t.Fatalf("expected recovery: %+v", result)
			}
		})
	}
}

func TestSyncRecoversGeneratedRootFileAndTimestampCollision(t *testing.T) {
	home := t.TempDir()
	configRoot := filepath.Join(home, configDirName)
	if err := os.MkdirAll(configRoot, 0o755); err != nil {
		t.Fatalf("mkdir kent root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, ".generated"), []byte("file"), 0o644); err != nil {
		t.Fatalf("write generated file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(configRoot, "recovered", "2026-05-04T18-43-16Z"), 0o755); err != nil {
		t.Fatalf("create collision: %v", err)
	}
	result, err := GeneratedSync(context.Background(), GeneratedSyncOptions{HomeDir: home, FS: testGeneratedFS(), Now: fixedNow()})
	if err != nil {
		t.Fatalf("recover generated file: %v", err)
	}
	want := filepath.Join(configRoot, "recovered", "2026-05-04T18-43-16Z-2", ".generated")
	if result.RecoveryPath != want {
		t.Fatalf("recovery path = %q, want %q", result.RecoveryPath, want)
	}
	assertFile(t, want, "file")
}

func TestSyncDetectsRecoveredRootNonEmptyWithoutNewRecovery(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, configDirName, "recovered", "old"), 0o755); err != nil {
		t.Fatalf("mkdir recovered: %v", err)
	}
	result, err := GeneratedSync(context.Background(), GeneratedSyncOptions{HomeDir: home, FS: testGeneratedFS(), Now: fixedNow()})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.Recovered {
		t.Fatalf("did not expect new recovery: %+v", result)
	}
	wantWarning := recoveredWarning(filepath.Join(home, configDirName, generatedDirName), filepath.Join(home, configDirName, recoveredDirName))
	if !result.RecoveredRootNonEmpty || result.RecoveredWarning != wantWarning {
		t.Fatalf("expected recovered warning: %+v", result)
	}
}

func TestSyncIgnoresRecoveredRootWarningCheckFailure(t *testing.T) {
	home := t.TempDir()
	configRoot := filepath.Join(home, configDirName)
	if err := os.MkdirAll(configRoot, 0o755); err != nil {
		t.Fatalf("mkdir kent root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, "recovered"), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write recovered file: %v", err)
	}

	result, err := GeneratedSync(context.Background(), GeneratedSyncOptions{HomeDir: home, FS: testGeneratedFS(), Now: fixedNow()})
	if err != nil {
		t.Fatalf("sync with invalid recovered root: %v", err)
	}
	if result.RecoveredRootNonEmpty || result.RecoveredWarning != "" {
		t.Fatalf("expected warning check failure to be ignored, got %+v", result)
	}
	assertFile(t, filepath.Join(home, configDirName, ".generated", "skills", "skill-creator", "SKILL.md"), testSkillMarkdown("skill-creator", "create skills"))
}

func TestSyncWritesMarkerWithTreeHash(t *testing.T) {
	home := t.TempDir()
	if _, err := GeneratedSync(context.Background(), GeneratedSyncOptions{HomeDir: home, FS: testGeneratedFS(), Now: fixedNow()}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, configDirName, ".generated", markerFileName))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	var parsed marker
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse marker: %v", err)
	}
	if parsed.SchemaVersion != markerSchemaVersion || !strings.HasPrefix(parsed.TreeHash, treeHashPrefix) {
		t.Fatalf("unexpected marker: %+v", parsed)
	}
}

func testGeneratedFS() fstest.MapFS {
	return fstest.MapFS{
		"skills/skill-creator/SKILL.md": {Data: []byte(testSkillMarkdown("skill-creator", "create skills"))},
	}
}

func testSkillMarkdown(name, description string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n\nUse this skill.\n"
}

func fixedNow() func() time.Time {
	return func() time.Time {
		return time.Date(2026, 5, 4, 18, 43, 16, 0, time.UTC)
	}
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	if got := readFile(t, path); got != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func assertPerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s perm = %s, want %s", path, got, want)
	}
}

func TestSyncWithConfigRootSeedsUnderRoot(t *testing.T) {
	configRoot := filepath.Join(t.TempDir(), "isolated-root")
	result, err := GeneratedSync(context.Background(), GeneratedSyncOptions{ConfigRoot: configRoot, FS: testGeneratedFS()})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	wantGeneratedRoot := filepath.Join(configRoot, ".generated")
	if result.GeneratedRoot != wantGeneratedRoot {
		t.Fatalf("generated root = %q, want %q", result.GeneratedRoot, wantGeneratedRoot)
	}
	wantSkillsRoot := filepath.Join(wantGeneratedRoot, "skills")
	if result.GeneratedSkillsRoot != wantSkillsRoot {
		t.Fatalf("generated skills root = %q, want %q", result.GeneratedSkillsRoot, wantSkillsRoot)
	}
	if result.RecoveryRoot != filepath.Join(configRoot, "recovered") {
		t.Fatalf("recovery root = %q, want under config root", result.RecoveryRoot)
	}
	assertFile(t, filepath.Join(wantSkillsRoot, "skill-creator", "SKILL.md"), testSkillMarkdown("skill-creator", "create skills"))
}

func TestSyncConfigRootTakesPrecedenceOverHome(t *testing.T) {
	home := t.TempDir()
	configRoot := filepath.Join(t.TempDir(), "isolated-root")
	if _, err := GeneratedSync(context.Background(), GeneratedSyncOptions{HomeDir: home, ConfigRoot: configRoot, FS: testGeneratedFS()}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if _, err := os.Stat(filepath.Join(configRoot, ".generated", "skills")); err != nil {
		t.Fatalf("expected generated assets under config root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, configDirName, ".generated")); !os.IsNotExist(err) {
		t.Fatalf("expected no generated assets under home, got err=%v", err)
	}
}

func TestGeneratedSkillsRootForResolvesUnderConfigRoot(t *testing.T) {
	configRoot := filepath.Join(t.TempDir(), "isolated-root")
	got, err := GeneratedSkillsRootFor(configRoot)
	if err != nil {
		t.Fatalf("GeneratedSkillsRootFor: %v", err)
	}
	want := filepath.Join(configRoot, ".generated", "skills")
	if got != want {
		t.Fatalf("GeneratedSkillsRootFor = %q, want %q", got, want)
	}
}

func TestGeneratedSkillsRootForEmptyMatchesDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := GeneratedSkillsRootFor("")
	if err != nil {
		t.Fatalf("GeneratedSkillsRootFor: %v", err)
	}
	want, err := GeneratedSkillsRoot()
	if err != nil {
		t.Fatalf("GeneratedSkillsRoot: %v", err)
	}
	if got != want {
		t.Fatalf("GeneratedSkillsRootFor(\"\") = %q, want %q", got, want)
	}
}

func TestRecoveredRootNonEmptyForUsesConfigRoot(t *testing.T) {
	configRoot := t.TempDir()
	// No recovered dir yet -> not flagged.
	if nonEmpty, err := RecoveredRootNonEmptyFor(configRoot); err != nil {
		t.Fatalf("RecoveredRootNonEmptyFor: %v", err)
	} else if nonEmpty {
		t.Fatal("expected empty recovered root to report false")
	}
	recoveredDir := filepath.Join(configRoot, recoveredDirName)
	if err := os.MkdirAll(recoveredDir, 0o755); err != nil {
		t.Fatalf("mkdir recovered: %v", err)
	}
	if err := os.WriteFile(filepath.Join(recoveredDir, "salvaged.md"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write salvaged file: %v", err)
	}
	if nonEmpty, err := RecoveredRootNonEmptyFor(configRoot); err != nil {
		t.Fatalf("RecoveredRootNonEmptyFor: %v", err)
	} else if !nonEmpty {
		t.Fatalf("expected populated recovered root %q to report true", recoveredDir)
	}
}

func TestSyncRendersReadmeFromConfigRoot(t *testing.T) {
	configRoot := t.TempDir()
	if _, err := GeneratedSync(context.Background(), GeneratedSyncOptions{ConfigRoot: configRoot, FS: testGeneratedFS()}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	readme := readFile(t, filepath.Join(configRoot, ".generated", "README.md"))
	wantHomeSkills := filepath.Join(configRoot, generatedSkillsDir)
	wantRecovered := filepath.Join(configRoot, recoveredDirName)
	if !strings.Contains(readme, wantHomeSkills) {
		t.Fatalf("README %q must reference the selected home skills root %q", readme, wantHomeSkills)
	}
	if !strings.Contains(readme, wantRecovered) {
		t.Fatalf("README %q must reference the selected recovery root %q", readme, wantRecovered)
	}
	// The default-root remediation paths must not leak into a non-default root README.
	if strings.Contains(readme, generatedRootDir) || strings.Contains(readme, recoveredDir) {
		t.Fatalf("README %q must not reference the default-root layout", readme)
	}
}

func TestRecoveredWarningForUsesConfigRoot(t *testing.T) {
	configRoot := t.TempDir()
	warning, err := RecoveredWarningFor(configRoot)
	if err != nil {
		t.Fatalf("RecoveredWarningFor: %v", err)
	}
	wantGenerated := filepath.Join(configRoot, generatedDirName)
	wantRecovered := filepath.Join(configRoot, recoveredDirName)
	if !strings.Contains(warning, wantGenerated) {
		t.Fatalf("warning %q must reference the selected generated root %q", warning, wantGenerated)
	}
	if !strings.Contains(warning, wantRecovered) {
		t.Fatalf("warning %q must reference the selected recovery root %q", warning, wantRecovered)
	}
	// The default-root remediation paths must not leak into a non-default root warning.
	if strings.Contains(warning, generatedRootDir) || strings.Contains(warning, recoveredDir) {
		t.Fatalf("warning %q must not reference the default-root layout", warning)
	}
}
