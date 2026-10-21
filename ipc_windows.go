package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"time"

	"github.com/Microsoft/go-winio"

	"github.com/ddagebi07/gcfldrive-client/internal/drive"
	"github.com/ddagebi07/gcfldrive-client/internal/guard"
)

// Anyone signed in may talk to the service; what they may ask for is decided by
// the administrator password, not by who they are.
const pipeSDDL = "D:P(A;;GA;;;SY)(A;;GRGW;;;AU)"

func listenGuard() (net.Listener, error) {
	return winio.ListenPipe(guard.PipePath, &winio.PipeConfig{SecurityDescriptor: pipeSDDL, InputBufferSize: 4096, OutputBufferSize: 4096})
}

// serveGuard answers requests one JSON line at a time until the listener closes.
func serveGuard(ln net.Listener, handle func(guard.Request) guard.Response) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(30 * time.Second))
			line, err := bufio.NewReaderSize(c, 4096).ReadBytes('\n')
			if err != nil {
				return
			}
			var req guard.Request
			resp := guard.Response{Error: "요청을 읽지 못했어요."}
			if json.Unmarshal(line, &req) == nil {
				resp = handle(req)
			}
			_, _ = c.Write(guard.Encode(resp))
		}(conn)
	}
}

// askGuard sends one request to the service.
func askGuard(req guard.Request) error {
	timeout := 3 * time.Second
	conn, err := winio.DialPipe(guard.PipePath, &timeout)
	if err != nil {
		return errors.New("보호 서비스에 연결하지 못했어요.")
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	if _, err := conn.Write(guard.Encode(req)); err != nil {
		return errors.New("보호 서비스에 연결하지 못했어요.")
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return errors.New("보호 서비스가 응답하지 않아요.")
	}
	var resp guard.Response
	if json.Unmarshal(line, &resp) != nil {
		return errors.New("보호 서비스의 응답을 읽지 못했어요.")
	}
	if !resp.OK {
		return errors.New(resp.Error)
	}
	return nil
}

// pipeGuard is the window's way to the service (drive.Guard).
type pipeGuard struct{}

var _ drive.Guard = pipeGuard{}

func (pipeGuard) Check(pw string) error { return askGuard(guard.Request{Op: "check", Password: pw}) }
func (pipeGuard) Maintenance(pw string, minutes int) error {
	return askGuard(guard.Request{Op: "maintenance", Password: pw, Minutes: minutes})
}
func (pipeGuard) Uninstall(pw string) error {
	return askGuard(guard.Request{Op: "uninstall", Password: pw})
}
