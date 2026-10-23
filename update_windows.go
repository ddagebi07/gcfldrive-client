package main

import (
	"context"
	"math/rand/v2"
	"os"
	"time"

	"github.com/ddagebi07/gcfldrive-client/internal/update"
)

const updateEvery = 6 * time.Hour

// autoUpdate checks GitHub for a newer signed release a few minutes after the
// service starts and then every few hours. When it put one in place it closes
// updated and returns; the service then restarts itself from the new file.
// PCs in a school share one internet address, so the checks are spread out.
func (g *guardService) autoUpdate(updated chan<- struct{}) {
	_ = os.Remove(installExe + ".old") // left by the last update; in use until that process ended
	c := update.NewChecker()
	wait := time.Duration(2+rand.IntN(10)) * time.Minute
	for {
		time.Sleep(wait)
		wait = updateEvery + time.Duration(rand.IntN(60))*time.Minute
		if g.isUninstalling() {
			return
		}
		if g.updateOnce(c) {
			close(updated)
			return
		}
	}
}

func (g *guardService) updateOnce(c *update.Checker) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	m, err := c.Latest(ctx, version)
	if err != nil || m == nil {
		return false
	}
	body, err := c.Download(ctx, m)
	if err != nil {
		return false
	}
	defer clear(body)
	return installUpdate(body) == nil
}

// installUpdate puts the new program in place of the running one. The backup
// goes first: harden() copies the backup over a program that differs from it,
// so after a crash halfway the PC still ends up with the new version.
func installUpdate(body []byte) error {
	if err := writeFile(backupExe, body); err != nil {
		return err
	}
	// A running program cannot be overwritten, but it can be renamed out of the way.
	_ = os.Remove(installExe + ".old")
	if err := os.Rename(installExe, installExe+".old"); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := writeFile(installExe, body); err != nil {
		_ = os.Rename(installExe+".old", installExe)
		return err
	}
	return nil
}

func writeFile(dst string, body []byte) error {
	tmp := dst + ".update" // not ".new": harden's copyFile may be writing that one at the same time
	if err := os.WriteFile(tmp, body, 0o755); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
