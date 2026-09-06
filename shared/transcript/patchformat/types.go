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

type PresentationVariant string

const (
	PresentationVariantChanges      PresentationVariant = "changes"
	PresentationVariantInvalidInput PresentationVariant = "invalid_input"
)

type FileOperationKind string

const (
	FileOperationAdd    FileOperationKind = "add"
	FileOperationUpdate FileOperationKind = "update"
	FileOperationMove   FileOperationKind = "move"
	FileOperationDelete FileOperationKind = "delete"
)

type ChangedLineKind string

const (
	ChangedLineAdded   ChangedLineKind = "added"
	ChangedLineRemoved ChangedLineKind = "removed"
)

type Path struct {
	Absolute string
	Relative string
}

func (p Path) Valid() bool {
	return p.Absolute != "" && p.Relative != ""
}

type ChangedLine struct {
	Kind    ChangedLineKind
	Content string
}

func (l ChangedLine) Valid() bool {
	return l.Kind == ChangedLineAdded || l.Kind == ChangedLineRemoved
}

type ChangeGroup struct {
	Lines []ChangedLine
}

func (g ChangeGroup) Valid() bool {
	if len(g.Lines) == 0 {
		return false
	}
	for _, line := range g.Lines {
		if !line.Valid() {
			return false
		}
	}
	return true
}

type FileOperation struct {
	Kind     FileOperationKind
	Source   *Path
	Groups   []ChangeGroup
	Deletion *WholeFileDeletionOperation
}

func (o FileOperation) Valid() bool {
	switch o.Kind {
	case FileOperationAdd:
		if o.Source != nil || o.Deletion != nil || len(o.Groups) == 0 {
			return false
		}
		for _, group := range o.Groups {
			for _, line := range group.Lines {
				if line.Kind != ChangedLineAdded {
					return false
				}
			}
		}
	case FileOperationUpdate:
		if o.Source != nil || o.Deletion != nil {
			return false
		}
	case FileOperationMove:
		if o.Source == nil || !o.Source.Valid() || o.Deletion != nil {
			return false
		}
	case FileOperationDelete:
		if o.Source != nil || o.Deletion == nil || len(o.Groups) != 0 {
			return false
		}
	default:
		return false
	}
	for _, group := range o.Groups {
		if !group.Valid() {
			return false
		}
	}
	return true
}

type FileChange struct {
	Path       Path
	Added      int
	Removed    *int
	Operations []FileOperation
}

func (f FileChange) Valid() bool {
	if !f.Path.Valid() || f.Added < 0 || f.Removed != nil && *f.Removed < 0 || len(f.Operations) == 0 {
		return false
	}
	for _, operation := range f.Operations {
		if !operation.Valid() {
			return false
		}
	}
	expected := f
	refreshFileCounts(&expected)
	if f.Added != expected.Added {
		return false
	}
	if f.Removed == nil || expected.Removed == nil {
		return f.Removed == nil && expected.Removed == nil
	}
	return *f.Removed == *expected.Removed
}

type Changes struct {
	Files []FileChange
}

func (c Changes) Valid() bool {
	if len(c.Files) == 0 {
		return false
	}
	paths := make(map[string]struct{}, len(c.Files))
	for _, file := range c.Files {
		if !file.Valid() {
			return false
		}
		if _, duplicate := paths[file.Path.Absolute]; duplicate {
			return false
		}
		paths[file.Path.Absolute] = struct{}{}
	}
	return true
}

type InvalidInput struct {
	InputDetail string
}

type Presentation struct {
	Variant PresentationVariant
	*Changes
	InvalidInput *InvalidInput
}

func (p Presentation) Valid() bool {
	switch p.Variant {
	case PresentationVariantChanges:
		return p.Changes != nil && p.Changes.Valid() && p.InvalidInput == nil
	case PresentationVariantInvalidInput:
		return p.Changes == nil && p.InvalidInput != nil
	default:
		return false
	}
}

func ClonePresentation(in *Presentation) *Presentation {
	if in == nil {
		return nil
	}
	out := *in
	if in.Changes != nil {
		changes := Changes{}
		if len(in.Changes.Files) > 0 {
			changes.Files = make([]FileChange, 0, len(in.Changes.Files))
			for _, file := range in.Changes.Files {
				copyFile := file
				if file.Removed != nil {
					removed := *file.Removed
					copyFile.Removed = &removed
				}
				copyFile.Operations = make([]FileOperation, len(file.Operations))
				for operationIndex, operation := range file.Operations {
					copyOperation := operation
					if operation.Source != nil {
						source := *operation.Source
						copyOperation.Source = &source
					}
					if operation.Deletion != nil {
						deletion := *operation.Deletion
						if operation.Deletion.Disposition != nil {
							disposition := *operation.Deletion.Disposition
							deletion.Disposition = &disposition
						}
						copyOperation.Deletion = &deletion
					}
					if len(operation.Groups) > 0 {
						copyOperation.Groups = make([]ChangeGroup, len(operation.Groups))
						for groupIndex, group := range operation.Groups {
							copyOperation.Groups[groupIndex] = ChangeGroup{
								Lines: append([]ChangedLine(nil), group.Lines...),
							}
						}
					}
					copyFile.Operations[operationIndex] = copyOperation
				}
				changes.Files = append(changes.Files, copyFile)
			}
		}
		out.Changes = &changes
	}
	if in.InvalidInput != nil {
		invalidInput := *in.InvalidInput
		out.InvalidInput = &invalidInput
	}
	return &out
}

// RenderedPatch is the pre-cutover persisted metadata shape. Current builders
// and consumers use Presentation.
type RenderedPatch struct {
	Files        []RenderedFile
	SummaryLines []RenderedLine
	DetailLines  []RenderedLine
}

type RenderedFile struct {
	AbsPath            string
	RelPath            string
	Added              int
	Removed            int
	Diff               []string
	WholeFileDeletions []WholeFileDeletionOperation
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

func (r RenderedPatch) SummaryText() string {
	return joinRenderedLines(r.SummaryLines)
}

func (r RenderedPatch) DetailText() string {
	return joinRenderedLines(r.DetailLines)
}

func RemovedLineCount[T RenderedFile | FileChange](file T) *int {
	switch typed := any(file).(type) {
	case FileChange:
		if typed.Removed == nil {
			return nil
		}
		removed := *typed.Removed
		return &removed
	case RenderedFile:
		total := typed.Removed
		known := typed.Removed > 0
		groups := make(map[WholeFileDeletionGroupID]struct{}, len(typed.WholeFileDeletions))
		for _, operation := range typed.WholeFileDeletions {
			if operation.Disposition == nil {
				continue
			}
			known = true
			group := operation.Disposition.PhysicalGroup
			if _, exists := groups[group]; exists {
				continue
			}
			groups[group] = struct{}{}
			total += operation.Disposition.Removed
		}
		if !known {
			return nil
		}
		return &total
	default:
		panic("unsupported patch file facts")
	}
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
