// Package drive is the 극플드라이브 client core: the server API, the PIN
// session (kept only in memory) and the local server the UI talks to.
package drive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// DefaultServer is the production archive server.
const DefaultServer = "https://gcflarchive.co.kr"

const maxFileBytes = 35 << 20 // server quota is 30MB per person; leave headroom

// APIError is an error whose message can be shown to the student as is.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return e.Message }

// File is one entry of a person's drive, as listed by the server.
type File struct {
	FileID       string  `json:"file_id"`
	Filename     string  `json:"filename"`
	FileSize     int64   `json:"file_size"`
	UploadedAt   string  `json:"uploaded_at"`
	ExpiresAt    string  `json:"expires_at"`
	ShortCode    string  `json:"short_code"`
	DownloadedAt *string `json:"downloaded_at"`
	DeleteAt     *string `json:"delete_at"`
}

// Fetched is a file body received for printing. Kind is what the server says it
// is: pdf | hwp | image | text.
type Fetched struct {
	Kind        string
	ContentType string
	Body        []byte
}

// API talks to the archive server. It remembers how far the server clock is
// from this PC's clock, because shared school PCs often have a wrong time and
// the countdowns are computed from server timestamps.
type API struct {
	base string
	hc   *http.Client
	skew atomic.Int64 // server time minus local time, in milliseconds
}

func NewAPI(base string) *API {
	return &API{base: strings.TrimRight(base, "/"), hc: &http.Client{}}
}

// ServerNow is the server's idea of the current time.
func (a *API) ServerNow() time.Time {
	return time.Now().Add(time.Duration(a.skew.Load()) * time.Millisecond)
}

func (a *API) driveURL(pin string, tail ...string) string {
	parts := []string{a.base, "api", "share", "anon", url.PathEscape(pin)}
	for _, t := range tail {
		parts = append(parts, url.PathEscape(t))
	}
	return strings.Join(parts, "/")
}

func (a *API) do(ctx context.Context, method, target string, timeout time.Duration) (*http.Response, error) {
	cancel := context.CancelFunc(func() {})
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	resp, err := a.hc.Do(req)
	if err != nil {
		cancel()
		return nil, errors.New("서버에 연결하지 못했어요. 인터넷 연결을 확인해 주세요.")
	}
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	a.noteDate(resp)
	return resp, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error { defer c.cancel(); return c.ReadCloser.Close() }

func (a *API) noteDate(resp *http.Response) {
	if t, err := http.ParseTime(resp.Header.Get("Date")); err == nil {
		a.skew.Store(time.Until(t).Milliseconds())
	}
}

func errorFrom(resp *http.Response) error {
	var payload struct {
		Error string `json:"error"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	_ = json.Unmarshal(data, &payload)
	msg := payload.Error
	if msg == "" {
		msg = "서버가 응답하지 않아요. 잠시 뒤에 다시 시도해 주세요."
	}
	return &APIError{Status: resp.StatusCode, Message: msg}
}

// List returns the files held under a PIN. An unknown PIN is an *APIError with status 404.
func (a *API) List(ctx context.Context, pin string) ([]File, error) {
	resp, err := a.do(ctx, http.MethodGet, a.driveURL(pin), 30*time.Second)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errorFrom(resp)
	}
	var out struct {
		Files []File `json:"files"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return nil, errors.New("서버 응답을 읽지 못했어요. 잠시 뒤에 다시 시도해 주세요.")
	}
	return out.Files, nil
}

// Fetch downloads a file for printing. The server converts office documents to
// PDF and starts the file's 5-minute deletion clock.
func (a *API) Fetch(ctx context.Context, pin, fileID string) (*Fetched, error) {
	resp, err := a.do(ctx, http.MethodGet, a.driveURL(pin, "file", fileID), 3*time.Minute)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errorFrom(resp)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFileBytes+1))
	if err != nil {
		return nil, errors.New("파일을 받는 중에 연결이 끊겼어요. 다시 시도해 주세요.")
	}
	if len(body) > maxFileBytes {
		return nil, errors.New("파일이 너무 커서 출력할 수 없어요.")
	}
	kind := resp.Header.Get("X-Drive-Kind")
	switch kind {
	case "pdf", "hwp", "image", "text":
	default:
		return nil, fmt.Errorf("이 형식은 출력할 수 없어요.")
	}
	return &Fetched{Kind: kind, ContentType: resp.Header.Get("Content-Type"), Body: body}, nil
}

// Delete removes one of the files under the PIN from the server.
func (a *API) Delete(ctx context.Context, pin, fileID string) error {
	resp, err := a.do(ctx, http.MethodPost, a.driveURL(pin, "delete", fileID), 30*time.Second)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errorFrom(resp)
	}
	return nil
}
