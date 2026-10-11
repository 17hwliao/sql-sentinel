package configenv

import (
	"errors"
	"os"
	"strings"
)

// LoadSecretFiles supports Compose secrets without putting secret values into
// YAML or process arguments. An explicit file and value cannot conflict.
func LoadSecretFiles() error {
	for _, entry := range os.Environ() {
		name, path, ok := strings.Cut(entry, "=")
		if !ok || !strings.HasSuffix(name, "_FILE") || path == "" {
			continue
		}
		key := strings.TrimSuffix(name, "_FILE")
		if !strings.HasPrefix(key, "AGENTMESH_") && !strings.HasPrefix(key, "SQL_SENTINEL_") && !strings.HasPrefix(key, "GITHUB_") && key != "OPENAI_API_KEY" && key != "ARK_API_KEY" {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil || len(raw) > 16384 {
			return errors.New("secret_file_invalid")
		}
		value := strings.TrimSpace(string(raw))
		if value == "" {
			return errors.New("secret_file_empty")
		}
		if existing := os.Getenv(key); existing != "" && existing != value {
			return errors.New("secret_configuration_conflict")
		}
		if err = os.Setenv(key, value); err != nil {
			return errors.New("secret_configuration_failed")
		}
	}
	return nil
}
