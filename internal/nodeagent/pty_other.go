//go:build !linux

package nodeagent

import "errors"

func startShell(string, int, int) (ptySession, error) {
	return nil, errors.New("terminals are supported on Linux nodes only")
}
