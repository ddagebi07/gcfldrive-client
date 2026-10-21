package drive

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// SessionTimeout is how long a login survives without any use of the screen.
const SessionTimeout = 5 * time.Minute

// Session is the one PIN entered on this PC. The PIN lives only here, in
// memory; the web page never holds it.
type Session struct {
	mu       sync.Mutex
	timeout  time.Duration
	now      func() time.Time
	pin      string
	last     time.Time
	blobs    map[string]*Blob
	onLogout func()
}

// Blob is a downloaded file waiting to be printed by the page.
type Blob struct {
	Kind        string
	ContentType string
	Name        string
	Body        []byte
}

func NewSession(timeout time.Duration) *Session {
	return &Session{timeout: timeout, now: time.Now, blobs: map[string]*Blob{}}
}

// OnLogout registers a callback run (outside the lock) every time a login ends.
func (s *Session) OnLogout(f func()) { s.onLogout = f }

func (s *Session) Login(pin string) {
	s.mu.Lock()
	s.pin, s.last = pin, s.now()
	s.blobs = map[string]*Blob{}
	s.mu.Unlock()
}

// PIN returns the PIN in use, or ok=false if nobody is signed in (or it just expired).
func (s *Session) PIN() (pin string, ok bool) {
	s.expireIfIdle()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pin, s.pin != ""
}

// Touch records use of the screen and pushes the logout time back.
func (s *Session) Touch() {
	s.mu.Lock()
	if s.pin != "" {
		s.last = s.now()
	}
	s.mu.Unlock()
}

// Remaining is the time left until automatic logout (0 when signed out).
func (s *Session) Remaining() time.Duration {
	s.expireIfIdle()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pin == "" {
		return 0
	}
	return max(0, s.timeout-s.now().Sub(s.last))
}

func (s *Session) expireIfIdle() {
	s.mu.Lock()
	idle := s.pin != "" && s.now().Sub(s.last) >= s.timeout
	s.mu.Unlock()
	if idle {
		s.Logout()
	}
}

// Logout forgets the PIN and every downloaded file.
func (s *Session) Logout() {
	s.mu.Lock()
	was := s.pin != ""
	s.pin = ""
	for _, b := range s.blobs {
		clear(b.Body)
	}
	s.blobs = map[string]*Blob{}
	cb := s.onLogout
	s.mu.Unlock()
	if was && cb != nil {
		cb()
	}
}

// Hold stores a downloaded file and returns the one-time id the page fetches it by.
func (s *Session) Hold(b *Blob) string {
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	id := hex.EncodeToString(raw)
	s.mu.Lock()
	s.blobs[id] = b
	s.mu.Unlock()
	return id
}

func (s *Session) Blob(id string) (*Blob, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.blobs[id]
	return b, ok
}

// Drop frees one downloaded file once it was printed.
func (s *Session) Drop(id string) {
	s.mu.Lock()
	if b, ok := s.blobs[id]; ok {
		clear(b.Body)
		delete(s.blobs, id)
	}
	s.mu.Unlock()
}
