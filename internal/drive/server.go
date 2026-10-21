package drive

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

//go:embed all:web
var webFS embed.FS

// MaxSheets is the school's per-person limit of paper for one print job.
const MaxSheets = 7

var pinRe = regexp.MustCompile(`^\d{4}$`)

// Host is the native window the page runs in. The Windows UI implements it.
type Host interface {
	// OtherWay lets the student leave the launcher: it leaves full screen and
	// minimizes the window so the desktop shows, and holds back the nudge for a while.
	OtherWay()
	// Exit ends the program (administrator only).
	Exit()
}

// Guard is the protection service on the same PC. Every call carries the
// administrator password; the service decides whether it is right.
type Guard interface {
	Check(password string) error
	Maintenance(password string, minutes int) error
	Uninstall(password string) error
}

// Server is the loopback web server the window loads: the page, the file API
// (the PIN never leaves this process) and a pass-through of the site's HWP viewer.
type Server struct {
	api   *API
	sess  *Session
	host  Host
	guard Guard
	token string

	mu         sync.Mutex
	fullscreen bool

	Addr string // host:port once listening
}

func NewServer(api *API, sess *Session, host Host, guard Guard) *Server {
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	return &Server{api: api, sess: sess, host: host, guard: guard, token: hex.EncodeToString(raw)}
}

// SetFullscreen is called by the window when it changes mode, so the page can match.
func (s *Server) SetFullscreen(on bool) {
	s.mu.Lock()
	s.fullscreen = on
	s.mu.Unlock()
}

// Listen starts serving on a random loopback port and returns the URL the window opens.
func (s *Server) Listen() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	s.Addr = ln.Addr().String()
	go func() { _ = (&http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}).Serve(ln) }()
	return "http://" + s.Addr + "/?t=" + s.token, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	web, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServerFS(web))

	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("POST /api/activity", s.handleActivity)
	mux.HandleFunc("GET /api/files", s.handleFiles)
	mux.HandleFunc("POST /api/print", s.handlePrint)
	mux.HandleFunc("GET /api/blob/{id}", s.handleBlob)
	mux.HandleFunc("POST /api/blob/{id}/drop", s.handleBlobDrop)
	mux.HandleFunc("POST /api/delete", s.handleDelete)
	mux.HandleFunc("POST /api/other-way", s.handleOtherWay)
	mux.HandleFunc("POST /api/admin/check", s.handleAdmin("check"))
	mux.HandleFunc("POST /api/admin/maintenance", s.handleAdmin("maintenance"))
	mux.HandleFunc("POST /api/admin/uninstall", s.handleAdmin("uninstall"))
	// The page embeds the site's own HWP viewer, served from here so it is same-origin.
	mux.HandleFunc("GET /static/editor/", s.proxySite)
	mux.HandleFunc("GET /static/js/rhwp/", s.proxySite)
	return s.guardRequests(mux)
}

// guardRequests keeps other programs on this PC (a browser a student opens) out:
// the request must come with the secret token the window was started with, and
// the Host header must be our own loopback address (no DNS rebinding).
func (s *Server) guardRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != s.Addr {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if t := r.URL.Query().Get("t"); t != "" {
			if subtle.ConstantTimeCompare([]byte(t), []byte(s.token)) == 1 {
				http.SetCookie(w, &http.Cookie{Name: "gd", Value: s.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
				r.AddCookie(&http.Cookie{Name: "gd", Value: s.token})
			}
		}
		c, err := r.Cookie("gd")
		if err != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.token)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		status := apiErr.Status
		if status == http.StatusForbidden || status == http.StatusUnauthorized {
			status = http.StatusForbidden
		}
		writeJSON(w, status, map[string]string{"error": apiErr.Message})
		return
	}
	writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
}

func readBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(v); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "요청을 읽지 못했어요."})
		return false
	}
	return true
}

// fileView is one row of the drive screen.
type fileView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	RemainingMS int64  `json:"remainingMs"`
	Urgent      bool   `json:"urgent"` // the 5-minute deletion clock is running
}

func (s *Server) view(files []File) []fileView {
	now := s.api.ServerNow()
	out := make([]fileView, 0, len(files))
	for _, f := range files {
		v := fileView{ID: f.FileID, Name: DisplayName(f.Filename, f.FileSize)}
		end, ok := ParseTime(f.ExpiresAt)
		if f.DeleteAt != nil {
			if t, ok2 := ParseTime(*f.DeleteAt); ok2 {
				end, ok, v.Urgent = t, true, true
			}
		}
		if ok {
			v.RemainingMS = max(0, end.Sub(now).Milliseconds())
		}
		if ok && v.RemainingMS == 0 {
			continue // already gone on the server
		}
		out = append(out, v)
	}
	return out
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PIN string `json:"pin"`
	}
	if !readBody(w, r, &in) {
		return
	}
	if !pinRe.MatchString(in.PIN) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "PIN 4자리를 입력해 주세요."})
		return
	}
	files, err := s.api.List(r.Context(), in.PIN)
	if err != nil {
		writeError(w, err)
		return
	}
	s.sess.Login(in.PIN)
	writeJSON(w, http.StatusOK, map[string]any{"files": s.view(files)})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.sess.Logout()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	_, in := s.sess.PIN()
	s.mu.Lock()
	full := s.fullscreen
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"loggedIn": in, "remainingMs": s.sess.Remaining().Milliseconds(),
		"fullscreen": full, "maxSheets": MaxSheets,
	})
}

func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	s.sess.Touch()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	pin, ok := s.sess.PIN()
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "PIN을 다시 입력해 주세요."})
		return
	}
	files, err := s.api.List(r.Context(), pin)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			// every file is gone (printed and expired, or deleted)
			writeJSON(w, http.StatusOK, map[string]any{"files": []fileView{}})
			return
		}
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": s.view(files)})
}

func (s *Server) handlePrint(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if !readBody(w, r, &in) {
		return
	}
	pin, ok := s.sess.PIN()
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "PIN을 다시 입력해 주세요."})
		return
	}
	s.sess.Touch()
	got, err := s.api.Fetch(r.Context(), pin, in.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	if _, still := s.sess.PIN(); !still { // logged out while downloading
		clear(got.Body)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "PIN을 다시 입력해 주세요."})
		return
	}
	id := s.sess.Hold(&Blob{Kind: got.Kind, ContentType: got.ContentType, Name: in.Name, Body: got.Body})
	writeJSON(w, http.StatusOK, map[string]string{"kind": got.Kind, "blob": id})
}

func (s *Server) handleBlob(w http.ResponseWriter, r *http.Request) {
	b, ok := s.sess.Blob(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	ct := "application/octet-stream"
	switch b.Kind {
	case "pdf":
		ct = "application/pdf"
	case "image", "text":
		ct = b.ContentType
	}
	w.Header().Set("Content-Type", ct)
	_, _ = w.Write(b.Body)
}

func (s *Server) handleBlobDrop(w http.ResponseWriter, r *http.Request) {
	s.sess.Drop(r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID string `json:"id"`
	}
	if !readBody(w, r, &in) {
		return
	}
	pin, ok := s.sess.PIN()
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "PIN을 다시 입력해 주세요."})
		return
	}
	s.sess.Touch()
	if err := s.api.Delete(r.Context(), pin, in.ID); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleOtherWay(w http.ResponseWriter, r *http.Request) {
	s.sess.Logout()
	s.host.OtherWay()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdmin(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Password string `json:"password"`
			Minutes  int    `json:"minutes"`
		}
		if !readBody(w, r, &in) {
			return
		}
		if s.guard == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "보호 서비스에 연결하지 못했어요."})
			return
		}
		var err error
		switch op {
		case "check":
			err = s.guard.Check(in.Password)
		case "maintenance":
			err = s.guard.Maintenance(in.Password, in.Minutes)
		case "uninstall":
			err = s.guard.Uninstall(in.Password)
		}
		if err != nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		if op == "maintenance" || op == "uninstall" {
			go func() { time.Sleep(400 * time.Millisecond); s.host.Exit() }()
		}
	}
}

// proxySite passes the HWP viewer's files through from the archive site.
func (s *Server) proxySite(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.URL.Path, "..") {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.api.base+r.URL.Path+"?"+r.URL.RawQuery, nil)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	resp, err := s.api.hc.Do(req)
	if err != nil {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for _, h := range []string{"Content-Type", "Content-Length"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
