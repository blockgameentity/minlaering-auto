package essay

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrWordRange reports output that does not satisfy the task's word count.
var ErrWordRange = errors.New("text outside the required word range")

var (
	fenceRe   = regexp.MustCompile("(?s)^\\s*```[a-zA-Z]*\\s*|\\s*```\\s*$")
	mdHeadRe  = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s+`)
	mdBullet  = regexp.MustCompile(`(?m)^\s*[-*•]\s+`)
	mdBoldIt  = regexp.MustCompile(`\*\*|__|\*|_`)
	quoteWrap = regexp.MustCompile(`^\s*["“”']|["“”']\s*$`)
)

// Clean turns a raw model response into the plain prose the answer field wants:
// no code fences, headings, bullets, markdown emphasis or wrapping quotes.
func Clean(s string) string {
	s = fenceRe.ReplaceAllString(s, "")
	s = mdHeadRe.ReplaceAllString(s, "")
	s = mdBullet.ReplaceAllString(s, "")
	s = mdBoldIt.ReplaceAllString(s, "")

	// Drop editor-style labels such as "Answer:" or "Debate post:".
	if idx := strings.Index(s, ":"); idx > 0 && idx < 40 {
		head := strings.ToLower(strings.TrimSpace(s[:idx]))
		if !strings.Contains(head, " ") || strings.HasSuffix(head, "post") || strings.HasSuffix(head, "answer") {
			s = s[idx+1:]
		}
	}
	s = quoteWrap.ReplaceAllString(s, "")

	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		if ln = strings.TrimSpace(ln); ln != "" {
			out = append(out, ln)
		}
	}
	return strings.Join(out, "\n\n")
}

// Words counts whitespace separated words.
func Words(s string) int { return len(strings.Fields(s)) }

// Fit enforces the brief's word range. Long text is trimmed sentence by
// sentence (never below the minimum); short text is returned with ErrWordRange
// so the caller can ask the model again.
func Fit(text string, min, max int) (string, error) {
	if max == 0 {
		return text, nil
	}
	words := Words(text)
	if words >= min && words <= max {
		return text, nil
	}
	if words < min {
		return text, fmt.Errorf("%w: got %d words, want %d-%d", ErrWordRange, words, min, max)
	}

	sentences := splitSentences(text)
	for len(sentences) > 1 && Words(strings.Join(sentences, " ")) > max {
		candidate := strings.Join(sentences[:len(sentences)-1], " ")
		if Words(candidate) < min {
			break
		}
		sentences = sentences[:len(sentences)-1]
	}
	trimmed := strings.Join(sentences, " ")
	if w := Words(trimmed); w > max {
		return trimmed, fmt.Errorf("%w: got %d words, want %d-%d", ErrWordRange, w, min, max)
	}
	return trimmed, nil
}

var sentenceEnd = regexp.MustCompile(`(?s)[.!?]["”]?\s+`)

func splitSentences(s string) []string {
	idx := sentenceEnd.FindAllStringIndex(s, -1)
	var out []string
	prev := 0
	for _, m := range idx {
		out = append(out, strings.TrimSpace(s[prev:m[1]]))
		prev = m[1]
	}
	if rest := strings.TrimSpace(s[prev:]); rest != "" {
		out = append(out, rest)
	}
	if len(out) == 0 {
		return []string{s}
	}
	return out
}
