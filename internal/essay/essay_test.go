package essay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"minlaering-auto/internal/model"
)

// Fixtures mirror the shape of GET /schools/{school}/assignments/208?homeworkId=430896.
func exercise(title string) model.Section {
	return model.Section{ID: 81931, SectionRelationID: 82051, Title: title, Type: model.TaskType{MachineName: "exercise"}}
}

func questionSection(content string) model.Section {
	return model.Section{ID: 81932, SectionRelationID: 82052, Content: content, Type: model.TaskType{MachineName: "question"}}
}

func freetext(id int, instr string, wordCounter bool) model.Task {
	return model.Task{
		ID:      id,
		Type:    model.TaskType{MachineName: "freetext"},
		Content: ptr(""),
		Conf:    model.Conf{WordCounter: wordCounter},
		Instruction: func() *string {
			if instr == "" {
				return nil
			}
			return &instr
		}(),
	}
}

func ptr(s string) *string { return &s }

func TestBuildBriefReadsTaskText(t *testing.T) {
	ex := exercise("Assignment 2: Skriv et debatindlæg på 100-150 ord om AI")
	sec := questionSection(`Du er debattør med <strong>klar holdning</strong> til kunstig intelligens (AI).
		Skriv et debatindlæg til AI Magazine på 100 til 150 ord og inddrag to forskellige
		<a class="glossaryTooltip">[adjektiver/tillægsord/1]</a> samt to
		<a class="glossaryTooltip">[adverbier/biord/1]</a>. Overvej appelformerne logos og patos.
		(english)Write in English.(/english)`)
	task := freetext(78995, "In this text, the use of logos appeals to the reader's logical reasoning.", true)

	b := BuildBrief(ex, []model.Section{ex, sec}, task)

	if b.WordMin != 100 || b.WordMax != 150 {
		t.Fatalf("word range = %d-%d, want 100-150", b.WordMin, b.WordMax)
	}
	if b.Kind != "debate post" {
		t.Fatalf("kind = %q", b.Kind)
	}
	if len(b.Appeals) == 0 {
		t.Fatal("appeal forms were not detected")
	}
	joined := strings.Join(b.Context, " ") + " " + b.Question
	for _, want := range []string{"adjektiver", "adverbier", "appelformer"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("brief lost %q from the prompt: %s", want, joined)
		}
	}
	for _, unwanted := range []string{"<strong>", "(english)", "/tillægsord/"} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("brief still contains markup %q: %s", unwanted, joined)
		}
	}
	if !hasChecklist(b.Checklist, "adjective") || !hasChecklist(b.Checklist, "adverb") {
		t.Fatalf("checklist = %v", b.Checklist)
	}
	if !strings.Contains(b.Label(), "100-150") {
		t.Fatalf("label = %q", b.Label())
	}
}

func hasChecklist(items []string, needle string) bool {
	for _, it := range items {
		if strings.Contains(it, needle) {
			return true
		}
	}
	return false
}

func TestBuildBriefFallsBackToDefaultRange(t *testing.T) {
	b := BuildBrief(exercise("Assignment 3"), []model.Section{questionSection("Ok, skriv.")}, freetext(1, "", true))
	if b.WordMin != 100 || b.WordMax != 150 {
		t.Fatalf("word range = %d-%d, want the 100-150 default", b.WordMin, b.WordMax)
	}
}

func TestClean(t *testing.T) {
	in := "```markdown\n# Debate post\n**AI** is _useful_.\n\n\n- Not a list really.\n```"
	got := Clean(in)
	for _, unwanted := range []string{"```", "#", "**", "- "} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("Clean left %q in %q", unwanted, got)
		}
	}
	if !strings.Contains(got, "AI is useful.") {
		t.Fatalf("Clean mangled the text: %q", got)
	}
}

func TestFit(t *testing.T) {
	in := "One two three. Four five six. Seven eight nine."

	if got, err := Fit(in, 3, 20); err != nil || got != in {
		t.Fatalf("in-range text changed: %q %v", got, err)
	}
	if got, err := Fit(in, 1, 6); err != nil || Words(got) != 6 {
		t.Fatalf("trim = %q (%d words) err=%v", got, Words(got), err)
	}
	if _, err := Fit("too short", 10, 20); err == nil {
		t.Fatal("expected a word-range error for short text")
	}
	if got, err := Fit("anything", 0, 0); err != nil || got != "anything" {
		t.Fatalf("unbounded range must pass through, got %q %v", got, err)
	}
}

// newLLM wires an LLM at a stub server and returns it.
func newLLM(t *testing.T, handler http.HandlerFunc, temp float64) (*LLM, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	l, err := NewLLM(srv.URL+"/v1", "stub-model", "test-key")
	if err != nil {
		t.Fatal(err)
	}
	l.Temp = temp
	return l, srv
}

func completion(content string) string {
	return fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":%s}}]}`, mustJSON(content))
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestLLMGenerate(t *testing.T) {
	var gotPath, gotAuth, gotModel string
	var gotPrompt string

	l, _ := newLLM(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		gotModel = req.Model
		gotPrompt = req.Messages[len(req.Messages)-1].Content
		_, _ = w.Write([]byte(completion("Artificial intelligence deserves both enthusiasm and caution, " +
			"and this debate post argues for a balanced view of the technology in everyday life.")))
	}, 0.66)

	out, err := l.Generate(context.Background(), Brief{
		Exercise: "Assignment 2: AI",
		Kind:     "debate post",
		Language: "English",
		WordMin:  1,
		WordMax:  45,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "balanced view") {
		t.Fatalf("unexpected text: %q", out)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("posted to %q", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("auth header = %q", gotAuth)
	}
	if gotModel != "stub-model" {
		t.Fatalf("model = %q", gotModel)
	}
	if !strings.Contains(gotPrompt, "debate post") || !strings.Contains(gotPrompt, "English") {
		t.Fatalf("prompt missing requirements: %s", gotPrompt)
	}
}

func TestLLMRetriesWhenTooShort(t *testing.T) {
	calls := 0
	l, _ := newLLM(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req chatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		prompt := req.Messages[len(req.Messages)-1].Content
		if calls == 1 {
			if strings.Contains(prompt, "CORRECTION") {
				t.Error("first call must not carry a correction")
			}
			_, _ = w.Write([]byte(completion("Too short.")))
			return
		}
		if !strings.Contains(prompt, "CORRECTION") || !strings.Contains(prompt, "between 10 and 20 words") {
			t.Errorf("retry prompt missing feedback: %s", prompt)
		}
		_, _ = w.Write([]byte(completion("One two three four five six seven eight nine ten eleven.")))
	}, 0)

	out, err := l.Generate(context.Background(), Brief{Kind: "short text", WordMin: 10, WordMax: 20})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if Words(out) != 11 {
		t.Fatalf("words = %d, text = %q", Words(out), out)
	}
}

func TestLLMGivesUp(t *testing.T) {
	l, _ := newLLM(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(completion("still short")))
	}, 0)
	l.Retries = 1

	_, err := l.Generate(context.Background(), Brief{Kind: "short text", WordMin: 100, WordMax: 150})
	if err == nil || !strings.Contains(err.Error(), "word") {
		t.Fatalf("expected a word-range failure, got %v", err)
	}
}

func TestLLMHTTPError(t *testing.T) {
	l, _ := newLLM(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key","type":"auth"}}`))
	}, 0)

	_, err := l.Generate(context.Background(), Brief{Kind: "short text"})
	if err == nil || !strings.Contains(err.Error(), "invalid api key") {
		t.Fatalf("expected the provider error, got %v", err)
	}
}

func TestNewLLMDefaultsToNeuralwatt(t *testing.T) {
	llm, err := NewLLM("", "", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if llm.BaseURL != "https://api.neuralwatt.com/v1" {
		t.Fatalf("base URL = %q", llm.BaseURL)
	}
	if llm.Model != "mimo-v2.6-pro" {
		t.Fatalf("model = %q", llm.Model)
	}

	// The default provider is hosted, so it must ask for a key.
	_, err = NewLLM("", "", "")
	if err == nil || !strings.Contains(err.Error(), "NEURALWATT_API_KEY") {
		t.Fatalf("expected a NEURALWATT_API_KEY hint, got %v", err)
	}
}

func TestNewLLMValidation(t *testing.T) {
	if _, err := NewLLM("https://api.openai.com/v1", "gpt-4o-mini", ""); err == nil {
		t.Fatal("expected an error for a missing OpenAI key")
	}
	if llm, err := NewLLM("http://localhost:11434/v1", "llama3", ""); err != nil || llm.APIKey != "" {
		t.Fatalf("local endpoints must work without a key: %v", err)
	}
	if llm, err := NewLLM("https://api.neuralwatt.com/v1/", "", "sk-test"); err != nil || llm.BaseURL != "https://api.neuralwatt.com/v1" {
		t.Fatalf("trailing slash handling: %v %v", llm, err)
	}
}

func TestPromptsForbidEmDashes(t *testing.T) {
	if !strings.Contains(systemPrompt, "em dash") {
		t.Fatal("system prompt must forbid em dashes")
	}
	prompt := userPrompt(Brief{Kind: "debate post", Language: "English", WordMin: 100, WordMax: 150}, "")
	if !strings.Contains(prompt, "em dashes") {
		t.Fatalf("user prompt must repeat the em-dash rule: %s", prompt)
	}
}

func TestStatic(t *testing.T) {
	g := Static{Text: "  my own text  "}
	out, err := g.Generate(context.Background(), Brief{})
	if err != nil || out != "  my own text  " {
		t.Fatalf("Static returned %q %v", out, err)
	}
	if _, err := (Static{}).Generate(context.Background(), Brief{}); err == nil {
		t.Fatal("empty static text must fail")
	}
}
