package drive

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// fakeArchive is a stand-in for the archive server's drive routes.
type fakeArchive struct {
	mu       sync.Mutex
	pins     map[string]string // 학번 -> PIN
	files    map[string][]File // by 학번
	bodies   map[string][]byte
	deleted  []string
	maxSheet int
	srv      *httptest.Server
}

func newFakeArchive() *fakeArchive {
	f := &fakeArchive{pins: map[string]string{}, files: map[string][]File{}, bodies: map[string][]byte{}, maxSheet: 7}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

func iso(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000") + "Z" }

func (f *fakeArchive) add(hakbun, pin, id, name string, size int64, body []byte, deleteIn *time.Duration) {
	file := File{FileID: id, Filename: name, FileSize: size, UploadedAt: iso(time.Now()), ExpiresAt: iso(time.Now().Add(24 * time.Hour)), ShortCode: id}
	if deleteIn != nil {
		d := iso(time.Now().Add(*deleteIn))
		file.DeleteAt = &d
	}
	f.mu.Lock()
	f.pins[hakbun] = pin
	f.files[hakbun] = append(f.files[hakbun], file)
	f.bodies[id] = body
	f.mu.Unlock()
}

func (f *fakeArchive) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	jsonOut := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	errOut := func(code int, msg string) { jsonOut(code, map[string]string{"error": msg}) }

	switch r.URL.Path {
	case "/api/share/settings":
		jsonOut(200, map[string]int{"maxSheets": f.maxSheet})
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/") // api share drive <학번> <pin> [file|delete <id>]
	if len(parts) < 5 || parts[2] != "drive" {
		http.NotFound(w, r)
		return
	}
	hakbun, pin := parts[3], parts[4]
	if f.pins[hakbun] != pin {
		errOut(403, "PIN이 맞지 않아요. 다시 입력해주세요.")
		return
	}
	list := f.files[hakbun]
	switch {
	case len(parts) == 5:
		jsonOut(200, map[string]any{"status": "ok", "hakbun": hakbun, "files": list})
	case len(parts) == 7 && parts[5] == "file":
		for _, file := range list {
			if file.FileID == parts[6] {
				w.Header().Set("X-Drive-Kind", map[bool]string{true: "pdf", false: "text"}[strings.HasSuffix(file.Filename, ".pdf")])
				w.Header().Set("Content-Type", "application/pdf")
				_, _ = w.Write(f.bodies[file.FileID])
				return
			}
		}
		errOut(404, "파일을 찾지 못했어요.")
	case len(parts) == 7 && parts[5] == "delete":
		for i, file := range list {
			if file.FileID == parts[6] {
				f.files[hakbun] = append(list[:i:i], list[i+1:]...)
				f.deleted = append(f.deleted, file.FileID)
				jsonOut(200, map[string]string{"status": "ok"})
				return
			}
		}
		errOut(404, "파일을 찾지 못했어요.")
	default:
		http.NotFound(w, r)
	}
}
