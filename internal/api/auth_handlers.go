package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/auth"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{1,31}$`)
var emailPattern = regexp.MustCompile(`^[^@\s]{1,64}@[^@\s]{1,190}\.[^@\s]{2,63}$`)

func validateUserFields(username, email string) error {
	if !usernamePattern.MatchString(username) {
		return errf(400, "invalid_username", "Username must be 2-32 characters: letters, digits, '.', '_' or '-'")
	}
	if email != "" && !emailPattern.MatchString(email) {
		return errf(400, "invalid_email", "Email address is not valid")
	}
	return nil
}

type userView struct {
	*store.User
	CSRF string `json:"csrf_token,omitempty"`
}

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	n, err := s.st.CountUsers(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"needs_setup": n == 0, "token_required": s.cfg.SetupToken != ""})
}

type credentialsReq struct {
	Username    string `json:"username"`
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
	SetupToken  string `json:"setup_token"`
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if !s.loginLimiter.Allow("setup:" + s.clientIP(r)) {
		writeErr(w, r, errf(429, "rate_limited", "Too many attempts"))
		return
	}
	var req credentialsReq
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	n, err := s.st.CountUsers(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if n > 0 {
		writeErr(w, r, errf(409, "already_setup", "VaultDock is already set up"))
		return
	}
	if s.cfg.SetupToken != "" && subtle.ConstantTimeCompare([]byte(strings.TrimSpace(req.SetupToken)), []byte(s.cfg.SetupToken)) != 1 {
		writeErr(w, r, errf(403, "bad_setup_token", "Setup token is incorrect. It was printed by the installer and is stored in the .env file as VAULTDOCK_SETUP_TOKEN"))
		return
	}
	u, err := s.createUser(r.Context(), req, "admin", -1)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, u.ID, "account.setup", u.Username, "initial administrator created", true)
	s.startSession(w, r, u)
}

func (s *Server) createUser(ctx context.Context, req credentialsReq, role string, quota int64) (*store.User, error) {
	req.Username = strings.TrimSpace(req.Username)
	req.Email = strings.TrimSpace(req.Email)
	if err := validateUserFields(req.Username, req.Email); err != nil {
		return nil, err
	}
	if err := auth.ValidatePassword(req.Password); err != nil {
		return nil, errf(400, "weak_password", "%s", err.Error())
	}
	hash, err := auth.HashPassword(ctx, req.Password)
	if err != nil {
		return nil, err
	}
	loc, err := s.st.DefaultLocation(ctx)
	if err != nil {
		return nil, err
	}
	if quota < 0 {
		quota = s.settings.Get().DefaultQuotaBytes
	}
	if len(req.DisplayName) > 80 {
		req.DisplayName = req.DisplayName[:80]
	}
	u, err := s.st.CreateUser(ctx, store.NewUser{Username: req.Username, Email: req.Email, DisplayName: strings.TrimSpace(req.DisplayName), PasswordHash: hash, Role: role, QuotaBytes: quota, LocationID: loc.ID})
	if errors.Is(err, store.ErrConflict) {
		return nil, errf(409, "user_exists", "That username or email is already registered")
	}
	return u, err
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !s.settings.Get().RegistrationEnabled {
		writeErr(w, r, errf(403, "registration_closed", "Registration is disabled. Ask an administrator for an account"))
		return
	}
	if !s.loginLimiter.Allow("register:" + s.clientIP(r)) {
		writeErr(w, r, errf(429, "rate_limited", "Too many attempts, try again later"))
		return
	}
	var req credentialsReq
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	u, err := s.createUser(r.Context(), req, "user", -1)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, u.ID, "account.register", u.Username, "self-registration", true)
	s.startSession(w, r, u)
}

// checkCredentials verifies login and password with rate limiting and
// lockout. It returns a generic error to avoid user enumeration.
func (s *Server) checkCredentials(r *http.Request, login, password string) (*store.User, error) {
	ip := s.clientIP(r)
	login = strings.TrimSpace(login)
	key := "u:" + strings.ToLower(login)
	if blocked, d := s.loginFails.Blocked(key); blocked {
		return nil, errf(429, "locked", "Too many failed attempts for this account. Try again in %d minutes", int(d.Minutes())+1)
	}
	if blocked, _ := s.loginFails.Blocked("ip:" + ip); blocked {
		return nil, errf(429, "locked", "Too many failed attempts from your address. Try again later")
	}
	u, err := s.st.UserByLogin(r.Context(), login)
	hash := ""
	if err == nil {
		hash = u.PasswordHash
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	ok, verr := auth.VerifyPassword(r.Context(), password, hash)
	if verr != nil && hash != "" {
		return nil, verr
	}
	if u == nil || !ok {
		s.loginFails.Fail(key)
		s.loginFails.Fail("ip:" + ip)
		var uid int64
		if u != nil {
			uid = u.ID
		}
		s.activity(r, uid, "auth.failed", login, "invalid credentials", true)
		return nil, errf(401, "invalid_credentials", "Incorrect username or password")
	}
	if u.Disabled {
		s.activity(r, u.ID, "auth.denied", u.Username, "account disabled", true)
		return nil, errf(403, "account_disabled", "This account has been disabled")
	}
	s.loginFails.Reset(key)
	return u, nil
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.loginLimiter.Allow("login:" + s.clientIP(r)) {
		w.Header().Set("Retry-After", "30")
		writeErr(w, r, errf(429, "rate_limited", "Too many sign-in attempts, wait a moment"))
		return
	}
	var req struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	u, err := s.checkCredentials(r, req.Login, req.Password)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.st.TouchLogin(r.Context(), u.ID)
	s.activity(r, u.ID, "auth.login", u.Username, r.UserAgent(), true)
	s.startSession(w, r, u)
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u *store.User) {
	token := auth.RandomToken(32)
	t := time.Now()
	ua := r.UserAgent()
	if len(ua) > 300 {
		ua = ua[:300]
	}
	sess := &store.Session{
		ID: auth.HashToken(token), UserID: u.ID, CSRF: auth.RandomToken(24), IP: s.clientIP(r), UserAgent: ua,
		CreatedAt: t.UnixMilli(), LastSeenAt: t.UnixMilli(), ExpiresAt: t.Add(sessionTTL).UnixMilli(),
	}
	if err := s.st.CreateSession(r.Context(), sess); err != nil {
		writeErr(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: s.cookieSecure(r), Expires: t.Add(sessionTTL),
	})
	writeJSON(w, 200, map[string]any{"user": userView{User: u, CSRF: sess.CSRF}})
}

func (s *Server) cookieSecure(r *http.Request) bool {
	switch s.cfg.CookieSecure {
	case "true":
		return true
	case "false":
		return false
	}
	return s.isHTTPS(r)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if p.Session != nil {
		_ = s.st.DeleteSession(r.Context(), p.Session.ID)
		s.activity(r, p.User.ID, "auth.logout", p.User.Username, "", false)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1, SameSite: http.SameSiteLaxMode, Secure: s.cookieSecure(r)})
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	v := userView{User: p.User}
	if p.Session != nil {
		v.CSRF = p.Session.CSRF
	}
	writeJSON(w, 200, map[string]any{"user": v})
}

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	var req struct {
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	req.Email = strings.TrimSpace(req.Email)
	if req.Email != "" && !emailPattern.MatchString(req.Email) {
		writeErr(w, r, errf(400, "invalid_email", "Email address is not valid"))
		return
	}
	dn := strings.TrimSpace(req.DisplayName)
	if len(dn) > 80 {
		dn = dn[:80]
	}
	if err := s.st.UpdateUser(r.Context(), p.User.ID, store.UserPatch{Email: &req.Email, DisplayName: &dn}); err != nil {
		if errors.Is(err, store.ErrConflict) {
			err = errf(409, "email_taken", "That email is already in use")
		}
		writeErr(w, r, err)
		return
	}
	u, _ := s.st.UserByID(r.Context(), p.User.ID)
	writeJSON(w, 200, map[string]any{"user": userView{User: u, CSRF: p.Session.CSRF}})
}

func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	var req struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if _, err := s.checkCredentials(r, p.User.Username, req.Current); err != nil {
		writeErr(w, r, errf(400, "wrong_password", "Current password is incorrect"))
		return
	}
	if err := auth.ValidatePassword(req.New); err != nil {
		writeErr(w, r, errf(400, "weak_password", "%s", err.Error()))
		return
	}
	hash, err := auth.HashPassword(r.Context(), req.New)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := s.st.SetPassword(r.Context(), p.User.ID, hash); err != nil {
		writeErr(w, r, err)
		return
	}
	n, _ := s.st.DeleteUserSessions(r.Context(), p.User.ID, p.Session.ID)
	s.activity(r, p.User.ID, "account.password_changed", p.User.Username, "other sessions signed out", true)
	writeJSON(w, 200, map[string]any{"ok": true, "sessions_revoked": n})
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	list, err := s.st.ListSessions(r.Context(), p.User.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	for i := range list {
		list[i].Current = list[i].ID == p.Session.ID
	}
	writeJSON(w, 200, map[string]any{"sessions": list})
}

func (s *Server) handleSessionRevoke(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	ok, err := s.st.DeleteSessionByPublicID(r.Context(), p.User.ID, chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if !ok {
		writeErr(w, r, store.ErrNotFound)
		return
	}
	s.activity(r, p.User.ID, "account.session_revoked", "", "", true)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleSessionsRevokeOthers(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	n, err := s.st.DeleteUserSessions(r.Context(), p.User.ID, p.Session.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, p.User.ID, "account.sessions_revoked", "", "", true)
	writeJSON(w, 200, map[string]any{"revoked": n})
}
