package drive

import (
	"fmt"
	"math"
	"path"
	"strings"
	"time"
)

// DisplayName is what a row shows: the name without its extension and the
// extension and size in brackets, e.g. "과제 [PDF,2MB]".
func DisplayName(filename string, size int64) string {
	ext := path.Ext(filename)
	base := strings.TrimSuffix(filename, ext)
	if base == "" {
		base = filename
		ext = ""
	}
	label := strings.ToUpper(strings.TrimPrefix(ext, "."))
	if label == "" {
		return fmt.Sprintf("%s [%s]", base, FormatSize(size))
	}
	return fmt.Sprintf("%s [%s,%s]", base, label, FormatSize(size))
}

// FormatSize is KB/MB without a space ("20KB", "2MB", "1.5MB"); anything under
// 1KB counts as 1KB.
func FormatSize(size int64) string {
	const kb, mb = 1024, 1024 * 1024
	if size < mb {
		return fmt.Sprintf("%dKB", max(1, int64(math.Ceil(float64(size)/kb))))
	}
	v := float64(size) / mb
	if v >= 10 || math.Abs(v-math.Round(v)) < 0.05 {
		return fmt.Sprintf("%dMB", int64(math.Round(v)))
	}
	return fmt.Sprintf("%.1fMB", v)
}

// ParseTime reads the server's ISO timestamps ("2026-10-21T14:00:00.123456Z").
func ParseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// Remaining is the badge text of a row: "6d", "17h", "2m 30s", "45s".
func Remaining(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d >= time.Minute:
		return fmt.Sprintf("%dm %ds", int(d/time.Minute), int(d%time.Minute/time.Second))
	default:
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
}
