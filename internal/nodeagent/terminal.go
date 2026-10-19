package nodeagent

// Terminal sessions opened by the server's superusers. They run only when
// this node's owner set VAD_NODE_TERMINAL=on (the server cannot change
// that), as VAD_NODE_TERMINAL_USER. Output is posted to the server as it
// arrives; input is long-polled.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/nodeapi"
)

// NewShell starts the shell of a terminal session (startShell; tests replace it).
var NewShell = startShell

// PTYSession is a shell on a pseudo terminal (pty_linux.go).
type PTYSession = ptySession

// ptySession is a shell on a pseudo terminal (pty_linux.go).
type ptySession interface {
	io.ReadWriter
	Resize(cols, rows int) error
	Kill()
	Wait() (exitCode int)
}

func (a *Agent) startTerminal(ctx context.Context, auth map[string]string, p nodeapi.JobPayload) error {
	if !a.cfg.Terminal {
		return errors.New("terminal access is turned off on this node (its owner can set VAD_NODE_TERMINAL=on in .env)")
	}
	if p.Session == "" {
		return errors.New("no session")
	}
	pty, err := NewShell(a.cfg.TerminalUser, max(p.Cols, 20), max(p.Rows, 5))
	if err != nil {
		return fmt.Errorf("cannot start a shell: %w", err)
	}
	a.log.Info("terminal opened by the server", "session", p.Session[:min(6, len(p.Session))], "user", a.cfg.TerminalUser)
	sctx, cancel := context.WithCancel(ctx)
	base := fmt.Sprintf("%s/api/v1/node/terminal/%s", a.cfg.ServerURL, p.Session)
	post := func(data []byte, exit string) {
		req, _ := http.NewRequestWithContext(context.WithoutCancel(sctx), "POST", base+"/output", bytes.NewReader(data))
		for k, v := range auth {
			req.Header.Set(k, v)
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		if exit != "" {
			req.Header.Set("X-Terminal-Exit", exit)
		}
		if resp, err := a.http.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 404 {
				cancel()
			}
		}
	}
	// Output: post what the shell writes; end when it exits.
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := pty.Read(buf)
			if n > 0 {
				post(append([]byte(nil), buf[:n]...), "")
			}
			if err != nil {
				break
			}
		}
		code := pty.Wait()
		post(nil, fmt.Sprintf("the shell exited (code %d)", code))
		cancel()
	}()
	// Input: long poll; stop on close or when the session is gone.
	go func() {
		defer pty.Kill()
		client := &http.Client{Timeout: 60 * time.Second}
		for sctx.Err() == nil {
			req, _ := http.NewRequestWithContext(sctx, "GET", base+"/input", nil)
			for k, v := range auth {
				req.Header.Set(k, v)
			}
			resp, err := client.Do(req)
			if err != nil {
				if !sleep(sctx, 2*time.Second) {
					return
				}
				continue
			}
			var in nodeapi.TerminalInput
			code := resp.StatusCode
			if code == 200 {
				_ = json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&in)
			}
			resp.Body.Close()
			switch {
			case code == 404 || code == 401:
				return
			case code != 200:
				continue
			}
			if len(in.Data) > 0 {
				_, _ = pty.Write(in.Data)
			}
			if in.Cols > 0 && in.Rows > 0 {
				_ = pty.Resize(in.Cols, in.Rows)
			}
			if in.Close {
				return
			}
		}
	}()
	return nil
}

// shellPath picks the login shell to run.
func shellPath() string {
	for _, s := range []string{"/bin/bash", "/usr/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(s); err == nil {
			return s
		}
	}
	return "/bin/sh"
}
