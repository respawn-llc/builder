package main

import (
	"embed"
	"flag"
	"fmt"
	"io"
	"strconv"
	"time"

	"core/shared/config"
)

//go:embed help/*.txt
var helpFS embed.FS

func writeEmbeddedUsage(fs *flag.FlagSet, name string, includeFlags bool) {
	if fs == nil {
		return
	}
	data, err := helpFS.ReadFile("help/" + name)
	if err != nil {
		panic(fmt.Sprintf("read help text %s: %v", name, err))
	}
	_, _ = io.WriteString(fs.Output(), string(data))
	if includeFlags {
		writeCommandFlagDefaults(fs)
	}
}

type commandUsage struct {
	helpFile             string
	includeEmbeddedFlags bool
	includeCommandFlags  bool
	title                string
	lines                []string
}

func (u commandUsage) write(fs *flag.FlagSet) {
	if fs == nil {
		return
	}
	if u.title != "" {
		writeHelpSection(fs.Output(), u.title, u.lines...)
	} else {
		writeEmbeddedUsage(fs, u.helpFile, u.includeEmbeddedFlags)
	}
	if u.includeCommandFlags && flagSetHasDefinitions(fs) {
		writeHelpSection(fs.Output(), "Flags:")
		writeCommandFlagDefaults(fs)
	}
}

func writeCommandFlagDefaults(fs *flag.FlagSet) {
	if fs == nil {
		return
	}
	fs.VisitAll(func(definition *flag.Flag) {
		flagName := "--" + definition.Name
		valueName, usage := flag.UnquoteUsage(definition)
		if valueName != "" {
			flagName += " " + valueName
		}
		_, _ = fmt.Fprintf(fs.Output(), "  %s\n    \t%s", flagName, usage)
		if metadata := commandFlagDefaultMetadataFor(definition); metadata.Visible {
			defaultValue := metadata.Value
			if metadata.Quote {
				defaultValue = strconv.Quote(defaultValue)
			}
			_, _ = fmt.Fprintf(fs.Output(), " (default %s)", defaultValue)
		}
		_, _ = fmt.Fprintln(fs.Output())
	})
}

type commandFlagDefaultMetadata struct {
	Value   string
	Visible bool
	Quote   bool
}

func commandFlagDefaultMetadataFor(definition *flag.Flag) commandFlagDefaultMetadata {
	metadata := commandFlagDefaultMetadata{Value: definition.DefValue}
	getter, ok := definition.Value.(flag.Getter)
	if !ok {
		metadata.Visible = metadata.Value != ""
		return metadata
	}
	switch value := getter.Get().(type) {
	case string:
		metadata.Visible = value != ""
		metadata.Quote = true
	case bool:
		metadata.Visible = value
	case int:
		metadata.Visible = value != 0
	case int64:
		metadata.Visible = value != 0
	case time.Duration:
		metadata.Visible = value != 0
	case uint:
		metadata.Visible = value != 0
	case uint64:
		metadata.Visible = value != 0
	case float64:
		metadata.Visible = value != 0
	default:
		metadata.Visible = metadata.Value != ""
	}
	return metadata
}

func flagSetHasDefinitions(fs *flag.FlagSet) bool {
	hasDefinitions := false
	fs.VisitAll(func(*flag.Flag) {
		hasDefinitions = true
	})
	return hasDefinitions
}

func leafCommandUsage(synopsis string, summary string, details ...string) commandUsage {
	lines := []string{"  " + synopsis, "", summary}
	lines = append(lines, details...)
	return commandUsage{
		title:               "Usage:",
		lines:               lines,
		includeCommandFlags: true,
	}
}

func writeHelpSection(w io.Writer, title string, lines ...string) {
	if w == nil || title == "" {
		return
	}
	_, _ = fmt.Fprintln(w, title)
	for _, line := range lines {
		_, _ = fmt.Fprintln(w, line)
	}
	if len(lines) > 0 {
		_, _ = fmt.Fprintln(w)
	}
}

var (
	rootUsage           = commandUsage{helpFile: "root.txt", includeEmbeddedFlags: true}
	runUsage            = commandUsage{helpFile: "run.txt", includeEmbeddedFlags: true}
	sessionIDUsage      = commandUsage{helpFile: "session_id.txt"}
	goalUsage           = commandUsage{helpFile: "goal.txt"}
	goalShowUsage       = leafCommandUsage(config.Command+" goal show [--json] [--session <id>]", "Show a session's goal and status.")
	goalSetUsage        = leafCommandUsage(config.Command+" goal set [--session <id>] <objective>", "Set the objective that guides a session.")
	goalPauseUsage      = leafCommandUsage(config.Command+" goal pause [--session <id>]", "Pause an active session goal.", "", "User-only; unavailable inside Kent shell commands.")
	goalResumeUsage     = leafCommandUsage(config.Command+" goal resume [--session <id>]", "Resume a paused session goal.", "", "User-only; unavailable inside Kent shell commands.")
	goalCompleteUsage   = leafCommandUsage(config.Command+" goal complete [--session <id>] [--confirm]", "Mark a session goal complete.", "", "Agents must pass `--confirm`; user invocations do not require it.")
	goalClearUsage      = leafCommandUsage(config.Command+" goal clear [--session <id>]", "Remove a session goal.", "", "User-only; unavailable inside Kent shell commands.")
	questionUsage       = commandUsage{helpFile: "question.txt"}
	questionShowUsage   = leafCommandUsage(config.Command+" question (--session <id>|--task <task>) [--project <project>]", "Show the first pending question selected by Session or Workflow Task.")
	questionAnswerUsage = leafCommandUsage(config.Command+" question answer (--session <id>|--task <task>) [--project <project>] [--option <number>] [--commentary <text>]", "Answer the first pending question selected by Session or Workflow Task.", "", "Provide --option, --commentary, or both.")
	worktreeUsage       = leafCommandUsage(config.Command+" worktree <status|list|create|enter|leave|delete> ...", "Inspect workspace worktrees and manage a session's execution target.")
	worktreeStatusUsage = leafCommandUsage(config.Command+" worktree status [--session <id>] [--json]", "Inspect the selected session's recorded worktree target.")
	worktreeListUsage   = leafCommandUsage(config.Command+" worktree list [--session <id>] [--json]", "List registered, external, and missing worktrees.")
	worktreeCreateUsage = leafCommandUsage(config.Command+" worktree create [--session <id>] [--base <ref>] [--json] <branch-or-ref> [path]", "Create and set up a worktree without entering it.")
	worktreeEnterUsage  = leafCommandUsage(config.Command+" worktree enter [--session <id>] [--json] <selector>", "Schedule the session to enter a worktree.")
	worktreeLeaveUsage  = leafCommandUsage(config.Command+" worktree leave [--session <id>] [--json]", "Schedule the session to return to the main workspace.")
	worktreeDeleteUsage = leafCommandUsage(config.Command+" worktree delete [--session <id>] [--force] [--delete-branch] [--force-delete-branch] [--json] <selector>", "Delete a worktree; agent shell commands always retain branches.")
	workflowUsage       = commandUsage{helpFile: "workflow.txt"}
	workflowCreateUsage = leafCommandUsage(config.Command+" workflow create [--description <text>] [--json] <name>", "Create a workflow with `backlog` start and `done` terminal nodes.")
	workflowDeleteUsage = leafCommandUsage(config.Command+" workflow delete <uuid> [--confirm] [--json]", "Preview or permanently delete a workflow.", "", "Without `--confirm`, the command reports the deletion impact and makes no changes.")
	workflowListUsage   = leafCommandUsage(config.Command+" workflow list [--project <path-or-id>] [--page-size <n>] [--page-token <token>] [--json]", "List workflow definitions.")
	workflowGraphUsage  = leafCommandUsage(
		config.Command+" workflow graph <inspect|apply> ...",
		"Inspect or apply a complete Workflow graph.",
	)
	workflowGraphInspectUsage = leafCommandUsage(
		config.Command+" workflow graph inspect <uuid> [--json]",
		"Write the complete authored graph as JSON.",
	)
	workflowGraphApplyUsage = leafCommandUsage(
		config.Command+" workflow graph apply <path|-> [--confirm] [--json]",
		"Atomically apply a complete Workflow graph document.",
		"",
		"Use - to read the document from standard input.",
		"Without --confirm, destructive changes report fresh impact and make no changes.",
	)
	workflowNodeUsage       = leafCommandUsage(config.Command+" workflow node <add|update> ...", "Add or change workflow nodes.")
	workflowNodeAddUsage    = leafCommandUsage(config.Command+" workflow node add <uuid> --key <key> --kind <kind> [flags]", "Add a node to a workflow.")
	workflowNodeUpdateUsage = leafCommandUsage(config.Command+" workflow node update <uuid> <node-key> [flags]", "Change a workflow node.")
	workflowEdgeUsage       = leafCommandUsage(config.Command+" workflow edge <add|update> ...", "Add or change workflow transition branches.")
	workflowEdgeAddUsage    = leafCommandUsage(config.Command+" workflow edge add <uuid> --from <node-key> --transition <key> --edge-key <key> --to <node-key> --context <mode> [flags]", "Add a transition branch between two workflow nodes.")
	workflowEdgeUpdateUsage = leafCommandUsage(config.Command+" workflow edge update <uuid> <edge-id> [flags]", "Change a workflow transition branch.", "", "Use either `--param` or `--clear-params`, not both.")
	workflowLinkUsage       = leafCommandUsage(config.Command+" workflow link <project> <uuid> [--default] [--json]", "Make a workflow available to a project.")
	workflowUnlinkUsage     = leafCommandUsage(config.Command+" workflow unlink <project> <uuid> [--json]", "Remove a workflow from a project.")
	workflowDefaultUsage    = leafCommandUsage(config.Command+" workflow default <project> <uuid> [--json]", "Choose the workflow used when a project task omits `--workflow`.")
	workflowValidateUsage   = leafCommandUsage(config.Command+" workflow validate <uuid> [--mode <mode>] [--json]", "Check whether a workflow is valid for draft editing, task creation, or execution.")
	workflowInspectUsage    = leafCommandUsage(config.Command+" workflow inspect <uuid> [--summary] [--json]", "Inspect a workflow graph or metadata summary.")
	taskUsage               = commandUsage{helpFile: "task.txt"}
	taskCreateUsage         = leafCommandUsage(config.Command+" task create --title <title> (--body <body>|--body-file <path>) [--workflow <uuid>] [flags]", "Create a task in a project.")
	taskEditUsage           = leafCommandUsage(config.Command+" task edit <task> [flags]", "Change a task's title, body, or source workspace.")
	taskStartUsage          = leafCommandUsage(config.Command+" task start <task> [--project <project>] [--execution-target none|head|default-branch|ref:<revision>] [--branch-name <name>] [--ignore-dependencies] [--json]", "Move a new task from the start node into its first executable workflow node.", "", "A workflow Session may start another Task, but not its own.")
	taskInterruptUsage      = leafCommandUsage(config.Command+" task interrupt <task> [--project <project>] [--session <session-id>] [--reason <text>]", "Interrupt live workflow work on a task.", "", "A workflow Session may interrupt another Task, but not its own.")
	taskResumeUsage         = leafCommandUsage(config.Command+" task resume <task> [--project <project>] [--branch-name <name>]", "Resume interrupted work on a task.", "", "A workflow Session may resume another Task, but not its own.")
	taskApproveUsage        = leafCommandUsage(config.Command+" task approve <approval-id>", "Approve a pending workflow transition.", "", "A workflow Session may approve another Task, but not its own.")
	taskMoveUsage           = leafCommandUsage(config.Command+" task move <task> <target-node-id> [--project <project>] [--transition <key>] [--values-json <json>|--values-file <path>] [--commentary <text>] [--execution-target none|head|default-branch|ref:<revision>] [--branch-name <name>] [--ignore-dependencies] [--json]", "Move a task to a workflow node.", "", "A workflow Session may move another Task, but not its own.")
	taskCompleteUsage       = leafCommandUsage(config.Command+" task complete [--transition <key>] [--commentary <text>] [--param name=value] [--session <session-id>|--task <task> [--project <project>]] [--force]", "Submit your task result.", "", "Use this to submit your task and end your turn. This is the only way to end your turn during a workflow.", "Use `--json` or `--json-file` instead of field flags to submit a JSON transition result.", "Positional arguments are not accepted.", "If you're stuck for any reason, use ask_question to ask for help instead of attempting to submit a final_answer. Invoke this command exactly as is described in the workflow instructions you received in a developer reminder.")
	taskListUsage           = leafCommandUsage(config.Command+" task list [--workflow <uuid>] [flags]", "List and filter tasks in a project.")
	taskSearchUsage         = commandUsage{helpFile: "task_search.txt", includeEmbeddedFlags: true}
	taskShowUsage           = leafCommandUsage(config.Command+" task show <task> [--project <project>] [--json]", "Show task content, workflow state, Current Nodes, and comments.")
	taskSessionsUsage       = leafCommandUsage(config.Command+" task sessions <task> [--project <project>] [--offset <n>] [--limit <n>] [--json]", "List retained agent Sessions for a task.")
	taskDeleteUsage         = leafCommandUsage(config.Command+" task delete <task> [--project <project>]", "Permanently delete a task.", "", "User-only; unavailable inside Kent shell commands.")
	taskLabelUsage          = leafCommandUsage(config.Command+" task label <add|create|delete|list|remove|rename> ...", "Manage Project labels and task label assignments.")
	taskLabelCreateUsage    = leafCommandUsage(config.Command+" task label create [--project <project>] [--json] <name>", "Create a Project label.")
	taskLabelListUsage      = leafCommandUsage(config.Command+" task label list [--project <project>] [--name <name>] [--json]", "List labels in a Project catalog.")
	taskLabelRenameUsage    = leafCommandUsage(config.Command+" task label rename --label <name-or-uuid> [--project <project>] [--json] <new-name>", "Rename a Project label.")
	taskLabelDeleteUsage    = leafCommandUsage(config.Command+" task label delete --label <name-or-uuid> [--project <project>] [--json]", "Delete a Project label.")
	taskLabelAddUsage       = leafCommandUsage(config.Command+" task label add <short-id-or-task-id> --label <name-or-uuid>... [--project <project>] [--json]", "Assign existing Project labels to a task.")
	taskLabelRemoveUsage    = leafCommandUsage(config.Command+" task label remove <short-id-or-task-id> --label <name-or-uuid>... [--project <project>] [--json]", "Remove Project labels from a task.")
	taskCommentUsage        = leafCommandUsage(config.Command+" task comment <add|list|replace|delete> ...", "Add, read, replace, or delete task comments.")
	taskCommentAddUsage     = leafCommandUsage(config.Command+" task comment add <task> (--body <text>|--body-file <path>) [flags]", "Add a comment to a task.")
	taskCommentListUsage    = leafCommandUsage(config.Command+" task comment list <task> [flags]", "List a task's comments.")
	taskCommentReplaceUsage = leafCommandUsage(config.Command+" task comment replace <comment-id> --body <text>", "Replace a comment's body.")
	taskCommentDeleteUsage  = leafCommandUsage(config.Command+" task comment delete <comment-id>", "Permanently delete a comment.", "", "User-only; unavailable inside Kent shell commands.")
	projectUsage            = commandUsage{helpFile: "project.txt"}
	projectDefaultUsage     = commandUsage{helpFile: "project_default.txt", includeEmbeddedFlags: true}
	detachUsage             = commandUsage{helpFile: "detach.txt", includeEmbeddedFlags: true}
	projectListUsage        = commandUsage{helpFile: "project_list.txt"}
	projectCreateUsage      = commandUsage{helpFile: "project_create.txt", includeEmbeddedFlags: true}
	projectDeleteUsage      = leafCommandUsage(
		config.Command+" project delete <project-id> [--confirm] [--json]",
		"Delete a Project by canonical ID.",
		"",
		"Deletion is non-interactive and requires --confirm.",
		"Selection accepts only the canonical Project ID; --json emits one stable result envelope.",
		"Agent shells are denied only when the Project contains unfinished work, including Backlog Tasks.",
		"Task state may change before deletion is processed; server blockers remain authoritative.",
		"Workspace files are never deleted.",
	)
	attachUsage           = commandUsage{helpFile: "attach.txt", includeEmbeddedFlags: true}
	rebindUsage           = commandUsage{helpFile: "rebind.txt"}
	serveUsage            = commandUsage{helpFile: "serve.txt", includeEmbeddedFlags: true}
	serviceUsage          = commandUsage{helpFile: "service.txt"}
	serviceStatusUsage    = commandUsage{helpFile: "service_status.txt", includeEmbeddedFlags: true}
	serviceInstallUsage   = commandUsage{helpFile: "service_install.txt", includeEmbeddedFlags: true}
	serviceUninstallUsage = commandUsage{helpFile: "service_uninstall.txt", includeEmbeddedFlags: true}
	serviceRestartUsage   = commandUsage{helpFile: "service_restart.txt", includeEmbeddedFlags: true}
)

var (
	taskDependencyUsage       = leafCommandUsage(config.Command+" task dep <add|remove|list> ...", "Manage direct Task Dependencies.")
	taskDependencyAddUsage    = leafCommandUsage(config.Command+" task dep add --blocker <task> --blocked <task> [--project <project>] [--json]", "Add a direct Task Dependency.")
	taskDependencyRemoveUsage = leafCommandUsage(config.Command+" task dep remove --blocker <task> --blocked <task> [--project <project>] [--json]", "Remove a direct Task Dependency.")
	taskDependencyListUsage   = leafCommandUsage(config.Command+" task dep list <task> [--direction blocks|blocked-by] [--project <project>] [--json]", "List a Task's direct dependencies.")
)
