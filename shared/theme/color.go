package theme

import (
	"fmt"
	"strconv"
	"strings"
)

func BlendColor(foreground Color, background Color, foregroundPercent uint8) Color {
	if foregroundPercent > 100 {
		panic(fmt.Sprintf("blend theme colors with invalid foreground percent %d", foregroundPercent))
	}
	foregroundRGB := mustParseTrueColor(foreground.TrueColor)
	backgroundRGB := mustParseTrueColor(background.TrueColor)
	backgroundPercent := 100 - foregroundPercent
	blend := func(foregroundComponent, backgroundComponent uint8) uint8 {
		return uint8(
			(uint16(foregroundComponent)*uint16(foregroundPercent) +
				uint16(backgroundComponent)*uint16(backgroundPercent)) / 100,
		)
	}
	return Color{
		ANSI:    background.ANSI,
		ANSI256: background.ANSI256,
		TrueColor: fmt.Sprintf(
			"#%02X%02X%02X",
			blend(foregroundRGB[0], backgroundRGB[0]),
			blend(foregroundRGB[1], backgroundRGB[1]),
			blend(foregroundRGB[2], backgroundRGB[2]),
		),
	}
}

func mustParseTrueColor(raw string) [3]uint8 {
	trimmed := strings.TrimPrefix(strings.TrimSpace(raw), "#")
	if len(trimmed) != 6 {
		panic(fmt.Sprintf("parse theme true color %q: expected #RRGGBB", raw))
	}
	var out [3]uint8
	for index := range out {
		value, err := strconv.ParseUint(trimmed[index*2:index*2+2], 16, 8)
		if err != nil {
			panic(fmt.Sprintf("parse theme true color %q: %v", raw, err))
		}
		out[index] = uint8(value)
	}
	return out
}
