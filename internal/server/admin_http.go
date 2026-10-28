package server

import (
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"needle/internal/logging"
)

const adminCookieName = "needle_admin_session"

func adminJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}

func adminError(w http.ResponseWriter, status int, message string) {
	adminJSON(w, status, map[string]string{"error": message})
}

func adminBody(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(value); err != nil {
		adminError(w, http.StatusBadRequest, "Invalid or oversized JSON request")
		return false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		adminError(w, http.StatusBadRequest, "Expected one JSON object")
		return false
	}
	return true
}

func adminMutation(w http.ResponseWriter, r *http.Request, account adminAccount, session *adminSession) bool {
	origin, err := AdminOrigin(r.Header.Get("Origin"))
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || origin != account.Origin || mediaType != "application/json" || r.Header.Get("X-Needle-Admin") != "1" {
		adminError(w, http.StatusForbidden, "Request origin or content type rejected")
		return false
	}
	if session != nil && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(session.CSRF)) != 1 {
		adminError(w, http.StatusForbidden, "Session verification failed; reload this page")
		return false
	}
	return true
}

func adminCookie(w http.ResponseWriter, account adminAccount, token string, expires time.Time) {
	maxAge := int(adminSessionLifetime.Seconds())
	if token == "" {
		maxAge = -1
	}
	http.SetCookie(w, &http.Cookie{Name: adminCookieName, Value: token, Path: "/api/admin/", HttpOnly: true,
		Secure: strings.HasPrefix(account.Origin, "https://"), SameSite: http.SameSiteStrictMode, MaxAge: maxAge, Expires: expires})
}

func (h *Handler) handleAdminPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/admin" || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	data, err := staticFiles.ReadFile("static/admin.html")
	if err != nil {
		http.Error(w, "admin page unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	w.Write(data)
}

func (h *Handler) handleAdmin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	account, err := h.store.adminAccount()
	configured := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		h.logs.Check("admin_account", "ERROR", err)
		adminError(w, 500, "Admin storage unavailable")
		return
	}
	var token string
	if cookie, err := r.Cookie(adminCookieName); err == nil && len(cookie.Value) == 43 {
		token = cookie.Value
	}
	session, authenticated := h.admin.session(token, account, h.now())
	authenticated = authenticated && configured
	if r.URL.Path == "/api/admin/status" && r.Method == http.MethodGet {
		status := map[string]any{"configured": configured, "authenticated": authenticated}
		if authenticated {
			status["username"], status["csrf_token"], status["expires_at"] = session.Username, session.CSRF, session.Expires.Unix()
		}
		adminJSON(w, 200, status)
		return
	}
	if r.URL.Path == "/api/admin/login" && r.Method == http.MethodPost {
		if !configured {
			adminError(w, http.StatusServiceUnavailable, "Initialize the administrator on the Server using admin-init")
			return
		}
		if !adminMutation(w, r, account, nil) {
			return
		}
		if !h.admin.allowLogin(r.RemoteAddr, h.now()) {
			w.Header().Set("Retry-After", "60")
			adminError(w, 429, "Too many login attempts; try again in one minute")
			return
		}
		var input struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if !adminBody(w, r, &input) {
			return
		}
		select {
		case h.admin.kdf <- struct{}{}:
			defer func() { <-h.admin.kdf }()
		default:
			w.Header().Set("Retry-After", "2")
			adminError(w, 429, "Another login is being verified; try again shortly")
			return
		}
		valid := verifyAdminPassword(account.PasswordHash, input.Password)
		if !valid || input.Username != account.Username {
			h.logs.Check("admin_login", "WARN", errors.New("invalid credentials"))
			adminError(w, 401, "Incorrect username or password")
			return
		}
		current, err := h.store.adminAccount()
		if err != nil || current.Generation != account.Generation {
			adminError(w, 401, "Administrator settings changed; sign in again")
			return
		}
		h.admin.logout(token)
		newToken, newSession, err := h.admin.createSession(account, h.now())
		if err != nil {
			adminError(w, 503, "Session capacity reached; try again later")
			return
		}
		adminCookie(w, account, newToken, newSession.Expires)
		h.logs.Check("admin_login", "WARN", nil)
		adminJSON(w, 200, map[string]any{"username": newSession.Username, "csrf_token": newSession.CSRF, "expires_at": newSession.Expires.Unix()})
		return
	}
	if !authenticated {
		adminError(w, 401, "Sign in to manage nodes")
		return
	}
	if r.Method != http.MethodGet && !adminMutation(w, r, account, &session) {
		return
	}
	if r.URL.Path == "/api/admin/logout" && r.Method == http.MethodPost {
		h.admin.logout(token)
		adminCookie(w, account, "", time.Unix(1, 0))
		adminJSON(w, 200, map[string]string{"status": "ok"})
		return
	}
	if r.URL.Path == "/api/admin/nodes" && r.Method == http.MethodGet {
		h.fullMu.Lock()
		nodes, revision, err := h.store.NodeProfiles()
		h.fullMu.Unlock()
		if err != nil {
			h.logs.Check("admin_nodes", "ERROR", err)
			adminError(w, 500, "Could not load node profiles")
			return
		}
		adminJSON(w, 200, map[string]any{"nodes": nodes, "order_version": revision})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/admin/nodes/") && r.Method == http.MethodPatch {
		id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/admin/nodes/"), 10, 64)
		if err != nil || id < 1 {
			adminError(w, 400, "Invalid node ID")
			return
		}
		var input NodeProfileInput
		if !adminBody(w, r, &input) {
			return
		}
		if _, _, _, err := input.normalize(h.now()); err != nil {
			adminError(w, 422, err.Error())
			return
		}
		h.fullMu.Lock()
		err = h.store.UpdateNodeProfile(id, input, session.Username)
		if err == nil {
			h.invalidateCommitted(id)
			live := h.store.live
			live.mu.Lock()
			live.broadcastLocked(nodeEvent{Kind: "resync"})
			live.mu.Unlock()
		}
		h.fullMu.Unlock()
		if errors.Is(err, ErrProfileConflict) {
			adminError(w, 409, err.Error())
			return
		}
		if errors.Is(err, sql.ErrNoRows) {
			adminError(w, 404, "Node no longer exists")
			return
		}
		if err != nil {
			if strings.HasPrefix(err.Error(), "position must") {
				adminError(w, 422, err.Error())
				return
			}
			h.logs.Check("admin_save", "ERROR", err)
			adminError(w, 500, "Could not save node profile")
			return
		}
		logging.Print("INFO", "admin", "node_profile_updated", "agent_id", id, "actor", session.Username)
		adminJSON(w, 200, map[string]string{"status": "ok"})
		return
	}
	adminError(w, 404, "Unknown admin endpoint")
}
