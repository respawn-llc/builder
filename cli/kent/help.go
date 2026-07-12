package main

import (
	"embed"
	"flag"
	"fmt"
	"io"

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
		fs.PrintDefaults()
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
		fs.PrintDefaults()
	}
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
	rootUsage               = commandUsage{helpFile: "root.txt", includeEmbeddedFlags: true}
	runUsage                = commandUsage{helpFile: "run.txt", includeEmbeddedFlags: true}
	sessionIDUsage          = commandUsage{helpFile: "session_id.txt"}
	goalUsage               = commandUsage{helpFile: "goal.txt"}
	goalShowUsage           = leafCommandUsage(config.Command+" goal show [--json] [--session <id>]", "Show a session's goal and status.")
	goalSetUsage            = leafCommandUsage(config.Command+" goal set [--session <id>] <objective>", "Set the objective that guides a session.")
	goalPauseUsage          = leafCommandUsage(config.Command+" goal pause [--session <id>]", "Pause an active session goal.", "", "User-only; unavailable inside Kent shell commands.")
	goalResumeUsage         = leafCommandUsage(config.Command+" goal resume [--session <id>]", "Resume a paused session goal.", "", "User-only; unavailable inside Kent shell commands.")
	goalCompleteUsage       = leafCommandUsage(config.Command+" goal complete [--session <id>] [--confirm]", "Mark a session goal complete.", "", "Agents must pass `--confirm`; user invocations do not require it.")
	goalClearUsage          = leafCommandUsage(config.Command+" goal clear [--session <id>]", "Remove a session goal.", "", "User-only; unavailable inside Kent shell commands.")
	worktreeUsage           = leafCommandUsage(config.Command+" worktree [status] [--session <id>] [--json]", "Inspect the selected session's recorded worktree target.")
	worktreeStatusUsage     = leafCommandUsage(config.Command+" worktree status [--session <id>] [--json]", "Inspect the selected session's recorded worktree target.")
	workflowUsage           = commandUsage{helpFile: "workflow.txt"}
	workflowCreateUsage     = leafCommandUsage(config.Command+" workflow create [--description <text>] [--json] <name>", "Create a workflow with `backlog` start and `done` terminal nodes.")
	workflowListUsage       = leafCommandUsage(config.Command+" workflow list [--page-size <n>] [--page-token <token>] [--json]", "List workflow definitions.")
	workflowNodeUsage       = leafCommandUsage(config.Command+" workflow node <add|update> ...", "Add or change workflow nodes.")
	workflowNodeAddUsage    = leafCommandUsage(config.Command+" workflow node add <workflow> --key <key> --kind <kind> [flags]", "Add a node to a workflow.")
	workflowNodeUpdateUsage = leafCommandUsage(config.Command+" workflow node update <workflow> <node-key> [flags]", "Change a workflow node.")
	workflowEdgeUsage       = leafCommandUsage(config.Command+" workflow edge <add|update> ...", "Add or change workflow transition branches.")
	workflowEdgeAddUsage    = leafCommandUsage(config.Command+" workflow edge add <workflow> --from <node-key> --transition <key> --edge-key <key> --to <node-key> --context <mode> [flags]", "Add a transition branch between two workflow nodes.")
	workflowEdgeUpdateUsage = leafCommandUsage(config.Command+" workflow edge update <workflow> <edge-id> [flags]", "Change a workflow transition branch.", "", "Use either `--param` or `--clear-params`, not both.")
	workflowLinkUsage       = leafCommandUsage(config.Command+" workflow link <project> <workflow> [--default] [--json]", "Make a workflow available to a project.")
	workflowUnlinkUsage     = leafCommandUsage(config.Command+" workflow unlink <project> <workflow> [--json]", "Remove a workflow from a project.")
	workflowDefaultUsage    = leafCommandUsage(config.Command+" workflow default <project> <workflow> [--json]", "Choose the workflow used when a project task omits `--workflow`.")
	workflowValidateUsage   = leafCommandUsage(config.Command+" workflow validate <workflow> [--mode <mode>] [--json]", "Check whether a workflow is valid for draft editing, task creation, or execution.")
	workflowInspectUsage    = leafCommandUsage(config.Command+" workflow inspect <workflow> [--json]", "Show a workflow's nodes, transitions, and configuration.")
	taskUsage               = commandUsage{helpFile: "task.txt"}
	taskCreateUsage         = leafCommandUsage(config.Command+" task create --title <title> (--body <body>|--body-file <path>) [flags]", "Create a task in a project.")
	taskEditUsage           = leafCommandUsage(config.Command+" task edit <task> [flags]", "Change a task's title, body, or source workspace.")
	taskStartUsage          = leafCommandUsage(config.Command+" task start <task> [--project <project>]", "Move a new task from the start node into its first executable workflow node.", "", "User-only; unavailable inside Kent shell commands.")
	taskResumeUsage         = leafCommandUsage(config.Command+" task resume <task> [--project <project>]", "Resume interrupted work on a task.", "", "User-only; unavailable inside Kent shell commands.")
	taskApproveUsage        = leafCommandUsage(config.Command+" task approve <transition-id>", "Approve a pending workflow transition.", "", "User-only; unavailable inside Kent shell commands.")
	taskMoveUsage           = leafCommandUsage(config.Command+" task move <task> <target-node-id> [flags]", "Move a task to a workflow node.", "", "User-only; unavailable inside Kent shell commands.")
	taskCompleteUsage       = leafCommandUsage(config.Command+" task complete [--transition <key>] [--commentary <text>] [--param name=value] [--run <run-id>|--session <session-id>|--task <task> [--project <project>]] [--force]", "Submit the transition result for an active workflow run.", "", "Inside a Kent shell command, the current run is selected automatically.", "Outside Kent shell commands, pass `--force` and exactly one of `--run`, `--session`, or `--task`.", "Use `--json` or `--json-file` instead of field flags to submit a JSON transition result.", "Positional arguments are not accepted.")
	taskListUsage           = leafCommandUsage(config.Command+" task list [flags]", "List and filter tasks in a project.")
	taskShowUsage           = leafCommandUsage(config.Command+" task show <task> [--project <project>] [--json]", "Show task content, workflow state, runs, and comments.")
	taskCancelUsage         = leafCommandUsage(config.Command+" task cancel <task> [--reason <text>] [--project <project>]", "Cancel a task and stop further workflow automation.", "", "User-only; unavailable inside Kent shell commands.")
	taskDeleteUsage         = leafCommandUsage(config.Command+" task delete <task> [--project <project>]", "Permanently delete a task.", "", "User-only; unavailable inside Kent shell commands.")
	taskCommentUsage        = leafCommandUsage(config.Command+" task comment <add|list|replace|delete> ...", "Add, read, replace, or delete task comments.")
	taskCommentAddUsage     = leafCommandUsage(config.Command+" task comment add <task> (--body <text>|--body-file <path>) [flags]", "Add a comment to a task.")
	taskCommentListUsage    = leafCommandUsage(config.Command+" task comment list <task> [flags]", "List a task's comments.")
	taskCommentReplaceUsage = leafCommandUsage(config.Command+" task comment replace <comment-id> --body <text>", "Replace a comment's body.")
	taskCommentDeleteUsage  = leafCommandUsage(config.Command+" task comment delete <comment-id>", "Permanently delete a comment.", "", "User-only; unavailable inside Kent shell commands.")
	projectUsage            = commandUsage{helpFile: "project.txt"}
	projectListUsage        = commandUsage{helpFile: "project_list.txt"}
	projectCreateUsage      = commandUsage{helpFile: "project_create.txt", includeEmbeddedFlags: true}
	attachUsage             = commandUsage{helpFile: "attach.txt", includeEmbeddedFlags: true}
	rebindUsage             = commandUsage{helpFile: "rebind.txt"}
	serveUsage              = commandUsage{helpFile: "serve.txt", includeEmbeddedFlags: true}
	serviceUsage            = commandUsage{helpFile: "service.txt"}
	serviceStatusUsage      = commandUsage{helpFile: "service_status.txt", includeEmbeddedFlags: true}
	serviceInstallUsage     = commandUsage{helpFile: "service_install.txt", includeEmbeddedFlags: true}
	serviceUninstallUsage   = commandUsage{helpFile: "service_uninstall.txt", includeEmbeddedFlags: true}
	serviceRestartUsage     = commandUsage{helpFile: "service_restart.txt", includeEmbeddedFlags: true}
)
