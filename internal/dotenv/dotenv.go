// Package dotenv reads a minimal .env file (KEY=VALUE lines) so the CLI can pick
// up NEURALWATT_API_KEY without exporting it in the shell. Real environment
// variables always win over values from the file.
package dotenv

import (
	"os"
	"strings"
)

// Env is a loaded set of key/value pairs.
type Env struct {
	values map[string]string
	files  []string
}

// Load reads the given files, ignoring those that do not exist. An empty list
// means ".env".
func Load(paths ...string) Env {
	if len(paths) == 0 {
		paths = []string{".env"}
	}
	e := Env{values: map[string]string{}}
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		// The path comes from the user (-env-file), so reading it is intended.
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		e.files = append(e.files, p)
		for _, line := range strings.Split(string(raw), "\n") {
			if k, v, ok := parseLine(line); ok {
				e.values[k] = v
			}
		}
	}
	return e
}

// Files reports which files were read, for -v output.
func (e Env) Files() []string { return e.files }

// Get returns the first non-empty value for keys, preferring the process
// environment over the file.
func (e Env) Get(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	for _, k := range keys {
		if v := e.values[k]; v != "" {
			return v
		}
	}
	return ""
}

// Lookup reports a value that came from the file (not the environment).
func (e Env) Lookup(key string) (string, bool) {
	v, ok := e.values[key]
	return v, ok && v != ""
}

func parseLine(line string) (key, value string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	line = strings.TrimPrefix(line, "export ")
	eq := strings.Index(line, "=")
	if eq <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:eq])
	value = strings.TrimSpace(line[eq+1:])
	if key == "" {
		return "", "", false
	}
	value = unquote(value)
	if i := strings.Index(value, " #"); i >= 0 {
		value = strings.TrimSpace(value[:i])
	}
	return key, value, true
}

func unquote(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}
