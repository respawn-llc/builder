package core_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestChecksumLibrarySupportsAvailableImplementations(t *testing.T) {
	repoRoot := findRepoRoot(t)
	libraryPath := filepath.Join(repoRoot, "scripts", "lib", "checksum.sh")
	tested := 0

	for _, commandName := range []string{"sha256sum", "shasum"} {
		if _, err := exec.LookPath(commandName); err != nil {
			continue
		}
		tested++
		t.Run(commandName, func(t *testing.T) {
			tempDir := t.TempDir()
			payloadPath := filepath.Join(tempDir, "payload")
			if err := os.WriteFile(payloadPath, []byte("kent checksum fixture"), 0o600); err != nil {
				t.Fatalf("write checksum fixture: %v", err)
			}
			manifestPath := filepath.Join(tempDir, "checksums.txt")

			const script = `
set -eu
. "$1"
selected="$(kent_select_sha256_command "test checksum library" "$2")"
[ "$selected" = "$2" ]
digest="$(kent_sha256_file "$selected" "$3")"
printf '%s  %s\n' "$digest" "$3" >"$4"
kent_verify_sha256_manifest "$selected" "$4"
`
			command := exec.Command("sh", "-c", script, "checksum-library-test", libraryPath, commandName, payloadPath, manifestPath)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("exercise %s checksum implementation: %v\n%s", commandName, err, output)
			}
		})
	}

	if tested == 0 {
		t.Skip("no supported SHA-256 executable is installed")
	}
}
