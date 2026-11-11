# minlaering-auto

A Go CLI that completes a Minlæring assignment (opgavesæt) through the student
API, authenticating with a session captured in a browser `.har` archive.

```console
$ go run . "https://app.minlaering.dk/opgavesaet/208?homeworkId=430896"

Minlæring CLI — authenticated as school XXXX via app.minlaering.dk_Archive [26-11-11 21-18-45].har
Homework 430896

Assignment 208 — Eksamensopgave 10 (A-niveau) (A-level)
  Assignment 1: [Verballeddenes/Udsagnsleddenes/3] tider i arti…  9/9   100.0%
  Assignment 2: Skriv et debatindlæg på 100-150 ord om AI         1/1   100.0%
  Assignment 3: Aktive og passive [verballed/upsagnsled/3] i Ma…  8/8   100.0%

pass 1/3: 3 task(s) to answer
  task 78991 multiplechoice -> Korrekt (11.11 pts)
  task 78992 dropdown       -> Korrekt (11.11 pts)
  task 78993 insertword     -> Korrekt (11.11 pts)

Assignment 1: [Verballeddenes/Udsagnsleddenes/3] tider i …  9/9  100.0%   85.00 pts
Assignment 2: Skriv et debatindlæg på 100-150 ord om AI     1/1  100.0%    0.00 pts
Assignment 3: Aktive og passive [verballed/udsagnsled/3] …  8/8  100.0%  100.00 pts
tasks completed                                            18/18
Assignment 208 complete: 18 task(s) done, 16 graded correct, 184.70 pts.
```

## Build

```console
go build -o minlaering-auto .
go test ./...
```

Only the standard library is used.

## Authentication

Put the `.har` export in the working directory (or point at it with `-har`) and
run the CLI. Credentials are read from the archive:

| what | where it comes from |
| --- | --- |
| Bearer token | `Authorization` header of any `api.minlaering.dk` request |
| school id | `/schools/{id}/...` in a request URL |
| `X-ML-App-Version`, `User-Agent`, `Origin` | request headers |
| assignment / homework id | `/assignments/{id}` + `?homeworkId={id}`, used as defaults |

If several archives are present, the newest one is tried first, and the next one
is used if the first has no usable token. An expired session surfaces as
`HTTP 401/403` with a hint to export a fresh HAR.

## Usage

```text
minlaering-auto [flags] ["<assignment URL>"]

  -har string            HAR archive or directory of .har files (default: cwd)
  -url string            e.g. https://app.minlaering.dk/opgavesaet/208?homeworkId=430896
  -assignment int        assignment id (overrides -url)
  -homework int          homework id (overrides -url)
  -list                  list tasks, status and the answer that would be sent
  -dry-run               print the payloads without sending them
  -all                   complete every assignment attached to the homework
  -passes int            verify-and-retry passes (default 3)
  -force                 re-submit tasks that are already done
  -env-file string       file with API keys (default .env)
  -llm-url string        OpenAI-compatible base URL (default https://api.neuralwatt.com/v1)
  -llm-model string      model id (default mimo-v2.6-pro)
  -llm-key string        API key (default NEURALWATT_API_KEY from .env)
  -llm-temperature float sampling temperature (default 0.8)
  -essay string          use this text for every freetext task instead of an LLM
  -essay-file string     read that text from a file
  -time-spent int        time_spent reported per answer (default 30)
  -delay duration        pause between submissions (default 400ms)
  -complete-homework     POST /homework/{id}/complete when everything is done
  -json                  machine readable report
  -v                     verbose
```

Typical runs:

```console
# what is left?
minlaering-auto -list "https://app.minlaering.dk/opgavesaet/208?homeworkId=430896"

# preview the exact JSON bodies
minlaering-auto -dry-run "https://app.minlaering.dk/opgavesaet/208?homeworkId=430896"

# the whole homework (both opgavesæt) instead of one assignment
minlaering-auto -all -homework 430896
```

## How it works

1. `GET /schools/{school}/assignments/{id}?homeworkId={hid}` returns the whole
   assignment tree: exercise → question sections → tasks.
2. For every task that is not `correct` yet, `internal/solve` derives the answer
   from the configuration the API already sent to the browser — no OCR, no LLM:

   | task type | leaked solution |
   | --- | --- |
   | `multiplechoice` | `conf.options[].is_correct` |
   | `clickword` | `conf.options[].is_correct` (one `is_clicked` per word) |
   | `dropdown` | `conf.content` markers: `{ - \| simpel tid-correct \| udvidet tid }` |
   | `insertword` | `conf.content` groups: `{correct~original}`, alternatives split on `\|` |
   | `freetext` | generated per task by an LLM from the assignment prompt (see below) |

3. The payload mirrors what the web app posts, including a 32-character random
   `nonce`, `homework_id` and `time_spent`:

   ```http
   POST /schools/XXXX/exercise/82034/sections/82047/tasks/78987/answer
   {"options":[{"answer_text":"…","group":"0","copy_paste":false}],
    "nonce":"…","homework_id":430896,"time_spent":30}
   ```

4. Each response is graded immediately. Insert-word tasks that accept several
   paraphrases expose them as separate candidates, which are tried in order.
   After each pass the assignment is re-fetched, so the retry loop only touches
   what is still open. Non-gradeable free-text tasks end in status `ungraded`
   with `is_completed: true`, which counts as done.

## Essay generation (LLM)

Free-text tasks have nothing machine-checkable to read, so their text is written
per task by a language model. Nothing is hardcoded: `internal/essay` builds the
prompt from the assignment itself (exercise title, every section instruction, the
question prompt, the task text), extracts the word range (`100-150 ord`), the
genre and the required appeal forms, and asks the model for that text only.

**Default provider: [Neuralwatt](https://neuralwatt.com), model `mimo-v2.6-pro`.**

```console
$ cp .env.example .env      # then paste your key
$ cat .env
NEURALWATT_API_KEY=sk-...
```

`NEURALWATT_API_KEY` may come from `.env`, from the shell, or from `-llm-key`;
real environment variables always win over the file. Any OpenAI-compatible
endpoint works:

```console
# other base URL / model
minlaering-auto -llm-url https://openrouter.ai/api/v1 -llm-model some/model ...

# local Ollama, no key needed
minlaering-auto -llm-url http://localhost:11434/v1 -llm-model llama3 ...

# skip the LLM entirely and submit your own text
minlaering-auto -essay-file my-essay.md ...
```

Output is cleaned (no code fences, headings, bullets, markdown or wrapping
quotes), trimmed sentence-by-sentence to the word range, and **em dashes and en
dashes are forbidden by the prompt** so the text reads like a student wrote it.
`mimo-v2.6-pro` is a reasoning model, so the token budget starts at 4096 and
doubles automatically when a response is cut off before the text starts; a
missing answer is reported instead of submitting an empty one.

`-list` shows what would be generated (genre and word range) without calling the
API, so inspecting an assignment stays free.

## Layout

```text
main.go                     flags, orchestration, reporting
internal/har                HAR parsing, credential extraction
internal/api                HTTP client (assignment, answer, homework)
internal/model              API payload types (tolerant of "0" vs 0)
internal/solve              per-task-type answer derivation + tests
internal/essay              prompt building, LLM client, text validation
internal/dotenv             minimal .env reader (NEURALWATT_API_KEY)
internal/nonce              the app's 32-char client nonce
```

## Notes

* Answers are only submitted for tasks that are not done yet; `-force` re-sends
  everything (with `-dry-run` this is a cheap way to preview generated essays).
* Essay text is generated once per task per run and reused for retries, so a
  verify pass never produces two different answers for the same task.
* `-complete-homework` is opt-in because marking a homework complete is a
  student-facing action that cannot be undone from this CLI.
* The tool needs no password: it reuses whatever session the HAR was captured
  with, so re-export the HAR after logging in again when it expires.
