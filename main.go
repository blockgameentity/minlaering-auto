// Command minlaering-auto completes a Minlæring assignment (opgavesæt) through
// the student API, authenticating with a session captured in a .har archive.
//
//	minlaering-auto \
//	  -har "app.minlaering.dk_Archive [26-11-11 21-18-17].har" \
//	  "https://app.minlaering.dk/opgavesaet/208?homeworkId=430896"
//
// Every graded task type leaks its solution in the JSON the browser receives, so
// no OCR or language model is involved there: the CLI reads the correct options
// out of conf.is_correct, the "-correct" markers and the {answer~original}
// groups, posts them to /tasks/{id}/answer and re-reads the assignment to verify.
//
// Free-text tasks (essays) have no machine-checkable answer, so their text is
// generated per task by an LLM from the assignment prompt itself (or supplied
// with -essay/-essay-file). Nothing is hardcoded.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"minlaering-auto/internal/api"
	"minlaering-auto/internal/dotenv"
	"minlaering-auto/internal/essay"
	"minlaering-auto/internal/har"
	"minlaering-auto/internal/model"
	"minlaering-auto/internal/nonce"
	"minlaering-auto/internal/solve"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type config struct {
	harPath    string
	url        string
	assignment int
	homework   int

	essay     string
	essayFile string

	envFile  string
	llmURL   string
	llmModel string
	llmKey   string
	llmTemp  float64

	timeSpent int
	delay     time.Duration
	passes    int

	dryRun   bool
	force    bool
	types    string
	list     bool
	all      bool
	complete bool
	jsonOut  bool
	verbose  bool
}

func run() error {
	var cfg config
	flag.StringVar(&cfg.harPath, "har", "", "HAR archive or directory containing .har files (default: current directory)")
	flag.StringVar(&cfg.url, "url", "", "assignment URL, e.g. https://app.minlaering.dk/opgavesaet/208?homeworkId=430896")
	flag.IntVar(&cfg.assignment, "assignment", 0, "assignment (opgavesæt) id, overrides -url")
	flag.IntVar(&cfg.homework, "homework", 0, "homework id, overrides -url")
	flag.StringVar(&cfg.essay, "essay", "", "use this text for every freetext task instead of generating one")
	flag.StringVar(&cfg.essayFile, "essay-file", "", "read that text from a file")
	flag.StringVar(&cfg.envFile, "env-file", ".env", "file with API keys (NEURALWATT_API_KEY=...)")
	flag.StringVar(&cfg.llmURL, "llm-url", "", "OpenAI-compatible base URL for essay generation (default "+essay.DefaultBaseURL+")")
	flag.StringVar(&cfg.llmModel, "llm-model", "", "model id for essay generation (default "+essay.DefaultModel+")")
	flag.StringVar(&cfg.llmKey, "llm-key", "", "API key for essay generation (default: NEURALWATT_API_KEY from the env file)")
	flag.Float64Var(&cfg.llmTemp, "llm-temperature", 0, "sampling temperature for essay generation (default 0.8, or MINLAERING_LLM_TEMPERATURE)")
	flag.IntVar(&cfg.timeSpent, "time-spent", 30, "time_spent (seconds) reported with each answer")
	flag.DurationVar(&cfg.delay, "delay", 400*time.Millisecond, "pause between submissions")
	flag.IntVar(&cfg.passes, "passes", 3, "verify-and-retry passes over unfinished tasks")
	flag.BoolVar(&cfg.dryRun, "dry-run", false, "show the answers without submitting anything")
	flag.BoolVar(&cfg.force, "force", false, "re-submit tasks that are already done")
	flag.StringVar(&cfg.types, "types", "", "only handle these task types, e.g. freetext or multiplechoice,dropdown")
	flag.BoolVar(&cfg.list, "list", false, "list tasks with status and the answer that would be sent")
	flag.BoolVar(&cfg.all, "all", false, "complete every assignment attached to the homework, not just one")
	flag.BoolVar(&cfg.complete, "complete-homework", false, "mark the homework complete once every task is done")
	flag.BoolVar(&cfg.jsonOut, "json", false, "print the final report as JSON")
	flag.BoolVar(&cfg.verbose, "v", false, "verbose output")
	flag.Parse()

	if args := flag.Args(); len(args) > 0 && cfg.url == "" {
		cfg.url = args[0]
	}

	return runConfig(cfg)
}

func runConfig(cfg config) error {
	creds, err := har.Load(cfg.harPath)
	if err != nil {
		return fmt.Errorf("load HAR credentials: %w", err)
	}

	urlAssignment, urlHomework, err := api.ResolveURL(cfg.url)
	if err != nil {
		return fmt.Errorf("parse assignment URL: %w", err)
	}
	assignmentID := firstNonZero(cfg.assignment, urlAssignment, creds.AssignmentID)
	homeworkID := firstNonZero(cfg.homework, urlHomework, creds.HomeworkID)

	if assignmentID == 0 && !cfg.all {
		return errors.New("no assignment id: pass a URL such as " +
			"https://app.minlaering.dk/opgavesaet/208?homeworkId=430896, or -assignment/-homework, or -all")
	}
	if cfg.all && homeworkID == 0 {
		return errors.New("-all needs a homework id (-homework or ?homeworkId= in the URL)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	env := dotenv.Load(cfg.envFile)
	if cfg.verbose && !cfg.jsonOut {
		if files := env.Files(); len(files) > 0 {
			fmt.Fprintf(os.Stderr, "note: loaded %s\n", strings.Join(files, ", "))
		}
	}

	a := &app{
		cfg:    cfg,
		client: api.New(creds),
		cache:  map[int]string{},
	}
	a.gen, a.genErr = buildGenerator(cfg, env)
	if a.genErr != nil && cfg.verbose && !cfg.jsonOut {
		fmt.Fprintf(os.Stderr, "note: %v\n", a.genErr)
	}

	if !cfg.jsonOut {
		fmt.Printf("Minlæring CLI — authenticated as %s\n", creds)
		if homeworkID > 0 {
			fmt.Printf("Homework %d\n", homeworkID)
		}
		fmt.Println()
	}

	ids := []int{assignmentID}
	if cfg.all {
		ids, err = homeworkAssignments(ctx, a.client, homeworkID)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return fmt.Errorf("homework %d has no assignments", homeworkID)
		}
	}

	reports := make([]report, 0, len(ids))
	for _, id := range ids {
		r, err := a.process(ctx, id, homeworkID)
		if err != nil {
			return err
		}
		reports = append(reports, r)
	}

	if cfg.list || cfg.dryRun {
		return nil
	}

	if err := emit(reports, cfg); err != nil {
		return fmt.Errorf("write report: %w", err)
	}

	var open int
	for _, r := range reports {
		open += r.Total - r.Done
	}
	if open > 0 {
		return fmt.Errorf("%d task(s) still not completed", open)
	}

	if cfg.complete && homeworkID > 0 {
		out := map[string]any{}
		if err := a.client.Post(ctx, fmt.Sprintf("/homework/%d/complete", homeworkID), nil, &out); err != nil {
			return fmt.Errorf("complete homework: %w", err)
		}
		if !cfg.jsonOut {
			fmt.Printf("\nHomework %d marked complete.\n", homeworkID)
		}
	}
	return nil
}

// app bundles the API client, the text generator and the per-run text cache.
type app struct {
	cfg    config
	client *api.Client
	gen    essay.Generator
	genErr error
	cache  map[int]string
}

// buildGenerator returns the text source for freetext tasks: user supplied text
// when -essay/-essay-file is given, an LLM otherwise.
func buildGenerator(cfg config, env dotenv.Env) (essay.Generator, error) {
	text := strings.TrimSpace(cfg.essay)
	if cfg.essayFile != "" {
		raw, err := os.ReadFile(cfg.essayFile)
		if err != nil {
			return nil, fmt.Errorf("read essay file: %w", err)
		}
		text = strings.TrimSpace(string(raw))
	}
	if text != "" {
		return essay.Static{Text: text}, nil
	}

	llm, err := essay.NewLLM(
		firstNonEmpty(cfg.llmURL, env.Get("MINLAERING_LLM_BASE_URL", "NEURALWATT_BASE_URL", "OPENAI_BASE_URL", "LLM_BASE_URL")),
		firstNonEmpty(cfg.llmModel, env.Get("MINLAERING_LLM_MODEL", "NEURALWATT_MODEL", "OPENAI_MODEL", "LLM_MODEL")),
		firstNonEmpty(cfg.llmKey, env.Get("MINLAERING_LLM_KEY", "NEURALWATT_API_KEY", "OPENAI_API_KEY", "OPENROUTER_API_KEY", "GROQ_API_KEY", "LLM_API_KEY")),
	)
	if err != nil {
		return nil, err
	}
	if temp := firstNonZeroFloat(cfg.llmTemp, parseFloat(env.Get("MINLAERING_LLM_TEMPERATURE"))); temp > 0 {
		llm.Temp = temp
	}
	return llm, nil
}

// homeworkAssignments returns the assignment ids attached to a homework.
func homeworkAssignments(ctx context.Context, client *api.Client, homeworkID int) ([]int, error) {
	var hw struct {
		Parents []struct {
			ID int `json:"id"`
		} `json:"parents"`
	}
	if err := client.Get(ctx, fmt.Sprintf("/homework/%d", homeworkID), &hw); err != nil {
		return nil, fmt.Errorf("fetch homework: %w", err)
	}
	ids := make([]int, 0, len(hw.Parents))
	for _, p := range hw.Parents {
		if p.ID != 0 {
			ids = append(ids, p.ID)
		}
	}
	return ids, nil
}

func (a *app) process(ctx context.Context, assignmentID, homeworkID int) (report, error) {
	assignment, err := a.client.Assignment(ctx, assignmentID, homeworkID)
	if err != nil {
		return report{}, fmt.Errorf("fetch assignment %d: %w", assignmentID, err)
	}

	plans := filterTypes(collect(assignment.Sections, nil, nil), a.cfg.types)
	if !a.cfg.jsonOut {
		fmt.Printf("Assignment %d — %s (%s-level)\n", assignment.ID, assignment.Title, assignment.Level)
		printSectionLine(assignment.Sections)
		fmt.Println()
	}

	if a.cfg.list {
		for _, p := range plans {
			fmt.Printf("%-7d %-14s %-12s %s\n", p.task.ID, p.task.Type.MachineName,
				statusLabel(p.task), a.describe(p))
		}
		return report{}, nil
	}

	if a.cfg.dryRun {
		for _, p := range filterPending(plans, a.cfg.force) {
			if err := a.showPayload(ctx, p); err != nil {
				fmt.Fprintf(os.Stderr, "  task %d (%s): %v\n", p.task.ID, p.task.Type.MachineName, err)
			}
		}
		return report{}, nil
	}

	for pass := 1; pass <= a.cfg.passes; pass++ {
		pending := filterPending(plans, a.cfg.force && pass == 1)
		if len(pending) == 0 {
			break
		}
		if !a.cfg.jsonOut {
			fmt.Printf("pass %d/%d: %d task(s) to answer\n", pass, a.cfg.passes, len(pending))
		}

		for _, p := range pending {
			if err := a.answerTask(ctx, p); err != nil {
				fmt.Fprintf(os.Stderr, "  task %d (%s): %v\n", p.task.ID, p.task.Type.MachineName, err)
			}
			if err := sleepCtx(ctx, a.cfg.delay); err != nil {
				return report{}, err
			}
		}

		// Re-read the assignment so the next pass sees the real statuses.
		assignment, err = a.client.Assignment(ctx, assignmentID, homeworkID)
		if err != nil {
			return report{}, fmt.Errorf("re-fetch assignment %d: %w", assignmentID, err)
		}
		plans = collect(assignment.Sections, nil, nil)
	}

	return buildReport(assignmentID, homeworkID, assignment.Title, plans), nil
}

// plan pairs a task with its section ancestry; both the exercise and the owning
// section are needed to build the answer URL, the ancestors to build the prompt.
type plan struct {
	exercise model.Section
	section  model.Section
	task     model.Task
	path     []model.Section
}

func collect(sections []model.Section, exercise *model.Section, path []model.Section) []plan {
	var out []plan
	for _, s := range sections {
		out = append(out, collectFrom(s, exercise, path)...)
	}
	return out
}

func collectFrom(sec model.Section, exercise *model.Section, path []model.Section) []plan {
	var out []plan
	ex := exercise
	if sec.Type.MachineName == "exercise" {
		ex = &sec
	}
	cur := append(append([]model.Section{}, path...), sec)
	if len(sec.Tasks) > 0 {
		owner := sec
		if ex != nil {
			owner = *ex
		}
		for _, t := range sec.Tasks {
			out = append(out, plan{exercise: owner, section: sec, task: t, path: cur})
		}
	}
	for _, child := range sec.Sections {
		out = append(out, collectFrom(child, ex, cur)...)
	}
	return out
}

func filterPending(plans []plan, includeDone bool) []plan {
	var out []plan
	for _, p := range plans {
		if includeDone || !p.task.IsDone() {
			out = append(out, p)
		}
	}
	return out
}

// filterTypes keeps only the task types the user asked for (-types).
func filterTypes(plans []plan, types string) []plan {
	if strings.TrimSpace(types) == "" {
		return plans
	}
	want := map[string]bool{}
	for _, t := range strings.Split(types, ",") {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			want[t] = true
		}
	}
	out := make([]plan, 0, len(plans))
	for _, p := range plans {
		if want[strings.ToLower(p.task.Type.MachineName)] {
			out = append(out, p)
		}
	}
	return out
}

func isFreeText(t model.Task) bool {
	return t.Type.MachineName == "freetext" || t.Type.MachineName == "readtext"
}

// textFor returns the essay text of a freetext task, generating it once per run.
func (a *app) textFor(ctx context.Context, p plan) (string, error) {
	if !isFreeText(p.task) {
		return "", nil
	}
	if txt, ok := a.cache[p.task.ID]; ok {
		return txt, nil
	}
	if a.genErr != nil {
		return "", a.genErr
	}
	if a.gen == nil {
		return "", errors.New("no essay generator configured")
	}

	brief := essay.BuildBrief(p.exercise, p.path, p.task)
	txt, err := a.gen.Generate(ctx, brief)
	if err != nil {
		return "", fmt.Errorf("%s: %w", brief.Label(), err)
	}
	if _, isStatic := a.gen.(essay.Static); isStatic {
		if fitted, ferr := essay.Fit(txt, brief.WordMin, brief.WordMax); ferr != nil {
			fmt.Fprintf(os.Stderr, "  warning: task %d: %v (submitting as given)\n", p.task.ID, ferr)
		} else {
			txt = fitted
		}
	}
	if !a.cfg.jsonOut {
		fmt.Printf("  task %d %-14s generated %d words (%s)\n", p.task.ID, p.task.Type.MachineName, essay.Words(txt), brief.Label())
	}
	a.cache[p.task.ID] = txt
	return txt, nil
}

func (a *app) describe(p plan) string {
	if !isFreeText(p.task) {
		return solve.Describe(p.task, "")
	}
	if txt, ok := a.cache[p.task.ID]; ok {
		return fmt.Sprintf("%d words: %s", essay.Words(txt), truncate(txt, 90))
	}
	if a.genErr != nil {
		return "needs -essay/-essay-file or an LLM (" + a.genErr.Error() + ")"
	}
	brief := essay.BuildBrief(p.exercise, p.path, p.task)
	return brief.Label()
}

// answerTask posts the best candidate answer and walks the fallback candidates
// when the API grades the answer as wrong.
func (a *app) answerTask(ctx context.Context, p plan) error {
	text, err := a.textFor(ctx, p)
	if err != nil {
		return err
	}
	attempts, err := solve.Build(p.task, text)
	if err != nil {
		return fmt.Errorf("build answer: %w", err)
	}

	for i, att := range attempts {
		body := a.payload(att)
		res, err := a.client.Submit(ctx, p.exercise.SectionRelationID, p.section.SectionRelationID, p.task.ID, body)
		if err != nil {
			return fmt.Errorf("submit answer: %w", err)
		}
		status := res.Status.MachineName
		switch {
		case status == "correct" || (status == "ungraded" && res.IsCompleted):
			fmt.Printf("  task %d %-14s -> %s (%.2f pts)\n",
				p.task.ID, p.task.Type.MachineName, res.Status.Title, res.Points)
			return nil
		case a.cfg.verbose:
			fmt.Printf("  task %d %-14s attempt %d -> %s (%.2f pts)\n",
				p.task.ID, p.task.Type.MachineName, i+1, res.Status.Title, res.Points)
		}
	}
	return nil
}

func (a *app) payload(att solve.Attempt) map[string]any {
	body := make(map[string]any, len(att.Body)+4)
	for k, v := range att.Body {
		body[k] = v
	}
	body["nonce"] = nonce.New()
	body["time_spent"] = a.cfg.timeSpent
	if a.cfg.homework > 0 {
		body["homework_id"] = a.cfg.homework
	}
	return body
}

func (a *app) showPayload(ctx context.Context, p plan) error {
	text, err := a.textFor(ctx, p)
	if err != nil {
		return err
	}
	attempts, err := solve.Build(p.task, text)
	if err != nil {
		return fmt.Errorf("build answer: %w", err)
	}
	enc, err := json.Marshal(a.payload(attempts[0]))
	if err != nil {
		return fmt.Errorf("encode payload: %w", err)
	}
	fmt.Printf("  task %d (%s) would POST %s\n", p.task.ID, p.task.Type.MachineName, enc)
	return nil
}

// sleepCtx sleeps between submissions but returns early on Ctrl-C.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func statusLabel(t model.Task) string {
	if t.Answer == nil {
		return "unanswered"
	}
	return t.Answer.Status.MachineName
}

func printSectionLine(sections []model.Section) {
	for _, s := range sections {
		done, total := countSection(s)
		fmt.Printf("  %-62s %2d/%-2d  %5.1f%%\n", truncate(s.Title, 62), done, total, pct(done, total))
	}
}

func countSection(s model.Section) (done, total int) {
	for _, t := range s.Tasks {
		total++
		if t.IsDone() {
			done++
		}
	}
	for _, c := range s.Sections {
		d, t := countSection(c)
		done += d
		total += t
	}
	return done, total
}

type report struct {
	AssignmentID int        `json:"assignment_id"`
	HomeworkID   int        `json:"homework_id"`
	Title        string     `json:"title"`
	Done         int        `json:"done"`
	Correct      int        `json:"correct"`
	Total        int        `json:"total"`
	Points       float64    `json:"points"`
	Complete     bool       `json:"complete"`
	Sections     []secStats `json:"sections"`
	Tasks        []taskStat `json:"tasks"`
}

type secStats struct {
	Title    string  `json:"title"`
	Done     int     `json:"done"`
	Correct  int     `json:"correct"`
	Total    int     `json:"total"`
	Progress float64 `json:"progress"`
	Points   float64 `json:"points"`
}

type taskStat struct {
	ID     int     `json:"id"`
	Type   string  `json:"type"`
	Status string  `json:"status"`
	Points float64 `json:"points"`
}

func buildReport(assignmentID, homeworkID int, title string, plans []plan) report {
	r := report{AssignmentID: assignmentID, HomeworkID: homeworkID, Title: title, Total: len(plans)}
	seen := map[int]bool{}
	for _, p := range plans {
		r.Tasks = append(r.Tasks, taskStat{
			ID:     p.task.ID,
			Type:   p.task.Type.MachineName,
			Status: statusLabel(p.task),
			Points: pointsOf(p.task),
		})
		if p.task.IsDone() {
			r.Done++
		}
		if p.task.IsCorrect() {
			r.Correct++
		}
		r.Points += pointsOf(p.task)
		if !seen[p.exercise.ID] {
			seen[p.exercise.ID] = true
			done, total := countSection(p.exercise)
			correct := 0
			for _, t := range flatten(p.exercise) {
				if t.IsCorrect() {
					correct++
				}
			}
			r.Sections = append(r.Sections, secStats{
				Title:    p.exercise.Title,
				Done:     done,
				Correct:  correct,
				Total:    total,
				Progress: float64(p.exercise.UserProgress.Progress),
				Points:   float64(p.exercise.UserProgress.Points),
			})
		}
	}
	r.Points = round2(r.Points)
	for i := range r.Sections {
		r.Sections[i].Points = round2(r.Sections[i].Points)
	}
	r.Complete = r.Total > 0 && r.Done == r.Total
	return r
}

func flatten(s model.Section) []model.Task {
	out := append([]model.Task{}, s.Tasks...)
	for _, c := range s.Sections {
		out = append(out, flatten(c)...)
	}
	return out
}

func emit(reports []report, cfg config) error {
	if cfg.jsonOut {
		var v any = reports
		if len(reports) == 1 {
			v = reports[0]
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}

	for _, r := range reports {
		for _, s := range r.Sections {
			fmt.Printf("%-58s %2d/%-2d %5.1f%%  %6.2f pts\n",
				truncate(s.Title, 58), s.Done, s.Total, s.Progress, s.Points)
		}
		fmt.Printf("%-58s %2d/%-2d\n", "tasks completed", r.Done, r.Total)
		switch {
		case r.Complete:
			fmt.Printf("Assignment %d complete: %d task(s) done, %d graded correct, %.2f pts.\n",
				r.AssignmentID, r.Done, r.Correct, r.Points)
		default:
			fmt.Printf("Assignment %d: %d task(s) outstanding.\n", r.AssignmentID, r.Total-r.Done)
		}
		fmt.Println()
	}
	return nil
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func pct(done, total int) float64 {
	if total == 0 {
		return 100
	}
	return float64(done) / float64(total) * 100
}

func pointsOf(t model.Task) float64 {
	if t.Answer == nil {
		return 0
	}
	return t.Answer.Points
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func firstNonZero(vals ...int) int {
	for _, v := range vals {
		if v != 0 {
			return v
		}
	}
	return 0
}

func firstNonZeroFloat(vals ...float64) float64 {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

func parseFloat(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return v
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
