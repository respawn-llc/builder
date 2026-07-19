package ongoing

import (
	"fmt"
	"strings"
)

func resetScrollRegionAndOriginMode() string {
	return "\x1b[r\x1b[?6l"
}

func semanticOutputSequence() string {
	return "\x1b]133;C\x1b\\"
}

func redrawableSemanticPromptSequence() string {
	return "\x1b]133;A;redraw=1\x1b\\"
}

func writeMutableBandErase(builder *strings.Builder, terminalHeight, bandHeight int) {
	startRow := terminalHeight - bandHeight + 1
	for row := startRow; row <= terminalHeight; row++ {
		fmt.Fprintf(builder, "\x1b[%d;1H", row)
		// Retire semantic prompt metadata before this row can rejoin the
		// immutable area; erasing cells alone does not clear that metadata.
		builder.WriteString(semanticOutputSequence())
		builder.WriteString("\x1b[2K")
	}
}

func writeImmutableRegionScrollForLiveBandGrowth(builder *strings.Builder, terminalHeight, previousBandHeight, nextBandHeight int) {
	delta := nextBandHeight - previousBandHeight
	if delta <= 0 {
		return
	}
	oldImmutableBottom := terminalHeight - previousBandHeight
	if oldImmutableBottom < 1 {
		return
	}
	if delta > oldImmutableBottom {
		delta = oldImmutableBottom
	}
	fmt.Fprintf(builder, "\x1b[1;%dr\x1b[%d;1H", oldImmutableBottom, oldImmutableBottom)
	for range delta {
		builder.WriteString("\r\n")
	}
	builder.WriteString(resetScrollRegionAndOriginMode())
}

func writeRetainedMutableBand(builder *strings.Builder, terminalHeight, retainedHeight int, lines []string) {
	if retainedHeight <= 0 {
		return
	}
	retainedStartRow := terminalHeight - retainedHeight + 1
	fmt.Fprintf(builder, "\x1b[%d;1H", retainedStartRow)
	// At the left margin OSC 133 A marks the complete retained region without
	// advancing. Supporting terminals clear the region before resize reflow.
	builder.WriteString(redrawableSemanticPromptSequence())
	startRow := terminalHeight - len(lines) + 1
	for index, line := range lines {
		row := startRow + index
		if index > 0 || row != retainedStartRow {
			fmt.Fprintf(builder, "\x1b[%d;1H", row)
		}
		builder.WriteString(line)
	}
}

func writeImmutableRowsAboveMutableBand(
	builder *strings.Builder,
	terminalHeight int,
	previousBandHeight int,
	bandHeight int,
	rows []string,
) {
	if len(rows) == 0 {
		return
	}
	bottom := terminalHeight - bandHeight
	if bottom < 1 {
		return
	}
	previousBottom := terminalHeight - min(previousBandHeight, terminalHeight)
	appendRow := bottom
	if previousBottom >= 1 && previousBottom < bottom {
		appendRow = previousBottom
	}
	fmt.Fprintf(builder, "\x1b[1;%dr\x1b[%d;1H", bottom, appendRow)
	for _, row := range rows {
		builder.WriteString("\r\n")
		builder.WriteString(row)
	}
	builder.WriteString(resetScrollRegionAndOriginMode())
}

func writeCursor(builder *strings.Builder, cursor Cursor) {
	if !cursor.Visible {
		builder.WriteString("\x1b[?25l")
		return
	}
	fmt.Fprintf(builder, "\x1b[%d;%dH\x1b[?25h", cursor.Row, cursor.Column)
}
