//go:build !windows

package main

import "fmt"

func main() {
	fmt.Println("극플드라이브 클라이언트는 Windows 전용이에요. 화면 확인은 `go run ./cmd/uipreview`")
}
