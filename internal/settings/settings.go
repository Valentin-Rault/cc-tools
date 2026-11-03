// Package settings is what pr-watch and cc-status both read from pr-watch's config file: the theme and the keys.
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Valentin-Rault/cc-tools/internal/keymap"
)

// Path is pr-watch's config file: $PR_WATCH_CONFIG, else ~/.config/pr-watch/config.json.
func Path() string {
	if p := os.Getenv("PR_WATCH_CONFIG"); p != "" {
		return p
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config/pr-watch/config.json")
}

// Shared is the settings both tools follow.
type Shared struct {
	Theme string                 `json:"theme"`
	Keys  map[string]keymap.List `json:"keys"`
}

// Load reads them; without a config file there are none.
func Load() (Shared, error) {
	var s Shared
	b, err := os.ReadFile(Path())
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("%s: %w", Path(), err)
	}
	return s, nil
}
