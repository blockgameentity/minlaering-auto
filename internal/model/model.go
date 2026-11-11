// Package model holds the API payload types returned by api.minlaering.dk.
package model

import (
	"encoding/json"
	"strconv"
	"strings"
)

// FlexInt tolerates both numeric and quoted numeric JSON values, because the
// API is inconsistent about group indices ("0" vs 0).
type FlexInt int

// UnmarshalJSON implements json.Unmarshaler.
func (f *FlexInt) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == "" {
		*f = 0
		return nil
	}
	s = strings.Trim(s, `"`)
	if s == "" {
		*f = 0
		return nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return err
	}
	*f = FlexInt(v)
	return nil
}

// MarshalJSON implements json.Marshaler.
func (f FlexInt) MarshalJSON() ([]byte, error) {
	return json.Marshal(int(f))
}

// FlexFloat tolerates numeric, quoted numeric and null JSON values.
type FlexFloat float64

// UnmarshalJSON implements json.Unmarshaler.
func (f *FlexFloat) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == "" || s == `""` {
		*f = 0
		return nil
	}
	s = strings.Trim(s, `"`)
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	*f = FlexFloat(v)
	return nil
}

// MarshalJSON implements json.Marshaler.
func (f FlexFloat) MarshalJSON() ([]byte, error) { return json.Marshal(float64(f)) }

// Option is one answer option of a task.
type Option struct {
	Option      string  `json:"option"`
	AnswerText  string  `json:"answer_text"`
	Group       FlexInt `json:"group"`
	Hash        string  `json:"hash"`
	IsCorrect   *bool   `json:"is_correct"`
	IsNeutral   *bool   `json:"is_neutral"`
	IsClickable *bool   `json:"is_clickable"`
}

// Correct reports whether the option is marked as correct by the API.
func (o Option) Correct() bool { return o.IsCorrect != nil && *o.IsCorrect }

// Part is one rendered part of a task (text, group, word, linebreak).
type Part struct {
	Type        string  `json:"type"`
	Content     string  `json:"content"`
	Group       FlexInt `json:"group"`
	WordIndex   *int    `json:"word_index"`
	Placeholder string  `json:"placeholder"`
}

// Group describes an insert-word input group.
type Group struct {
	Width FlexFloat `json:"width"`
}

// Conf is the per-task configuration payload. It is where the API leaks the
// correct answers (is_correct flags, "-correct" markers, {answer~original}).
type Conf struct {
	Content       string   `json:"content"`
	Options       []Option `json:"options"`
	Parts         []Part   `json:"parts"`
	Groups        []Group  `json:"groups"`
	Gradeable     bool     `json:"gradeable"`
	ValidateOrder bool     `json:"validate_order"`
	WordCounter   bool     `json:"word_counter"`
}

// Status is the grading status of an answer.
type Status struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	MachineName string `json:"machine_name"`
}

// TaskType identifies the widget used to answer a task.
type TaskType struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	MachineName string `json:"machine_name"`
	Gradeable   bool   `json:"gradeable"`
}

// AnswerConf is the stored answer configuration (echo of what was submitted).
type AnswerConf struct {
	Options    []Option `json:"options"`
	Nonce      string   `json:"nonce"`
	HomeworkID int      `json:"homework_id"`
}

// TaskAnswer is the stored/graded answer of one task.
type TaskAnswer struct {
	ID          int        `json:"id"`
	TaskID      int        `json:"task_id"`
	Status      Status     `json:"status"`
	IsCompleted bool       `json:"is_completed"`
	Points      float64    `json:"points"`
	Score       float64    `json:"score"`
	Attempts    int        `json:"attempts"`
	Conf        AnswerConf `json:"conf"`
}

// Task is a single question inside a section.
type Task struct {
	ID          int         `json:"id"`
	Content     *string     `json:"content"`
	Instruction *string     `json:"instruction"`
	Conf        Conf        `json:"conf"`
	Type        TaskType    `json:"type"`
	Answer      *TaskAnswer `json:"answer"`
}

// Status returns the machine name of the answer status ("" when unanswered).
func (t Task) Status() string {
	if t.Answer == nil {
		return ""
	}
	return t.Answer.Status.MachineName
}

// IsCorrect reports whether the task holds a fully correct answer.
func (t Task) IsCorrect() bool { return t.Status() == "correct" }

// IsDone reports whether the task no longer needs an answer. Non-gradeable
// tasks (essays, reading tasks) end in the "ungraded" state once submitted.
func (t Task) IsDone() bool {
	if t.Answer == nil || !t.Answer.IsCompleted {
		return false
	}
	switch t.Status() {
	case "correct", "ungraded":
		return true
	default:
		return false
	}
}

// Section is an exercise or question group; sections nest arbitrarily.
type Section struct {
	ID                int       `json:"id"`
	SectionRelationID int       `json:"section_relation_id"`
	Title             string    `json:"title"`
	Content           string    `json:"content"`
	Instruction       *string   `json:"instruction"`
	Conf              Conf      `json:"conf"`
	Prefix            string    `json:"prefix"`
	Type              TaskType  `json:"type"`
	Sections          []Section `json:"sections"`
	Tasks             []Task    `json:"tasks"`
	UserProgress      struct {
		Started   bool      `json:"started"`
		Completed bool      `json:"completed"`
		Progress  FlexFloat `json:"progress"`
		Points    FlexFloat `json:"points"`
		Score     FlexFloat `json:"score"`
	} `json:"user_progress"`
}

// Assignment is the exercise set (opgavesæt) returned by
// GET /schools/{school}/assignments/{id}?homeworkId={id}.
type Assignment struct {
	ID       int       `json:"id"`
	Title    string    `json:"title"`
	Level    string    `json:"level"`
	Sections []Section `json:"sections"`
}
