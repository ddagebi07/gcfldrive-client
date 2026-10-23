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
	"sync/atomic"
	"time"
)

//go:embed all:web
var webFS embed.FS

// DefaultMaxSheets is the school's per-person limit of paper for one print job
// until the server says otherwise (the librarian sets it online).
const DefaultMaxSheets = 7

var (
	pinRe    = regexp.MustCompile(`^\d{4}$`)
	hakbunRe = regexp.MustCompile(`^\d{5}$`)
)

// Host is the native window the page runs in. The Windows UI implements it.
type Host interface {
	// OtherWay lets the student leave the launcher: it leaves full screen and
	// minimizes the window so the desktop shows, and holds back the nudge for a while.
	OtherWay()
	// NoticeDone is called when the person dismissed a print warning, so the window
	// can go back to where it was (minimized) if it only came up for the warning.
	NoticeDone()
	// Exit ends the program (administrator only).
	Exit()
	// OpenFile shows a file the page cannot draw (a photo) in the Windows default
	// viewer, so the student prints from there. name is the original file name;
	// the server only passes names that ViewerFile accepted.
	OpenFile(name string, body []byte) error
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
	maxSheets  atomic.Int32

	names map[string]string // file id -> original file name, from the last listing

	menus   menuCache
	banners bannerCache

	Addr string // host:port once listening
}

func NewServer(api *API, sess *Session, host Host, guard Guard) *Server {
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	s := &Server{api: api, sess: sess, host: host, guard: guard, token: hex.EncodeToString(raw), names: map[string]string{}}
	s.maxSheets.Store(DefaultMaxSheets)
	return s
}

// SyncSettings keeps the paper limit in step with what the librarian set online.
func (s *Server) SyncSettings(every time.Duration) {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		if n, err := s.api.MaxSheets(ctx); err == nil {
			s.maxSheets.Store(int32(n))
		}
		cancel()
		time.Sleep(every)
	}
}

// SignedIn reports whether a student is using the screen now.
func (s *Server) SignedIn() bool { return s.sess.SignedIn() }

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
	mux.HandleFunc("GET /api/banner", s.handleBanner)
	mux.HandleFunc("GET /api/banner/img/{id}", s.handleBannerImage)
	mux.HandleFunc("POST /api/other-way", s.handleOtherWay)
	mux.HandleFunc("POST /api/notice-done", func(w http.ResponseWriter, r *http.Request) { s.host.NoticeDone(); w.WriteHeader(http.StatusNoContent) })
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
	names := make(map[string]string, len(files))
	for _, f := range files {
		names[f.FileID] = f.Filename
	}
	s.mu.Lock()
	s.names = names
	s.mu.Unlock()
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
		Hakbun string `json:"hakbun"`
		PIN    string `json:"pin"`
	}
	if !readBody(w, r, &in) {
		return
	}
	if !hakbunRe.MatchString(in.Hakbun) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "학번 5자리를 입력해 주세요."})
		return
	}
	if !pinRe.MatchString(in.PIN) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "PIN 4자리를 입력해 주세요."})
		return
	}
	files, err := s.api.List(r.Context(), in.Hakbun, in.PIN)
	if err != nil {
		writeError(w, err)
		return
	}
	s.sess.Login(in.Hakbun, in.PIN)
	writeJSON(w, http.StatusOK, map[string]any{"hakbun": in.Hakbun, "files": s.view(files)})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.sess.Logout()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	hakbun, _, in := s.sess.Credentials()
	s.mu.Lock()
	full := s.fullscreen
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"loggedIn": in, "hakbun": hakbun, "remainingMs": s.sess.Remaining().Milliseconds(),
		"fullscreen": full, "maxSheets": s.maxSheets.Load(),
	})
}

func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	s.sess.Touch()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	hakbun, pin, ok := s.sess.Credentials()
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "다시 로그인해 주세요."})
		return
	}
	files, err := s.api.List(r.Context(), hakbun, pin)
	if err != nil {
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
	hakbun, pin, ok := s.sess.Credentials()
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "다시 로그인해 주세요."})
		return
	}
	s.sess.Touch()
	got, err := s.api.Fetch(r.Context(), hakbun, pin, in.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !s.sess.SignedIn() { // logged out while downloading
		clear(got.Body)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "다시 로그인해 주세요."})
		return
	}
	s.mu.Lock()
	filename := s.names[in.ID]
	s.mu.Unlock()
	switch {
	case ViewerFile(filename):
		// The page cannot print these itself (nothing happened on 출력), so the
		// Windows viewer opens them; the print service still counts the sheets.
		err := s.host.OpenFile(filename, got.Body)
		clear(got.Body)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"kind": "viewer"})
		return
	case got.Kind != "pdf" && got.Kind != "hwp" && got.Kind != "text" && got.Kind != "image":
		clear(got.Body)
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "이 형식은 출력할 수 없어요."})
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
	hakbun, pin, ok := s.sess.Credentials()
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "다시 로그인해 주세요."})
		return
	}
	s.sess.Touch()
	if err := s.api.Delete(r.Context(), hakbun, pin, in.ID); err != nil {
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

// ---- 대기 화면 캐러셀: 앞으로의 급식 두 끼 + 사서 선생님의 JPG 배너 ----------------------------

type bannerCache struct {
	mu     sync.Mutex
	images map[string][]byte
}

func (c *bannerCache) get(id string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.images[id]
	return b, ok
}

// keep stores the current banner images and forgets the ones the librarian removed.
func (c *bannerCache) keep(id string, data []byte) {
	c.mu.Lock()
	if c.images == nil {
		c.images = map[string][]byte{}
	}
	c.images[id] = data
	c.mu.Unlock()
}

func (c *bannerCache) retain(ids map[string]bool) {
	c.mu.Lock()
	for id := range c.images {
		if !ids[id] {
			delete(c.images, id)
		}
	}
	c.mu.Unlock()
}

type slideView struct {
	Kind   string   `json:"kind"` // meal | image
	Label  string   `json:"label,omitempty"`
	Date   string   `json:"date,omitempty"`
	Dishes []string `json:"dishes,omitempty"`
	Src    string   `json:"src,omitempty"`
}

func (s *Server) handleBanner(w http.ResponseWriter, r *http.Request) {
	slides := []slideView{}
	meals, _ := NextMeals(s.api.ServerNow(), 2, func(date string) (map[string][]string, error) {
		return s.menus.get(date, 30*time.Minute, func(d string) (map[string][]string, error) { return s.api.MealMenu(r.Context(), d) })
	})
	for _, m := range meals {
		slides = append(slides, slideView{Kind: "meal", Label: m.Label, Date: m.Date, Dishes: m.Dishes})
	}
	if list, err := s.api.Banners(r.Context()); err == nil {
		keep := map[string]bool{}
		for _, b := range list {
			if _, ok := s.banners.get(b.ID); !ok {
				data, err := s.api.BannerImage(r.Context(), b.ID)
				if err != nil {
					continue
				}
				s.banners.keep(b.ID, data)
			}
			keep[b.ID] = true
			slides = append(slides, slideView{Kind: "image", Src: "/api/banner/img/" + b.ID})
		}
		s.banners.retain(keep)
	}
	writeJSON(w, http.StatusOK, map[string]any{"slides": slides})
}

func (s *Server) handleBannerImage(w http.ResponseWriter, r *http.Request) {
	data, ok := s.banners.get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	_, _ = w.Write(data)
}
