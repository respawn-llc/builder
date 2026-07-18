package patchformat

import (
	"fmt"
	"strings"
)

type Document struct {
	Hunks []any
}

type AddFile struct {
	Path    string
	Content []string
}

type DeleteFile struct {
	Path string
}

type WholeFileDeletionOperationID struct {
	HunkOrdinal int `json:"hunk_ordinal"`
}

type WholeFileDeletionGroupID struct {
	FirstOperation WholeFileDeletionOperationID `json:"first_operation"`
}

type WholeFileDeletionDisposition struct {
	PhysicalGroup WholeFileDeletionGroupID `json:"physical_group"`
	Removed       int                      `json:"removed"`
}

type WholeFileDeletionOperation struct {
	ID          WholeFileDeletionOperationID  `json:"id"`
	Disposition *WholeFileDeletionDisposition `json:"disposition"`
}

type WholeFileDeletionFact struct {
	PhysicalGroup WholeFileDeletionGroupID
	OperationIDs  []WholeFileDeletionOperationID
	Removed       int
}

type WholeFileDeletionFactMismatchKind string

const (
	WholeFileDeletionFactMismatchMissingOperation    WholeFileDeletionFactMismatchKind = "missing_operation"
	WholeFileDeletionFactMismatchUnexpectedOperation WholeFileDeletionFactMismatchKind = "unexpected_operation"
	WholeFileDeletionFactMismatchDuplicateOperation  WholeFileDeletionFactMismatchKind = "duplicate_operation"
	WholeFileDeletionFactMismatchInvalidCount        WholeFileDeletionFactMismatchKind = "invalid_count"
	WholeFileDeletionFactMismatchInvalidGroup        WholeFileDeletionFactMismatchKind = "invalid_group"
)

type WholeFileDeletionFactMismatch struct {
	Kind                 WholeFileDeletionFactMismatchKind
	ExpectedOperationIDs []WholeFileDeletionOperationID
	ReceivedOperationIDs []WholeFileDeletionOperationID
	PhysicalGroup        *WholeFileDeletionGroupID
	Removed              *int
}

func (m *WholeFileDeletionFactMismatch) Error() string {
	if m == nil {
		return "whole-file deletion fact mismatch"
	}
	group := "absent"
	if m.PhysicalGroup != nil {
		group = fmt.Sprintf("%+v", *m.PhysicalGroup)
	}
	removed := "absent"
	if m.Removed != nil {
		removed = fmt.Sprintf("%d", *m.Removed)
	}
	return fmt.Sprintf(
		"whole-file deletion fact mismatch (kind=%q expected=%+v received=%+v physical_group=%s removed=%s)",
		m.Kind,
		m.ExpectedOperationIDs,
		m.ReceivedOperationIDs,
		group,
		removed,
	)
}

type UpdateFile struct {
	Path    string
	MoveTo  string
	Changes []ChangeLine
}

type ChangeLine struct {
	Kind      rune
	Content   string
	EndOfFile bool
}

type RenderedLineKind string

const (
	RenderedLineKindHeader RenderedLineKind = "header"
	RenderedLineKindFile   RenderedLineKind = "file"
	RenderedLineKindDiff   RenderedLineKind = "diff"
	RenderedLineKindRaw    RenderedLineKind = "raw"
)

type RenderedLine struct {
	Kind      RenderedLineKind
	Text      string
	FileIndex int
	Path      string
}

type RenderedFile struct {
	AbsPath            string
	RelPath            string
	Added              int
	Removed            int
	Diff               []string
	WholeFileDeletions []WholeFileDeletionOperation
}

type RenderedPatch struct {
	Files        []RenderedFile
	SummaryLines []RenderedLine
	DetailLines  []RenderedLine
}

func Clone(in *RenderedPatch) *RenderedPatch {
	if in == nil {
		return nil
	}
	out := &RenderedPatch{}
	if len(in.Files) > 0 {
		out.Files = make([]RenderedFile, 0, len(in.Files))
		for _, file := range in.Files {
			copyFile := file
			copyFile.Diff = append([]string(nil), file.Diff...)
			if len(file.WholeFileDeletions) > 0 {
				copyFile.WholeFileDeletions = make(
					[]WholeFileDeletionOperation,
					len(file.WholeFileDeletions),
				)
				for index, operation := range file.WholeFileDeletions {
					copyFile.WholeFileDeletions[index] = operation
					if operation.Disposition != nil {
						disposition := *operation.Disposition
						copyFile.WholeFileDeletions[index].Disposition = &disposition
					}
				}
			}
			out.Files = append(out.Files, copyFile)
		}
	}
	out.SummaryLines = append([]RenderedLine(nil), in.SummaryLines...)
	out.DetailLines = append([]RenderedLine(nil), in.DetailLines...)
	return out
}

func (r RenderedPatch) SummaryText() string {
	return joinRenderedLines(r.SummaryLines)
}

func (r RenderedPatch) DetailText() string {
	return joinRenderedLines(r.DetailLines)
}

func joinRenderedLines(lines []RenderedLine) string {
	if len(lines) == 0 {
		return ""
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, line.Text)
	}
	return strings.Join(out, "\n")
}
