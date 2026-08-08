package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"core/shared/serverapi"
)

type taskSearchPlainProjection struct {
	Groups []taskSearchPlainGroup
}

type taskSearchPlainGroup struct {
	ShortID           string
	Title             string
	Lines             []taskSearchPlainLine
	RemainingHitCount int
}

type taskSearchPlainLineKind uint8

const (
	taskSearchPlainLineKindHit taskSearchPlainLineKind = iota
	taskSearchPlainLineKindCommentHeading

	taskSearchPlainNoMatchesLine         = "No matches."
	taskSearchPlainCommentHeading        = "comments:"
	taskSearchPlainLiteralOmissionMarker = "…"
)

type taskSearchPlainLine struct {
	Kind    taskSearchPlainLineKind
	Literal *serverapi.TaskSearchLiteralHit
	FTS5    *serverapi.TaskSearchFTS5Hit
}

func taskSearchPlainProjectionFromResponse(response serverapi.TaskSearchResponse) (taskSearchPlainProjection, error) {
	if err := response.Validate(); err != nil {
		return taskSearchPlainProjection{}, err
	}
	if len(response.Groups) == 0 {
		return taskSearchPlainProjection{}, nil
	}
	groups := make([]taskSearchPlainGroup, 0, len(response.Groups))
	for _, group := range response.Groups {
		lines := make([]taskSearchPlainLine, 0, len(group.Hits)+1)
		commentHeadingWritten := false
		for _, hit := range group.Hits {
			if hit.Source.Kind == serverapi.TaskSearchSourceKindShortID {
				continue
			}
			if hit.Source.Kind == serverapi.TaskSearchSourceKindComment && !commentHeadingWritten {
				lines = append(lines, taskSearchPlainLine{Kind: taskSearchPlainLineKindCommentHeading})
				commentHeadingWritten = true
			}
			line := taskSearchPlainLine{Kind: taskSearchPlainLineKindHit}
			if response.Mode == serverapi.TaskSearchModeLiteral {
				line.Literal = hit.Literal
			} else {
				line.FTS5 = hit.FTS5
			}
			lines = append(lines, line)
		}
		lastOrdinal := group.Hits[len(group.Hits)-1].Ordinal
		groups = append(groups, taskSearchPlainGroup{
			ShortID:           group.ShortID,
			Title:             group.Title,
			Lines:             lines,
			RemainingHitCount: group.TotalHitCount - lastOrdinal,
		})
	}
	return taskSearchPlainProjection{Groups: groups}, nil
}

func writeTaskSearchPlainProjection(stdout io.Writer, projection taskSearchPlainProjection) error {
	if len(projection.Groups) == 0 {
		_, err := fmt.Fprintln(stdout, taskSearchPlainNoMatchesLine)
		return err
	}
	for _, group := range projection.Groups {
		if _, err := fmt.Fprintln(stdout, taskSearchPlainTaskHeader(group.ShortID, group.Title)); err != nil {
			return err
		}
		for _, line := range group.Lines {
			switch line.Kind {
			case taskSearchPlainLineKindCommentHeading:
				if _, err := fmt.Fprintln(stdout, taskSearchPlainCommentHeading); err != nil {
					return err
				}
			case taskSearchPlainLineKindHit:
				fragment, err := taskSearchPlainFragment(line)
				if err != nil {
					return err
				}
				if _, err := fmt.Fprintln(stdout, fragment); err != nil {
					return err
				}
			default:
				return fmt.Errorf("task search plain output has invalid line kind %d", line.Kind)
			}
		}
		if group.RemainingHitCount > 0 {
			if _, err := fmt.Fprintln(stdout, taskSearchPlainRemainingHitsLine(group.RemainingHitCount)); err != nil {
				return err
			}
		}
	}
	return nil
}

func taskSearchPlainFragment(line taskSearchPlainLine) (string, error) {
	if (line.Literal == nil) == (line.FTS5 == nil) {
		return "", fmt.Errorf(
			"task search plain hit must contain exactly one payload: literal=%t fts5=%t",
			line.Literal != nil,
			line.FTS5 != nil,
		)
	}
	if line.FTS5 != nil {
		return taskSearchPlainWhitespace(line.FTS5.Snippet), nil
	}
	var fragment strings.Builder
	if line.Literal.LeftTruncated {
		fragment.WriteString(taskSearchPlainLiteralOmissionMarker)
	}
	fragment.WriteString(line.Literal.Before)
	fragment.WriteString(line.Literal.Match)
	fragment.WriteString(line.Literal.After)
	if line.Literal.RightTruncated {
		fragment.WriteString(taskSearchPlainLiteralOmissionMarker)
	}
	return taskSearchPlainWhitespace(fragment.String()), nil
}

func taskSearchPlainWhitespace(fragment string) string {
	return strings.Join(strings.Fields(fragment), " ")
}

func taskSearchPlainTaskHeader(shortID string, title string) string {
	return shortID + ": " + title
}

func taskSearchPlainRemainingHitsLine(count int) string {
	return "[" + strconv.Itoa(count) + " more hits]"
}
