package drive

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// fakeArchive is a stand-in for the archive server's /api/share/anon/* routes.
type fakeArchive struct {
	mu      sync.Mutex
	files   map[string][]File // by PIN
	bodies  map[string][]byte
	deleted []string
	srv     *httptest.Server
}

func newFakeArchive() *fakeArchive {
	f := &fakeArchive{files: map[string][]File{}, bodies: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

func iso(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000") + "Z" }

func (f *fakeArchive) add(pin, id, name string, size int64, body []byte, deleteIn *time.Duration) {
	file := File{FileID: id, Filename: name, FileSize: size, UploadedAt: iso(time.Now()), ExpiresAt: iso(time.Now().Add(24 * time.Hour)), ShortCode: id}
	if deleteIn != nil {
		d := iso(time.Now().Add(*deleteIn))
		file.DeleteAt = &d
	}
	f.mu.Lock()
	f.files[pin] = append(f.files[pin], file)
	f.bodies[id] = body
	f.mu.Unlock()
}

func (f *fakeArchive) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/") // api share anon <pin> [file|delete <id>]
	if len(parts) < 4 || parts[2] != "anon" {
		http.NotFound(w, r)
		return
	}
	pin := parts[3]
	jsonErr := func(code int, msg string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
	}
	list := f.files[pin]
	switch {
	case len(parts) == 4:
		if len(list) == 0 {
			jsonErr(404, "이 PIN으로 보관 중인 파일이 없어요. PIN을 다시 확인해 주세요.")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "files": list})
	case len(parts) == 6 && parts[4] == "file":
		for _, file := range list {
			if file.FileID == parts[5] {
				w.Header().Set("X-Drive-Kind", map[bool]string{true: "pdf", false: "text"}[strings.HasSuffix(file.Filename, ".pdf")])
				w.Header().Set("Content-Type", "application/pdf")
				_, _ = w.Write(f.bodies[file.FileID])
				return
			}
		}
		jsonErr(404, "파일을 찾지 못했어요.")
	case len(parts) == 6 && parts[4] == "delete":
		for i, file := range list {
			if file.FileID == parts[5] {
				f.files[pin] = append(list[:i:i], list[i+1:]...)
				f.deleted = append(f.deleted, file.FileID)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"status":"ok"}`))
				return
			}
		}
		jsonErr(404, "파일을 찾지 못했어요.")
	default:
		http.NotFound(w, r)
	}
}
