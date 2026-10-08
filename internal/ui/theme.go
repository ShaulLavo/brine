package ui

import (
	"image/color"
	"os"

	"charm.land/lipgloss/v2"
)

type Palette struct {
	Accent color.Color
	Muted  color.Color
}

func DefaultPalette() Palette {
	return Palette{Accent: lipgloss.BrightBlue, Muted: lipgloss.BrightBlack}
}

type Theme struct {
	Title   lipgloss.Style
	Divider lipgloss.Style
	Help    lipgloss.Style
}

func NewTheme(noColor bool) Theme {
	plain := lipgloss.NewStyle()
	if noColor {
		return Theme{Title: plain, Divider: plain, Help: plain}
	}
	palette := DefaultPalette()
	return Theme{
		Title:   plain.Bold(true).Foreground(palette.Accent),
		Divider: plain.Foreground(palette.Muted),
		Help:    plain.Foreground(palette.Muted),
	}
}

func ThemeFromEnv() Theme {
	return NewTheme(os.Getenv("NO_COLOR") != "")
}
