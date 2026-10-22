package drive

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"
)

type fakeHost struct{ otherWay, exited int }

func (h *fakeHost) OtherWay()   { h.otherWay++ }
func (h *fakeHost) Exit()       { h.exited++ }
func (h *fakeHost) NoticeDone() {}

type fakeGuard struct{ ok bool }

func (g fakeGuard) Check(pw string) error              { return g.err(pw) }
func (g fakeGuard) Maintenance(pw string, m int) error { return g.err(pw) }
func (g fakeGuard) Uninstall(pw string) error          { return g.err(pw) }
func (g fakeGuard) err(pw string) error {
	if pw == "right-password" {
		return nil
	}
	return &APIError{Message: "비밀번호가 맞지 않아요."}
}

type rig struct {
	t      *testing.T
	arch   *fakeArchive
	sess   *Session
	host   *fakeHost
	client *http.Client
	base   string
}

func newRig(t *testing.T) *rig {
	arch := newFakeArchive()
	t.Cleanup(arch.srv.Close)
	sess := NewSession(SessionTimeout)
	host := &fakeHost{}
	srv := NewServer(NewAPI(arch.srv.URL), sess, host, fakeGuard{})
	url, err := srv.Listen()
	if err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	r := &rig{t: t, arch: arch, sess: sess, host: host, client: c, base: "http://" + srv.Addr}
	if resp, err := c.Get(url); err != nil || resp.StatusCode != 200 { // opens with the token like the window does
		t.Fatalf("open: %v %v", err, resp)
	}
	return r
}

func (r *rig) do(method, path, body string) (int, string) {
	req, _ := http.NewRequest(method, r.base+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestRequestsWithoutTheWindowsTokenAreRefused(t *testing.T) {
	r := newRig(t)
	for _, path := range []string{"/", "/api/state", "/api/files"} {
		resp, err := http.Get(r.base + path)
		if err != nil || resp.StatusCode != 403 {
			t.Errorf("%s without token: %v %v", path, resp.StatusCode, err)
		}
	}
	req, _ := http.NewRequest("GET", r.base+"/api/state", nil)
	req.Host = "evil.example"
	req.AddCookie(&http.Cookie{Name: "gd", Value: "x"})
	if resp, _ := http.DefaultClient.Do(req); resp != nil && resp.StatusCode == 200 {
		t.Error("foreign Host header accepted")
	}
}

func TestLoginListPrintDelete(t *testing.T) {
	r := newRig(t)
	soon := 150 * time.Second
	r.arch.add("30512", "1234", "a", "과제.pdf", 2*1024*1024, []byte("%PDF-body"), nil)
	r.arch.add("30512", "1234", "b", "영어 에세이.hwpx", 20*1024, []byte("x"), &soon)

	if code, _ := r.do("POST", "/api/login", `{"hakbun":"30512","pin":"12"}`); code != 400 {
		t.Errorf("short pin: %d", code)
	}
	if code, _ := r.do("POST", "/api/login", `{"hakbun":"123","pin":"1234"}`); code != 400 {
		t.Errorf("short hakbun: %d", code)
	}
	if code, body := r.do("POST", "/api/login", `{"hakbun":"30512","pin":"9999"}`); code != 403 || !strings.Contains(body, "PIN이 맞지 않아요") {
		t.Errorf("wrong pin: %d %s", code, body)
	}
	code, body := r.do("POST", "/api/login", `{"hakbun":"30512","pin":"1234"}`)
	if code != 200 || !strings.Contains(body, "과제 [PDF,2MB]") || !strings.Contains(body, "영어 에세이 [HWPX,20KB]") {
		t.Fatalf("login: %d %s", code, body)
	}
	if !strings.Contains(body, `"urgent":true`) {
		t.Errorf("deletion clock not flagged: %s", body)
	}
	if _, state := r.do("GET", "/api/state", ""); !strings.Contains(state, `"loggedIn":true`) || !strings.Contains(state, `"maxSheets":7`) || !strings.Contains(state, `"hakbun":"30512"`) {
		t.Errorf("state: %s", state)
	}

	code, body = r.do("POST", "/api/print", `{"id":"a","name":"과제"}`)
	if code != 200 || !strings.Contains(body, `"kind":"pdf"`) {
		t.Fatalf("print: %d %s", code, body)
	}
	blob := body[strings.Index(body, `"blob":"`)+8:]
	blob = blob[:strings.Index(blob, `"`)]
	if code, got := r.do("GET", "/api/blob/"+blob, ""); code != 200 || got != "%PDF-body" {
		t.Errorf("blob: %d %q", code, got)
	}
	r.do("POST", "/api/blob/"+blob+"/drop", "")
	if code, _ := r.do("GET", "/api/blob/"+blob, ""); code != 404 {
		t.Errorf("blob should be gone after drop: %d", code)
	}

	if code, _ := r.do("POST", "/api/delete", `{"id":"a"}`); code != 200 || len(r.arch.deleted) != 1 {
		t.Errorf("delete: %d %v", code, r.arch.deleted)
	}
}

func TestLogoutForgetsPinAndBlobs(t *testing.T) {
	r := newRig(t)
	r.arch.add("30512", "1234", "a", "a.pdf", 10, []byte("%PDF"), nil)
	r.do("POST", "/api/login", `{"hakbun":"30512","pin":"1234"}`)
	_, body := r.do("POST", "/api/print", `{"id":"a","name":"a"}`)
	blob := body[strings.Index(body, `"blob":"`)+8:]
	blob = blob[:strings.Index(blob, `"`)]
	r.do("POST", "/api/logout", "")
	if code, _ := r.do("GET", "/api/files", ""); code != 401 {
		t.Errorf("files after logout: %d", code)
	}
	if code, _ := r.do("GET", "/api/blob/"+blob, ""); code != 404 {
		t.Errorf("blob after logout: %d", code)
	}
}

func TestSessionExpiresWhenIdleAndTouchExtends(t *testing.T) {
	now := time.Now()
	s := NewSession(5 * time.Minute)
	s.now = func() time.Time { return now }
	s.Login("30512", "1234")
	now = now.Add(4*time.Minute + 58*time.Second)
	if got := s.Remaining(); got != 2*time.Second {
		t.Errorf("remaining %v", got)
	}
	s.Touch()
	now = now.Add(4 * time.Minute)
	if !s.SignedIn() {
		t.Error("touch should extend the session")
	}
	now = now.Add(2 * time.Minute)
	if s.SignedIn() {
		t.Error("should have expired")
	}
}

func TestOtherWayLogsOutAndTellsTheWindow(t *testing.T) {
	r := newRig(t)
	r.arch.add("30512", "1234", "a", "a.pdf", 10, nil, nil)
	r.do("POST", "/api/login", `{"hakbun":"30512","pin":"1234"}`)
	r.do("POST", "/api/other-way", "")
	if r.host.otherWay != 1 {
		t.Error("window not told")
	}
	if r.sess.SignedIn() {
		t.Error("still signed in")
	}
}

func TestAdminNeedsThePassword(t *testing.T) {
	r := newRig(t)
	if code, _ := r.do("POST", "/api/admin/check", `{"password":"nope"}`); code != 403 {
		t.Errorf("wrong password: %d", code)
	}
	if code, _ := r.do("POST", "/api/admin/maintenance", `{"password":"right-password","minutes":10}`); code != 200 {
		t.Errorf("right password: %d", code)
	}
	time.Sleep(600 * time.Millisecond)
	if r.host.exited != 1 {
		t.Error("window should exit after maintenance")
	}
}

func TestRecoverGivesThePinOnlyToInitialPasswordStudents(t *testing.T) {
	r := newRig(t)
	r.arch.add("30512", "1343", "a", "a.pdf", 10, nil, nil)
	r.arch.initial["30512"] = "홍길동"
	code, body := r.do("POST", "/api/recover", `{"hakbun":"30512","name":"홍길동"}`)
	if code != 200 || !strings.Contains(body, `"pin":"1343"`) {
		t.Fatalf("recover: %d %s", code, body)
	}
	if r.sess.SignedIn() {
		t.Error("recover alone must not sign in; the page signs in with the returned PIN")
	}
	if code, body := r.do("POST", "/api/recover", `{"hakbun":"30512","name":"다른사람"}`); code != 404 || strings.Contains(body, "1343") {
		t.Errorf("wrong name: %d %s", code, body)
	}
	if code, _ := r.do("POST", "/api/recover", `{"hakbun":"30512","name":""}`); code != 400 {
		t.Errorf("empty name: %d", code)
	}
}

func TestLimitFollowsTheServer(t *testing.T) {
	arch := newFakeArchive()
	t.Cleanup(arch.srv.Close)
	arch.maxSheet = 12
	srv := NewServer(NewAPI(arch.srv.URL), NewSession(SessionTimeout), &fakeHost{}, nil)
	if srv.maxSheets.Load() != DefaultMaxSheets {
		t.Fatal("starts at the default")
	}
	go srv.SyncSettings(time.Hour)
	deadline := time.Now().Add(3 * time.Second)
	for srv.maxSheets.Load() != 12 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if srv.maxSheets.Load() != 12 {
		t.Fatal("limit not synced")
	}
}

func TestFormat(t *testing.T) {
	cases := map[string]string{
		DisplayName("과제.pdf", 2*1024*1024):  "과제 [PDF,2MB]",
		DisplayName("영어 에세이.hwpx", 20*1024): "영어 에세이 [HWPX,20KB]",
		DisplayName("noext", 100):           "noext [1KB]",
		DisplayName("a.b.docx", 1536*1024):  "a.b [DOCX,1.5MB]",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
	for d, want := range map[time.Duration]string{6*24*time.Hour + time.Hour: "6d", 17*time.Hour + time.Minute: "17h", 150 * time.Second: "2m 30s", 45 * time.Second: "45s", -time.Second: "0s"} {
		if got := Remaining(d); got != want {
			t.Errorf("Remaining(%v)=%q want %q", d, got, want)
		}
	}
	if tm, ok := ParseTime("2026-10-21T14:00:00.123456Z"); !ok || tm.Hour() != 14 {
		t.Error("ParseTime")
	}
}
