// Command vad-node is the VaultDock storage node agent. install.sh
// ("Install Node") installs it as the systemd service vad-node:
//
//	vad-node run [--dir DIR]      join the server (admin approval), then heartbeat and self-update
//	vad-node status [--dir DIR]   show the join/connection state (exit 0 approved, 2 waiting, 3 rejected, 4 error, retrying)
//	vad-node version
//
// DIR defaults to the parent of the binary's folder (<dir>/bin/vad-node).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/nodeagent"
)

func defaultDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return filepath.Dir(filepath.Dir(exe))
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: vad-node run|status|version [--dir DIR]")
		os.Exit(2)
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "installation directory (holds .env)")
	_ = fs.Parse(os.Args[2:])

	switch cmd {
	case "version":
		fmt.Println(nodeagent.Version)
	case "status":
		os.Exit(status(*dir))
	case "run":
		os.Exit(run(*dir))
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		os.Exit(2)
	}
}

func run(dir string) int {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := nodeagent.LoadConfig(dir)
	if err != nil {
		log.Error("configuration", "err", err)
		return 1
	}
	log.Info("vad-node starting", "version", nodeagent.Version, "server", cfg.ServerURL, "name", cfg.Name, "dir", cfg.Dir)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = nodeagent.New(cfg, log).Run(ctx)
	if errors.Is(err, nodeagent.ErrUpdated) {
		return nodeagent.ExitRestart
	}
	return 0
}

func status(dir string) int {
	cfg, err := nodeagent.LoadConfig(dir)
	if err != nil {
		fmt.Println("error:", err)
		return 3
	}
	st, err := nodeagent.ReadStatus(cfg)
	if err != nil {
		fmt.Println("starting: no status yet")
		return 2
	}
	fmt.Printf("%s: %s\n", st.State, st.Message)
	if st.LastHeartbeat > 0 {
		fmt.Printf("last heartbeat: %s\n", time.UnixMilli(st.LastHeartbeat).Format(time.RFC1123))
	}
	switch st.State {
	case "approved":
		return 0
	case "rejected":
		return 3
	case "error":
		return 4 // the service keeps retrying (for example the server is unreachable)
	}
	return 2 // pending, starting, expired, revoked: a request is (re)sent

}
