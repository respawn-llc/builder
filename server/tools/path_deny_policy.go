package tools

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"core/shared/config"
)

type PathMatcherKind string

const (
	PathMatcherLiteral PathMatcherKind = "literal"
	PathMatcherGlob    PathMatcherKind = "glob"
	PathMatcherRegex   PathMatcherKind = "regex"
)

type PathMatcherConfig struct {
	Kind        PathMatcherKind
	Pattern     string
	LiteralTree bool
}

type PathDenyRuleConfig struct {
	Label   *string
	Matcher PathMatcherConfig
	Message string
}

type PathDenyMatch struct {
	Label   *string
	Message string
}

type PathDenyPolicy struct {
	rules []compiledPathDenyRule
}

type compiledPathDenyRule struct {
	label   *string
	message string
	matcher pathMatcher
}

type pathMatcher interface {
	Match(pathIdentity string) bool
}

type literalPathMatcher struct {
	root string
	tree bool
}

type globPathMatcher struct {
	pattern string
}

type regexPathMatcher struct {
	pattern *regexp.Regexp
}

func CompilePathDenyPolicy(rules []PathDenyRuleConfig) (PathDenyPolicy, error) {
	compiled := make([]compiledPathDenyRule, 0, len(rules))
	for index, rule := range rules {
		message := strings.TrimSpace(rule.Message)
		if message == "" {
			return PathDenyPolicy{}, fmt.Errorf("compile path deny rule %d: message is required", index+1)
		}
		matcher, err := compilePathMatcher(rule.Matcher)
		if err != nil {
			return PathDenyPolicy{}, fmt.Errorf("compile path deny rule %d: %w", index+1, err)
		}
		label, err := compilePathDenyRuleLabel(rule.Label)
		if err != nil {
			return PathDenyPolicy{}, fmt.Errorf("compile path deny rule %d: %w", index+1, err)
		}
		compiled = append(compiled, compiledPathDenyRule{
			label:   label,
			message: message,
			matcher: matcher,
		})
	}
	return PathDenyPolicy{rules: compiled}, nil
}

func (p PathDenyPolicy) Match(candidate string) (PathDenyMatch, bool, error) {
	if len(p.rules) == 0 {
		return PathDenyMatch{}, false, nil
	}
	identity, err := config.CanonicalPathIdentity(candidate)
	if err != nil {
		return PathDenyMatch{}, false, err
	}
	for _, rule := range p.rules {
		if rule.matcher.Match(identity) {
			return PathDenyMatch{Label: rule.label, Message: rule.message}, true, nil
		}
	}
	return PathDenyMatch{}, false, nil
}

func compilePathDenyRuleLabel(raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*raw)
	if trimmed == "" {
		return nil, fmt.Errorf("diagnostic label cannot be blank")
	}
	return &trimmed, nil
}

func compilePathMatcher(cfg PathMatcherConfig) (pathMatcher, error) {
	switch cfg.Kind {
	case PathMatcherLiteral:
		pattern, err := config.CanonicalPathIdentity(cfg.Pattern)
		if err != nil {
			return nil, err
		}
		return literalPathMatcher{root: pattern, tree: cfg.LiteralTree}, nil
	case PathMatcherGlob:
		pattern, err := config.CanonicalPathIdentity(cfg.Pattern)
		if err != nil {
			return nil, err
		}
		normalized := filepath.ToSlash(pattern)
		if err := validateGlobPattern(normalized); err != nil {
			return nil, err
		}
		return globPathMatcher{pattern: normalized}, nil
	case PathMatcherRegex:
		pattern := filepath.ToSlash(strings.TrimSpace(cfg.Pattern))
		if pattern == "" {
			return nil, fmt.Errorf("regex pattern is required")
		}
		if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
			pattern = "(?i:" + pattern + ")"
		}
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("compile regex path matcher: %w", err)
		}
		return regexPathMatcher{pattern: compiled}, nil
	default:
		return nil, fmt.Errorf("unsupported path matcher kind %q", cfg.Kind)
	}
}

func (m literalPathMatcher) Match(candidate string) bool {
	if candidate == m.root {
		return true
	}
	if !m.tree {
		return false
	}
	rel, err := filepath.Rel(m.root, candidate)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (m globPathMatcher) Match(candidate string) bool {
	return matchGlobSegments(splitGlobPath(m.pattern), splitGlobPath(filepath.ToSlash(candidate)))
}

func (m regexPathMatcher) Match(candidate string) bool {
	return m.pattern.MatchString(filepath.ToSlash(candidate))
}

func validateGlobPattern(pattern string) error {
	for _, segment := range splitGlobPath(pattern) {
		if segment == "**" {
			continue
		}
		if _, err := path.Match(segment, segment); err != nil {
			return fmt.Errorf("invalid glob path matcher: %w", err)
		}
	}
	return nil
}

func splitGlobPath(value string) []string {
	return strings.Split(value, "/")
}

func matchGlobSegments(pattern []string, candidate []string) bool {
	if len(pattern) == 0 {
		return len(candidate) == 0
	}
	if pattern[0] == "**" {
		if matchGlobSegments(pattern[1:], candidate) {
			return true
		}
		return len(candidate) > 0 && matchGlobSegments(pattern, candidate[1:])
	}
	if len(candidate) == 0 {
		return false
	}
	matched, err := path.Match(pattern[0], candidate[0])
	if err != nil || !matched {
		return false
	}
	return matchGlobSegments(pattern[1:], candidate[1:])
}
