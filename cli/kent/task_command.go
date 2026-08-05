package main

import "io"

func taskSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	return dispatchCommandGroup(args, stdout, stderr, commandGroup{
		path:  "task",
		usage: taskUsage,
		routes: map[string]commandHandler{
			"create":       taskCreateSubcommand,
			"edit":         taskEditSubcommand,
			"start":        taskStartSubcommand,
			"interrupt":    taskInterruptSubcommand,
			"list":         taskListSubcommand,
			"search":       taskSearchSubcommand,
			"show":         taskShowSubcommand,
			"delete":       taskDeleteSubcommand,
			"approve":      taskApproveSubcommand,
			"move":         taskMoveSubcommand,
			"complete":     taskCompleteSubcommand,
			"wait":         taskWaitSubcommand,
			"watch":        taskWatchSubcommand,
			"resume":       taskResumeSubcommand,
			"comment":      taskCommentSubcommand,
			"comments":     taskCommentSubcommand,
			"label":        taskLabelSubcommand,
			"dep":          taskDependencySubcommand,
			"deps":         taskDependencySubcommand,
			"dependency":   taskDependencySubcommand,
			"dependencies": taskDependencySubcommand,
		},
	})
}
