package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type release struct {
	srv  *httptest.Server
	pub  string
	priv ed25519.PrivateKey
	man  Manifest
	exe  []byte
}

func newRelease(t *testing.T, version string, exe []byte) *release {
	pub, priv, _ := ed25519.GenerateKey(nil)
	sum := sha256.Sum256(exe)
	r := &release{pub: base64.StdEncoding.EncodeToString(pub), priv: priv, exe: exe,
		man: Manifest{Version: version, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(exe))}}
	r.man.Sign(priv)
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/latest/download/latest.json":
			_ = json.NewEncoder(w).Encode(r.man)
		case "/download/" + r.man.Version + "/gcfldrive.exe":
			_, _ = w.Write(r.exe)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *release) checker() *Checker {
	return &Checker{Base: r.srv.URL, PublicKey: r.pub, Client: r.srv.Client()}
}

func TestNewerSignedReleaseIsDownloaded(t *testing.T) {
	r := newRelease(t, "v1.10.0", []byte("new program"))
	m, err := r.checker().Latest(context.Background(), "v1.9.3")
	if err != nil || m == nil || m.Version != "v1.10.0" {
		t.Fatalf("latest: %v %v", m, err)
	}
	body, err := r.checker().Download(context.Background(), m)
	if err != nil || string(body) != "new program" {
		t.Fatalf("download: %q %v", body, err)
	}
}

func TestNothingToDo(t *testing.T) {
	r := newRelease(t, "v1.2.0", []byte("x"))
	for _, cur := range []string{"v1.2.0", "v1.3.0", "dev", ""} {
		if m, err := r.checker().Latest(context.Background(), cur); m != nil || err != nil {
			t.Errorf("current %q: %v %v", cur, m, err)
		}
	}
	c := r.checker()
	c.PublicKey = ""
	if m, _ := c.Latest(context.Background(), "v1.0.0"); m != nil {
		t.Error("no public key must mean no updates")
	}
}

func TestForgedReleasesAreRefused(t *testing.T) {
	r := newRelease(t, "v2.0.0", []byte("good"))
	_, other, _ := ed25519.GenerateKey(nil)

	r.man.Sign(other) // signed by someone else
	if _, err := r.checker().Latest(context.Background(), "v1.0.0"); err == nil {
		t.Error("foreign signature accepted")
	}
	r.man.Sign(r.priv)
	r.man.Version = "v9.0.0" // version changed after signing
	if _, err := r.checker().Latest(context.Background(), "v1.0.0"); err == nil {
		t.Error("edited manifest accepted")
	}
	r.man.Version = "v2.0.0"
	m, err := r.checker().Latest(context.Background(), "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	r.exe = []byte("evil") // same size, different bytes
	if _, err := r.checker().Download(context.Background(), m); err == nil {
		t.Error("swapped program accepted")
	}
}
