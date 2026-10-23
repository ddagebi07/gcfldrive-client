// Package update finds and downloads a newer version of the program. It does
// not depend on Windows: the protection service puts the file in place.
//
// A release on GitHub carries two files: gcfldrive.exe and latest.json, a
// manifest signed with the maintainer's private key (go run ./cmd/release). The
// program only trusts a file whose SHA-256 is in a manifest signed by the key
// in publickey.txt, because the service runs it as SYSTEM on every PC.
package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultBase is where releases are published. /latest/download/ always points
// at the newest non-draft, non-prerelease release and is not rate limited like the API.
const DefaultBase = "https://github.com/ddagebi07/gcfldrive-client/releases"

// MaxSize bounds the download (the program is about 10MB).
const MaxSize = 64 << 20

//go:embed publickey.txt
var publicKeyText string

// PublicKey is the maintainer's ed25519 key (base64), or empty when no key was
// made yet: then the program never updates itself.
func PublicKey() string { return strings.TrimSpace(publicKeyText) }

// Manifest is latest.json.
type Manifest struct {
	Version string `json:"version"` // a tag like v1.2.0
	SHA256  string `json:"sha256"`  // of gcfldrive.exe, hex
	Size    int64  `json:"size"`
	Sig     string `json:"sig"` // base64 ed25519 over SignedText()
}

// SignedText is what the signature covers: the version too, so an old
// release's manifest cannot be passed off as a new one.
func (m *Manifest) SignedText() []byte {
	return []byte("gcfldrive-update\n" + m.Version + "\n" + strings.ToLower(m.SHA256) + "\n" + strconv.FormatInt(m.Size, 10) + "\n")
}

// Sign fills in Sig (used by cmd/release).
func (m *Manifest) Sign(priv ed25519.PrivateKey) {
	m.Sig = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, m.SignedText()))
}

// Verify checks the signature with a base64 public key.
func (m *Manifest) Verify(publicKey string) error {
	pub, err := base64.StdEncoding.DecodeString(publicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("update: no valid public key")
	}
	sig, err := base64.StdEncoding.DecodeString(m.Sig)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(pub), m.SignedText(), sig) {
		return errors.New("update: bad signature")
	}
	if _, ok := parseVersion(m.Version); !ok || m.Size <= 0 || m.Size > MaxSize {
		return errors.New("update: bad manifest")
	}
	return nil
}

// Checker talks to the release site.
type Checker struct {
	Base      string // DefaultBase unless testing
	PublicKey string // base64
	Client    *http.Client
}

func NewChecker() *Checker {
	return &Checker{Base: DefaultBase, PublicKey: PublicKey(), Client: &http.Client{Timeout: 10 * time.Minute}}
}

// Latest returns the newest release if it is newer than current and properly
// signed, or nil when there is nothing to do. A "dev" build never updates.
func (c *Checker) Latest(ctx context.Context, current string) (*Manifest, error) {
	cur, ok := parseVersion(current)
	if !ok || c.PublicKey == "" {
		return nil, nil
	}
	raw, err := c.get(ctx, c.Base+"/latest/download/latest.json", 1<<16)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("update: manifest: %w", err)
	}
	if err := m.Verify(c.PublicKey); err != nil {
		return nil, err
	}
	next, _ := parseVersion(m.Version)
	if !newer(next, cur) {
		return nil, nil
	}
	return &m, nil
}

// Download fetches the release's program and checks it against the manifest.
func (c *Checker) Download(ctx context.Context, m *Manifest) ([]byte, error) {
	body, err := c.get(ctx, c.Base+"/download/"+m.Version+"/gcfldrive.exe", MaxSize)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	if int64(len(body)) != m.Size || !strings.EqualFold(hex.EncodeToString(sum[:]), m.SHA256) {
		return nil, errors.New("update: the download does not match the signed manifest")
	}
	return body, nil
}

func (c *Checker) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update: %s: %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("update: response too large")
	}
	return body, nil
}

// parseVersion reads "v1.2.3" (the "v" is optional).
func parseVersion(s string) ([3]int, bool) {
	var v [3]int
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(s), "v"), ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

func newer(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}
