package drive

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func kst(y int, m time.Month, d, h, min int) time.Time { return time.Date(y, m, d, h, min, 0, 0, KST) }

// A school week: Mon 2026-10-19 .. Fri 2026-10-23 have 중식·석식, the weekend has nothing.
func weekMenu(date string) (map[string][]string, error) {
	switch date {
	case "20261019", "20261020", "20261021", "20261022", "20261023", "20261026":
		return map[string][]string{"중식": {"잡곡밥", date + "국"}, "석식": {"카레라이스"}}, nil
	}
	return map[string][]string{}, nil
}

func labels(s []MealSlide) string {
	var out []string
	for _, m := range s {
		out = append(out, m.Label)
	}
	return strings.Join(out, ", ")
}

func TestNextTwoMealsFollowTheHomeWidgetRule(t *testing.T) {
	cases := []struct {
		now  time.Time
		want string
	}{
		{kst(2026, 10, 22, 7, 0), "오늘 중식, 오늘 석식"},   // 전날 19시 ~ 당일 12시
		{kst(2026, 10, 21, 19, 0), "내일 중식, 내일 석식"},  // 19시가 되면 내일 것
		{kst(2026, 10, 22, 12, 0), "오늘 중식, 오늘 석식"},  // 12시 정각까지는 중식
		{kst(2026, 10, 22, 12, 1), "오늘 석식, 내일 중식"},  // 12시~19시
		{kst(2026, 10, 22, 18, 59), "오늘 석식, 내일 중식"}, // 19시 전까지 석식
		{kst(2026, 10, 22, 19, 0), "내일 중식, 내일 석식"},
		{kst(2026, 10, 23, 15, 0), "오늘 석식, 월요일 중식"},  // 금요일 오후: 주말 건너뛰기
		{kst(2026, 10, 23, 20, 0), "월요일 중식, 월요일 석식"}, // 금요일 저녁
		{kst(2026, 10, 24, 10, 0), "모레 중식, 모레 석식"},   // 토요일: 월요일은 모레(+2)
	}
	for _, c := range cases {
		got, err := NextMeals(c.now, 2, weekMenu)
		if err != nil || labels(got) != c.want {
			t.Errorf("%s: got %q (%v), want %q", c.now.Format("01-02 15:04 Mon"), labels(got), err, c.want)
		}
	}
}

func TestNextMealsUsesKoreanTimeAndFormatsTheDate(t *testing.T) {
	// 2026-10-22 03:00 UTC is 12:00 KST: still 중식.
	got, _ := NextMeals(time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC), 2, weekMenu)
	if labels(got) != "오늘 중식, 오늘 석식" || got[0].Date != "10월 22일 (목)" {
		t.Errorf("got %q %q", labels(got), got[0].Date)
	}
}

func TestNextMealsErrors(t *testing.T) {
	down := func(string) (map[string][]string, error) { return nil, errors.New("down") }
	if _, err := NextMeals(kst(2026, 10, 22, 7, 0), 2, down); err == nil {
		t.Error("all days failing must be an error")
	}
	// one day failing does not hide the others
	flaky := func(d string) (map[string][]string, error) {
		if d == "20261022" {
			return nil, errors.New("down")
		}
		return weekMenu(d)
	}
	got, err := NextMeals(kst(2026, 10, 22, 7, 0), 2, flaky)
	if err != nil || labels(got) != "내일 중식, 내일 석식" {
		t.Errorf("got %q %v", labels(got), err)
	}
	// nothing for 11 days: no slides, no error
	none := func(string) (map[string][]string, error) { return map[string][]string{}, nil }
	if got, err := NextMeals(kst(2026, 10, 22, 7, 0), 2, none); err != nil || len(got) != 0 {
		t.Errorf("got %v %v", got, err)
	}
}

func TestBannerEndpointMixesMealsAndLibrariansImages(t *testing.T) {
	r := newRig(t)
	today := time.Now().In(KST)
	// today's and tomorrow's menus, whatever day the test runs on
	for i := 0; i <= 10; i++ {
		d := today.AddDate(0, 0, i).Format("20060102")
		r.arch.menus[d] = map[string][]string{"중식": {"잡곡밥"}, "석식": {"카레"}}
	}
	r.arch.banners = []string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"}
	r.arch.images["aaaaaaaaaaaaaaaa"] = []byte("\xff\xd8\xffA")
	r.arch.images["bbbbbbbbbbbbbbbb"] = []byte("\xff\xd8\xffB")

	code, body := r.do("GET", "/api/banner", "")
	if code != 200 || strings.Count(body, `"kind":"meal"`) != 2 || strings.Count(body, `"kind":"image"`) != 2 {
		t.Fatalf("banner: %d %s", code, body)
	}
	if code, img := r.do("GET", "/api/banner/img/aaaaaaaaaaaaaaaa", ""); code != 200 || img != "\xff\xd8\xffA" {
		t.Errorf("image: %d %q", code, img)
	}
	// the librarian removes one: it disappears from the carousel and from the cache
	r.arch.banners = []string{"bbbbbbbbbbbbbbbb"}
	_, body = r.do("GET", "/api/banner", "")
	if strings.Count(body, `"kind":"image"`) != 1 {
		t.Errorf("after removal: %s", body)
	}
	if code, _ := r.do("GET", "/api/banner/img/aaaaaaaaaaaaaaaa", ""); code != 404 {
		t.Errorf("removed image still served: %d", code)
	}
}
