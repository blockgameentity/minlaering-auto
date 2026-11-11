package essay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Generator produces the text for one freetext task.
type Generator interface {
	Generate(ctx context.Context, b Brief) (string, error)
}

// Static returns a text the user supplied with -essay / -essay-file.
type Static struct{ Text string }

// Generate implements Generator.
func (s Static) Generate(context.Context, Brief) (string, error) {
	if strings.TrimSpace(s.Text) == "" {
		return "", fmt.Errorf("empty essay text")
	}
	return s.Text, nil
}

// LLM talks to any OpenAI-compatible chat/completions endpoint: Neuralwatt
// (the default), OpenAI, OpenRouter, Groq, vLLM, LM Studio, Ollama (/v1) and
// similar gateways.
type LLM struct {
	BaseURL string
	Model   string
	APIKey  string
	Temp    float64
	Tokens  int
	Retries int
	HTTP    *http.Client
}

// Provider defaults: Neuralwatt serves MiMo-V2.6-Pro over an OpenAI-compatible
// API, which makes it a sensible out-of-the-box choice for essay generation.
const (
	DefaultBaseURL = "https://api.neuralwatt.com/v1"
	DefaultModel   = "mimo-v2.6-pro"
)

// hostedProviders map a base URL fragment to the environment variable that holds
// its API key, so a missing key fails fast with an actionable message.
var hostedProviders = []struct {
	host string
	env  string
}{
	{"api.neuralwatt.com", "NEURALWATT_API_KEY"},
	{"api.openai.com", "OPENAI_API_KEY"},
	{"openrouter.ai", "OPENROUTER_API_KEY"},
	{"api.groq.com", "GROQ_API_KEY"},
}

// NewLLM validates the configuration and applies defaults.
func NewLLM(baseURL, model, apiKey string) (*LLM, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = DefaultModel
	}
	if apiKey == "" {
		for _, p := range hostedProviders {
			if strings.Contains(baseURL, p.host) {
				return nil, fmt.Errorf("%s needs an API key: put %s=... in .env, export it, or pass -llm-key", p.host, p.env)
			}
		}
	}
	return &LLM{
		BaseURL: baseURL,
		Model:   model,
		APIKey:  apiKey,
		Temp:    0.8,
		Tokens:  4096, // reasoning models spend most of this before writing
		Retries: 2,
		HTTP:    &http.Client{Timeout: 120 * time.Second},
	}, nil
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// maxTokensCap bounds the automatic budget growth for reasoning models.
const maxTokensCap = 32768

// Generate asks the model for the answer and validates the word count, asking
// again with explicit feedback when the answer misses the range.
func (l *LLM) Generate(ctx context.Context, b Brief) (string, error) {
	attempts := l.Retries + 1
	var lastErr error
	feedback := ""

	for i := 0; i < attempts; i++ {
		res, err := l.complete(ctx, b, feedback, "")
		if err != nil {
			return "", err
		}
		text := Clean(res.text)
		if text == "" {
			if res.finishReason == "length" {
				// A reasoning model burned the whole budget before writing.
				lastErr = fmt.Errorf("no answer text: the %d token budget was used up before the model started writing (raise -llm-max-tokens)", l.Tokens)
				if l.Tokens < maxTokensCap {
					l.Tokens *= 2
					if l.Tokens > maxTokensCap {
						l.Tokens = maxTokensCap
					}
				}
				feedback = "Your previous attempt ran out of tokens before producing an answer. Write the answer first, then stop."
				continue
			}
			lastErr = fmt.Errorf("model returned an empty answer")
			feedback = "Your previous answer was empty. Return only the requested text."
			continue
		}
		fitted, err := Fit(text, b.WordMin, b.WordMax)
		if err == nil {
			return fitted, nil
		}
		lastErr = err
		feedback = fmt.Sprintf("Your previous answer was %d words. It must be between %d and %d words. Rewrite it so it fits exactly.",
			Words(text), b.WordMin, b.WordMax)
	}
	return "", fmt.Errorf("generate answer: %w", lastErr)
}

type generation struct {
	text         string
	finishReason string
}

func (l *LLM) complete(ctx context.Context, b Brief, feedback, _ string) (generation, error) {
	body, err := json.Marshal(chatRequest{
		Model: l.Model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt(b, feedback)},
		},
		Temperature: l.Temp,
		MaxTokens:   l.Tokens,
	})
	if err != nil {
		return generation{}, fmt.Errorf("encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return generation{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if l.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+l.APIKey)
	}

	resp, err := l.HTTP.Do(req)
	if err != nil {
		return generation{}, fmt.Errorf("call llm: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return generation{}, fmt.Errorf("read llm response: %w", err)
	}

	var out chatResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return generation{}, fmt.Errorf("decode llm response (HTTP %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := strings.TrimSpace(string(raw))
		if out.Error != nil && out.Error.Message != "" {
			msg = out.Error.Message
		}
		return generation{}, fmt.Errorf("llm HTTP %d: %s", resp.StatusCode, truncate(msg, 300))
	}
	if out.Error != nil && out.Error.Message != "" {
		return generation{}, fmt.Errorf("llm: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return generation{}, fmt.Errorf("llm returned no choices")
	}
	c := out.Choices[0]
	return generation{text: c.Message.Content, finishReason: c.FinishReason}, nil
}

const systemPrompt = `You are an upper-secondary student in Denmark writing an English exam answer.
You write natural, idiomatic English at CEFR B2/C1 with British spelling.
You answer only with the requested text: no title, no headings, no bullet points,
no markdown, no explanations and no quotation marks around the text.
Never use em dashes (—) or en dashes (–) anywhere; use commas, colons, full stops
or the word "and" instead.`

func userPrompt(b Brief, feedback string) string {
	var sb strings.Builder
	sb.WriteString("Write the answer to the assignment below.\n\n")

	if b.Exercise != "" {
		sb.WriteString("ASSIGNMENT: " + b.Exercise + "\n\n")
	}
	if len(b.Context) > 0 {
		sb.WriteString("ASSIGNMENT INSTRUCTIONS\n")
		for _, c := range b.Context {
			sb.WriteString("- " + c + "\n")
		}
		sb.WriteString("\n")
	}
	if b.Question != "" && b.Question != b.Exercise {
		sb.WriteString("QUESTION\n" + b.Question + "\n\n")
	}
	if b.Task != "" {
		sb.WriteString("TASK\n" + b.Task + "\n\n")
	}

	sb.WriteString("REQUIREMENTS\n")
	sb.WriteString("- Genre: " + b.Kind + "\n")
	sb.WriteString("- Language: " + b.Language + "\n")
	if b.Level != "" {
		sb.WriteString("- Level: " + b.Level + "\n")
	}
	if b.WordMax > 0 {
		sb.WriteString(fmt.Sprintf("- Length: between %d and %d words, and never outside that range\n", b.WordMin, b.WordMax))
	}
	if len(b.Appeals) > 0 {
		sb.WriteString("- Appeal forms to use: " + strings.Join(b.Appeals, ", ") + "\n")
	}
	for _, c := range b.Checklist {
		sb.WriteString("- " + c + "\n")
	}
	sb.WriteString("- One flowing text, no headings, no lists, no markdown\n")
	sb.WriteString("- No em dashes or en dashes: use commas, colons, full stops or \"and\"\n")

	if feedback != "" {
		sb.WriteString("\nCORRECTION\n" + feedback + "\n")
	}
	sb.WriteString("\nReturn only the text of the answer.")
	return sb.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
