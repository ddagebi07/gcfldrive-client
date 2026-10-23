//go:build !windows

package main

import "fmt"

func main() {
	fmt.Println("gcfldrive-setup.exe 는 Windows 전용이에요. build.sh 로 만드세요.")
}
