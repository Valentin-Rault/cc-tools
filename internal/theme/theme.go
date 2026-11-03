// Package theme is the colorschemes pr-watch and cc-status are drawn in. pr-watch's t picks one and writes it to the
// config ("theme"); cc-status's popup follows.
package theme

import (
	"image/color"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Theme is a colorscheme's colours, by the role the tools give them. They paint their own background, the way an
// editor colorscheme does, so they look the same whatever the terminal's palette. That needs true colour, which
// current terminals, zellij and tmux have.
type Theme struct {
	Name                                            string
	Light                                           bool
	Bg, BgDark, BgHL                                string // the background; the status line's; a selected row's and key caps'
	Fg, Fg2, Dark5, Comment, Gutter                 string // text from the brightest down to the borders
	Blue, Cyan, Magenta, Orange, Yellow, Green, Red string
	GitAdd, GitDel                                  string // a PR's lines added and deleted
	Search                                          string // the background of what / found
}

// Themes, the first by default.
var Themes = []Theme{
	{Name: "tokyonight", Bg: "#1a1b26", BgDark: "#16161e", BgHL: "#292e42",
		Fg: "#c0caf5", Fg2: "#a9b1d6", Dark5: "#737aa2", Comment: "#565f89", Gutter: "#3b4261",
		Blue: "#7aa2f7", Cyan: "#7dcfff", Magenta: "#bb9af7", Orange: "#ff9e64", Yellow: "#e0af68", Green: "#9ece6a", Red: "#f7768e",
		GitAdd: "#449dab", GitDel: "#db4b4b", Search: "#3d59a1"},
	{Name: "catppuccin-mocha", Bg: "#1e1e2e", BgDark: "#181825", BgHL: "#313244",
		Fg: "#cdd6f4", Fg2: "#bac2de", Dark5: "#7f849c", Comment: "#6c7086", Gutter: "#45475a",
		Blue: "#89b4fa", Cyan: "#89dceb", Magenta: "#cba6f7", Orange: "#fab387", Yellow: "#f9e2af", Green: "#a6e3a1", Red: "#f38ba8",
		GitAdd: "#94e2d5", GitDel: "#eba0ac", Search: "#3e5767"},
	{Name: "catppuccin-latte", Light: true, Bg: "#eff1f5", BgDark: "#e6e9ef", BgHL: "#ccd0da", // greys a step darker: on light, the lightest fade out
		Fg: "#4c4f69", Fg2: "#5c5f77", Dark5: "#7c7f93", Comment: "#8c8fa1", Gutter: "#acb0be",
		Blue: "#1e66f5", Cyan: "#04a5e5", Magenta: "#8839ef", Orange: "#fe640b", Yellow: "#df8e1d", Green: "#40a02b", Red: "#d20f39",
		GitAdd: "#179299", GitDel: "#e64553", Search: "#a8daf0"},
	{Name: "gruvbox", Bg: "#282828", BgDark: "#1d2021", BgHL: "#3c3836",
		Fg: "#ebdbb2", Fg2: "#d5c4a1", Dark5: "#a89984", Comment: "#928374", Gutter: "#504945",
		Blue: "#83a598", Cyan: "#8ec07c", Magenta: "#d3869b", Orange: "#fe8019", Yellow: "#fabd2f", Green: "#b8bb26", Red: "#fb4934",
		GitAdd: "#8ec07c", GitDel: "#cc241d", Search: "#076678"},
	{Name: "nord", Bg: "#2e3440", BgDark: "#272c36", BgHL: "#3b4252",
		Fg: "#e5e9f0", Fg2: "#d8dee9", Dark5: "#7b88a1", Comment: "#616e88", Gutter: "#4c566a",
		Blue: "#81a1c1", Cyan: "#88c0d0", Magenta: "#b48ead", Orange: "#d08770", Yellow: "#ebcb8b", Green: "#a3be8c", Red: "#bf616a",
		GitAdd: "#8fbcbb", GitDel: "#bf616a", Search: "#5e81ac"},
	{Name: "dracula", Bg: "#282a36", BgDark: "#21222c", BgHL: "#44475a",
		Fg: "#f8f8f2", Fg2: "#e2e2dc", Dark5: "#8f9abb", Comment: "#6272a4", Gutter: "#44475a",
		Blue: "#bd93f9", Cyan: "#8be9fd", Magenta: "#ff79c6", Orange: "#ffb86c", Yellow: "#f1fa8c", Green: "#50fa7b", Red: "#ff5555",
		GitAdd: "#50fa7b", GitDel: "#ff5555", Search: "#6272a4"},
	{Name: "kanagawa", Bg: "#1f1f28", BgDark: "#16161d", BgHL: "#2a2a37",
		Fg: "#dcd7ba", Fg2: "#c8c093", Dark5: "#938aa9", Comment: "#727169", Gutter: "#54546d",
		Blue: "#7e9cd8", Cyan: "#7fb4ca", Magenta: "#957fb8", Orange: "#ffa066", Yellow: "#e6c384", Green: "#98bb6c", Red: "#e46876",
		GitAdd: "#76946a", GitDel: "#c34043", Search: "#2d4f67"},
}

// Named is the theme by that name, else the default and false.
func Named(name string) (Theme, bool) {
	if i := slices.IndexFunc(Themes, func(t Theme) bool { return t.Name == name }); i >= 0 {
		return Themes[i], true
	}
	return Themes[0], false
}

func Names() []string {
	var out []string
	for _, t := range Themes {
		out = append(out, t.Name)
	}
	return out
}

// OnBg lays s over bg, exactly w cells wide, in fg where s sets no colour. lipgloss ends every styled span with a
// reset, which would show the terminal's own background, so each reset is followed by bg again.
func OnBg(s string, w int, bg, fg color.Color) string {
	set := ansi.Style{}.BackgroundColor(bg).ForegroundColor(fg).String()
	s = ansi.Truncate(s, w, "…")
	return set + strings.ReplaceAll(s, "\x1b[m", "\x1b[m"+set) + strings.Repeat(" ", max(0, w-ansi.StringWidth(s))) + "\x1b[m"
}
