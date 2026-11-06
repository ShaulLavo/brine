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

type Tone uint8

const (
	Plain Tone = iota
	Muted
	Accent
	Good
	Bad
	Warn
)

type Theme struct {
	noColor                               bool
	Plain, Muted, Accent, Good, Bad, Warn lipgloss.Style
	Title                                 lipgloss.Style
	Divider                               lipgloss.Style
	Help                                  lipgloss.Style
}

func NewTheme(noColor bool) Theme {
	plain := lipgloss.NewStyle()
	if noColor {
		return Theme{noColor: true, Title: plain, Divider: plain, Help: plain, Plain: plain, Muted: plain, Accent: plain, Good: plain, Bad: plain, Warn: plain}
	}
	palette := DefaultPalette()
	return Theme{
		Plain: plain, Muted: plain.Foreground(palette.Muted), Accent: plain.Foreground(palette.Accent),
		Good: plain.Foreground(lipgloss.Green), Bad: plain.Foreground(lipgloss.Red), Warn: plain.Foreground(lipgloss.Yellow),
		Title:   plain.Bold(true).Foreground(palette.Accent),
		Divider: plain.Foreground(palette.Muted),
		Help:    plain.Foreground(palette.Muted),
	}
}

func ThemeFromEnv() Theme {
	return NewTheme(os.Getenv("NO_COLOR") != "")
}

// Style returns the style for a semantic tone.
func (t Theme) Style(tone Tone) lipgloss.Style {
	switch tone {
	case Muted:
		return t.Muted
	case Accent:
		return t.Accent
	case Good:
		return t.Good
	case Bad:
		return t.Bad
	case Warn:
		return t.Warn
	default:
		return t.Plain
	}
}
