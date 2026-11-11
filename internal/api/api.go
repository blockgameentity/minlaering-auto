// Package api is a thin client for the Minlæring student API.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"minlaering-auto/internal/har"
	"minlaering-auto/internal/model"
)

// DefaultBaseURL is the production API endpoint.
const DefaultBaseURL = "https://" + har.APIHost

// Client talks to api.minlaering.dk with the credentials of a browser session.
type Client struct {
	base  string
	creds *har.Credentials
	http  *http.Client
}

// New returns a client authenticated with the given HAR credentials.
func New(creds *har.Credentials) *Client {
	return &Client{
		base:  DefaultBaseURL,
		creds: creds,
		http:  &http.Client{Timeout: 45 * time.Second},
	}
}

// SchoolID returns the school scope used by every request.
func (c *Client) SchoolID() int { return c.creds.SchoolID }

type envelope struct {
	Data   json.RawMessage `json:"data"`
	Status int             `json:"status"`
	Msg    string          `json:"message"`
}

type apiError struct {
	StatusCode int
	Message    string
	Body       string
}

func (e *apiError) Error() string {
	msg := fmt.Sprintf("api: HTTP %d", e.StatusCode)
	if e.Message != "" {
		msg += ": " + e.Message
	}
	if e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden {
		msg += " (the session in the HAR file looks expired or out of scope: log in again and export a fresh HAR)"
	}
	return msg
}

// Assignment fetches an exercise set. homeworkID is optional (0 = no homework).
func (c *Client) Assignment(ctx context.Context, assignmentID, homeworkID int) (*model.Assignment, error) {
	path := fmt.Sprintf("/schools/%d/assignments/%d", c.creds.SchoolID, assignmentID)
	if homeworkID > 0 {
		path += "?homeworkId=" + strconv.Itoa(homeworkID)
	}
	var out model.Assignment
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Submit posts an answer payload for one task and returns the graded result.
func (c *Client) Submit(ctx context.Context, exerciseRelationID, sectionRelationID, taskID int, body any) (*model.TaskAnswer, error) {
	path := fmt.Sprintf("/schools/%d/exercise/%d/sections/%d/tasks/%d/answer",
		c.creds.SchoolID, exerciseRelationID, sectionRelationID, taskID)
	var out model.TaskAnswer
	if err := c.do(ctx, http.MethodPost, path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Get performs a GET request and decodes the data envelope into out.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

// Post performs a POST request and decodes the data envelope into out.
func (c *Client) Post(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPost, path, body, out)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return err
	}
	c.headers(req)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}

	var env envelope
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &env)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &apiError{StatusCode: resp.StatusCode, Message: env.Msg, Body: truncate(string(raw), 400)}
	}
	if out == nil || len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func (c *Client) headers(req *http.Request) {
	h := req.Header
	h.Set("Authorization", "Bearer "+c.creds.Token)
	h.Set("Accept", "application/json")
	h.Set("X-Requested-With", "XMLHttpRequest")
	h.Set("ML-Ident", "app-frontend")
	h.Set("X-ML-App-Version", c.creds.AppVersion)
	h.Set("User-Agent", c.creds.UserAgent)
	if c.creds.Origin != "" {
		h.Set("Origin", c.creds.Origin)
		h.Set("Referer", c.creds.Origin+"/")
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ResolveURL accepts either a bare assignment URL/id pair or the student-facing
// app URL (https://app.minlaering.dk/opgavesaet/208?homeworkId=430896).
func ResolveURL(raw string) (assignmentID, homeworkID int, err error) {
	if raw == "" {
		return 0, 0, nil
	}
	u, perr := url.Parse(raw)
	if perr != nil {
		return 0, 0, fmt.Errorf("invalid URL %q: %w", raw, perr)
	}
	q := u.Query()
	if v := q.Get("homeworkId"); v != "" {
		homeworkID, _ = strconv.Atoi(v)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, p := range parts {
		if (p == "opgavesaet" || p == "assignments" || p == "assignment") && i+1 < len(parts) {
			assignmentID, _ = strconv.Atoi(parts[i+1])
		}
	}
	if assignmentID == 0 && len(parts) == 1 {
		assignmentID, _ = strconv.Atoi(parts[0])
	}
	return assignmentID, homeworkID, nil
}
