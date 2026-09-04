package patchformat

import (
	"path/filepath"
	"strings"
)

func Render(src, cwd string) Presentation {
	doc, err := Parse(src)
	if err != nil {
		return InvalidInputPresentation(src)
	}
	changes := Format(doc, cwd)
	if !changes.Valid() {
		return InvalidInputPresentation(src)
	}
	return Presentation{
		Variant: PresentationVariantChanges,
		Changes: &changes,
	}
}

func RenderEdit(path, oldText, newText, cwd string) Presentation {
	changes := Format(Document{Hunks: []any{UpdateFile{
		Path:    path,
		Changes: editChangeLines(oldText, newText),
	}}}, cwd)
	return Presentation{
		Variant: PresentationVariantChanges,
		Changes: &changes,
	}
}

func InvalidInputPresentation(inputDetail string) Presentation {
	return Presentation{
		Variant: PresentationVariantInvalidInput,
		InvalidInput: &InvalidInput{
			InputDetail: inputDetail,
		},
	}
}

func editChangeLines(oldText, newText string) []ChangeLine {
	oldLines := normalizedEditLines(oldText)
	newLines := normalizedEditLines(newText)
	commonPrefix := 0
	for commonPrefix < len(oldLines) && commonPrefix < len(newLines) && oldLines[commonPrefix] == newLines[commonPrefix] {
		commonPrefix++
	}
	oldSuffix := len(oldLines)
	newSuffix := len(newLines)
	for oldSuffix > commonPrefix && newSuffix > commonPrefix && oldLines[oldSuffix-1] == newLines[newSuffix-1] {
		oldSuffix--
		newSuffix--
	}
	out := make([]ChangeLine, 0, oldSuffix-commonPrefix+newSuffix-commonPrefix)
	for _, line := range oldLines[commonPrefix:oldSuffix] {
		out = append(out, ChangeLine{Kind: '-', Content: line})
	}
	for _, line := range newLines[commonPrefix:newSuffix] {
		out = append(out, ChangeLine{Kind: '+', Content: line})
	}
	if len(out) == 0 && oldText != newText {
		out = append(out, ChangeLine{Kind: '-', Content: oldText}, ChangeLine{Kind: '+', Content: newText})
	}
	return out
}

func normalizedEditLines(text string) []string {
	lines := strings.Split(strings.TrimSuffix(strings.ReplaceAll(text, "\r\n", "\n"), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

func Format(doc Document, cwd string) Changes {
	files := buildFileChanges(doc, cwd)
	return Changes{Files: files}
}

func buildFileChanges(doc Document, cwd string) []FileChange {
	files := make([]FileChange, 0, 8)
	byAbsolutePath := make(map[string]int, 8)

	getFile := func(path string) *FileChange {
		resolved := resolvePath(path, cwd)
		if !resolved.Valid() {
			return nil
		}
		if index, ok := byAbsolutePath[resolved.Absolute]; ok {
			return &files[index]
		}
		removed := 0
		files = append(files, FileChange{
			Path:    resolved,
			Removed: &removed,
		})
		index := len(files) - 1
		byAbsolutePath[resolved.Absolute] = index
		return &files[index]
	}

	for ordinal, hunk := range doc.Hunks {
		switch operation := hunk.(type) {
		case AddFile:
			file := getFile(operation.Path)
			if file == nil {
				continue
			}
			file.Operations = append(file.Operations, FileOperation{
				Kind: FileOperationAdd,
				Groups: []ChangeGroup{{
					Lines: changedLines(ChangedLineAdded, operation.Content),
				}},
			})
			refreshFileCounts(file)
		case UpdateFile:
			targetPath := operation.Path
			kind := FileOperationUpdate
			var source *Path
			if strings.TrimSpace(operation.MoveTo) != "" {
				targetPath = operation.MoveTo
				kind = FileOperationMove
				resolvedSource := resolvePath(operation.Path, cwd)
				source = &resolvedSource
			}
			file := getFile(targetPath)
			if file == nil {
				continue
			}
			file.Operations = append(file.Operations, FileOperation{
				Kind:   kind,
				Source: source,
				Groups: changedLineGroups(operation.Changes),
			})
			refreshFileCounts(file)
		case DeleteFile:
			file := getFile(operation.Path)
			if file == nil {
				continue
			}
			file.Operations = append(file.Operations, FileOperation{
				Kind: FileOperationDelete,
				Deletion: &WholeFileDeletionOperation{
					ID: WholeFileDeletionOperationID{HunkOrdinal: ordinal},
				},
			})
			refreshFileCounts(file)
		}
	}
	return files
}

func changedLines(kind ChangedLineKind, content []string) []ChangedLine {
	lines := make([]ChangedLine, 0, len(content))
	for _, line := range content {
		lines = append(lines, ChangedLine{Kind: kind, Content: line})
	}
	return lines
}

func changedLineGroups(changes []ChangeLine) []ChangeGroup {
	groups := make([]ChangeGroup, 0, 4)
	current := ChangeGroup{}
	flush := func() {
		if len(current.Lines) == 0 {
			return
		}
		groups = append(groups, current)
		current = ChangeGroup{}
	}
	for _, change := range changes {
		var kind ChangedLineKind
		switch change.Kind {
		case '+':
			kind = ChangedLineAdded
		case '-':
			kind = ChangedLineRemoved
		default:
			flush()
			continue
		}
		current.Lines = append(current.Lines, ChangedLine{
			Kind:    kind,
			Content: change.Content,
		})
	}
	flush()
	return groups
}

func refreshFileCounts(file *FileChange) {
	added := 0
	removed := 0
	pendingDeletion := false
	countedDeletionGroups := make(map[WholeFileDeletionGroupID]struct{})
	for _, operation := range file.Operations {
		for _, group := range operation.Groups {
			for _, line := range group.Lines {
				switch line.Kind {
				case ChangedLineAdded:
					added++
				case ChangedLineRemoved:
					removed++
				}
			}
		}
		if operation.Deletion == nil {
			continue
		}
		if operation.Deletion.Disposition == nil {
			pendingDeletion = true
			continue
		}
		group := operation.Deletion.Disposition.PhysicalGroup
		if _, counted := countedDeletionGroups[group]; counted {
			continue
		}
		countedDeletionGroups[group] = struct{}{}
		removed += operation.Deletion.Disposition.Removed
	}
	file.Added = added
	if pendingDeletion {
		file.Removed = nil
		return
	}
	file.Removed = intPointer(removed)
}

func intPointer(value int) *int {
	return &value
}

func ApplyWholeFileDeletionFacts(
	presentation Presentation,
	facts []WholeFileDeletionFact,
) (Presentation, *WholeFileDeletionFactMismatch) {
	out := ClonePresentation(&presentation)
	if out == nil || out.Changes == nil {
		return presentation, deletionFactMismatch(
			WholeFileDeletionFactMismatchMissingOperation,
			nil,
			nil,
			nil,
			nil,
		)
	}
	type operationLocation struct {
		fileIndex      int
		operationIndex int
	}
	expected := make([]WholeFileDeletionOperationID, 0)
	locations := make(map[WholeFileDeletionOperationID]operationLocation)
	for fileIndex := range out.Files {
		for operationIndex, operation := range out.Files[fileIndex].Operations {
			if operation.Deletion == nil {
				continue
			}
			id := operation.Deletion.ID
			expected = append(expected, id)
			if _, duplicate := locations[id]; duplicate {
				return presentation, deletionFactMismatch(
					WholeFileDeletionFactMismatchDuplicateOperation,
					expected,
					nil,
					nil,
					nil,
				)
			}
			locations[id] = operationLocation{
				fileIndex:      fileIndex,
				operationIndex: operationIndex,
			}
		}
	}

	received := make([]WholeFileDeletionOperationID, 0, len(expected))
	seen := make(map[WholeFileDeletionOperationID]struct{}, len(expected))
	seenGroups := make(map[WholeFileDeletionGroupID]struct{}, len(facts))
	for _, fact := range facts {
		group := fact.PhysicalGroup
		removed := fact.Removed
		if fact.Removed < 0 {
			return presentation, deletionFactMismatch(
				WholeFileDeletionFactMismatchInvalidCount,
				expected,
				append(received, fact.OperationIDs...),
				&group,
				&removed,
			)
		}
		if len(fact.OperationIDs) == 0 ||
			fact.PhysicalGroup.FirstOperation != fact.OperationIDs[0] {
			return presentation, deletionFactMismatch(
				WholeFileDeletionFactMismatchInvalidGroup,
				expected,
				append(received, fact.OperationIDs...),
				&group,
				&removed,
			)
		}
		for index := 1; index < len(fact.OperationIDs); index++ {
			if fact.OperationIDs[index-1].HunkOrdinal >
				fact.OperationIDs[index].HunkOrdinal {
				return presentation, deletionFactMismatch(
					WholeFileDeletionFactMismatchInvalidGroup,
					expected,
					append(received, fact.OperationIDs...),
					&group,
					&removed,
				)
			}
		}
		if _, duplicate := seenGroups[group]; duplicate {
			return presentation, deletionFactMismatch(
				WholeFileDeletionFactMismatchInvalidGroup,
				expected,
				append(received, fact.OperationIDs...),
				&group,
				&removed,
			)
		}
		seenGroups[group] = struct{}{}
		for _, operationID := range fact.OperationIDs {
			received = append(received, operationID)
			if _, duplicate := seen[operationID]; duplicate {
				return presentation, deletionFactMismatch(
					WholeFileDeletionFactMismatchDuplicateOperation,
					expected,
					received,
					&group,
					&removed,
				)
			}
			seen[operationID] = struct{}{}
			location, exists := locations[operationID]
			if !exists {
				return presentation, deletionFactMismatch(
					WholeFileDeletionFactMismatchUnexpectedOperation,
					expected,
					received,
					&group,
					&removed,
				)
			}
			deletion := out.Files[location.fileIndex].Operations[location.operationIndex].Deletion
			if deletion.Disposition != nil {
				return presentation, deletionFactMismatch(
					WholeFileDeletionFactMismatchDuplicateOperation,
					expected,
					received,
					&group,
					&removed,
				)
			}
		}
	}
	if len(received) != len(expected) {
		return presentation, deletionFactMismatch(
			WholeFileDeletionFactMismatchMissingOperation,
			expected,
			received,
			nil,
			nil,
		)
	}

	for _, fact := range facts {
		for _, operationID := range fact.OperationIDs {
			location := locations[operationID]
			out.Files[location.fileIndex].Operations[location.operationIndex].Deletion.Disposition =
				&WholeFileDeletionDisposition{
					PhysicalGroup: fact.PhysicalGroup,
					Removed:       fact.Removed,
				}
		}
	}
	for index := range out.Files {
		refreshFileCounts(&out.Files[index])
	}
	return *out, nil
}

func deletionFactMismatch(
	kind WholeFileDeletionFactMismatchKind,
	expected []WholeFileDeletionOperationID,
	received []WholeFileDeletionOperationID,
	group *WholeFileDeletionGroupID,
	removed *int,
) *WholeFileDeletionFactMismatch {
	mismatch := &WholeFileDeletionFactMismatch{
		Kind:                 kind,
		ExpectedOperationIDs: append([]WholeFileDeletionOperationID(nil), expected...),
		ReceivedOperationIDs: append([]WholeFileDeletionOperationID(nil), received...),
	}
	if group != nil {
		copyGroup := *group
		mismatch.PhysicalGroup = &copyGroup
	}
	if removed != nil {
		copyRemoved := *removed
		mismatch.Removed = &copyRemoved
	}
	return mismatch
}

func resolvePath(path, cwd string) Path {
	p := strings.TrimSpace(path)
	if p == "" {
		return Path{}
	}
	requestedRelative := normalizeRequestedRelativePath(p)
	var absolute string
	if filepath.IsAbs(p) {
		absolute = filepath.Clean(p)
	} else if cwd != "" {
		absolute = filepath.Clean(filepath.Join(cwd, p))
	} else {
		absolute = filepath.Clean(p)
	}
	absolute = filepath.ToSlash(absolute)
	if cwd == "" {
		if filepath.IsAbs(p) {
			return Path{Absolute: absolute, Relative: absolute}
		}
		return Path{
			Absolute: absolute,
			Relative: "./" + filepath.ToSlash(strings.TrimPrefix(p, "./")),
		}
	}
	relative, err := filepath.Rel(cwd, filepath.FromSlash(absolute))
	if err != nil {
		return Path{Absolute: absolute, Relative: absolute}
	}
	relative = filepath.ToSlash(relative)
	if relative == "." {
		return Path{Absolute: absolute, Relative: "./"}
	}
	if !strings.HasPrefix(relative, "../") && relative != ".." {
		return Path{Absolute: absolute, Relative: "./" + relative}
	}
	if requestedRelative != "" {
		return Path{Absolute: absolute, Relative: requestedRelative}
	}
	return Path{Absolute: absolute, Relative: absolute}
}

func normalizeRequestedRelativePath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || filepath.IsAbs(trimmed) {
		return ""
	}
	cleaned := filepath.ToSlash(filepath.Clean(trimmed))
	if cleaned == "." {
		return "./"
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return cleaned
	}
	return "./" + strings.TrimPrefix(cleaned, "./")
}
