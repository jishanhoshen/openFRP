package api

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"tunnelhub/internal/auth"
	"tunnelhub/internal/store"
)

// ================================================================ auth

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func validEmail(s string) bool {
	if len(s) > 254 {
		return false
	}
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s && a.Name == ""
}

func validPassword(p string) string {
	switch n := utf8.RuneCountInString(p); {
	case n < 10:
		return "password must be at least 10 characters"
	case len(p) > 256:
		return "password must be at most 256 bytes"
	}
	return ""
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.OpenRegistration {
		writeErr(w, http.StatusForbidden, "registration is closed; ask an administrator for an account")
		return
	}
	var c credentials
	if !decode(w, r, &c) {
		return
	}
	c.Email = strings.TrimSpace(c.Email)
	if !validEmail(c.Email) {
		writeErr(w, http.StatusBadRequest, "invalid email address")
		return
	}
	if msg := validPassword(c.Password); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	hash, err := auth.HashPassword(c.Password)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	u, err := s.store.CreateUser(r.Context(), c.Email, hash, "user")
	if errors.Is(err, store.ErrConflict) {
		writeErr(w, http.StatusConflict, "email already registered")
		return
	} else if err != nil {
		s.internal(w, r, err)
		return
	}
	s.audit(r, &u, "", "user.register", u.Email, nil)
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !decode(w, r, &c) {
		return
	}
	// Per-account throttle on top of the per-IP one (slows distributed guessing).
	if !s.rl.allow("login-account", strings.ToLower(strings.TrimSpace(c.Email))) {
		w.Header().Set("Retry-After", "60")
		writeErr(w, http.StatusTooManyRequests, "too many login attempts for this account; try again later")
		return
	}
	u, err := s.store.UserByEmail(r.Context(), strings.TrimSpace(c.Email))
	if errors.Is(err, store.ErrNotFound) {
		auth.BurnPasswordCheck(c.Password) // equalise timing with the found case
		s.audit(r, nil, "", "auth.login_failed", "", map[string]any{"email": c.Email})
		writeErr(w, http.StatusUnauthorized, "invalid email or password")
		return
	} else if err != nil {
		s.internal(w, r, err)
		return
	}
	ok, err := auth.VerifyPassword(c.Password, u.PasswordHash)
	if err != nil || !ok || u.Disabled {
		s.audit(r, &u, "", "auth.login_failed", u.Email, nil)
		writeErr(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	token := auth.NewToken("ths_")
	if err := s.store.CreateSession(r.Context(), u.ID, auth.HashToken(token), s.cfg.SessionTTL,
		s.clientIP(r), r.UserAgent()); err != nil {
		s.internal(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: s.sessionCookie(), Value: token, Path: "/", HttpOnly: true,
		Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode,
		MaxAge: int(s.cfg.SessionTTL.Seconds()),
	})
	s.audit(r, &u, "", "auth.login", u.Email, nil)
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(s.sessionCookie()); err == nil {
		if err := s.store.DeleteSession(r.Context(), auth.HashToken(c.Value)); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: s.sessionCookie(), Value: "", Path: "/", HttpOnly: true,
		Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) me(w http.ResponseWriter, _ *http.Request, u store.User) {
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request, u store.User) {
	var body struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if !decode(w, r, &body) {
		return
	}
	if ok, err := auth.VerifyPassword(body.Current, u.PasswordHash); err != nil || !ok {
		writeErr(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}
	if msg := validPassword(body.New); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	hash, err := auth.HashPassword(body.New)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var keep []byte
	if c, err := r.Cookie(s.sessionCookie()); err == nil {
		keep = auth.HashToken(c.Value)
	}
	if err := s.store.SetPassword(r.Context(), u.ID, hash, keep); err != nil {
		s.internal(w, r, err)
		return
	}
	s.audit(r, &u, "", "user.password_changed", u.Email, nil)
	w.WriteHeader(http.StatusNoContent)
}

// ================================================================ API tokens

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request, u store.User) {
	ts, err := s.store.ListAPITokens(r.Context(), u.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ts)
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request, u store.User) {
	var body struct {
		Name          string `json:"name"`
		ExpiresInDays int    `json:"expires_in_days"` // 0 = never
	}
	if !decode(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if !validLabel(body.Name) {
		writeErr(w, http.StatusBadRequest, "name must be 1-64 printable characters")
		return
	}
	if body.ExpiresInDays < 0 || body.ExpiresInDays > 3650 {
		writeErr(w, http.StatusBadRequest, "expires_in_days must be 0-3650")
		return
	}
	var exp *time.Time
	if body.ExpiresInDays > 0 {
		t := time.Now().Add(time.Duration(body.ExpiresInDays) * 24 * time.Hour)
		exp = &t
	}
	token := auth.NewToken("thk_")
	t, err := s.store.CreateAPIToken(r.Context(), u.ID, body.Name, auth.HashToken(token), token[:10], exp)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.audit(r, &u, "", "token.create", body.Name, map[string]any{"token_id": t.ID})
	writeJSON(w, http.StatusCreated, struct {
		store.APIToken
		Token string `json:"token"` // shown once
	}{t, token})
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request, u store.User) {
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if err := s.store.RevokeAPIToken(r.Context(), u.ID, id); errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	} else if err != nil {
		s.internal(w, r, err)
		return
	}
	s.audit(r, &u, "", "token.revoke", "", map[string]any{"token_id": id})
	w.WriteHeader(http.StatusNoContent)
}

// ================================================================ devices

func validLabel(s string) bool {
	if s == "" || utf8.RuneCountInString(s) > 64 {
		return false
	}
	for _, c := range s {
		if !unicode.IsPrint(c) {
			return false
		}
	}
	return true
}

func newDeviceID() string {
	b := make([]byte, 10) // 80 bits -> 16 base32 chars
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "dev-" + strings.ToLower(base32.StdEncoding.EncodeToString(b))
}

// ownedDevice loads a device the user may act on. Others' devices are
// reported as 404 so IDs can't be probed.
func (s *Server) ownedDevice(w http.ResponseWriter, r *http.Request, u store.User) (store.Device, bool) {
	d, err := s.store.GetDevice(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && (d.Revoked || (d.UserID != u.ID && !u.IsAdmin()))) {
		writeErr(w, http.StatusNotFound, "device not found")
		return d, false
	} else if err != nil {
		s.internal(w, r, err)
		return d, false
	}
	return d, true
}

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request, u store.User) {
	ds, err := s.store.ListDevices(r.Context(), &u.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ds)
}

func (s *Server) getDevice(w http.ResponseWriter, r *http.Request, u store.User) {
	if d, ok := s.ownedDevice(w, r, u); ok {
		writeJSON(w, http.StatusOK, d)
	}
}

func (s *Server) claimDevice(w http.ResponseWriter, r *http.Request, u store.User) {
	var body struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if !decode(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name != "" && !validLabel(body.Name) {
		writeErr(w, http.StatusBadRequest, "name must be 1-64 printable characters")
		return
	}
	code := auth.NormalizePairingCode(body.Code)
	d, err := s.store.ClaimPairing(r.Context(), auth.HashToken(code), u.ID, newDeviceID(), body.Name, s.cfg.MaxDevicesPerUser)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.audit(r, &u, "", "device.claim_failed", "", nil)
		writeErr(w, http.StatusNotFound, "invalid or expired pairing code")
		return
	case errors.Is(err, store.ErrQuota):
		writeErr(w, http.StatusConflict, "device limit reached")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	s.audit(r, &u, d.ID, "device.claim", d.ID, map[string]any{"name": d.Name})
	writeJSON(w, http.StatusCreated, d)
}

func (s *Server) updateDevice(w http.ResponseWriter, r *http.Request, u store.User) {
	d, ok := s.ownedDevice(w, r, u)
	if !ok {
		return
	}
	var body struct {
		Name     *string `json:"name"`
		Disabled *bool   `json:"disabled"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Name != nil {
		n := strings.TrimSpace(*body.Name)
		if !validLabel(n) {
			writeErr(w, http.StatusBadRequest, "name must be 1-64 printable characters")
			return
		}
		body.Name = &n
	}
	d, err := s.store.UpdateDevice(r.Context(), d.ID, body.Name, body.Disabled)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.notify.notify(d.ID)
	s.audit(r, &u, d.ID, "device.update", d.ID, map[string]any{"name": body.Name, "disabled": body.Disabled})
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) revokeDevice(w http.ResponseWriter, r *http.Request, u store.User) {
	d, ok := s.ownedDevice(w, r, u)
	if !ok {
		return
	}
	if err := s.store.RevokeDevice(r.Context(), d.ID); errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "device not found")
		return
	} else if err != nil {
		s.internal(w, r, err)
		return
	}
	s.notify.notify(d.ID)
	s.audit(r, &u, d.ID, "device.revoke", d.ID, nil)
	w.WriteHeader(http.StatusNoContent)
}

// ================================================================ pairing (agent side)

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (s *Server) pairStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Hostname string `json:"hostname"`
		OS       string `json:"os"`
		Arch     string `json:"arch"`
		Version  string `json:"version"`
	}
	if !decode(w, r, &body) {
		return
	}
	meta := store.PairingMeta{
		Hostname: clip(body.Hostname, 64), OS: clip(body.OS, 32), Arch: clip(body.Arch, 32),
		AgentVersion: clip(body.Version, 32), IP: s.clientIP(r),
	}
	for range 3 {
		code := auth.NewPairingCode()
		poll := auth.NewToken("thp_")
		exp, err := s.store.CreatePairing(r.Context(), auth.HashToken(code), auth.HashToken(poll), meta, s.cfg.PairingTTL)
		if errors.Is(err, store.ErrConflict) {
			continue
		} else if err != nil {
			s.internal(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"code":                  code,
			"poll_secret":           poll,
			"expires_at":            exp,
			"poll_interval_seconds": 3,
			"claim_url":             strings.TrimRight(s.cfg.PublicURL, "/") + "/claim?code=" + code,
		})
		return
	}
	s.internal(w, r, errors.New("could not allocate a unique pairing code"))
}

func (s *Server) pairPoll(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PollSecret string `json:"poll_secret"`
	}
	if !decode(w, r, &body) {
		return
	}
	token := auth.NewToken("thd_")
	res, err := s.store.PollPairing(r.Context(), auth.HashToken(body.PollSecret), auth.HashToken(token))
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrExpired):
		writeErr(w, http.StatusGone, "pairing expired or already completed; start again")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	case res.Pending:
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "pending"})
		return
	}
	s.audit(r, nil, res.DeviceID, "device.paired", res.DeviceID, nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"device_id":    res.DeviceID,
		"device_token": token,
		"relay":        s.cfg.Relay,
	})
}

// ================================================================ admin

func (s *Server) adminListUsers(w http.ResponseWriter, r *http.Request, _ store.User) {
	us, err := s.store.ListUsers(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, us)
}

func (s *Server) adminUpdateUser(w http.ResponseWriter, r *http.Request, admin store.User) {
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, http.StatusNotFound, "user not found")
		return
	}
	var body struct {
		Role                *string `json:"role"`
		Disabled            *bool   `json:"disabled"`
		MaxDevices          *int    `json:"max_devices"`            // -1 = server default
		MaxTunnelsPerDevice *int    `json:"max_tunnels_per_device"` // -1 = server default
	}
	if !decode(w, r, &body) {
		return
	}
	for _, q := range []*int{body.MaxDevices, body.MaxTunnelsPerDevice} {
		if q != nil && (*q < -1 || *q > 1000) {
			writeErr(w, http.StatusBadRequest, "quota overrides must be -1 (default) or 0-1000")
			return
		}
	}
	if body.Role != nil && *body.Role != "user" && *body.Role != "admin" {
		writeErr(w, http.StatusBadRequest, "role must be user or admin")
		return
	}
	if id == admin.ID && ((body.Disabled != nil && *body.Disabled) || (body.Role != nil && *body.Role != "admin")) {
		writeErr(w, http.StatusBadRequest, "you cannot disable or demote yourself")
		return
	}
	u, err := s.store.UpdateUser(r.Context(), id, store.UserPatch{Role: body.Role, Disabled: body.Disabled,
		MaxDevices: body.MaxDevices, MaxTunnelsPerDevice: body.MaxTunnelsPerDevice})
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "user not found")
		return
	} else if err != nil {
		s.internal(w, r, err)
		return
	}
	s.notifyUserDevices(r, id) // disabling/enabling a user pauses/resumes their agents
	s.audit(r, &admin, "", "admin.user_update", u.Email, map[string]any{"user_id": id, "role": body.Role,
		"disabled": body.Disabled, "max_devices": body.MaxDevices, "max_tunnels_per_device": body.MaxTunnelsPerDevice})
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) adminListDevices(w http.ResponseWriter, r *http.Request, _ store.User) {
	ds, err := s.store.ListDevices(r.Context(), nil)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ds)
}

func (s *Server) adminAudit(w http.ResponseWriter, r *http.Request, _ store.User) {
	es, err := s.store.ListAudit(r.Context(), nil, 200)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, es)
}
