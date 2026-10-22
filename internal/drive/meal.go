package drive

import (
	"fmt"
	"sync"
	"time"
)

// KST is Korea Standard Time. Korea has no daylight saving time, so a fixed zone
// is exact and needs no time zone database on the PC.
var KST = time.FixedZone("KST", 9*3600)

var mealOrder = []string{"조식", "중식", "석식"}

// mealOver reports whether a meal on the given day has already passed at `now`. The
// rule follows the school website's home widget: 중식 until 12:00, 석식 until 19:00
// (so from 19:00 the next meal is tomorrow's 중식).
func mealOver(now time.Time, mealType string) bool {
	minutes := now.Hour()*60 + now.Minute()
	switch mealType {
	case "조식":
		return minutes >= 8*60+30
	case "중식":
		return minutes > 12*60
	default:
		return now.Hour() >= 19
	}
}

// MealSlide is one meal on the carousel.
type MealSlide struct {
	Label  string   `json:"label"` // "오늘 중식"
	Date   string   `json:"date"`  // "10월 22일 (목)"
	Dishes []string `json:"dishes"`
}

var weekdays = []string{"일", "월", "화", "수", "목", "금", "토"}

func dayWord(offset int) string {
	switch offset {
	case 0:
		return "오늘"
	case 1:
		return "내일"
	case 2:
		return "모레"
	}
	return ""
}

// NextMeals picks the next `count` meals from `now` on, skipping days without meals
// (weekends, holidays). getMenu returns one day's menu for a yyyymmdd date.
func NextMeals(now time.Time, count int, getMenu func(date string) (map[string][]string, error)) ([]MealSlide, error) {
	now = now.In(KST)
	var slides []MealSlide
	var firstErr error
	for offset := 0; offset <= 10 && len(slides) < count; offset++ {
		day := now.AddDate(0, 0, offset)
		menu, err := getMenu(day.Format("20060102"))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, mealType := range mealOrder {
			dishes := menu[mealType]
			if len(dishes) == 0 || (offset == 0 && mealOver(now, mealType)) {
				continue
			}
			label := mealType
			if w := dayWord(offset); w != "" {
				label = w + " " + mealType
			} else {
				label = weekdays[day.Weekday()] + "요일 " + mealType
			}
			slides = append(slides, MealSlide{
				Label:  label,
				Date:   fmt.Sprintf("%d월 %d일 (%s)", int(day.Month()), day.Day(), weekdays[day.Weekday()]),
				Dishes: dishes,
			})
			if len(slides) == count {
				break
			}
		}
	}
	if len(slides) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return slides, nil
}

// menuCache keeps day menus for a while so the carousel does not ask on every refresh.
type menuCache struct {
	mu   sync.Mutex
	days map[string]cachedMenu
}

type cachedMenu struct {
	at   time.Time
	menu map[string][]string
}

func (c *menuCache) get(date string, ttl time.Duration, load func(string) (map[string][]string, error)) (map[string][]string, error) {
	c.mu.Lock()
	if hit, ok := c.days[date]; ok && time.Since(hit.at) < ttl {
		c.mu.Unlock()
		return hit.menu, nil
	}
	c.mu.Unlock()
	menu, err := load(date)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.days == nil {
		c.days = map[string]cachedMenu{}
	}
	c.days[date] = cachedMenu{at: time.Now(), menu: menu}
	c.mu.Unlock()
	return menu, nil
}
