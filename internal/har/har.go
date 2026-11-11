// Package har extracts Minlæring API credentials from browser HAR archives.
package har

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// APIHost is the backend host the web app talks to.
const APIHost = "api.minlaering.dk"

// Header is a single HTTP header from a HAR entry.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Entry is one request/response pair in a HAR archive.
type Entry struct {
	StartedDateTime string `json:"startedDateTime"`
	Request         struct {
		Method  string   `json:"method"`
		URL     string   `json:"url"`
		Headers []Header `json:"headers"`
	} `json:"request"`
	Response struct {
		Status  int `json:"status"`
		Content struct {
			MimeType string `json:"mimeType"`
			Text     string `json:"text"`
		} `json:"content"`
	} `json:"response"`
}

// Archive is the subset of a HAR file we care about.
type Archive struct {
	Log struct {
		Version string `json:"version"`
		Creator struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"creator"`
		Entries []Entry `json:"entries"`
	} `json:"log"`
}

// Credentials is everything needed to talk to the API on behalf of the
// browser session that produced the HAR file.
type Credentials struct {
	Token      string // Bearer JWT
	SchoolID   int    // /schools/{id}/... scope
	AppVersion string // X-ML-App-Version sent by the web app
	UserAgent  string
	Origin     string

	// Optional hints discovered in the archive.
	AssignmentID int
	HomeworkID   int

	SourceFile string
}

func (c *Credentials) String() string {
	return fmt.Sprintf("school %d via %s", c.SchoolID, filepath.Base(c.SourceFile))
}

var (
	bearerRe     = regexp.MustCompile(`(?i)^Bearer\s+(\S+)$`)
	schoolRe     = regexp.MustCompile(`/schools/(\d+)/`)
	assignmentRe = regexp.MustCompile(`/assignments/(\d+)`)
	homeworkRe   = regexp.MustCompile(`[?&]homeworkId=(\d+)`)
)

// Load reads credentials from a HAR file, a directory containing HAR files, or
// "" (in which case the current directory is scanned).
func Load(path string) (*Credentials, error) {
	files, err := candidateFiles(path)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no .har files found in %q", path)
	}

	var firstErr error
	for _, f := range files {
		creds, err := Extract(f)
		if err == nil {
			return creds, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, fmt.Errorf("no usable credentials found: %w", firstErr)
}

// Extract parses one HAR file and returns the newest API session it contains.
func Extract(path string) (*Credentials, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var a Archive
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("%s: not a HAR archive: %w", path, err)
	}

	creds := &Credentials{SourceFile: path}
	best := -1 // index of entry that supplied the token

	for i := range a.Log.Entries {
		e := &a.Log.Entries[i]
		if !strings.Contains(e.Request.URL, APIHost) {
			continue
		}
		if creds.SchoolID == 0 {
			if m := schoolRe.FindStringSubmatch(e.Request.URL); m != nil {
				creds.SchoolID, _ = strconv.Atoi(m[1])
			}
		}
		if creds.AssignmentID == 0 {
			if m := assignmentRe.FindStringSubmatch(e.Request.URL); m != nil {
				creds.AssignmentID, _ = strconv.Atoi(m[1])
			}
		}
		if creds.HomeworkID == 0 {
			if m := homeworkRe.FindStringSubmatch(e.Request.URL); m != nil {
				creds.HomeworkID, _ = strconv.Atoi(m[1])
			}
		}
		if creds.AppVersion == "" {
			creds.AppVersion = header(e.Request.Headers, "X-ML-App-Version")
		}
		if creds.UserAgent == "" {
			creds.UserAgent = header(e.Request.Headers, "User-Agent")
		}
		if creds.Origin == "" {
			creds.Origin = header(e.Request.Headers, "Origin")
		}
		if auth := header(e.Request.Headers, "Authorization"); auth != "" {
			if m := bearerRe.FindStringSubmatch(auth); m != nil {
				// Prefer the latest request: it carries the freshest token.
				creds.Token = m[1]
				best = i
			}
		}
	}
	_ = best

	if creds.Token == "" {
		return nil, fmt.Errorf("%s: no Authorization: Bearer header for %s", path, APIHost)
	}
	if creds.SchoolID == 0 {
		return nil, fmt.Errorf("%s: no /schools/{id}/ URL, cannot determine school", path)
	}
	if creds.AppVersion == "" {
		creds.AppVersion = "2026-11-09T13:28:11.392Z"
	}
	if creds.Origin == "" {
		creds.Origin = "https://app.minlaering.dk"
	}
	if creds.UserAgent == "" {
		creds.UserAgent = "Mozilla/5.0 (X11; Linux x86_64; rv:159.0) Gecko/20100101 Firefox/159.0"
	}
	return creds, nil
}

func header(hs []Header, name string) string {
	for _, h := range hs {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

// candidateFiles resolves the -har argument to a list of files to try, newest
// first (a session that was captured last is most likely still valid).
func candidateFiles(path string) ([]string, error) {
	if path == "" {
		path = "."
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{path}, nil
	}
	dir := path
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	type fileInfo struct {
		path string
		mod  int64
	}
	var found []fileInfo
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".har") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		found = append(found, fileInfo{filepath.Join(dir, e.Name()), fi.ModTime().UnixNano()})
	}
	sort.Slice(found, func(i, j int) bool { return found[i].mod > found[j].mod })
	files := make([]string, len(found))
	for i, f := range found {
		files[i] = f.path
	}
	return files, nil
}
