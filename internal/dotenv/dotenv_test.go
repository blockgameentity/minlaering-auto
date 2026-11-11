package dotenv

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadParsesCommonForms(t *testing.T) {
	p := write(t, `# comment
NEURALWATT_API_KEY=sk-abc123
export MINLAERING_LLM_MODEL=mimo-v2.6-pro
QUOTED="value with spaces"
SINGLE='single quoted'
INLINE=value # trailing comment
EMPTY=
`)
	e := Load(p)

	cases := map[string]string{
		"NEURALWATT_API_KEY":   "sk-abc123",
		"MINLAERING_LLM_MODEL": "mimo-v2.6-pro",
		"QUOTED":               "value with spaces",
		"SINGLE":               "single quoted",
		"INLINE":               "value",
	}
	for key, want := range cases {
		if got := e.Get(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	if v, ok := e.Lookup("EMPTY"); ok {
		t.Fatalf("empty values must not be reported as set: %q", v)
	}
	if files := e.Files(); len(files) != 1 || files[0] != p {
		t.Fatalf("Files() = %v", files)
	}
}

func TestEnvironmentWinsOverFile(t *testing.T) {
	p := write(t, "NEURALWATT_API_KEY=from-file\n")
	t.Setenv("NEURALWATT_API_KEY", "from-env")

	e := Load(p)
	if got := e.Get("NEURALWATT_API_KEY"); got != "from-env" {
		t.Fatalf("Get = %q, want the environment value", got)
	}
	if got := e.Get("UNSET_KEY"); got != "" {
		t.Fatalf("unset key = %q", got)
	}
}

func TestGetAcceptsFallbackKeys(t *testing.T) {
	p := write(t, "NEURALWATT_API_KEY=only-this\n")
	e := Load(p)
	if got := e.Get("MINLAERING_LLM_KEY", "NEURALWATT_API_KEY"); got != "only-this" {
		t.Fatalf("fallback lookup = %q", got)
	}
}

func TestMissingFileIsIgnored(t *testing.T) {
	e := Load(filepath.Join(t.TempDir(), "nope.env"))
	if len(e.Files()) != 0 {
		t.Fatalf("Files() = %v", e.Files())
	}
	if got := e.Get("ANYTHING"); got != "" {
		t.Fatalf("Get on empty env = %q", got)
	}
}
