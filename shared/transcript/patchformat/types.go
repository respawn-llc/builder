package patchformat

import "strings"

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
	HunkOrdinal int
}

type WholeFileDeletionFact struct {
	ID      WholeFileDeletionOperationID
	Removed int
}

type WholeFileDeletionOperation struct {
	ID         WholeFileDeletionOperationID
	CountKnown bool
}

type WholeFileDeletionFactMismatchKind string

const (
	WholeFileDeletionFactMismatchDuplicate    WholeFileDeletionFactMismatchKind = "duplicate"
	WholeFileDeletionFactMismatchUnmatched    WholeFileDeletionFactMismatchKind = "unmatched"
	WholeFileDeletionFactMismatchMissing      WholeFileDeletionFactMismatchKind = "missing"
	WholeFileDeletionFactMismatchInvalidCount WholeFileDeletionFactMismatchKind = "invalid_count"
)

type WholeFileDeletionFactMismatchError struct {
	Kind WholeFileDeletionFactMismatchKind
	ID   WholeFileDeletionOperationID
}

func (e *WholeFileDeletionFactMismatchError) Error() string {
	return "whole-file deletion fact mismatch: " + string(e.Kind)
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
			copyFile.WholeFileDeletions = append(
				[]WholeFileDeletionOperation(nil),
				file.WholeFileDeletions...,
			)
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
