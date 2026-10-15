package main

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// tokyonight night. pr-watch paints its own background, the way an editor colorscheme does, so it looks the same
// whatever the terminal's palette. It needs true colour, which current terminals and zellij have.
var (
	bgColor      = lipgloss.Color("#1a1b26")
	bgDarkColor  = lipgloss.Color("#16161e")
	bgHLColor    = lipgloss.Color("#292e42")
	fgColor      = lipgloss.Color("#c0caf5")
	fg2Color     = lipgloss.Color("#a9b1d6")
	dark5Color   = lipgloss.Color("#737aa2")
	commentColor = lipgloss.Color("#565f89")
	gutterColor  = lipgloss.Color("#3b4261")
	blueColor    = lipgloss.Color("#7aa2f7")
	cyanColor    = lipgloss.Color("#7dcfff")
	magentaColor = lipgloss.Color("#bb9af7")
	orangeColor  = lipgloss.Color("#ff9e64")
	yellowColor  = lipgloss.Color("#e0af68")
	greenColor   = lipgloss.Color("#9ece6a")
	redColor     = lipgloss.Color("#f7768e")
	gitAddColor  = lipgloss.Color("#449dab")
	gitDelColor  = lipgloss.Color("#db4b4b")
)

func fg(c color.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }

var (
	text    = fg(fgColor)
	text2   = fg(fg2Color)
	faint   = fg(dark5Color)
	comment = fg(commentColor)
	gutter  = fg(gutterColor)
	blue    = fg(blueColor)
	cyan    = fg(cyanColor)
	orange  = fg(orangeColor)
	yellow  = fg(yellowColor)
	green   = fg(greenColor)
	red     = fg(redColor)
	gitAdd  = fg(gitAddColor)
	gitDel  = fg(gitDelColor)
	bold    = text.Bold(true)
	accent  = fg(magentaColor).Bold(true)
	keycap  = lipgloss.NewStyle().Background(bgHLColor).Foreground(fgColor)
	segment = lipgloss.NewStyle().Background(bgHLColor).Foreground(fg2Color)
	bar     = lipgloss.NewStyle().Background(bgDarkColor).Foreground(commentColor)
)

func pill(c color.Color) lipgloss.Style {
	return lipgloss.NewStyle().Background(c).Foreground(bgColor).Bold(true)
}

// onBg lays s over bg, exactly w cells wide. lipgloss ends every styled span with a reset, which would show the
// terminal's own background, so each reset is followed by bg again.
func onBg(s string, w int, bg color.Color) string {
	set := ansi.Style{}.BackgroundColor(bg).ForegroundColor(fgColor).String()
	s = ansi.Truncate(s, w, "…")
	return set + strings.ReplaceAll(s, "\x1b[m", "\x1b[m"+set) + strings.Repeat(" ", max(0, w-ansi.StringWidth(s))) + "\x1b[m"
}

// lift brightens the dim colours of a highlighted row, which would be hard to read on its background.
var lift = strings.NewReplacer(fgParam(commentColor), fgParam(dark5Color), fgParam(gutterColor), fgParam(dark5Color))

// fgParam is how lipgloss writes a true-colour foreground inside an escape sequence.
func fgParam(c color.Color) string {
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("38;2;%d;%d;%d", r>>8, g>>8, b>>8)
}

func pad(s string, n int) string { return s + strings.Repeat(" ", max(0, n-lipgloss.Width(s))) }

// cell truncates s to n cells and pads it to exactly n.
func cell(s string, n int) string { return pad(ansi.Truncate(s, n, "…"), n) }
