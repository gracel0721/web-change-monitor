// Package config holds paths and user-editable settings for webwatch.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const appDirName = "webwatch"

// Settings are the user-editable options persisted to config.json.
type Settings struct {
	OllamaURL   string `json:"ollama_url"`
	OllamaModel string `json:"ollama_model"`
	WebhookURL  string `json:"webhook_url"`
	ChromePath  string `json:"chrome_path"` // "" = let chromedp autodetect system Chrome
}

// Default returns the built-in default settings.
func Default() Settings {
	return Settings{
		OllamaURL:   "http://localhost:11434",
		OllamaModel: "llama3.2",
		WebhookURL:  "",
	}
}

// AppDir returns the on-disk app directory (~/.webwatch), creating it.
func AppDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, appDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// DBPath returns the default database location.
func DBPath() (string, error) {
	dir, err := AppDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "webwatch.db"), nil
}

// ConfigPath returns the settings file location.
func ConfigPath() (string, error) {
	dir, err := AppDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load reads settings, falling back to defaults when the file is missing.
func Load() (Settings, error) {
	s := Default()
	p, err := ConfigPath()
	if err != nil {
		return s, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, err
	}
	loaded := Settings{}
	if err := json.Unmarshal(data, &loaded); err != nil {
		return s, err
	}
	// Merge: only override non-empty fields so defaults survive.
	if loaded.OllamaURL != "" {
		s.OllamaURL = loaded.OllamaURL
	}
	if loaded.OllamaModel != "" {
		s.OllamaModel = loaded.OllamaModel
	}
	s.WebhookURL = loaded.WebhookURL
	if loaded.ChromePath != "" {
		s.ChromePath = loaded.ChromePath
	}
	return s, nil
}

// Save writes settings to disk.
func Save(s Settings) error {
	p, err := ConfigPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}