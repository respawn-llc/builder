package workflowcontract

import (
	"errors"
	"strings"
)

type TaskTitle string

func NewTaskTitle(raw string) (TaskTitle, error) {
	title := strings.TrimSpace(raw)
	if title == "" {
		return "", errors.New("task title is required")
	}
	return TaskTitle(title), nil
}

func (title TaskTitle) String() string {
	return string(title)
}
