package main

import "io"

func taskSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	return dispatchCommandGroup(args, stdout, stderr, commandGroup{
		path:  "task",
		usage: taskUsage,
		routes: map[string]commandHandler{
			"create":    taskCreateSubcommand,
			"edit":      taskEditSubcommand,
			"start":     taskStartSubcommand,
			"interrupt": taskInterruptSubcommand,
			"list":      taskListSubcommand,
			"show":      taskShowSubcommand,
			"delete":    taskDeleteSubcommand,
			"approve":   taskApproveSubcommand,
			"move":      taskMoveSubcommand,
			"complete":  taskCompleteSubcommand,
			"resume":    taskResumeSubcommand,
			"comment":   taskCommentSubcommand,
			"comments":  taskCommentSubcommand,
			"label":     taskLabelSubcommand,
		},
	})
}
