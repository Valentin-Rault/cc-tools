package main

import (
	"fmt"
	"image/color"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// A theme is a colorscheme's colours, by the role pr-watch gives them. pr-watch paints its own background, the way
// an editor colorscheme does, so it looks the same whatever the terminal's palette. It needs true colour, which current
// terminals and zellij have. t picks one (themepick.go); the ? popup lists it, the key help under the lists doesn't.
type theme struct {
	name                                            string
	light                                           bool
	bg, bgDark, bgHL                                string // the background; the status line's; a selected row's and key caps'
	fg, fg2, dark5, comment, gutter                 string // text from the brightest down to the borders
	blue, cyan, magenta, orange, yellow, green, red string
	gitAdd, gitDel                                  string // a PR's lines added and deleted
	search                                          string // the background of what / found
}

// themes, the first by default.
var themes = []theme{
	{name: "tokyonight", bg: "#1a1b26", bgDark: "#16161e", bgHL: "#292e42",
		fg: "#c0caf5", fg2: "#a9b1d6", dark5: "#737aa2", comment: "#565f89", gutter: "#3b4261",
		blue: "#7aa2f7", cyan: "#7dcfff", magenta: "#bb9af7", orange: "#ff9e64", yellow: "#e0af68", green: "#9ece6a", red: "#f7768e",
		gitAdd: "#449dab", gitDel: "#db4b4b", search: "#3d59a1"},
	{name: "catppuccin-mocha", bg: "#1e1e2e", bgDark: "#181825", bgHL: "#313244",
		fg: "#cdd6f4", fg2: "#bac2de", dark5: "#7f849c", comment: "#6c7086", gutter: "#45475a",
		blue: "#89b4fa", cyan: "#89dceb", magenta: "#cba6f7", orange: "#fab387", yellow: "#f9e2af", green: "#a6e3a1", red: "#f38ba8",
		gitAdd: "#94e2d5", gitDel: "#eba0ac", search: "#3e5767"},
	{name: "catppuccin-latte", light: true, bg: "#eff1f5", bgDark: "#e6e9ef", bgHL: "#ccd0da", // greys a step darker: on light, the lightest fade out
		fg: "#4c4f69", fg2: "#5c5f77", dark5: "#7c7f93", comment: "#8c8fa1", gutter: "#acb0be",
		blue: "#1e66f5", cyan: "#04a5e5", magenta: "#8839ef", orange: "#fe640b", yellow: "#df8e1d", green: "#40a02b", red: "#d20f39",
		gitAdd: "#179299", gitDel: "#e64553", search: "#a8daf0"},
	{name: "gruvbox", bg: "#282828", bgDark: "#1d2021", bgHL: "#3c3836",
		fg: "#ebdbb2", fg2: "#d5c4a1", dark5: "#a89984", comment: "#928374", gutter: "#504945",
		blue: "#83a598", cyan: "#8ec07c", magenta: "#d3869b", orange: "#fe8019", yellow: "#fabd2f", green: "#b8bb26", red: "#fb4934",
		gitAdd: "#8ec07c", gitDel: "#cc241d", search: "#076678"},
	{name: "nord", bg: "#2e3440", bgDark: "#272c36", bgHL: "#3b4252",
		fg: "#e5e9f0", fg2: "#d8dee9", dark5: "#7b88a1", comment: "#616e88", gutter: "#4c566a",
		blue: "#81a1c1", cyan: "#88c0d0", magenta: "#b48ead", orange: "#d08770", yellow: "#ebcb8b", green: "#a3be8c", red: "#bf616a",
		gitAdd: "#8fbcbb", gitDel: "#bf616a", search: "#5e81ac"},
	{name: "dracula", bg: "#282a36", bgDark: "#21222c", bgHL: "#44475a",
		fg: "#f8f8f2", fg2: "#e2e2dc", dark5: "#8f9abb", comment: "#6272a4", gutter: "#44475a",
		blue: "#bd93f9", cyan: "#8be9fd", magenta: "#ff79c6", orange: "#ffb86c", yellow: "#f1fa8c", green: "#50fa7b", red: "#ff5555",
		gitAdd: "#50fa7b", gitDel: "#ff5555", search: "#6272a4"},
	{name: "kanagawa", bg: "#1f1f28", bgDark: "#16161d", bgHL: "#2a2a37",
		fg: "#dcd7ba", fg2: "#c8c093", dark5: "#938aa9", comment: "#727169", gutter: "#54546d",
		blue: "#7e9cd8", cyan: "#7fb4ca", magenta: "#957fb8", orange: "#ffa066", yellow: "#e6c384", green: "#98bb6c", red: "#e46876",
		gitAdd: "#76946a", gitDel: "#c34043", search: "#2d4f67"},
}

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

func init() { useTheme(themes[0].name) }

func fg(c color.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }

// useTheme makes it the theme everything is drawn in; false when there's none by that name.
func useTheme(name string) bool {
	i := slices.IndexFunc(themes, func(t theme) bool { return t.name == name })
	if i < 0 {
		return false
	}
	t, c := themes[i], lipgloss.Color
	themeName = t.name
	bgColor, bgDarkColor, bgHLColor = c(t.bg), c(t.bgDark), c(t.bgHL)
	fgColor, fg2Color, dark5Color, commentColor, gutterColor = c(t.fg), c(t.fg2), c(t.dark5), c(t.comment), c(t.gutter)
	blueColor, cyanColor, magentaColor, orangeColor = c(t.blue), c(t.cyan), c(t.magenta), c(t.orange)
	yellowColor, greenColor, redColor, gitAddColor, gitDelColor = c(t.yellow), c(t.green), c(t.red), c(t.gitAdd), c(t.gitDel)

	text, text2, faint, comment, gutter = fg(fgColor), fg(fg2Color), fg(dark5Color), fg(commentColor), fg(gutterColor)
	blue, cyan, orange, yellow, green, red = fg(blueColor), fg(cyanColor), fg(orangeColor), fg(yellowColor), fg(greenColor), fg(redColor)
	gitAdd, gitDel = fg(gitAddColor), fg(gitDelColor)
	bold, accent = text.Bold(true), fg(magentaColor).Bold(true)
	keycap = lipgloss.NewStyle().Background(bgHLColor).Foreground(fgColor)
	segment = lipgloss.NewStyle().Background(bgHLColor).Foreground(fg2Color)
	bar = lipgloss.NewStyle().Background(bgDarkColor).Foreground(commentColor)
	searchHit = lipgloss.NewStyle().Background(c(t.search)).Foreground(fgColor)
	// lift brightens the dim colours of a highlighted row, which would be hard to read on its background
	lift = strings.NewReplacer(fgParam(commentColor), fgParam(dark5Color), fgParam(gutterColor), fgParam(dark5Color))
	return true
}

func themeNames() []string {
	var out []string
	for _, t := range themes {
		out = append(out, t.name)
	}
	return out
}

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

// fgParam is how lipgloss writes a true-colour foreground inside an escape sequence.
func fgParam(c color.Color) string {
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("38;2;%d;%d;%d", r>>8, g>>8, b>>8)
}

func pad(s string, n int) string { return s + strings.Repeat(" ", max(0, n-lipgloss.Width(s))) }

// cell truncates s to n cells and pads it to exactly n.
func cell(s string, n int) string { return pad(ansi.Truncate(s, n, "…"), n) }
