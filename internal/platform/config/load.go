package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"gopkg.in/yaml.v3"
)

const (
	defaultConfigPath      = "config/config.yml"
	defaultDebugConfigPath = "config/config_debug.yml"
	envConfigVar           = "DATABUS_CONFIG"
)

// Load читает YAML, подставляет ${VAR} и ${VAR:default} из окружения,
// разбирает в Config. Выбор файла:
//  1. flags.ConfigPath
//  2. $DATABUS_CONFIG
//  3. config/config_debug.yml (если flags.Debug)
//  4. config/config.yml
//
// workingDir — каталог, относительно которого ищется дефолтный путь.
func Load(flags Flags, workingDir string) (*Config, string, error) {
	path := resolveConfigPath(flags, workingDir)

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, path, fmt.Errorf("read config %q: %w", path, err)
	}

	expanded := expandEnv(raw)

	var cfg Config
	if err := yaml.Unmarshal(expanded, &cfg); err != nil {
		return nil, path, fmt.Errorf("parse yaml %q: %w", path, err)
	}

	applyDefaults(&cfg)

	if err := Validate(&cfg); err != nil {
		return nil, path, fmt.Errorf("validate config %q: %w", path, err)
	}

	return &cfg, path, nil
}

func resolveConfigPath(flags Flags, workingDir string) string {
	if flags.ConfigPath != "" {
		return flags.ConfigPath
	}
	if env := os.Getenv(envConfigVar); env != "" {
		return env
	}
	if flags.Debug {
		return filepath.Join(workingDir, defaultDebugConfigPath)
	}
	return filepath.Join(workingDir, defaultConfigPath)
}

// envVarPattern — ${VAR} или ${VAR:default_value}.
// default может содержать любые символы кроме '}'.
var envVarPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::([^}]*))?\}`)

// expandEnv заменяет ${VAR} / ${VAR:default} значениями из окружения.
// Если переменная не установлена и дефолта нет — оставляет пустую строку.
func expandEnv(data []byte) []byte {
	return envVarPattern.ReplaceAllFunc(data, func(match []byte) []byte {
		groups := envVarPattern.FindSubmatch(match)
		name := string(groups[1])
		def := ""
		if len(groups) > 2 {
			def = string(groups[2])
		}
		if v, ok := os.LookupEnv(name); ok {
			return []byte(v)
		}
		return []byte(def)
	})
}
