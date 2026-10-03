package userenv

import (
	"fmt"
	"strings"
)

// Parse reads one KEY=VALUE assignment per line. Values may be empty and may
// contain additional '=' characters.
func Parse(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]string{}, nil
	}

	out := make(map[string]string)
	for index, line := range strings.Split(raw, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !validKey(key) || strings.ContainsRune(value, '\x00') {
			return nil, fmt.Errorf("line %d must use a valid VAR=VAL assignment", index+1)
		}
		out[key] = value
	}
	return out, nil
}

func validKey(key string) bool {
	if key == "" || strings.ContainsAny(key, "=\x00") {
		return false
	}
	for index, r := range key {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '_' || (index > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}
