package patchformat

import (
	"fmt"
	"path/filepath"
	"strings"
)

func Render(src, cwd string) RenderedPatch {
	doc, err := Parse(src)
	if err != nil {
		return Raw(src)
	}
	rendered := Format(doc, cwd)
	if len(rendered.Files) == 0 {
		return Raw(src)
	}
	return rendered
}

func RenderEdit(path, oldText, newText, cwd string) RenderedPatch {
	return Format(Document{Hunks: []any{UpdateFile{
		Path:    path,
		Changes: editChangeLines(oldText, newText),
	}}}, cwd)
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

func Raw(src string) RenderedPatch {
	trimmed := strings.TrimSpace(src)
	if trimmed == "" {
		line := RenderedLine{Kind: RenderedLineKindRaw, Text: "empty patch input", FileIndex: -1}
		return RenderedPatch{SummaryLines: []RenderedLine{line}, DetailLines: []RenderedLine{line}}
	}
	detail := make([]RenderedLine, 0, 8)
	for _, line := range strings.Split(strings.ReplaceAll(trimmed, "\r\n", "\n"), "\n") {
		detail = append(detail, RenderedLine{Kind: RenderedLineKindRaw, Text: line, FileIndex: -1})
	}
	return RenderedPatch{SummaryLines: []RenderedLine{detail[0]}, DetailLines: detail}
}

func Format(doc Document, cwd string) RenderedPatch {
	files := buildRenderedFiles(doc, cwd)
	if len(files) == 0 {
		return RenderedPatch{}
	}

	rendered := RenderedPatch{Files: files}
	if len(files) == 1 {
		file := files[0]
		rendered.SummaryLines = []RenderedLine{{
			Kind:      RenderedLineKindFile,
			Text:      summaryLine(file),
			FileIndex: 0,
			Path:      file.RelPath,
		}}
		detailLinePath := file.RelPath
		if strings.TrimSpace(file.AbsPath) != "" {
			detailLinePath = file.AbsPath
		}
		rendered.DetailLines = append(rendered.DetailLines, RenderedLine{
			Kind:      RenderedLineKindFile,
			Text:      detailHeader(file),
			FileIndex: 0,
			Path:      detailLinePath,
		})
		for _, diff := range file.Diff {
			rendered.DetailLines = append(rendered.DetailLines, RenderedLine{Kind: RenderedLineKindDiff, Text: diff, FileIndex: 0})
		}
		return rendered
	}

	for idx, file := range files {
		rendered.SummaryLines = append(rendered.SummaryLines, RenderedLine{
			Kind:      RenderedLineKindFile,
			Text:      summaryLine(file),
			FileIndex: idx,
			Path:      file.RelPath,
		})
		detailLinePath := file.RelPath
		if strings.TrimSpace(file.AbsPath) != "" {
			detailLinePath = file.AbsPath
		}
		rendered.DetailLines = append(rendered.DetailLines, RenderedLine{
			Kind:      RenderedLineKindFile,
			Text:      detailHeader(file),
			FileIndex: idx,
			Path:      detailLinePath,
		})
		for _, diff := range file.Diff {
			rendered.DetailLines = append(rendered.DetailLines, RenderedLine{Kind: RenderedLineKindDiff, Text: diff, FileIndex: idx})
		}
	}
	return rendered
}

func buildRenderedFiles(doc Document, cwd string) []RenderedFile {
	files := make([]RenderedFile, 0, 8)
	byAbs := make(map[string]int, 8)

	getFile := func(path string) *RenderedFile {
		abs, rel := resolvePath(path, cwd)
		if abs == "" {
			return nil
		}
		if idx, ok := byAbs[abs]; ok {
			return &files[idx]
		}
		files = append(files, RenderedFile{AbsPath: abs, RelPath: rel, Diff: make([]string, 0, 32)})
		idx := len(files) - 1
		byAbs[abs] = idx
		return &files[idx]
	}

	for _, hunk := range doc.Hunks {
		switch op := hunk.(type) {
		case AddFile:
			file := getFile(op.Path)
			if file == nil {
				continue
			}
			for _, line := range op.Content {
				file.Added++
				file.Diff = append(file.Diff, "+"+line)
			}
		case UpdateFile:
			target := op.Path
			if strings.TrimSpace(op.MoveTo) != "" {
				target = op.MoveTo
			}
			file := getFile(target)
			if file == nil {
				continue
			}
			for _, change := range op.Changes {
				switch change.Kind {
				case '+':
					file.Added++
				case '-':
					file.Removed++
				}
				file.Diff = append(file.Diff, renderChangeLine(change))
			}
		case DeleteFile:
			file := getFile(op.Path)
			if file == nil {
				continue
			}
			file.Removed++
			file.Diff = append(file.Diff, "-<deleted file>")
		}
	}

	return files
}

func renderChangeLine(change ChangeLine) string {
	if change.EndOfFile {
		return "*** End of File"
	}
	if change.Kind == ' ' && change.Content == "" {
		return ""
	}
	return string(change.Kind) + change.Content
}

func summaryLine(file RenderedFile) string {
	line := file.RelPath
	if line == "" {
		line = file.AbsPath
	}
	if file.Added > 0 {
		line += fmt.Sprintf(" +%d", file.Added)
	}
	if file.Removed > 0 {
		line += fmt.Sprintf(" -%d", file.Removed)
	}
	return line
}

func detailHeader(file RenderedFile) string {
	line := file.AbsPath
	if line == "" {
		line = file.RelPath
	}
	return line
}

func resolvePath(path, cwd string) (string, string) {
	p := strings.TrimSpace(path)
	if p == "" {
		return "", ""
	}
	requestedRel := normalizeRequestedRelativePath(p)
	var abs string
	if filepath.IsAbs(p) {
		abs = filepath.Clean(p)
	} else if cwd != "" {
		abs = filepath.Clean(filepath.Join(cwd, p))
	} else {
		abs = filepath.Clean(p)
	}
	abs = filepath.ToSlash(abs)
	if cwd == "" {
		if filepath.IsAbs(p) {
			return abs, abs
		}
		return abs, "./" + filepath.ToSlash(strings.TrimPrefix(p, "./"))
	}
	rel, err := filepath.Rel(cwd, filepath.FromSlash(abs))
	if err != nil {
		return abs, abs
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return abs, "./"
	}
	if !strings.HasPrefix(rel, "../") && rel != ".." {
		return abs, "./" + rel
	}
	if requestedRel != "" {
		return abs, requestedRel
	}
	return abs, abs
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
