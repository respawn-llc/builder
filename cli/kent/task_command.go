package main

import "io"

func taskSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	return dispatchCommandGroup(args, stdout, stderr, commandGroup{
		path:  "task",
		usage: taskUsage,
		routes: map[string]commandHandler{
			"create":   taskCreateSubcommand,
			"edit":     taskEditSubcommand,
			"start":    taskStartSubcommand,
			"list":     taskListSubcommand,
			"show":     taskShowSubcommand,
			"cancel":   taskCancelSubcommand,
			"delete":   taskDeleteSubcommand,
			"approve":  taskApproveSubcommand,
			"move":     taskMoveSubcommand,
			"complete": taskCompleteSubcommand,
			"resume":   taskResumeSubcommand,
			"comment":  taskCommentSubcommand,
			"comments": taskCommentSubcommand,
			"label":    taskLabelSubcommand,
		},
	})
}
