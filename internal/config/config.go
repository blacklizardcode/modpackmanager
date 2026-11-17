// Package config persists user preferences.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Config struct {
	CurseForgeAPIKey string   `json:"curseforgeApiKey,omitempty"`
	Recent           []string `json:"recent,omitempty"`
	LastDir          string   `json:"lastDir,omitempty"`
	SearchSource     string   `json:"searchSource,omitempty"` // modrinth, curseforge, both
	WindowWidth      int      `json:"windowWidth,omitempty"`
	WindowHeight     int      `json:"windowHeight,omitempty"`
}

var mu sync.Mutex

// Dir returns the config directory (overridable with MPM_CONFIG_DIR for tests).
func Dir() string {
	if d := os.Getenv("MPM_CONFIG_DIR"); d != "" {
		return d
	}
	d, err := os.UserConfigDir()
	if err != nil {
		d = os.TempDir()
	}
	return filepath.Join(d, "modpackmanager")
}

// CacheDir returns the cache directory (icons, downloaded jars).
func CacheDir() string {
	if d := os.Getenv("MPM_CACHE_DIR"); d != "" {
		return d
	}
	d, err := os.UserCacheDir()
	if err != nil {
		d = os.TempDir()
	}
	return filepath.Join(d, "modpackmanager")
}

func path() string { return filepath.Join(Dir(), "config.json") }

// Load reads the config; a missing or broken file yields defaults.
func Load() *Config {
	mu.Lock()
	defer mu.Unlock()
	c := &Config{}
	if data, err := os.ReadFile(path()); err == nil {
		_ = json.Unmarshal(data, c)
	}
	if c.SearchSource == "" {
		c.SearchSource = "modrinth"
	}
	return c
}

// Save writes the config with owner-only permissions (it may hold an API key).
func (c *Config) Save() error {
	mu.Lock()
	defer mu.Unlock()
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path())
}

// APIKey returns the CurseForge key, preferring the environment.
func (c *Config) APIKey() string {
	for _, env := range []string{"MPM_CURSEFORGE_API_KEY", "CF_API_KEY", "CURSEFORGE_API_KEY"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v
		}
	}
	return strings.TrimSpace(c.CurseForgeAPIKey)
}

// AddRecent moves path to the front of the recent list.
func (c *Config) AddRecent(p string) {
	abs, err := filepath.Abs(p)
	if err == nil {
		p = abs
	}
	out := []string{p}
	for _, r := range c.Recent {
		if r != p {
			out = append(out, r)
		}
	}
	if len(out) > 10 {
		out = out[:10]
	}
	c.Recent = out
}

// RemoveRecent drops a path from the recent list.
func (c *Config) RemoveRecent(p string) {
	var out []string
	for _, r := range c.Recent {
		if r != p {
			out = append(out, r)
		}
	}
	c.Recent = out
}
