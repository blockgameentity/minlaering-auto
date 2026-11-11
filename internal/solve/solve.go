// Package solve derives correct answer payloads from the task configuration
// that the API sends to the browser.
//
// Every task type leaks its solution in one of three ways:
//
//	multiplechoice / clickword : conf.options[].is_correct
//	dropdown                   : "-correct" marker in conf.content
//	insertword                 : "{correct~original}" in conf.content
//
// A payload is exactly what the web app posts to .../tasks/{id}/answer
// (the caller adds nonce, homework_id and time_spent).
package solve

import (
	"errors"
	"fmt"
	"html"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"minlaering-auto/internal/model"
)

// ErrUnsupported is returned for task types we cannot answer automatically.
var ErrUnsupported = errors.New("unsupported task type")

var (
	braceRe   = regexp.MustCompile(`\{([^{}]*)\}`)
	tagRe     = regexp.MustCompile(`<[^>]*>`)
	spaceRe   = regexp.MustCompile(`[ \t\r\n]+`)
	correctRe = regexp.MustCompile(`(?i)-correct$`)
)

// Attempt is one candidate answer payload for a task, most likely first.
type Attempt struct {
	Body map[string]any
}

// Build returns the candidate answer payloads for a task, best first.
//
// text is only used for freetext tasks (the essay to submit). Retries walk the
// candidate list, which matters for insert-word tasks that accept several
// paraphrases.
func Build(t model.Task, text string) ([]Attempt, error) {
	switch t.Type.MachineName {
	case "multiplechoice":
		return buildChoice(t)
	case "dropdown":
		return buildDropdown(t)
	case "insertword":
		return buildInsert(t)
	case "clickword":
		return buildClick(t)
	case "freetext", "readtext":
		if strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("freetext task needs text (-essay / -essay-file)")
		}
		return []Attempt{{Body: map[string]any{
			"content":    text,
			"copy_paste": false,
		}}}, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupported, t.Type.MachineName)
	}
}

// Describe renders the answer we would submit, for -list/-dry-run output.
func Describe(t model.Task, text string) string {
	switch t.Type.MachineName {
	case "multiplechoice":
		if opts, err := buildChoice(t); err == nil {
			return optionText(t, hashOf(opts[0]))
		}
	case "dropdown":
		if opts, err := buildDropdown(t); err == nil {
			var parts []string
			for _, o := range opts[0].Body["options"].([]map[string]any) {
				parts = append(parts, optionText(t, o["option"].(string)))
			}
			return strings.Join(parts, ", ")
		}
	case "clickword":
		var words []string
		for _, o := range t.Conf.Options {
			if o.Correct() {
				words = append(words, o.Option)
			}
		}
		if len(words) > 0 {
			return strings.Join(words, " ")
		}
	case "insertword":
		groups := bracedGroups(t.Conf.Content)
		var answers []string
		for _, g := range groups {
			if c := candidates(g); len(c) > 0 {
				answers = append(answers, c[0])
			}
		}
		return strings.Join(answers, " | ")
	case "freetext", "readtext":
		return fmt.Sprintf("%d word(s) of free text", len(strings.Fields(text)))
	}
	return ""
}

func buildChoice(t model.Task) ([]Attempt, error) {
	for _, o := range t.Conf.Options {
		if o.Correct() {
			return []Attempt{{Body: map[string]any{
				"options": []map[string]any{{"option": o.Hash}},
			}}}, nil
		}
	}
	return nil, fmt.Errorf("task %d (multiplechoice): no option flagged is_correct", t.ID)
}

func buildDropdown(t model.Task) ([]Attempt, error) {
	groups := groupOptions(t.Conf.Options)
	answers := make(map[int][]string, len(groups))

	marked := bracedGroups(t.Conf.Content)
	for i := range groups {
		if i < len(marked) {
			for _, part := range strings.Split(marked[i], "|") {
				part = normalise(part)
				if correctRe.MatchString(part) {
					name := normalise(correctRe.ReplaceAllString(part, ""))
					answers[i] = appendUnique(answers[i], name)
				}
			}
		}
		if len(answers[i]) == 0 {
			// Fallback: the API sometimes echoes is_correct on stored answers.
			for _, o := range groups[i] {
				if o.Correct() {
					answers[i] = appendUnique(answers[i], normalise(o.Option))
				}
			}
		}
		if len(answers[i]) == 0 {
			return nil, fmt.Errorf("task %d (dropdown): group %d has no -correct marker", t.ID, i)
		}
	}

	maxLen := 1
	for _, a := range answers {
		if len(a) > maxLen {
			maxLen = len(a)
		}
	}

	attempts := make([]Attempt, 0, maxLen)
	for k := 0; k < maxLen; k++ {
		opts := make([]map[string]any, 0, len(groups))
		for i := range groups {
			idx := k
			if idx >= len(answers[i]) {
				idx = len(answers[i]) - 1
			}
			o, ok := lookup(groups[i], answers[i][idx])
			if !ok {
				return nil, fmt.Errorf("task %d (dropdown): option %q not found in group %d", t.ID, answers[i][idx], i)
			}
			opts = append(opts, map[string]any{"option": o.Hash, "group": i})
		}
		attempts = append(attempts, Attempt{Body: map[string]any{"options": opts}})
	}
	return attempts, nil
}

func buildInsert(t model.Task) ([]Attempt, error) {
	groups := bracedGroups(t.Conf.Content)
	if len(groups) == 0 {
		return nil, fmt.Errorf("task %d (insertword): no {answer~original} group in conf.content", t.ID)
	}

	perGroup := make([][]string, len(groups))
	maxLen := 1
	for i, g := range groups {
		perGroup[i] = candidates(g)
		if len(perGroup[i]) == 0 {
			return nil, fmt.Errorf("task %d (insertword): empty answer group %d", t.ID, i)
		}
		if len(perGroup[i]) > maxLen {
			maxLen = len(perGroup[i])
		}
	}

	attempts := make([]Attempt, 0, maxLen)
	for k := 0; k < maxLen; k++ {
		opts := make([]map[string]any, 0, len(groups))
		for i := range groups {
			idx := k
			if idx >= len(perGroup[i]) {
				idx = len(perGroup[i]) - 1
			}
			opts = append(opts, map[string]any{
				"answer_text": perGroup[i][idx],
				"group":       strconv.Itoa(i),
				"copy_paste":  false,
			})
		}
		attempts = append(attempts, Attempt{Body: map[string]any{"options": opts}})
	}
	return attempts, nil
}

func buildClick(t model.Task) ([]Attempt, error) {
	correct := clickMask(t)
	if len(correct) != len(t.Conf.Options) {
		return nil, fmt.Errorf("task %d (clickword): %d click flags for %d options",
			t.ID, len(correct), len(t.Conf.Options))
	}
	opts := make([]map[string]any, 0, len(t.Conf.Options))
	for i, o := range t.Conf.Options {
		opts = append(opts, map[string]any{"option": o.Hash, "is_clicked": correct[i]})
	}
	return []Attempt{{Body: map[string]any{"options": opts}}}, nil
}

// clickMask returns one bool per option: true when the word must be clicked.
func clickMask(t model.Task) []bool {
	flagged := false
	mask := make([]bool, len(t.Conf.Options))
	for i, o := range t.Conf.Options {
		if o.IsCorrect != nil {
			flagged = true
		}
		mask[i] = o.Correct()
	}
	if flagged {
		return mask
	}

	// Fallback: reconstruct from the braces in conf.content, which mark the
	// words that belong to the passive construction.
	tokens := markedWords(t.Conf.Content)
	if len(tokens) != len(mask) {
		return mask
	}
	for i := range tokens {
		mask[i] = tokens[i].marked
	}
	return mask
}

// candidates extracts the accepted answers of one insert-word group:
// text before the first "~", split on "|".
func candidates(group string) []string {
	head := group
	if idx := strings.Index(head, "~"); idx >= 0 {
		head = head[:idx]
	}
	var out []string
	for _, alt := range strings.Split(head, "|") {
		if alt = normalise(alt); alt != "" {
			out = appendUnique(out, alt)
		}
	}
	return out
}

func bracedGroups(content string) []string {
	var out []string
	for _, m := range braceRe.FindAllStringSubmatch(content, -1) {
		out = append(out, m[1])
	}
	return out
}

func normalise(s string) string {
	s = html.UnescapeString(tagRe.ReplaceAllString(s, ""))
	s = strings.ReplaceAll(s, "\u00a0", " ")
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}

func groupOptions(opts []model.Option) map[int][]model.Option {
	out := map[int][]model.Option{}
	for _, o := range opts {
		g := int(o.Group)
		out[g] = append(out[g], o)
	}
	return out
}

func lookup(opts []model.Option, name string) (model.Option, bool) {
	for _, o := range opts {
		if normalise(o.Option) == name {
			return o, true
		}
	}
	return model.Option{}, false
}

func appendUnique(list []string, v string) []string {
	if slices.Contains(list, v) {
		return list
	}
	return append(list, v)
}

func hashOf(a Attempt) string {
	if opts, ok := a.Body["options"].([]map[string]any); ok && len(opts) > 0 {
		if h, ok := opts[0]["option"].(string); ok {
			return h
		}
	}
	return ""
}

func optionText(t model.Task, hash string) string {
	for _, o := range t.Conf.Options {
		if o.Hash == hash {
			return o.Option
		}
	}
	return hash
}

type markedWord struct {
	word   string
	marked bool
}

// markedWords tokenises text, recording which words appeared inside braces.
// The API splits words and single punctuation characters into separate tokens
// ("Mississippi" + ","), so the mask lines up with conf.options.
func markedWords(content string) []markedWord {
	var out []markedWord
	var buf strings.Builder

	runes := []rune(content)
	for i := 0; i < len(runes); i++ {
		if runes[i] == '{' {
			out = append(out, tokenize(buf.String(), false)...)
			buf.Reset()

			j := i + 1
			for j < len(runes) && runes[j] != '}' {
				j++
			}
			inner := string(runes[i+1 : j])
			if idx := strings.Index(inner, "|"); idx >= 0 {
				inner = inner[:idx]
			}
			out = append(out, tokenize(inner, true)...)
			i = j
			continue
		}
		buf.WriteRune(runes[i])
	}
	out = append(out, tokenize(buf.String(), false)...)
	return out
}

// tokenize splits a plain text segment into word and punctuation tokens.
func tokenize(s string, marked bool) []markedWord {
	var out []markedWord
	var word strings.Builder

	flush := func() {
		if word.Len() > 0 {
			out = append(out, markedWord{word: word.String(), marked: marked})
			word.Reset()
		}
	}

	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'' || r == '\u2019':
			word.WriteRune(r)
		case unicode.IsSpace(r):
			flush()
		default:
			flush()
			out = append(out, markedWord{word: string(r), marked: marked})
		}
	}
	flush()
	return out
}
