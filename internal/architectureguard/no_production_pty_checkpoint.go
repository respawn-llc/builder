package architectureguard

import (
	"go/ast"
	"go/token"
	"path"
	"strconv"
)

var forbiddenPTYCheckpointImports = map[string]struct{}{
	"core/internal/testharness/pty":          {},
	"core/internal/testharness/pty/analyzer": {},
}

var forbiddenPTYCheckpointIdentifiers = map[string]struct{}{
	"TerminalPhase":                   {},
	"TerminalPhaseMarker":             {},
	"TerminalPhaseMarkerEncoder":      {},
	"TerminalPhaseMarkerSink":         {},
	"TerminalPhaseMarkerSinkObserver": {},
	"uiTerminalRenderPhaseState":      {},
}

var forbiddenPTYCheckpointLiterals = map[string]struct{}{
	"kent-pty-checkpoint":           {},
	"\x1b]777;":                     {},
	"\x1b]777;kent-pty-checkpoint;": {},
}

func CheckNoProductionPTYCheckpointProtocol(root string) error {
	return checkProductionGo(root, goASTPolicy{
		errorHeading: "PTY checkpoint protocol is forbidden in production app and TUI code",
		inspect: func(source goSourceFile) []string {
			if !isAppOrTUIPath(source.relativePath) {
				return nil
			}
			var violations []string
			for _, spec := range source.file.Imports {
				importPath, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					violations = append(violations, source.detailedViolation(spec.Path, "invalid import path"))
					continue
				}
				if _, forbidden := forbiddenPTYCheckpointImports[importPath]; forbidden {
					violations = append(violations, source.detailedViolation(spec.Path, "forbidden import "+importPath))
				}
			}
			ast.Inspect(source.file, func(node ast.Node) bool {
				switch value := node.(type) {
				case *ast.Ident:
					if _, forbidden := forbiddenPTYCheckpointIdentifiers[value.Name]; forbidden {
						violations = append(violations, source.detailedViolation(value, "forbidden identifier "+value.Name))
					}
				case *ast.BasicLit:
					if value.Kind != token.STRING {
						return true
					}
					literal, err := strconv.Unquote(value.Value)
					if err != nil {
						return true
					}
					if _, forbidden := forbiddenPTYCheckpointLiterals[literal]; forbidden {
						violations = append(violations, source.detailedViolation(value, "forbidden PTY checkpoint literal"))
					}
				}
				return true
			})
			return violations
		},
	})
}

func isAppOrTUIPath(relativePath string) bool {
	for directory := path.Dir(relativePath); directory != "."; directory = path.Dir(directory) {
		if directory == "cli/app" || directory == "cli/tui" {
			return true
		}
	}
	return false
}
