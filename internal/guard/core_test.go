package guard

import (
	"os"
	"testing"
	"time"
)

func TestPasswordRoundTrip(t *testing.T) {
	if _, err := NewConfig("short"); err == nil {
		t.Fatal("short password accepted")
	}
	c, err := NewConfig("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Verify("correct horse") || c.Verify("correct horse ") || c.Verify("") {
		t.Fatal("verify wrong")
	}
	d, _ := NewConfig("correct horse")
	if c.Hash == d.Hash || c.Salt == d.Salt {
		t.Fatal("salt not random")
	}
}

func TestLimiterLocksAfterFiveFailsAndReleases(t *testing.T) {
	now := time.Now()
	l := NewLimiter()
	l.Now = func() time.Time { return now }
	for i := 0; i < 4; i++ {
		if l.Allow() != nil {
			t.Fatal("locked too early")
		}
		l.Record(false)
	}
	l.Record(false)
	if l.Allow() != ErrLocked {
		t.Fatal("should be locked")
	}
	now = now.Add(5*time.Minute + time.Second)
	if l.Allow() != nil {
		t.Fatal("should be open again")
	}
	l.Record(false)
	l.Record(true)
	for i := 0; i < 4; i++ {
		l.Record(false)
	}
	if l.Allow() != nil {
		t.Fatal("a right password resets the count")
	}
}

func TestSheets(t *testing.T) {
	cases := []struct {
		pages, copies int
		duplex        bool
		sheets        int
		over          bool
	}{
		{7, 1, false, 7, false},
		{8, 1, false, 8, true},
		{14, 1, true, 7, false},
		{15, 1, true, 8, true},
		{3, 3, false, 9, true},
		{3, 2, true, 4, false},
		{0, 1, false, 0, false},
		{5, 0, false, 5, false},
	}
	for _, c := range cases {
		if got := Sheets(c.pages, c.copies, c.duplex); got != c.sheets {
			t.Errorf("Sheets(%d,%d,%v)=%d want %d", c.pages, c.copies, c.duplex, got, c.sheets)
		}
		if OverLimit(c.pages, c.copies, c.duplex) != c.over {
			t.Errorf("OverLimit(%d,%d,%v) want %v", c.pages, c.copies, c.duplex, c.over)
		}
	}
}

// GCFL_TEST_PW holds the real default password; the test is skipped without it so
// the password never has to be written into the repository.
func TestDefaultConfigMatchesThePassword(t *testing.T) {
	pw := os.Getenv("GCFL_TEST_PW")
	if pw == "" {
		t.Skip("GCFL_TEST_PW not set")
	}
	c := DefaultConfig()
	if !c.Verify(pw) || c.Verify(pw+"x") || c.Verify("") {
		t.Fatal("default config does not match the password")
	}
}
