package core_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	testharness "core/internal/testharness/testsetup"
)

func TestBrewTapGeneratorMakesAppleSiliconFormulaLoadableOnIntelMac(t *testing.T) {
	formulaPath := generateBrewFormula(t)
	result := evaluateFormulaOnMac(t, formulaPath, false)
	if result.URL == "" {
		t.Fatal("Intel macOS cannot load the generated Kent formula because it has no active URL")
	}
	if !result.RequiresARM64 {
		t.Fatal("generated Kent formula must reject Intel macOS through its arm64 dependency")
	}
}

func TestBrewTapGeneratorKeepsAppleSiliconPrebuiltInstall(t *testing.T) {
	formulaPath := generateBrewFormula(t)
	result := evaluateFormulaOnMac(t, formulaPath, true)
	if result.URL == "" {
		t.Fatal("Apple Silicon macOS has no active Kent release archive")
	}
	if !result.RequiresARM64 {
		t.Fatal("generated Kent formula must retain its arm64 macOS requirement")
	}
	if len(result.Installed) != 1 {
		t.Fatalf("Apple Silicon Kent install must install exactly one prebuilt executable, got %v", result.Installed)
	}
	for source, destination := range result.Installed {
		if source != "kent_1.2.3_darwin_arm64" {
			t.Fatalf("Apple Silicon Kent install source = %q, want versioned release archive executable", source)
		}
		if destination != "kent" {
			t.Fatalf("Apple Silicon Kent install destination = %q, want kent", destination)
		}
	}
}

func generateBrewFormula(t *testing.T) string {
	t.Helper()
	repoRoot := testharness.RepositoryRoot(t)
	scriptPath := filepath.Join(repoRoot, "scripts", "update-brew-tap.sh")
	tapDir := t.TempDir()
	fakeBinDir := t.TempDir()
	fakeCurlPath := filepath.Join(fakeBinDir, "curl")
	if err := os.WriteFile(fakeCurlPath, []byte(`#!/usr/bin/env bash
set -euo pipefail

while [[ $# -gt 0 ]]; do
	case "$1" in
	-o)
		printf fixture >"$2"
		exit 0
		;;
	*)
		shift
		;;
	esac
done

exit 1
`), 0o755); err != nil {
		t.Fatalf("write fake curl: %v", err)
	}

	generateFormula := exec.Command(
		"bash",
		scriptPath,
		"--tap", tapDir,
		"--version", "v1.2.3",
		"--repo", "example.test/kent",
	)
	generateFormula.Dir = repoRoot
	generateFormula.Env = append(
		os.Environ(),
		"PATH="+fakeBinDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	if output, err := generateFormula.CombinedOutput(); err != nil {
		t.Fatalf("generate formula: %v\n%s", err, output)
	}

	return filepath.Join(tapDir, "Formula", "kent.rb")
}

type formulaEvaluation struct {
	URL           string            `json:"url"`
	RequiresARM64 bool              `json:"requires_arm64"`
	Installed     map[string]string `json:"installed"`
}

func evaluateFormulaOnMac(t *testing.T, formulaPath string, appleSilicon bool) formulaEvaluation {
	t.Helper()
	evaluateFormula := exec.Command("ruby", "-rjson", "-e", `
$apple_silicon = ARGV.fetch(1) == "arm"
fixture_version = ARGV.fetch(2)

module OS
  def self.mac?
    true
  end
end

module Hardware
  module CPU
    def self.arm?
      $apple_silicon
    end
  end
end

class Formula
  class Bin
    attr_reader :installed

    def install(mapping)
      @installed = mapping
    end
  end

  class << self
    attr_reader :formula_url, :dependencies, :formula_version

    def desc(*); end
    def homepage(*); end

    def version(value)
      if @inferred_version == value
        raise "explicit version #{value} is redundant with the stable URL"
      end
      @formula_version = value
    end

    def license(*); end
    def root_url(*); end
    def sha256(*)
      raise "sha256 cannot be declared directly inside on_macos" if @platform_block == :macos
    end
    def caveats(*); end
    def test(*); end

    def bottle
      yield
    end

    def depends_on(dependency)
      @dependencies ||= []
      @dependencies << dependency
    end

    def on_macos
      previous_platform_block = @platform_block
      @platform_block = :macos
      yield
    ensure
      @platform_block = previous_platform_block
    end

    def on_linux; end

    def on_arm
      previous_platform_block = @platform_block
      @platform_block = :arm
      yield if $apple_silicon
    ensure
      @platform_block = previous_platform_block
    end

    def on_intel
      previous_platform_block = @platform_block
      @platform_block = :intel
      yield unless $apple_silicon
    ensure
      @platform_block = previous_platform_block
    end

    def url(value)
      raise "url cannot be declared directly inside on_macos" if @platform_block == :macos
      @formula_url = value
      @inferred_version = $fixture_version if @platform_block.nil?
    end
  end

  attr_reader :bin

  def initialize
    @bin = Bin.new
  end

  def version
    self.class.formula_version || $fixture_version
  end
end

$fixture_version = fixture_version
load ARGV.fetch(0)
formula = Kent.new
formula.install
puts JSON.generate(
  url: Kent.formula_url,
  requires_arm64: Kent.dependencies.include?({ arch: :arm64 }),
  installed: formula.bin.installed,
)
`, formulaPath, map[bool]string{true: "arm", false: "intel"}[appleSilicon], "1.2.3")
	output, err := evaluateFormula.CombinedOutput()
	if err != nil {
		t.Fatalf("evaluate generated formula on macOS: %v\n%s", err, output)
	}

	var result formulaEvaluation
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode generated formula evaluation: %v", err)
	}
	return result
}
