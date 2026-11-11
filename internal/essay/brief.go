// Package essay turns a freetext task into a brief for a language model and
// validates what comes back. Nothing is hardcoded: the prompt is built from the
// task text the API already returned to the browser.
package essay

import (
	"html"
	"regexp"
	"strconv"
	"strings"

	"minlaering-auto/internal/model"
)

// Brief is everything a generator needs to write one answer.
type Brief struct {
	Exercise  string   // exercise ("Assignment 2: ...") title
	Context   []string // plain-text instructions from the section ancestry
	Question  string   // the question section prompt
	Task      string   // the task's own instruction/content
	Kind      string   // "debate post", "short analysis", "short text"
	Language  string   // "English"
	Level     string   // e.g. "A"
	WordMin   int      // 0 when the task has no word requirement
	WordMax   int
	Appeals   []string // appeal forms named by the assignment
	Checklist []string // explicit requirements extracted from the prompt
}

var (
	tagRe     = regexp.MustCompile(`<[^>]*>`)
	markupRe  = regexp.MustCompile(`\((?:/)?english\)`)
	glossRe   = regexp.MustCompile(`\[([^\[\]/]+)/[^\[\]]*\]`)
	spaceRe   = regexp.MustCompile(`\s+`)
	rangeRe   = regexp.MustCompile(`(?i)(\d{2,4})\s*(?:-|–|—|to|til)\s*(\d{2,4})\s*(?:ord|words)`)
	appealSet = map[string][]string{
		"logos":  {"logos", "logisk"},
		"ethos":  {"ethos", "etos", "troværdig"},
		"pathos": {"pathos", "patos", "følelser"},
	}
)

// BuildBrief assembles the brief for a freetext task. path holds the sections
// from the exercise down to (and including) the section that owns the task.
func BuildBrief(exercise model.Section, path []model.Section, task model.Task) Brief {
	b := Brief{
		Exercise: plain(exercise.Title),
		Language: "English",
		Level:    "A",
	}

	for _, s := range path {
		if txt := plain(s.Content); txt != "" {
			b.Context = append(b.Context, txt)
		}
		if txt := plainPtr(s.Instruction); txt != "" {
			b.Context = append(b.Context, txt)
		}
		if t := plain(s.Title); t != "" {
			b.Context = append(b.Context, t)
		}
	}
	b.Task = strings.TrimSpace(plainPtr(task.Instruction) + "\n" + plainPtr(task.Content))
	b.Question = plain(lastSection(path))

	all := strings.Join(append(append([]string{}, b.Context...), b.Question, b.Task), " ")
	b.WordMin, b.WordMax = wordRange(all, task.Conf.WordCounter)
	b.Appeals = appealsIn(all)
	b.Kind = kindOf(all, task)
	b.Checklist = checklist(all)
	return b
}

// Label is a one-line human description used by -list output.
func (b Brief) Label() string {
	if b.WordMin == 0 {
		return b.Kind + " (LLM, free length)"
	}
	return b.Kind + " (LLM, " + strconv.Itoa(b.WordMin) + "-" + strconv.Itoa(b.WordMax) + " words)"
}

func lastSection(path []model.Section) string {
	if len(path) == 0 {
		return ""
	}
	s := path[len(path)-1]
	if txt := plain(s.Content); txt != "" {
		return txt
	}
	return plainPtr(s.Instruction)
}

func plainPtr(s *string) string {
	if s == nil {
		return ""
	}
	return plain(*s)
}

// plain converts Minlæring HTML into readable prose. Glossary links look like
// "[adjektiver/tillægsord/1]" and are reduced to their English term.
func plain(s string) string {
	if s == "" {
		return ""
	}
	s = markupRe.ReplaceAllString(s, " ")
	s = tagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = glossRe.ReplaceAllString(s, "$1")
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}

func wordRange(text string, required bool) (int, int) {
	if m := rangeRe.FindStringSubmatch(text); m != nil {
		min, err1 := strconv.Atoi(m[1])
		max, err2 := strconv.Atoi(m[2])
		if err1 == nil && err2 == nil && min > 0 && max >= min {
			return min, max
		}
	}
	if required {
		return 100, 150
	}
	return 0, 0
}

func appealsIn(text string) []string {
	lower := strings.ToLower(text)
	var out []string
	for name, needles := range appealSet {
		for _, n := range needles {
			if strings.Contains(lower, n) {
				out = append(out, name)
				break
			}
		}
	}
	return out
}

func kindOf(text string, task model.Task) string {
	lower := strings.ToLower(text)
	switch {
	case task.Conf.WordCounter, strings.Contains(lower, "debatindlæg"), strings.Contains(lower, "debate"):
		return "debate post"
	case strings.Contains(lower, "passive"):
		return "short analysis"
	case strings.Contains(lower, "analyse"), strings.Contains(lower, "analysis"):
		return "short analysis"
	default:
		return "short text"
	}
}

// checklist pulls the concrete "you must ..." requirements out of the prompt so
// the model cannot silently drop them.
func checklist(text string) []string {
	var out []string
	lower := strings.ToLower(text)

	if strings.Contains(lower, "adjektiv") {
		out = append(out, "use at least two different adjectives")
	}
	if strings.Contains(lower, "adverbi") || strings.Contains(lower, "biord") {
		out = append(out, "use at least two different adverbs")
	}
	if strings.Contains(lower, "klar holdning") || strings.Contains(lower, "argumenterer enten for eller imod") {
		out = append(out, "state and defend one clear opinion")
	}
	if strings.Contains(lower, "appelformer") || len(appealsIn(text)) > 0 {
		out = append(out, "use appeal forms deliberately and, if asked, comment briefly on your choice")
	}
	if strings.Contains(lower, "passive") {
		out = append(out, "quote the passive constructions you discuss and name the verb forms")
	}
	return out
}
