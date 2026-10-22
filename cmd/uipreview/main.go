// Command uipreview serves the launcher page against a stub archive so the
// screens can be looked at in any browser (no Windows needed):
//
//	go run ./cmd/uipreview     # prints the URL to open
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/ddagebi07/gcfldrive-client/internal/drive"
)

type host struct{}

func (host) OtherWay()   { fmt.Println("other way") }
func (host) Exit()       { fmt.Println("exit") }
func (host) NoticeDone() { fmt.Println("notice done") }

func main() {
	iso := func(d time.Duration) string {
		return time.Now().Add(d).UTC().Format("2006-01-02T15:04:05.000000") + "Z"
	}
	row := func(id, name string, size int, expires time.Duration, del *time.Duration) string {
		d := "null"
		if del != nil {
			d = `"` + iso(*del) + `"`
		}
		return fmt.Sprintf(`{"file_id":%q,"filename":%q,"file_size":%d,"uploaded_at":%q,"expires_at":%q,"short_code":%q,"downloaded_at":null,"delete_at":%s}`,
			id, name, size, iso(0), iso(expires), id, d)
	}
	soon := 150 * time.Second
	files := strings.Join([]string{
		row("a", "창체 시간 보고서.pdf", 2<<20, 6*24*time.Hour, &soon),
		row("b", "생명과학 탐구 계획서.docx", 80<<10, 17*time.Hour, nil),
		row("c", "창체 시간 활동지.hwp", 40<<10, 5*24*time.Hour, nil),
		row("d", "가정통신문-진로창체부-2017-05호-아름다운 학교 만들기 캠페인.pdf", 300<<10, 7*24*time.Hour, nil),
		row("e", "영어 에세이.hwpx", 20<<10, 7*24*time.Hour, nil),
	}, ",")
	arch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/meal-menu":
			fmt.Fprint(w, `{"success":true,"meals":{"중식":["잡곡밥","미역국","닭갈비","콩나물무침","배추김치","깍두기","과일샐러드","요구르트"],"석식":["카레라이스","우동국물","군만두","단무지","포기김치"]}}`)
		case r.URL.Path == "/api/share/banners":
			fmt.Fprint(w, `{"banners":[{"id":"aaaaaaaaaaaaaaaa","url":"/api/share/banners/aaaaaaaaaaaaaaaa.jpg"}]}`)
		case strings.HasPrefix(r.URL.Path, "/api/share/banners/"):
			w.Header().Set("Content-Type", "image/jpeg")
			img := image.NewRGBA(image.Rect(0, 0, 720, 450))
			for y := 0; y < 450; y++ {
				for x := 0; x < 720; x++ {
					img.Set(x, y, color.RGBA{uint8(40 + x/6), uint8(90 + y/6), 220, 255})
				}
			}
			_ = jpeg.Encode(w, img, nil)
		case r.URL.Path == "/api/share/settings":
			fmt.Fprint(w, `{"maxSheets":7}`)
		case strings.Contains(r.URL.Path, "/file/") && os.Getenv("UIPREVIEW_PDF") != "":
			body, _ := os.ReadFile(os.Getenv("UIPREVIEW_PDF"))
			w.Header().Set("X-Drive-Kind", "pdf")
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write(body)
		case strings.HasSuffix(r.URL.Path, "/30512/1234"):
			fmt.Fprintf(w, `{"status":"ok","files":[%s]}`, files)
		case strings.HasSuffix(r.URL.Path, "/30513/0000"):
			fmt.Fprint(w, `{"status":"ok","files":[]}`)
		default:
			w.WriteHeader(403)
			fmt.Fprint(w, `{"error":"PIN이 맞지 않아요. 다시 입력해주세요."}`)
		}
	}))
	defer arch.Close()
	srv := drive.NewServer(drive.NewAPI(arch.URL), drive.NewSession(drive.SessionTimeout), host{}, nil)
	url, err := srv.Listen()
	if err != nil {
		panic(err)
	}
	if len(os.Args) > 1 && os.Args[1] == "full" {
		srv.SetFullscreen(true)
	}
	fmt.Println(url)
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	<-c
}
