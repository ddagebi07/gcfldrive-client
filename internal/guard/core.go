// Package guard holds the parts of the protection service that do not depend on
// Windows: the administrator password, the request protocol the window speaks to
// the service, and the paper limit rule. The Windows glue lives in package main.
package guard

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	ServiceName        = "GCFLDriveGuard"
	ServiceDisplayName = "극플드라이브 보호 서비스"
	PipePath           = `\\.\pipe\gcfldrive-guard`
	MaxSheetsPerJob    = 7 // 학교 방침: 1회 출력 종이 7장까지
	MinPasswordLength  = 8
	pbkdf2Iterations   = 210000
)

// Config is what the service keeps on disk (readable by SYSTEM only).
type Config struct {
	Salt       string `json:"salt"`
	Hash       string `json:"hash"`
	Iterations int    `json:"iterations"`
}

func hashPassword(password string, salt []byte, iter int) ([]byte, error) {
	return pbkdf2.Key(sha256.New, password, salt, iter, 32)
}

// NewConfig derives the stored form of an administrator password.
func NewConfig(password string) (*Config, error) {
	if len([]rune(password)) < MinPasswordLength {
		return nil, fmt.Errorf("비밀번호는 %d자 이상이어야 해요.", MinPasswordLength)
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	h, err := hashPassword(password, salt, pbkdf2Iterations)
	if err != nil {
		return nil, err
	}
	return &Config{Salt: base64.StdEncoding.EncodeToString(salt), Hash: base64.StdEncoding.EncodeToString(h), Iterations: pbkdf2Iterations}, nil
}

// Verify reports whether the password matches (constant time).
func (c *Config) Verify(password string) bool {
	salt, err1 := base64.StdEncoding.DecodeString(c.Salt)
	want, err2 := base64.StdEncoding.DecodeString(c.Hash)
	if err1 != nil || err2 != nil || c.Iterations < 1000 {
		return false
	}
	got, err := hashPassword(password, salt, c.Iterations)
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

// Request is one line of JSON the window (or the command line) sends to the service.
type Request struct {
	Op       string `json:"op"` // check | maintenance | uninstall | resume | ping
	Password string `json:"password,omitempty"`
	Minutes  int    `json:"minutes,omitempty"`
}

// Response answers a Request. Error is shown to the person as is.
type Response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func Encode(v any) []byte { b, _ := json.Marshal(v); return append(b, '\n') }

// Limiter slows down password guessing: after MaxFails wrong passwords in a row
// everything is refused for Lockout, and each wrong try costs a second.
type Limiter struct {
	MaxFails int
	Lockout  time.Duration
	Now      func() time.Time

	mu    sync.Mutex
	fails int
	until time.Time
}

func NewLimiter() *Limiter { return &Limiter{MaxFails: 5, Lockout: 5 * time.Minute, Now: time.Now} }

var ErrLocked = errors.New("비밀번호를 여러 번 틀려서 잠시 잠겼어요. 5분 뒤에 다시 시도해 주세요.")

// Allow says whether a password try may be made now.
func (l *Limiter) Allow() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.Now().Before(l.until) {
		return ErrLocked
	}
	return nil
}

// Record notes the result of a try and reports whether the delay penalty applies.
func (l *Limiter) Record(ok bool) (penalty time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if ok {
		l.fails = 0
		return 0
	}
	l.fails++
	if l.fails >= l.MaxFails {
		l.until = l.Now().Add(l.Lockout)
		l.fails = 0
	}
	return time.Second
}

// Sheets estimates the paper a print job uses. totalPages are the document's
// pages as the spooler counts them, copies the number of copies and duplex
// whether it prints on both sides.
func Sheets(totalPages, copies int, duplex bool) int {
	if totalPages <= 0 {
		return 0
	}
	if copies < 1 {
		copies = 1
	}
	perCopy := totalPages
	if duplex {
		perCopy = (totalPages + 1) / 2
	}
	return perCopy * copies
}

// OverLimit is true when a job uses more paper than the school allows.
func OverLimit(totalPages, copies int, duplex bool) bool {
	return Sheets(totalPages, copies, duplex) > MaxSheetsPerJob
}
