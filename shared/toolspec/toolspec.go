package toolspec

import (
	"sort"
	"strings"
)

type ID string

const (
	ToolExecCommand    ID = "exec_command"
	ToolWriteStdin     ID = "write_stdin"
	ToolViewImage      ID = "view_image"
	ToolPatch          ID = "patch"
	ToolEdit           ID = "edit"
	ToolAskQuestion    ID = "ask_question"
	ToolCompleteNode   ID = "complete_node"
	ToolTriggerHandoff ID = "trigger_handoff"
	ToolWebSearch      ID = "web_search"
)

var catalogIDs = []ID{
	ToolAskQuestion,
	ToolCompleteNode,
	ToolEdit,
	ToolExecCommand,
	ToolPatch,
	ToolTriggerHandoff,
	ToolViewImage,
	ToolWebSearch,
	ToolWriteStdin,
}

var defaultEnabledIDs = []ID{
	ToolAskQuestion,
	ToolExecCommand,
	ToolPatch,
	ToolTriggerHandoff,
	ToolViewImage,
	ToolWebSearch,
	ToolWriteStdin,
}

var parseAliases = map[string]ID{
	"ask_question":    ToolAskQuestion,
	"bash":            ToolExecCommand,
	"bash_command":    ToolExecCommand,
	"exec_command":    ToolExecCommand,
	"edit":            ToolEdit,
	"complete_node":   ToolCompleteNode,
	"patch":           ToolPatch,
	"read_image":      ToolViewImage,
	"shell":           ToolExecCommand,
	"shell_command":   ToolExecCommand,
	"trigger_handoff": ToolTriggerHandoff,
	"view_image":      ToolViewImage,
	"web_search":      ToolWebSearch,
	"replace":         ToolEdit,
	"write":           ToolEdit,
	"write_stdin":     ToolWriteStdin,
}

var configAliases = map[string]ID{
	"ask_question":    ToolAskQuestion,
	"edit":            ToolEdit,
	"exec_command":    ToolExecCommand,
	"patch":           ToolPatch,
	"read_image":      ToolViewImage,
	"shell":           ToolExecCommand,
	"trigger_handoff": ToolTriggerHandoff,
	"view_image":      ToolViewImage,
	"web_search":      ToolWebSearch,
	"write_stdin":     ToolWriteStdin,
}

func init() {
	sort.Slice(catalogIDs, func(i, j int) bool { return catalogIDs[i] < catalogIDs[j] })
	sort.Slice(defaultEnabledIDs, func(i, j int) bool { return defaultEnabledIDs[i] < defaultEnabledIDs[j] })
}

func ParseID(v string) (ID, bool) {
	id, ok := parseAliases[strings.TrimSpace(v)]
	return id, ok
}

func ParseConfigID(v string) (ID, bool) {
	id, ok := configAliases[strings.TrimSpace(v)]
	return id, ok
}

func ConfigName(id ID) string {
	if id == ToolExecCommand {
		return "shell"
	}
	return string(id)
}

func CatalogIDs() []ID {
	out := make([]ID, len(catalogIDs))
	copy(out, catalogIDs)
	return out
}

func DefaultEnabledToolIDs() []ID {
	out := make([]ID, len(defaultEnabledIDs))
	copy(out, defaultEnabledIDs)
	return out
}

func IDStrings(ids []ID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		out = append(out, string(id))
	}
	return out
}
