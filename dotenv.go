package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// loadDotEnv parses a KEY=VALUE config file into a map. Blank lines and lines
// starting with '#' are skipped. Values may be wrapped in single or double
// quotes (stripped); unquoted values may carry a trailing " # comment".
func loadDotEnv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	values := make(map[string]string)
	sc := bufio.NewScanner(f)
	for lineNo := 1; sc.Scan(); lineNo++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE, got %q", path, lineNo, line)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("%s:%d: empty key", path, lineNo)
		}
		values[key] = parseValue(strings.TrimSpace(value))
	}
	return values, sc.Err()
}

// parseValue strips surrounding quotes or a trailing inline comment.
func parseValue(value string) string {
	switch {
	case strings.HasPrefix(value, `"`), strings.HasPrefix(value, `'`):
		if end := strings.Index(value[1:], value[:1]); end >= 0 {
			return value[1 : 1+end]
		}
	default:
		if i := strings.Index(value, " #"); i >= 0 {
			value = strings.TrimSpace(value[:i])
		}
	}
	return value
}
