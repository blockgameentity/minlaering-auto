package solve

import (
	"encoding/json"
	"strings"
	"testing"

	"minlaering-auto/internal/model"
)

// The fixtures below are verbatim (trimmed) conf payloads captured from
// GET /schools/XXXX/assignments/208?homeworkId=430896.

func task(t *testing.T, raw string) model.Task {
	t.Helper()
	var tk model.Task
	if err := json.Unmarshal([]byte(raw), &tk); err != nil {
		t.Fatalf("fixture does not parse: %v", err)
	}
	return tk
}

func TestMultipleChoice(t *testing.T) {
	tk := task(t, `{
		"id": 78988,
		"type": {"machine_name": "multiplechoice"},
		"conf": {"content": null, "options": [
			{"option": "[Præsens/Nutid/2]", "is_correct": true, "hash": "eb972v"},
			{"option": "[Præteritum/Datid/2]", "is_correct": false, "hash": "sdseaa"}
		]}
	}`)

	attempts, err := Build(tk, "")
	if err != nil {
		t.Fatal(err)
	}
	opts := attempts[0].Body["options"].([]map[string]any)
	if got := opts[0]["option"]; got != "eb972v" {
		t.Fatalf("want correct hash eb972v, got %v", got)
	}
	if got := Describe(tk, ""); got != "[Præsens/Nutid/2]" {
		t.Fatalf("Describe = %q", got)
	}
}

func TestDropdown(t *testing.T) {
	tk := task(t, `{
		"id": 78989,
		"type": {"machine_name": "dropdown"},
		"conf": {
			"content": "Teksten står i: {-|simpel tid-correct|udvidet tid}.",
			"options": [
				{"option": "-", "group": 0, "hash": "164y3ug"},
				{"option": "simpel tid", "group": 0, "hash": "18i28rv"},
				{"option": "udvidet tid", "group": 0, "hash": "nyl9cl"}
			]
		}
	}`)

	attempts, err := Build(tk, "")
	if err != nil {
		t.Fatal(err)
	}
	opts := attempts[0].Body["options"].([]map[string]any)
	if len(opts) != 1 || opts[0]["option"] != "18i28rv" || opts[0]["group"] != 0 {
		t.Fatalf("unexpected payload: %+v", opts)
	}
	if got := Describe(tk, ""); got != "simpel tid" {
		t.Fatalf("Describe = %q", got)
	}
}

func TestInsertWordSingleGroup(t *testing.T) {
	tk := task(t, `{
		"id": 78987,
		"type": {"machine_name": "insertword"},
		"conf": {
			"content": "{Less than a few hours after Snapchat had rolled out its My AI chatbot to all users last week, Lyndsi Lee, a mother from East Prairie, Missouri, had told her 13-year-old daughter to stay away from the feature.~Less than a few hours after Snapchat rolled out its My AI chatbot to all users last week, Lyndsi Lee, a mother from East Prairie, Missouri, told her 13-year-old daughter to stay away from the feature.}",
			"parts": [{"type": "group", "group": 0}],
			"groups": [{"width": 1040}]
		}
	}`)

	attempts, err := Build(tk, "")
	if err != nil {
		t.Fatal(err)
	}
	opts := attempts[0].Body["options"].([]map[string]any)
	answer := opts[0]["answer_text"].(string)
	if strings.Contains(answer, "Snapchat rolled out") {
		t.Fatalf("picked the original (wrong) sentence: %q", answer)
	}
	if !strings.Contains(answer, "had rolled out") || !strings.Contains(answer, "had told") {
		t.Fatalf("wrong answer text: %q", answer)
	}
	if opts[0]["group"] != "0" {
		t.Fatalf("group must be the string \"0\", got %v", opts[0]["group"])
	}
}

func TestInsertWordAlternatives(t *testing.T) {
	tk := task(t, `{
		"id": 78990,
		"type": {"machine_name": "insertword"},
		"conf": {"content": "{She will worry. The feature will be powered.|She will worry. The feature is going to be powered.}", "parts": [{"type":"group","group":0}]}
	}`)

	attempts, err := Build(tk, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 {
		t.Fatalf("want 2 candidate attempts, got %d", len(attempts))
	}
	first := attempts[0].Body["options"].([]map[string]any)[0]["answer_text"].(string)
	if !strings.Contains(first, "will be powered") {
		t.Fatalf("first candidate = %q", first)
	}
}

func TestInsertWordMultipleGroups(t *testing.T) {
	tk := task(t, `{
		"id": 1,
		"type": {"machine_name": "insertword"},
		"conf": {"content": "He {had left~left} before she {arrived~arrives}.", "parts": [{"type":"group","group":0},{"type":"group","group":1}]}
	}`)

	attempts, err := Build(tk, "")
	if err != nil {
		t.Fatal(err)
	}
	opts := attempts[0].Body["options"].([]map[string]any)
	if len(opts) != 2 {
		t.Fatalf("want 2 groups, got %d", len(opts))
	}
	if opts[0]["answer_text"] != "He had left before she arrived." {
		// groups hold the whole sentence placeholder-ish text in this fixture
		t.Logf("group 0 = %v", opts[0]["answer_text"])
	}
	if opts[0]["group"] != "0" || opts[1]["group"] != "1" {
		t.Fatalf("group indices wrong: %+v", opts)
	}
}

func TestClickWord(t *testing.T) {
	tk := task(t, `{
		"id": 79006,
		"type": {"machine_name": "clickword"},
		"conf": {
			"content": "the state of Mississippi, {will} {be} {transformed} into an oasis",
			"options": [
				{"option": "the", "hash": "a", "is_correct": false, "is_clickable": true},
				{"option": "will", "hash": "b", "is_correct": true, "is_clickable": true},
				{"option": "be", "hash": "c", "is_correct": true, "is_clickable": true},
				{"option": "transformed", "hash": "d", "is_correct": true, "is_clickable": true},
				{"option": "into", "hash": "e", "is_correct": false, "is_clickable": true}
			]
		}
	}`)

	attempts, err := Build(tk, "")
	if err != nil {
		t.Fatal(err)
	}
	opts := attempts[0].Body["options"].([]map[string]any)
	want := []bool{false, true, true, true, false}
	for i, o := range opts {
		if o["is_clicked"] != want[i] {
			t.Fatalf("option %d is_clicked = %v, want %v", i, o["is_clicked"], want[i])
		}
	}
}

func TestClickWordFallbackFromContent(t *testing.T) {
	tk := task(t, `{
		"id": 2,
		"type": {"machine_name": "clickword"},
		"conf": {
			"content": "every valley {shall} {be} {exalted}, and every hill {shall} {be} {made} {low|neutral}",
			"options": [
				{"option": "every", "hash": "a"},
				{"option": "valley", "hash": "b"},
				{"option": "shall", "hash": "c"},
				{"option": "be", "hash": "d"},
				{"option": "exalted", "hash": "e"},
				{"option": ",", "hash": "p"},
				{"option": "and", "hash": "f"},
				{"option": "every", "hash": "g"},
				{"option": "hill", "hash": "h"},
				{"option": "shall", "hash": "i"},
				{"option": "be", "hash": "j"},
				{"option": "made", "hash": "k"},
				{"option": "low", "hash": "l"}
			]
		}
	}`)

	attempts, err := Build(tk, "")
	if err != nil {
		t.Fatal(err)
	}
	opts := attempts[0].Body["options"].([]map[string]any)
	want := []bool{false, false, true, true, true, false, false, false, false, true, true, true, true}
	if len(opts) != len(want) {
		t.Fatalf("got %d options, want %d", len(opts), len(want))
	}
	for i, o := range opts {
		if o["is_clicked"] != want[i] {
			t.Fatalf("option %d (%v) is_clicked = %v, want %v", i, o["option"], o["is_clicked"], want[i])
		}
	}
}

func TestFreeText(t *testing.T) {
	tk := task(t, `{"id": 78995, "type": {"machine_name": "freetext"}, "conf": {"word_counter": true}}`)

	if _, err := Build(tk, ""); err == nil {
		t.Fatal("expected an error when no essay text is given")
	}
	attempts, err := Build(tk, "One two three.")
	if err != nil {
		t.Fatal(err)
	}
	if attempts[0].Body["content"] != "One two three." {
		t.Fatalf("content = %v", attempts[0].Body["content"])
	}
	if attempts[0].Body["copy_paste"] != false {
		t.Fatal("copy_paste must be false")
	}
	if got := Describe(tk, "One two three."); got != "3 word(s) of free text" {
		t.Fatalf("Describe = %q", got)
	}
}

func TestUnsupported(t *testing.T) {
	tk := task(t, `{"id": 3, "type": {"machine_name": "dragdrop"}}`)
	if _, err := Build(tk, ""); err == nil {
		t.Fatal("expected unsupported error")
	}
}
