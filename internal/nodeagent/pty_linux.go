//go:build linux

package nodeagent

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

type linuxPTY struct {
	master *os.File
	cmd    *exec.Cmd
}

// startShell starts a login shell for username on a new pseudo terminal.
func startShell(username string, cols, rows int) (ptySession, error) {
	u, err := user.Lookup(username)
	if err != nil {
		return nil, fmt.Errorf("user %q: %w", username, err)
	}
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	fd := int(master.Fd())
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		master.Close()
		return nil, fmt.Errorf("unlockpt: %w", err)
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		master.Close()
		return nil, fmt.Errorf("ptsname: %w", err)
	}
	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil, err
	}
	defer slave.Close()
	_ = unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Row: uint16(rows), Col: uint16(cols)})

	sh := shellPath()
	cmd := exec.Command(sh, "-l")
	cmd.Dir = u.HomeDir
	cmd.Env = []string{"TERM=xterm-256color", "HOME=" + u.HomeDir, "USER=" + u.Username, "LOGNAME=" + u.Username, "SHELL=" + sh,
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if uid, _ := strconv.Atoi(u.Uid); uid != os.Getuid() {
		gid, _ := strconv.Atoi(u.Gid)
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}
	}
	if err := cmd.Start(); err != nil {
		master.Close()
		return nil, err
	}
	return &linuxPTY{master: master, cmd: cmd}, nil
}

func (p *linuxPTY) Read(b []byte) (int, error) {
	n, err := p.master.Read(b)
	if errors.Is(err, syscall.EIO) { // the shell closed the terminal
		err = os.ErrClosed
	}
	return n, err
}

func (p *linuxPTY) Write(b []byte) (int, error) { return p.master.Write(b) }

func (p *linuxPTY) Resize(cols, rows int) error {
	return unix.IoctlSetWinsize(int(p.master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: uint16(rows), Col: uint16(cols)})
}

func (p *linuxPTY) Kill() {
	if p.cmd.Process != nil {
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGHUP)
		_ = p.cmd.Process.Kill()
	}
	_ = p.master.Close()
}

func (p *linuxPTY) Wait() int {
	err := p.cmd.Wait()
	_ = p.master.Close()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	if err != nil {
		return -1
	}
	return 0
}
