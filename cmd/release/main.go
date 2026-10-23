// Command release makes the signing key once and signs each release:
//
//	go run ./cmd/release keygen                    # once: release.key (keep it secret) + internal/update/publickey.txt (commit it)
//	go run ./cmd/release sign v1.2.0 dist/gcfldrive.exe   # writes dist/latest.json
//
// Upload gcfldrive.exe and latest.json to a GitHub release tagged with the same
// version; every installed PC picks it up within a few hours.
package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ddagebi07/gcfldrive-client/internal/update"
)

const (
	keyFile = "release.key"
	pubFile = "internal/update/publickey.txt"
)

func main() {
	var err error
	switch {
	case len(os.Args) == 2 && os.Args[1] == "keygen":
		err = keygen()
	case len(os.Args) == 4 && os.Args[1] == "sign":
		err = sign(os.Args[2], os.Args[3])
	default:
		err = errors.New("사용법: go run ./cmd/release keygen | sign <v1.2.3> <gcfldrive.exe>")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func keygen() error {
	if _, err := os.Stat(keyFile); err == nil {
		return fmt.Errorf("%s 가 이미 있어요. 키를 바꾸면 설치된 PC가 새 버전을 믿지 않으니 지우지 마세요.", keyFile)
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return err
	}
	if err := os.WriteFile(keyFile, []byte(base64.StdEncoding.EncodeToString(priv.Seed())+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(pubFile, []byte(base64.StdEncoding.EncodeToString(pub)+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Printf("%s(비밀 키)와 %s(공개 키)를 만들었어요.\n", keyFile, pubFile)
	fmt.Printf("%s 는 저장소에 올리지 말고 안전한 곳에 백업해 두세요. 공개 키 파일은 커밋하세요.\n", keyFile)
	return nil
}

func sign(version, exe string) error {
	if !strings.HasPrefix(version, "v") {
		return errors.New("버전은 v1.2.3 처럼 적어 주세요.")
	}
	raw, err := os.ReadFile(keyFile)
	if err != nil {
		return fmt.Errorf("%s 가 없어요. 먼저 keygen 을 하세요.", keyFile)
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return fmt.Errorf("%s 를 읽지 못했어요.", keyFile)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	if got := base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)); got != update.PublicKey() {
		return fmt.Errorf("%s 와 %s 의 키가 맞지 않아요.", keyFile, pubFile)
	}
	body, err := os.ReadFile(exe)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	m := update.Manifest{Version: version, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}
	m.Sign(priv)
	if err := m.Verify(update.PublicKey()); err != nil {
		return err
	}
	out, _ := json.MarshalIndent(m, "", "  ")
	dst := filepath.Join(filepath.Dir(exe), "latest.json")
	if err := os.WriteFile(dst, append(out, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("%s 를 만들었어요. GitHub 릴리스 %s 에 gcfldrive.exe 와 함께 올리세요.\n", dst, version)
	return nil
}
