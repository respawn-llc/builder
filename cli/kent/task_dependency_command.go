package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"core/shared/config"
	"core/shared/serverapi"
)

type taskDependencyMutationKind string

const (
	taskDependencyMutationAdd    taskDependencyMutationKind = "add"
	taskDependencyMutationRemove taskDependencyMutationKind = "remove"
)

func taskDependencySubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	return dispatchCommandGroup(args, stdout, stderr, commandGroup{
		path:  "task dep",
		usage: taskDependencyUsage,
		routes: map[string]commandHandler{
			"add":    taskDependencyAddSubcommand,
			"remove": taskDependencyRemoveSubcommand,
			"list":   taskDependencyListSubcommand,
		},
	})
}

func taskDependencyAddSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	return taskDependencyMutationSubcommand(taskDependencyMutationAdd, args, stdout, stderr)
}

func taskDependencyRemoveSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	return taskDependencyMutationSubcommand(taskDependencyMutationRemove, args, stdout, stderr)
}

func taskDependencyMutationSubcommand(kind taskDependencyMutationKind, args []string, stdout io.Writer, stderr io.Writer) int {
	var usage commandUsage
	switch kind {
	case taskDependencyMutationAdd:
		usage = taskDependencyAddUsage
	case taskDependencyMutationRemove:
		usage = taskDependencyRemoveUsage
	default:
		panic(fmt.Sprintf("unsupported task dependency mutation %q", kind))
	}
	fs := newCommandFlagSet(config.Command+" task dep "+string(kind), stderr, usage)
	blockerRef := fs.String("blocker", "", "Blocker Task ID or project-scoped Short ID")
	blockedRef := fs.String("blocked", "", "Blocked Task ID or project-scoped Short ID")
	projectRef := fs.String("project", ".", "project ID or attached workspace path used to resolve Short IDs")
	jsonOut := fs.Bool("json", false, "write the typed dependency outcome as JSON")
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintf(stderr, "task dep %s does not accept positional arguments\n", kind)
		return 2
	}
	if strings.TrimSpace(*blockerRef) == "" || strings.TrimSpace(*blockedRef) == "" {
		fmt.Fprintf(stderr, "task dep %s requires --blocker <task> and --blocked <task>\n", kind)
		return 2
	}
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote workflowCommandRemote) int {
		blockerTaskID, err := resolveWorkflowTaskID(context.Background(), cfg, remote, *projectRef, *blockerRef)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		blockedTaskID, err := resolveWorkflowTaskID(context.Background(), cfg, remote, *projectRef, *blockedRef)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
		defer cancel()
		var response serverapi.WorkflowTaskDependencyMutationResponse
		switch kind {
		case taskDependencyMutationAdd:
			result, err := remote.AddWorkflowTaskDependency(ctx, serverapi.WorkflowTaskDependencyAddRequest{
				BlockerTaskID: blockerTaskID,
				BlockedTaskID: blockedTaskID,
			})
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			if err := result.Validate(); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			response = serverapi.WorkflowTaskDependencyMutationResponse(result)
		case taskDependencyMutationRemove:
			result, err := remote.RemoveWorkflowTaskDependency(ctx, serverapi.WorkflowTaskDependencyRemoveRequest{
				BlockerTaskID: blockerTaskID,
				BlockedTaskID: blockedTaskID,
			})
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			if err := result.Validate(); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			response = serverapi.WorkflowTaskDependencyMutationResponse(result)
		default:
			panic(fmt.Sprintf("unsupported task dependency mutation %q", kind))
		}
		if *jsonOut {
			return writeCommandJSON(stdout, stderr, response)
		}
		fmt.Fprintln(stdout, "done")
		return 0
	})
}

func taskDependencyListSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" task dep list", stderr, taskDependencyListUsage)
	projectRef := fs.String("project", ".", "project ID or attached workspace path used to resolve a Short ID")
	directionRaw := fs.String("direction", "", "relationship direction: blocks or blocked-by")
	jsonOut := fs.Bool("json", false, "write the complete dependency directions as JSON")
	positionals, flagArgs := takeLeadingPositionals(args, 1)
	if ok, exitCode := parseCommandFlags(fs, flagArgs); !ok {
		return exitCode
	}
	positionals = append(positionals, fs.Args()...)
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "task dep list requires <short-id-or-task-id>")
		return 2
	}
	if flagExplicit(fs, "direction") && strings.TrimSpace(*directionRaw) == "" {
		fmt.Fprintln(stderr, "--direction must not be blank")
		return 2
	}
	direction, err := parseTaskDependencyDirection(*directionRaw)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote workflowCommandRemote) int {
		taskID, err := resolveWorkflowTaskID(context.Background(), cfg, remote, *projectRef, positionals[0])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
		defer cancel()
		response, err := remote.ListWorkflowTaskDependencies(ctx, serverapi.WorkflowTaskDependencyListRequest{
			TaskID:    taskID,
			Direction: direction,
		})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := response.Validate(); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOut {
			return writeCommandJSON(stdout, stderr, response)
		}
		if err := writeTaskDependencyDirections(stdout, response.Directions); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	})
}

func parseTaskDependencyDirection(raw string) (*serverapi.WorkflowTaskDependencyDirection, error) {
	switch strings.TrimSpace(raw) {
	case "":
		return nil, nil
	case string(serverapi.WorkflowTaskDependencyDirectionBlocks):
		value := serverapi.WorkflowTaskDependencyDirectionBlocks
		return &value, nil
	case string(serverapi.WorkflowTaskDependencyDirectionBlockedBy):
		value := serverapi.WorkflowTaskDependencyDirectionBlockedBy
		return &value, nil
	default:
		return nil, errors.New("--direction must be blocks or blocked-by")
	}
}

type taskDependencyDirection interface {
	serverapi.WorkflowTaskDependencyListDirectionProjection | serverapi.WorkflowTaskDependencyDirectionProjection
}

func writeTaskDependencyDirections[T taskDependencyDirection](stdout io.Writer, directions []T) error {
	for _, wanted := range []serverapi.WorkflowTaskDependencyDirection{
		serverapi.WorkflowTaskDependencyDirectionBlocks,
		serverapi.WorkflowTaskDependencyDirectionBlockedBy,
	} {
		for _, raw := range directions {
			direction := dependencyDirectionForRender(raw)
			if direction.Direction != wanted || len(direction.Items) == 0 {
				continue
			}
			if wanted == serverapi.WorkflowTaskDependencyDirectionBlocks {
				fmt.Fprintf(stdout, "Blocks %d tasks:\n", direction.TotalCount)
			} else {
				fmt.Fprintln(stdout, "Blocked by:")
			}
			for _, item := range direction.Items {
				status, err := taskStatusText(item.Status)
				if err != nil {
					return err
				}
				fmt.Fprintf(stdout, "%s: %s (%s)\n", item.ShortID, item.Title, status)
			}
		}
	}
	return nil
}

func dependencyDirectionForRender[T taskDependencyDirection](direction T) serverapi.WorkflowTaskDependencyListDirectionProjection {
	switch value := any(direction).(type) {
	case serverapi.WorkflowTaskDependencyListDirectionProjection:
		return value
	case serverapi.WorkflowTaskDependencyDirectionProjection:
		return serverapi.WorkflowTaskDependencyListDirectionProjection{
			Direction:        value.Direction,
			TotalCount:       value.TotalCount,
			UnsatisfiedCount: value.UnsatisfiedCount,
			Items:            value.Items,
		}
	default:
		panic("unsupported dependency direction projection")
	}
}
