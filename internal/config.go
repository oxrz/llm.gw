package internal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	DaemonPort     = ":9898"
	ProxyPort      = ":9999"
	BackendPort    = ":9797"
	HealthURL      = "http://127.0.0.1:9797/health"
	HealthInterval = 30 * time.Second
)

type ModelsConfig struct {
	Default string            `json:"default"`
	Models  map[string]string `json:"models"`
}

var (
	DefaultModel  string
	ModelCommands map[string]string
)

// resolveModelName tries exact match first, then case-insensitive,
// then prefix match, then substring match.
func ResolveModelName(input string) string {
	if input == "" {
		return ""
	}

	// exact match
	if _, ok := ModelCommands[input]; ok {
		return input
	}

	lower := strings.ToLower(input)

	// case-insensitive exact match
	for name := range ModelCommands {
		if strings.EqualFold(name, input) {
			return name
		}
	}

	// prefix match (e.g. "qwen35" matches "Qwen3.6-35B-...")
	for name := range ModelCommands {
		if strings.HasPrefix(strings.ToLower(name), lower) {
			return name
		}
	}

	// substring match
	for name := range ModelCommands {
		if strings.Contains(strings.ToLower(name), lower) {
			return name
		}
	}

	return ""
}

func LoadModels(configPath string) error {
	if configPath == "" {
		// default: look next to the binary, then in CWD
		exe, err := os.Executable()
		if err == nil {
			configPath = filepath.Join(filepath.Dir(exe), ".models.json")
			if _, err := os.Stat(configPath); err != nil {
				configPath = ".models.json"
			}
		} else {
			configPath = ".models.json"
		}
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read models config %s: %w", configPath, err)
	}

	var cfg ModelsConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("failed to parse %s: %w", configPath, err)
	}

	if len(cfg.Models) == 0 {
		return fmt.Errorf("no models defined in %s", configPath)
	}

	DefaultModel = cfg.Default
	if DefaultModel == "" {
		// pick first model as default
		for name := range cfg.Models {
			DefaultModel = name
			break
		}
	}

	if _, ok := cfg.Models[DefaultModel]; !ok {
		return fmt.Errorf("default model %q not found in models", DefaultModel)
	}

	ModelCommands = cfg.Models
	return nil
}
