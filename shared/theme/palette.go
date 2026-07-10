package theme

import "github.com/charmbracelet/lipgloss"

type Color struct {
	ANSI      string
	ANSI256   string
	TrueColor string
}

func (c Color) Lipgloss() lipgloss.CompleteColor {
	return lipgloss.CompleteColor{ANSI: c.ANSI, ANSI256: c.ANSI256, TrueColor: c.TrueColor}
}

type AdaptiveColor struct {
	Light Color
	Dark  Color
}

func (c AdaptiveColor) Adaptive() lipgloss.CompleteAdaptiveColor {
	return lipgloss.CompleteAdaptiveColor{
		Light: c.Light.Lipgloss(),
		Dark:  c.Dark.Lipgloss(),
	}
}

func (c AdaptiveColor) Resolve(themeName string) Color {
	if Resolve(themeName) == Light {
		return c.Light
	}
	return c.Dark
}

type AppPalette struct {
	Primary    AdaptiveColor
	Secondary  AdaptiveColor
	Foreground AdaptiveColor
	Muted      AdaptiveColor
	Border     AdaptiveColor
	ModeBg     AdaptiveColor
	ModeText   AdaptiveColor
	ChatBg     AdaptiveColor
	InputBg    AdaptiveColor
}

type TranscriptPalette struct {
	Foreground          AdaptiveColor
	SelectionBackground AdaptiveColor
	SelectionForeground AdaptiveColor
	User                AdaptiveColor
	Assistant           AdaptiveColor
	Tool                AdaptiveColor
	ToolSuccess         AdaptiveColor
	ToolError           AdaptiveColor
	System              AdaptiveColor
	Success             AdaptiveColor
	Warning             AdaptiveColor
	Error               AdaptiveColor
	Compaction          AdaptiveColor
}

type StatusPalette struct {
	Success      AdaptiveColor
	Warning      AdaptiveColor
	Error        AdaptiveColor
	ContextEmpty AdaptiveColor
}

type Palette struct {
	App        AppPalette
	Transcript TranscriptPalette
	Status     StatusPalette
}

type ResolvedAppPalette struct {
	Primary    Color
	Secondary  Color
	Foreground Color
	Muted      Color
	Border     Color
	ModeBg     Color
	ModeText   Color
	ChatBg     Color
	InputBg    Color
}

type ResolvedTranscriptPalette struct {
	Foreground          Color
	SelectionBackground Color
	SelectionForeground Color
	User                Color
	Assistant           Color
	Tool                Color
	ToolSuccess         Color
	ToolError           Color
	System              Color
	Success             Color
	Warning             Color
	Error               Color
	Compaction          Color
}

type ResolvedStatusPalette struct {
	Success      Color
	Warning      Color
	Error        Color
	ContextEmpty Color
}

type ResolvedPalette struct {
	Mode       string
	App        ResolvedAppPalette
	Transcript ResolvedTranscriptPalette
	Status     ResolvedStatusPalette
}

func DefaultPalette() Palette {
	return defaultPalette
}

func ResolvePalette(themeName string) ResolvedPalette {
	return defaultPalette.Resolve(themeName)
}

func (p Palette) Resolve(themeName string) ResolvedPalette {
	resolvedTheme := Resolve(themeName)
	return ResolvedPalette{
		Mode: resolvedTheme,
		App: ResolvedAppPalette{
			Primary:    p.App.Primary.Resolve(resolvedTheme),
			Secondary:  p.App.Secondary.Resolve(resolvedTheme),
			Foreground: p.App.Foreground.Resolve(resolvedTheme),
			Muted:      p.App.Muted.Resolve(resolvedTheme),
			Border:     p.App.Border.Resolve(resolvedTheme),
			ModeBg:     p.App.ModeBg.Resolve(resolvedTheme),
			ModeText:   p.App.ModeText.Resolve(resolvedTheme),
			ChatBg:     p.App.ChatBg.Resolve(resolvedTheme),
			InputBg:    p.App.InputBg.Resolve(resolvedTheme),
		},
		Transcript: ResolvedTranscriptPalette{
			Foreground:          p.Transcript.Foreground.Resolve(resolvedTheme),
			SelectionBackground: p.Transcript.SelectionBackground.Resolve(resolvedTheme),
			SelectionForeground: p.Transcript.SelectionForeground.Resolve(resolvedTheme),
			User:                p.Transcript.User.Resolve(resolvedTheme),
			Assistant:           p.Transcript.Assistant.Resolve(resolvedTheme),
			Tool:                p.Transcript.Tool.Resolve(resolvedTheme),
			ToolSuccess:         p.Transcript.ToolSuccess.Resolve(resolvedTheme),
			ToolError:           p.Transcript.ToolError.Resolve(resolvedTheme),
			System:              p.Transcript.System.Resolve(resolvedTheme),
			Success:             p.Transcript.Success.Resolve(resolvedTheme),
			Warning:             p.Transcript.Warning.Resolve(resolvedTheme),
			Error:               p.Transcript.Error.Resolve(resolvedTheme),
			Compaction:          p.Transcript.Compaction.Resolve(resolvedTheme),
		},
		Status: ResolvedStatusPalette{
			Success:      p.Status.Success.Resolve(resolvedTheme),
			Warning:      p.Status.Warning.Resolve(resolvedTheme),
			Error:        p.Status.Error.Resolve(resolvedTheme),
			ContextEmpty: p.Status.ContextEmpty.Resolve(resolvedTheme),
		},
	}
}

var defaultPalette = Palette{
	App: AppPalette{
		Primary: AdaptiveColor{
			Light: Color{ANSI: "4", ANSI256: "33", TrueColor: "#3185FC"},
			Dark:  Color{ANSI: "4", ANSI256: "33", TrueColor: "#3185FC"},
		},
		Secondary: AdaptiveColor{
			Light: Color{ANSI: "3", ANSI256: "166", TrueColor: "#F96824"},
			Dark:  Color{ANSI: "3", ANSI256: "209", TrueColor: "#F96824"},
		},
		Foreground: AdaptiveColor{
			Light: Color{ANSI: "0", ANSI256: "235", TrueColor: "#12100E"},
			Dark:  Color{ANSI: "7", ANSI256: "231", TrueColor: "#FFFFFF"},
		},
		Muted: AdaptiveColor{
			Light: Color{ANSI: "8", ANSI256: "243", TrueColor: "#5A5651"},
			Dark:  Color{ANSI: "8", ANSI256: "103", TrueColor: "#8F97A1"},
		},
		Border: AdaptiveColor{
			Light: Color{ANSI: "7", ANSI256: "254", TrueColor: "#F0EFFD"},
			Dark:  Color{ANSI: "8", ANSI256: "237", TrueColor: "#34373C"},
		},
		ModeBg: AdaptiveColor{
			Light: Color{ANSI: "7", ANSI256: "254", TrueColor: "#F0EFFD"},
			Dark:  Color{ANSI: "8", ANSI256: "237", TrueColor: "#34373C"},
		},
		ModeText: AdaptiveColor{
			Light: Color{ANSI: "0", ANSI256: "235", TrueColor: "#12100E"},
			Dark:  Color{ANSI: "7", ANSI256: "231", TrueColor: "#FFFFFF"},
		},
		ChatBg: AdaptiveColor{
			Light: Color{ANSI: "7", ANSI256: "255", TrueColor: "#FFFFFF"},
			Dark:  Color{ANSI: "0", ANSI256: "233", TrueColor: "#12100E"},
		},
		InputBg: AdaptiveColor{
			Light: Color{ANSI: "7", ANSI256: "254", TrueColor: "#F0EFFD"},
			Dark:  Color{ANSI: "8", ANSI256: "236", TrueColor: "#1C1A18"},
		},
	},
	Transcript: TranscriptPalette{
		Foreground: AdaptiveColor{
			Light: Color{ANSI: "0", ANSI256: "235", TrueColor: "#12100E"},
			Dark:  Color{ANSI: "7", ANSI256: "231", TrueColor: "#FFFFFF"},
		},
		SelectionBackground: AdaptiveColor{
			Light: Color{ANSI: "4", ANSI256: "111", TrueColor: "#D0E2FF"},
			Dark:  Color{ANSI: "4", ANSI256: "25", TrueColor: "#1B3A5C"},
		},
		SelectionForeground: AdaptiveColor{
			Light: Color{ANSI: "0", ANSI256: "235", TrueColor: "#12100E"},
			Dark:  Color{ANSI: "7", ANSI256: "231", TrueColor: "#FFFFFF"},
		},
		User: AdaptiveColor{
			Light: Color{ANSI: "4", ANSI256: "33", TrueColor: "#3185FC"},
			Dark:  Color{ANSI: "4", ANSI256: "33", TrueColor: "#3185FC"},
		},
		Assistant: AdaptiveColor{
			Light: Color{ANSI: "2", ANSI256: "34", TrueColor: "#12BA85"},
			Dark:  Color{ANSI: "2", ANSI256: "85", TrueColor: "#12BA85"},
		},
		Tool: AdaptiveColor{
			Light: Color{ANSI: "4", ANSI256: "33", TrueColor: "#3185FC"},
			Dark:  Color{ANSI: "4", ANSI256: "33", TrueColor: "#3185FC"},
		},
		ToolSuccess: AdaptiveColor{
			Light: Color{ANSI: "2", ANSI256: "34", TrueColor: "#12BA85"},
			Dark:  Color{ANSI: "2", ANSI256: "85", TrueColor: "#12BA85"},
		},
		ToolError: AdaptiveColor{
			Light: Color{ANSI: "1", ANSI256: "161", TrueColor: "#DC2E3C"},
			Dark:  Color{ANSI: "1", ANSI256: "197", TrueColor: "#DC2E3C"},
		},
		System: AdaptiveColor{
			Light: Color{ANSI: "8", ANSI256: "243", TrueColor: "#5A5651"},
			Dark:  Color{ANSI: "8", ANSI256: "103", TrueColor: "#8F97A1"},
		},
		Success: AdaptiveColor{
			Light: Color{ANSI: "2", ANSI256: "34", TrueColor: "#12BA85"},
			Dark:  Color{ANSI: "2", ANSI256: "85", TrueColor: "#12BA85"},
		},
		Warning: AdaptiveColor{
			Light: Color{ANSI: "11", ANSI256: "220", TrueColor: "#FFE74C"},
			Dark:  Color{ANSI: "3", ANSI256: "216", TrueColor: "#FFE74C"},
		},
		Error: AdaptiveColor{
			Light: Color{ANSI: "1", ANSI256: "161", TrueColor: "#DC2E3C"},
			Dark:  Color{ANSI: "1", ANSI256: "197", TrueColor: "#DC2E3C"},
		},
		Compaction: AdaptiveColor{
			Light: Color{ANSI: "11", ANSI256: "220", TrueColor: "#FFE74C"},
			Dark:  Color{ANSI: "3", ANSI256: "216", TrueColor: "#FFE74C"},
		},
	},
	Status: StatusPalette{
		Success: AdaptiveColor{
			Light: Color{ANSI: "2", ANSI256: "34", TrueColor: "#12BA85"},
			Dark:  Color{ANSI: "2", ANSI256: "85", TrueColor: "#12BA85"},
		},
		Warning: AdaptiveColor{
			Light: Color{ANSI: "11", ANSI256: "220", TrueColor: "#FFE74C"},
			Dark:  Color{ANSI: "3", ANSI256: "216", TrueColor: "#FFE74C"},
		},
		Error: AdaptiveColor{
			Light: Color{ANSI: "1", ANSI256: "161", TrueColor: "#DC2E3C"},
			Dark:  Color{ANSI: "1", ANSI256: "197", TrueColor: "#DC2E3C"},
		},
		ContextEmpty: AdaptiveColor{
			Light: Color{ANSI: "8", ANSI256: "245", TrueColor: "#A39F99"},
			Dark:  Color{ANSI: "8", ANSI256: "103", TrueColor: "#8F97A1"},
		},
	},
}
