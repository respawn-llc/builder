package toolspec

import (
	"strings"
	"unicode"
)

type modelToolNameMatchStage uint8

const (
	modelToolNameMatchCanonical modelToolNameMatchStage = iota + 1
	modelToolNameMatchSemanticAlias
	modelToolNameMatchCanonicalCamelCase
	modelToolNameMatchSemanticAliasCamelCase
	modelToolNameMatchCaseInsensitive
)

type toolAliasSpec struct {
	id            ID
	aliases       []string
	variations    bool
	legacyAliases []string
	configurable  bool
	configAliases []string
}

type parameterAliasSpec struct {
	canonical string
	aliases   []string
}

type toolAliasCatalog struct {
	tools      []toolAliasSpec
	parameters map[ID][]parameterAliasSpec
}

func newToolAliasCatalog(specs []toolAliasSpec) toolAliasCatalog {
	tools := make([]toolAliasSpec, len(specs))
	for i, spec := range specs {
		tools[i] = toolAliasSpec{
			id:            spec.id,
			aliases:       append([]string(nil), spec.aliases...),
			variations:    spec.variations,
			legacyAliases: append([]string(nil), spec.legacyAliases...),
			configurable:  spec.configurable,
			configAliases: append([]string(nil), spec.configAliases...),
		}
	}
	return toolAliasCatalog{tools: tools}
}

func newParameterAliasCatalog(id ID, specs []parameterAliasSpec) map[ID][]parameterAliasSpec {
	owned := make([]parameterAliasSpec, 0, len(specs))
	seen := make(map[string]string)
	for _, spec := range specs {
		if strings.TrimSpace(spec.canonical) == "" {
			panic("parameter canonical name is required")
		}
		for _, spelling := range parameterSpellings(spec) {
			key := strings.ToLower(spelling)
			if previous, exists := seen[key]; exists && previous != spec.canonical {
				panic("parameter aliases resolve to different canonical parameters")
			}
			seen[key] = spec.canonical
		}
		owned = append(owned, parameterAliasSpec{
			canonical: spec.canonical,
			aliases:   append([]string(nil), spec.aliases...),
		})
	}
	return map[ID][]parameterAliasSpec{id: owned}
}

var modelToolAliases = newToolAliasCatalog([]toolAliasSpec{
	{
		id:            ToolExecCommand,
		aliases:       []string{"shell", "bash", "exec", "run_command", "shell_command", "run_shell", "bash_command"},
		variations:    true,
		legacyAliases: []string{"shell", "bash", "bash_command", "shell_command"},
		configurable:  true,
		configAliases: []string{"shell"},
	},
	{id: ToolWriteStdin, variations: true, configurable: true},
	{
		id:            ToolViewImage,
		aliases:       []string{"read_image", "open_image", "inspect_image", "vision", "read_pdf", "open_pdf", "inspect_pdf"},
		variations:    true,
		legacyAliases: []string{"read_image"},
		configurable:  true,
		configAliases: []string{"read_image"},
	},
	{id: ToolPatch, aliases: []string{"apply_patch", "edit"}, variations: true, configurable: true},
	{
		id:           ToolAskQuestion,
		aliases:      []string{"question", "ask_user_question", "request_user_input", "ask", "ask_user", "ask_human", "help", "say"},
		variations:   true,
		configurable: true,
	},
	{id: ToolCompleteNode},
	{id: ToolTriggerHandoff, aliases: []string{"handoff", "compact", "request_handoff"}, variations: true, configurable: true},
	{id: ToolWebSearch, configurable: true},
	{id: ToolEdit, aliases: []string{"edit_file", "str_replace_editor", "replace", "string_replace", "replace_text", "write"}, variations: true, legacyAliases: []string{"replace", "write"}, configurable: true},
})

func approvedModelParameterAliases() map[ID][]parameterAliasSpec {
	return map[ID][]parameterAliasSpec{
		ToolExecCommand: {
			{canonical: "cmd", aliases: []string{"command", "script"}},
			{canonical: "workdir", aliases: []string{"cwd", "working_directory", "working_dir"}},
			{canonical: "shell", aliases: []string{"shell_path", "interpreter"}},
			{canonical: "login", aliases: []string{"login_shell"}},
			{canonical: "tty", aliases: []string{"pty", "use_tty"}},
			{canonical: "raw", aliases: []string{"raw_output"}},
			{canonical: "yield_time_ms", aliases: []string{"yield_ms", "wait_ms"}},
			{canonical: "max_output_tokens", aliases: []string{"max_tokens", "output_token_limit"}},
		},
		ToolWriteStdin: {
			{canonical: "session_id", aliases: []string{"process_id", "shell_id"}},
			{canonical: "chars", aliases: []string{"input", "stdin", "text"}},
			{canonical: "yield_time_ms", aliases: []string{"yield_ms", "wait_ms"}},
			{canonical: "max_output_tokens", aliases: []string{"max_tokens", "output_token_limit"}},
		},
		ToolViewImage: {
			{canonical: "path", aliases: []string{"file_path", "image_path", "file", "pdf_path", "filename"}},
			{canonical: "raw", aliases: []string{"raw_output", "unoptimized", "disable_optimization", "original_quality"}},
		},
		ToolPatch: {
			{canonical: "patch", aliases: []string{"diff", "patch_text", "content", "patch_content", "input"}},
		},
		ToolEdit: {
			{canonical: "path", aliases: []string{"file_path", "file"}},
			{canonical: "old_string", aliases: []string{"old_text", "find", "search"}},
			{canonical: "new_string", aliases: []string{"new_text", "replacement", "replace"}},
			{canonical: "replace_all", aliases: []string{"all", "global"}},
		},
		ToolAskQuestion: {
			{canonical: "question", aliases: []string{"prompt", "message", "text"}},
			{canonical: "suggestions", aliases: []string{"options", "choices", "answers"}},
			{canonical: "recommended_option_index", aliases: []string{"recommended_index", "suggested_option_index", "default_index"}},
		},
		ToolTriggerHandoff: {
			{canonical: "summarizer_prompt", aliases: []string{"summary_prompt", "handoff_prompt", "compaction_prompt"}},
			{canonical: "future_agent_message", aliases: []string{"next_agent_message", "handoff_message", "continuation_message"}},
		},
	}
}

func init() {
	modelToolAliases.parameters = approvedModelParameterAliases()
	validateParameterAliasCatalog(modelToolAliases.parameters)
}

func validateParameterAliasCatalog(parameters map[ID][]parameterAliasSpec) {
	for id, specs := range parameters {
		newParameterAliasCatalog(id, specs)
	}
}

func modelToolAliasSpelling(spec toolAliasSpec) []string {
	out := make([]string, 0, len(spec.aliases)+1)
	for _, alias := range spec.aliases {
		out = append(out, alias)
		if kebab := kebabCase(alias); kebab != alias {
			out = append(out, kebab)
		}
	}
	return out
}

func modelToolAcceptedSpellings(spec toolAliasSpec) []string {
	out := []string{string(spec.id)}
	if spec.variations {
		out = append(out, kebabCase(string(spec.id)), generatedCamelCase(string(spec.id)))
	}
	out = append(out, modelToolAliasSpelling(spec)...)
	if spec.variations {
		for _, alias := range spec.aliases {
			out = append(out, generatedCamelCase(alias))
		}
	}
	return uniqueStrings(out)
}

func generatedCamelCase(spelling string) string {
	var out strings.Builder
	upperNext := false
	for _, r := range spelling {
		if r == '_' || r == '-' {
			upperNext = true
			continue
		}
		if upperNext {
			r = unicode.ToUpper(r)
			upperNext = false
		}
		out.WriteRune(r)
	}
	return out.String()
}

func kebabCase(spelling string) string {
	return strings.ReplaceAll(spelling, "_", "-")
}

func parameterSpellings(spec parameterAliasSpec) []string {
	out := make([]string, 0, len(spec.aliases)*3+4)
	appendGenerated := func(spelling string) {
		out = append(out, spelling)
		if kebab := kebabCase(spelling); kebab != spelling {
			out = append(out, kebab)
		}
		if camel := generatedCamelCase(spelling); camel != spelling {
			out = append(out, camel)
		}
	}
	appendGenerated(spec.canonical)
	for _, alias := range spec.aliases {
		appendGenerated(alias)
	}
	return out
}

func resolveModelToolName(name string, registered []ID) (ID, modelToolNameMatchStage, bool) {
	spelling := name
	active := make(map[ID]bool, len(registered))
	for _, id := range registered {
		active[id] = true
	}
	if id, stage, ok := resolveModelToolNameFromCatalog(spelling, active, len(active) > 0); ok {
		return id, stage, true
	}
	if len(active) > 0 {
		return resolveModelToolNameFromCatalog(spelling, nil, false)
	}
	return "", 0, false
}

func resolveModelToolNameFromCatalog(spelling string, active map[ID]bool, filterActive bool) (ID, modelToolNameMatchStage, bool) {
	find := func(stage modelToolNameMatchStage, candidates func(toolAliasSpec) []string) (ID, bool) {
		for _, spec := range modelToolAliases.tools {
			if filterActive && !active[spec.id] {
				continue
			}
			for _, candidate := range candidates(spec) {
				if spelling == candidate {
					return spec.id, true
				}
			}
		}
		return "", false
	}
	if id, ok := find(modelToolNameMatchCanonical, func(spec toolAliasSpec) []string { return []string{string(spec.id)} }); ok {
		return id, modelToolNameMatchCanonical, true
	}
	if id, ok := find(modelToolNameMatchSemanticAlias, func(spec toolAliasSpec) []string {
		out := modelToolAliasSpelling(spec)
		if spec.variations {
			if kebab := kebabCase(string(spec.id)); kebab != string(spec.id) {
				out = append(out, kebab)
			}
		}
		return out
	}); ok {
		return id, modelToolNameMatchSemanticAlias, true
	}
	if id, ok := find(modelToolNameMatchCanonicalCamelCase, func(spec toolAliasSpec) []string {
		if !spec.variations {
			return nil
		}
		return []string{generatedCamelCase(string(spec.id))}
	}); ok {
		return id, modelToolNameMatchCanonicalCamelCase, true
	}
	if id, ok := find(modelToolNameMatchSemanticAliasCamelCase, func(spec toolAliasSpec) []string {
		if !spec.variations {
			return nil
		}
		out := make([]string, 0, len(spec.aliases))
		for _, alias := range spec.aliases {
			out = append(out, generatedCamelCase(alias))
		}
		return out
	}); ok {
		return id, modelToolNameMatchSemanticAliasCamelCase, true
	}
	for _, spec := range modelToolAliases.tools {
		if len(active) > 0 && !active[spec.id] {
			continue
		}
		if !spec.variations {
			continue
		}
		for _, candidate := range modelToolAcceptedSpellings(spec) {
			if strings.EqualFold(spelling, candidate) {
				return spec.id, modelToolNameMatchCaseInsensitive, true
			}
		}
	}
	return "", 0, false
}

func ResolveModelToolName(name string, registered []ID) (ID, bool) {
	id, _, ok := resolveModelToolName(name, registered)
	return id, ok
}

func resolveModelParameterName(tool ID, name string) (string, int, bool) {
	spelling := name
	specs := modelToolAliases.parameters[tool]
	for _, spec := range specs {
		if spelling == spec.canonical {
			return spec.canonical, 0, true
		}
	}
	for _, spec := range specs {
		for _, candidate := range canonicalParameterDerivedSpellings(spec) {
			if spelling == candidate {
				return spec.canonical, 1, true
			}
		}
	}
	for _, spec := range specs {
		for _, candidate := range semanticParameterSpellings(spec) {
			if spelling == candidate {
				return spec.canonical, 2, true
			}
		}
	}
	for _, spec := range specs {
		for _, candidate := range canonicalParameterSpellings(spec) {
			if strings.EqualFold(spelling, candidate) {
				return spec.canonical, 1, true
			}
		}
	}
	for _, spec := range specs {
		for _, candidate := range semanticParameterSpellings(spec) {
			if strings.EqualFold(spelling, candidate) {
				return spec.canonical, 2, true
			}
		}
	}
	return "", 0, false
}

func ResolveModelParameterName(tool ID, name string) (string, bool) {
	canonical, _, ok := resolveModelParameterName(tool, name)
	return canonical, ok
}

func MatchModelParameterName(tool ID, name string) (string, int, bool) {
	return resolveModelParameterName(tool, name)
}

func canonicalParameterSpellings(spec parameterAliasSpec) []string {
	return append([]string{spec.canonical}, canonicalParameterDerivedSpellings(spec)...)
}

func canonicalParameterDerivedSpellings(spec parameterAliasSpec) []string {
	out := []string{generatedCamelCase(spec.canonical)}
	if kebab := kebabCase(spec.canonical); kebab != spec.canonical {
		out = append(out, kebab)
	}
	if spec.canonical == "yield_time_ms" {
		out = append(out, "yield-time_ms", "yield-time-ms")
	}
	return uniqueStrings(out)
}

func semanticParameterSpellings(spec parameterAliasSpec) []string {
	out := make([]string, 0, len(spec.aliases)*3)
	for _, alias := range spec.aliases {
		out = append(out, alias, generatedCamelCase(alias))
		if kebab := kebabCase(alias); kebab != alias {
			out = append(out, kebab)
		}
	}
	return uniqueStrings(out)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func buildLegacyParseAliases() map[string]ID {
	out := make(map[string]ID)
	for _, spec := range modelToolAliases.tools {
		out[string(spec.id)] = spec.id
		for _, alias := range spec.legacyAliases {
			out[alias] = spec.id
		}
	}
	return out
}

func buildConfigAliases() map[string]ID {
	out := make(map[string]ID)
	for _, spec := range modelToolAliases.tools {
		if !spec.configurable {
			continue
		}
		out[string(spec.id)] = spec.id
		for _, alias := range spec.configAliases {
			out[alias] = spec.id
		}
	}
	return out
}

func validateToolAliasCatalog(catalog toolAliasCatalog, registered []ID) {
	seen := make(map[string]ID)
	for _, id := range registered {
		for _, spec := range catalog.tools {
			if spec.id != id {
				continue
			}
			for _, spelling := range modelToolAcceptedSpellings(spec) {
				key := spelling
				if spec.variations {
					key = strings.ToLower(spelling)
				}
				if previous, exists := seen[key]; exists && previous != id {
					panic("tool aliases resolve to different registered tools")
				}
				seen[key] = id
			}
		}
	}
}

func ValidateModelToolAliases(registered []ID) {
	validateToolAliasCatalog(modelToolAliases, registered)
}
