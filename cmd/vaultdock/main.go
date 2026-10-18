// Command vaultdock runs the VaultDock server.
package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/api"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/app"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/auth"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/config"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/web"
)

func main() {
	cmd := "serve"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve()
	case "healthcheck":
		err = healthcheck()
	case "backup":
		err = backup(args)
	case "schema":
		err = schema()
	case "reset-password":
		err = resetPassword(args)
	case "version":
		fmt.Println(api.Version)
	default:
		fmt.Fprintf(os.Stderr, "usage: vaultdock [serve|healthcheck|backup <file>|schema|reset-password <username>|version]\n")
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func setupLogging(cfg *config.Config) {
	var lvl slog.Level
	_ = lvl.UnmarshalText([]byte(cfg.LogLevel))
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	if cfg.LogFormat == "text" {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	slog.SetDefault(slog.New(h))
}

func serve() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	setupLogging(cfg)
	slog.Info("starting VaultDock", "version", api.Version, "state_dir", cfg.StateDir, "listen", cfg.Listen)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, err := app.New(ctx, cfg)
	if err != nil {
		return err
	}
	defer a.Close()
	srv := a.Server
	if n, _ := a.Store.CountUsers(ctx); n == 0 {
		if cfg.SetupToken != "" {
			slog.Warn("no users yet: open the web interface and create the administrator account using the setup token from your .env file")
		} else {
			slog.Warn("no users yet and VAULTDOCK_SETUP_TOKEN is not set: the first visitor can create the administrator account. Set a setup token for public deployments")
		}
	}

	bgDone := make(chan struct{})
	go func() { a.Run(ctx); close(bgDone) }()
	defer func() { stop(); <-bgDone }()

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(api.SPAHandler(web.Dist())),
		ReadHeaderTimeout: 20 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug),
	}
	errCh := make(chan error, 1)
	switch {
	case cfg.TLSCert != "":
		slog.Info("serving HTTPS with provided certificate")
		go func() { errCh <- httpSrv.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey) }()
	case cfg.TLSSelfSigned:
		cert, fp, err := selfSignedCert(cfg)
		if err != nil {
			return err
		}
		httpSrv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		slog.Info("serving HTTPS with a self-signed certificate; confirm this fingerprint when connecting the desktop app", "sha256", fp)
		go func() { errCh <- httpSrv.ListenAndServeTLS("", "") }()
	default:
		slog.Info("serving plain HTTP; put VaultDock behind an HTTPS reverse proxy for remote access")
		go func() { errCh <- httpSrv.ListenAndServe() }()
	}
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		slog.Info("shutting down")
		sctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(sctx)
	}
	return nil
}

func selfSignedCert(cfg *config.Config) (tls.Certificate, string, error) {
	dir := filepath.Join(cfg.StateDir, "tls")
	certPath, keyPath := filepath.Join(dir, "selfsigned.crt"), filepath.Join(dir, "selfsigned.key")
	if c, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		leaf, _ := x509.ParseCertificate(c.Certificate[0])
		if leaf != nil && time.Until(leaf.NotAfter) > 30*24*time.Hour {
			return c, fingerprint(c.Certificate[0]), nil
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tls.Certificate{}, "", err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "VaultDock self-signed", Organization: []string{"VaultDock"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(825 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	for _, h := range strings.Split(os.Getenv("VAULTDOCK_TLS_HOSTS"), ",") {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if ip := net.ParseIP(h); ip != nil {
			tpl.IPAddresses = append(tpl.IPAddresses, ip)
		} else {
			tpl.DNSNames = append(tpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		return tls.Certificate{}, "", err
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return tls.Certificate{}, "", err
	}
	c, err := tls.LoadX509KeyPair(certPath, keyPath)
	return c, fingerprint(der), err
}

func fingerprint(der []byte) string {
	s := sha256.Sum256(der)
	h := strings.ToUpper(hex.EncodeToString(s[:]))
	var parts []string
	for i := 0; i < len(h); i += 2 {
		parts = append(parts, h[i:i+2])
	}
	return strings.Join(parts, ":")
}

func healthcheck() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	addr := cfg.Listen
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	scheme := "http"
	client := &http.Client{Timeout: 5 * time.Second}
	if cfg.TLSCert != "" || cfg.TLSSelfSigned {
		scheme = "https"
		certFile := cfg.TLSCert
		if certFile == "" {
			certFile = filepath.Join(cfg.StateDir, "tls", "selfsigned.crt")
		}
		pemBytes, err := os.ReadFile(certFile)
		if err != nil {
			return err
		}
		block, _ := pem.Decode(pemBytes)
		if block == nil {
			return errors.New("cannot read TLS certificate")
		}
		want := block.Bytes
		// The probe pins our own certificate exactly: the certificate may not
		// name 127.0.0.1, so hostname checks are replaced by a byte-for-byte
		// comparison with the configured leaf certificate.
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // replaced by VerifyConnection pinning below
			VerifyConnection: func(cs tls.ConnectionState) error {
				if len(cs.PeerCertificates) == 0 || string(cs.PeerCertificates[0].Raw) != string(want) {
					return errors.New("unexpected server certificate")
				}
				return nil
			},
		}}
	}
	resp, err := client.Get(scheme + "://" + addr + "/readyz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func backup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("usage: vaultdock backup <destination.db>")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	st, err := store.Open(context.Background(), app.DBPath(cfg))
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Backup(context.Background(), fs.Arg(0)); err != nil {
		return err
	}
	fmt.Println("database backup written to", fs.Arg(0))
	return nil
}

func schema() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	fmt.Printf("bundled schema version: %d\n", store.LatestSchemaVersion())
	if _, err := os.Stat(app.DBPath(cfg)); err != nil {
		fmt.Println("database: not created yet")
		return nil
	}
	st, err := store.Open(context.Background(), app.DBPath(cfg))
	if err != nil {
		return err
	}
	defer st.Close()
	v, err := st.SchemaVersion(context.Background())
	if err != nil {
		return err
	}
	fmt.Printf("database schema version: %d\n", v)
	return st.IntegrityCheck(context.Background())
}

func resetPassword(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: vaultdock reset-password <username>  (new password read from stdin)")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	st, err := store.Open(context.Background(), app.DBPath(cfg))
	if err != nil {
		return err
	}
	defer st.Close()
	u, err := st.UserByLogin(context.Background(), args[0])
	if err != nil {
		return fmt.Errorf("user %q not found", args[0])
	}
	fmt.Fprint(os.Stderr, "New password: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return err
	}
	pw := strings.TrimRight(line, "\r\n")
	if err := auth.ValidatePassword(pw); err != nil {
		return err
	}
	hash, err := auth.HashPassword(context.Background(), pw)
	if err != nil {
		return err
	}
	if err := st.SetPassword(context.Background(), u.ID, hash); err != nil {
		return err
	}
	_, _ = st.DeleteUserSessions(context.Background(), u.ID, "")
	fmt.Println("password updated and sessions signed out for", u.Username)
	return nil
}
