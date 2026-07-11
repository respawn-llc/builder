package analyzer

import (
	"fmt"
	"strings"
)

type Cell struct {
	Content    string
	Faint      bool
	Bold       bool
	Italic     bool
	Underline  bool
	Foreground string
	Background string
}

type ScreenSnapshot struct {
	Dimensions Dimensions
	Cells      [][]Cell
	Cursor     Position
}

type BlankFrameDiagnostic struct {
	Dimensions Dimensions
	Position   Position
	Content    string
}

func NewScreenSnapshot(dimensions Dimensions) ScreenSnapshot {
	if err := validateDimensions(dimensions.Rows, dimensions.Cols); err != nil {
		panic(fmt.Sprintf("new screen snapshot: %v", err))
	}
	cells := make([][]Cell, dimensions.Rows)
	for row := range cells {
		cells[row] = make([]Cell, dimensions.Cols)
	}
	return ScreenSnapshot{Dimensions: dimensions, Cells: cells}
}

func (s ScreenSnapshot) TextInRegion(region Region) string {
	if err := region.ValidateWithin(s.Dimensions); err != nil {
		panic(err)
	}
	var builder strings.Builder
	for row := region.Top; row < region.Bottom; row++ {
		if row > region.Top {
			builder.WriteByte('\n')
		}
		for col := region.Left; col < region.Right; col++ {
			builder.WriteString(s.Cells[row][col].Content)
		}
	}
	return builder.String()
}

func (s ScreenSnapshot) RenderText() string {
	return s.TextInRegion(Region{Top: 0, Bottom: s.Dimensions.Rows, Left: 0, Right: s.Dimensions.Cols})
}

func (s ScreenSnapshot) IsBlank() bool {
	return s.BlankFrameDiagnostic() == nil
}

func (s ScreenSnapshot) BlankFrameDiagnostic() *BlankFrameDiagnostic {
	for row := 0; row < s.Dimensions.Rows; row++ {
		for col := 0; col < s.Dimensions.Cols; col++ {
			if s.Cells[row][col].Content != "" {
				return &BlankFrameDiagnostic{
					Dimensions: s.Dimensions,
					Position:   Position{Row: row, Col: col},
					Content:    s.Cells[row][col].Content,
				}
			}
		}
	}
	return nil
}
