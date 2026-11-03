package main

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/Valentin-Rault/cc-tools/internal/theme"
	"github.com/charmbracelet/x/ansi"
)

// The theme's colours and the styles made of them. useTheme sets them all; whatever renders reads them then, so a
// new theme shows on the next frame.
var (
	themeName                                                                    string
	bgColor, bgDarkColor, bgHLColor, fgColor, fg2Color, dark5Color, commentColor color.Color
	gutterColor, blueColor, cyanColor, magentaColor, orangeColor, yellowColor    color.Color
	greenColor, redColor, gitAddColor, gitDelColor                               color.Color
	text, text2, faint, comment, gutter, blue, cyan, orange, yellow, green, red  lipgloss.Style
	gitAdd, gitDel, bold, accent, keycap, segment, bar                           lipgloss.Style
	searchHit                                                                    lipgloss.Style // what / found
	lift                                                                         *strings.Replacer
)

func init() { useTheme(theme.Themes[0].Name) }

func fg(c color.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }

// useTheme makes it the theme everything is drawn in; false when there's none by that name.
func useTheme(name string) bool {
	t, ok := theme.Named(name)
	if !ok {
		return false
	}
	c := lipgloss.Color
	themeName = t.Name
	bgColor, bgDarkColor, bgHLColor = c(t.Bg), c(t.BgDark), c(t.BgHL)
	fgColor, fg2Color, dark5Color, commentColor, gutterColor = c(t.Fg), c(t.Fg2), c(t.Dark5), c(t.Comment), c(t.Gutter)
	blueColor, cyanColor, magentaColor, orangeColor = c(t.Blue), c(t.Cyan), c(t.Magenta), c(t.Orange)
	yellowColor, greenColor, redColor, gitAddColor, gitDelColor = c(t.Yellow), c(t.Green), c(t.Red), c(t.GitAdd), c(t.GitDel)

	text, text2, faint, comment, gutter = fg(fgColor), fg(fg2Color), fg(dark5Color), fg(commentColor), fg(gutterColor)
	blue, cyan, orange, yellow, green, red = fg(blueColor), fg(cyanColor), fg(orangeColor), fg(yellowColor), fg(greenColor), fg(redColor)
	gitAdd, gitDel = fg(gitAddColor), fg(gitDelColor)
	bold, accent = text.Bold(true), fg(magentaColor).Bold(true)
	keycap = lipgloss.NewStyle().Background(bgHLColor).Foreground(fgColor)
	segment = lipgloss.NewStyle().Background(bgHLColor).Foreground(fg2Color)
	bar = lipgloss.NewStyle().Background(bgDarkColor).Foreground(commentColor)
	searchHit = lipgloss.NewStyle().Background(c(t.Search)).Foreground(fgColor)
	// lift brightens the dim colours of a highlighted row, which would be hard to read on its background
	lift = strings.NewReplacer(fgParam(commentColor), fgParam(dark5Color), fgParam(gutterColor), fgParam(dark5Color))
	return true
}

func themeNames() []string { return theme.Names() }

func pill(c color.Color) lipgloss.Style {
	return lipgloss.NewStyle().Background(c).Foreground(bgColor).Bold(true)
}

// onBg lays s over bg, exactly w cells wide (theme.OnBg), in the theme's text colour where s sets none.
func onBg(s string, w int, bg color.Color) string { return theme.OnBg(s, w, bg, fgColor) }

// fgParam is how lipgloss writes a true-colour foreground inside an escape sequence.
func fgParam(c color.Color) string {
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("38;2;%d;%d;%d", r>>8, g>>8, b>>8)
}

func pad(s string, n int) string { return s + strings.Repeat(" ", max(0, n-lipgloss.Width(s))) }

// cell truncates s to n cells and pads it to exactly n.
func cell(s string, n int) string { return pad(ansi.Truncate(s, n, "…"), n) }
