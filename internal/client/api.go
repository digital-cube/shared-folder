package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The wire types of the control API (internal/server/httpapi). They are
// repeated here so the client binary does not link the server.

// Event is one entry of the server's change log.
type Event struct {
	Seq     int64     `json:"seq"`
	TS      time.Time `json:"ts"`
	Member  string    `json:"member"`
	Op      string    `json:"op"`
	Path    string    `json:"path"`
	OldPath string    `json:"old_path,omitempty"`
	Hash    string    `json:"hash,omitempty"`
	Size    int64     `json:"size,omitempty"`
	Mtime   int64     `json:"mtime,omitempty"`
}

type changesResponse struct {
	Events []Event `json:"events"`
	Head   int64   `json:"head"`
	More   bool    `json:"more"`
	Next   int64   `json:"next"`
}

type announceRequest struct {
	Path     string `json:"path"`
	BaseHash string `json:"base_hash"`
	NewHash  string `json:"new_hash"`
	Size     int64  `json:"size"`
	Mtime    int64  `json:"mtime"`
}

type announceResult struct {
	Status   string `json:"status"` // "upload" or "unchanged"
	Path     string `json:"path"`
	UploadID string `json:"upload_id"`
	Dest     string `json:"dest"`
}

type opRequest struct {
	Op       string `json:"op"`
	Path     string `json:"path"`
	NewPath  string `json:"new_path,omitempty"`
	BaseHash string `json:"base_hash,omitempty"`
}

// result is what a commit or an op did.
type result struct {
	Status       string  `json:"status"` // applied, unchanged, conflict
	Path         string  `json:"path"`
	ConflictPath string  `json:"conflict_path"`
	Events       []Event `json:"events"`
	Head         int64   `json:"head"`
}

type syncReport struct {
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	FromSeq    int64     `json:"from_seq"`
	ToSeq      int64     `json:"to_seq"`
	Sent       int64     `json:"sent"`
	Received   int64     `json:"received"`
	Conflicts  int64     `json:"conflicts"`
	Error      string    `json:"error,omitempty"`
}

type sshKeyResponse struct {
	PublicKey   string `json:"public_key"`
	Fingerprint string `json:"fingerprint"`
	SSH         struct {
		User string `json:"user"`
		Host string `json:"host"`
		Port int    `json:"port"`
	} `json:"ssh"`
	// Replaced is set when the member's earlier, different key (another
	// device's) was replaced.
	Replaced *struct {
		Fingerprint string `json:"fingerprint"`
	} `json:"replaced"`
}

// APIError is a refusal of the server, with its machine-readable code
// (internal/server/errors.go).
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("server: %s: %s", e.Code, e.Message)
}

// codeOf returns the API error code of err, or "".
func codeOf(err error) string {
	var e *APIError
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// api talks to one folder of a server.
type api struct {
	base    string // https://host:port
	shareID string
	token   string
	hc      *http.Client
}

func newAPI(base, shareID, token string, hc *http.Client) *api {
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	return &api{base: strings.TrimRight(base, "/"), shareID: shareID, token: token, hc: hc}
}

func (a *api) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+"/v1/shares/"+url.PathEscape(a.shareID)+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var eb struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &eb) == nil && eb.Error.Code != "" {
			return &APIError{Status: resp.StatusCode, Code: eb.Error.Code, Message: eb.Error.Message}
		}
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 200 {
			msg = msg[:200] + "…"
		}
		return &APIError{Status: resp.StatusCode, Code: "http_" + strconv.Itoa(resp.StatusCode), Message: msg}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s %s: unexpected answer: %w", method, path, err)
	}
	return nil
}

// changes returns every event after since (following pages) and the HEAD
// of the first page; events after that HEAD wait for the next call.
func (a *api) changes(ctx context.Context, since int64) ([]Event, int64, error) {
	var all []Event
	var head int64 = -1
	for {
		var r changesResponse
		if err := a.do(ctx, http.MethodGet, "/changes?since="+strconv.FormatInt(since, 10), nil, &r); err != nil {
			return nil, 0, err
		}
		if head < 0 {
			head = r.Head
		}
		for _, e := range r.Events {
			if e.Seq <= head {
				all = append(all, e)
			}
		}
		if !r.More || r.Next >= head || r.Next <= since {
			return all, head, nil
		}
		since = r.Next
	}
}

// head returns the server's HEAD; it is also how join checks the token.
func (a *api) head(ctx context.Context) (int64, error) {
	var r changesResponse
	err := a.do(ctx, http.MethodGet, "/changes?limit=1&since=0", nil, &r)
	return r.Head, err
}

func (a *api) announce(ctx context.Context, req announceRequest) (*announceResult, error) {
	var r announceResult
	return &r, a.do(ctx, http.MethodPost, "/uploads", req, &r)
}

func (a *api) commit(ctx context.Context, uploadID string) (*result, error) {
	var r result
	return &r, a.do(ctx, http.MethodPost, "/uploads/"+url.PathEscape(uploadID)+"/commit", nil, &r)
}

func (a *api) op(ctx context.Context, req opRequest) (*result, error) {
	var r result
	return &r, a.do(ctx, http.MethodPost, "/ops", req, &r)
}

func (a *api) report(ctx context.Context, r syncReport) error {
	return a.do(ctx, http.MethodPost, "/sync-report", r, nil)
}

func (a *api) setSSHKey(ctx context.Context, pub string) (*sshKeyResponse, error) {
	var r sshKeyResponse
	return &r, a.do(ctx, http.MethodPut, "/ssh-key", map[string]string{"public_key": pub}, &r)
}
