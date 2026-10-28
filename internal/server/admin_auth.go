package server

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const adminSessionLifetime = 8 * time.Hour

type adminAccount struct {
	Username, PasswordHash, Origin, Generation string
}

type adminSession struct {
	Username, Generation, CSRF string
	Expires                    time.Time
}

type adminAttempts struct {
	Started time.Time
	Count   int
}

type adminState struct {
	mu       sync.Mutex
	sessions map[[32]byte]adminSession
	attempts map[string]adminAttempts
	global   adminAttempts
	kdf      chan struct{}
}

func newAdminState() *adminState {
	return &adminState{sessions: make(map[[32]byte]adminSession), attempts: make(map[string]adminAttempts), kdf: make(chan struct{}, 1)}
}

func randomAdminSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// AdminOrigin deliberately does not infer trust from forwarded headers. The
// administrator specifies the browser-facing origin, including behind TLS proxies.
func AdminOrigin(value string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Opaque != "" {
		return "", fmt.Errorf("enter the public origin only, e.g. https://needle.example.com")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return "", fmt.Errorf("admin access requires HTTPS; HTTP is allowed only on loopback for local testing")
		}
	}
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if port == "443" && u.Scheme == "https" || port == "80" && u.Scheme == "http" {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return u.Scheme + "://" + host, nil
}

func validateAdminCredentials(username, password string) error {
	if username != strings.TrimSpace(username) || username == "" || len(username) > 64 || !utf8.ValidString(username) || hasControl(username) {
		return fmt.Errorf("username must contain 1–64 bytes without surrounding whitespace or control characters")
	}
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) < 15 || len(password) > 1024 || hasControl(password) {
		return fmt.Errorf("password must contain at least 15 characters, at most 1024 bytes, and no control characters")
	}
	return nil
}

func hashAdminPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, 2, 19*1024, 1, 32)
	return "$argon2id$v=19$m=19456,t=2,p=1$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(key), nil
}

func verifyAdminPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	// Parameters are fixed and bounded, including for a corrupted database.
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" || parts[3] != "m=19456,t=2,p=1" {
		return false
	}
	salt, e1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, e2 := base64.RawStdEncoding.DecodeString(parts[5])
	if e1 != nil || e2 != nil || len(salt) != 16 || len(want) != 32 || len(password) > 1024 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, 2, 19*1024, 1, 32)
	return subtle.ConstantTimeCompare(got, want) == 1
}

func (s *Store) adminAccount() (adminAccount, error) {
	var a adminAccount
	err := s.db.QueryRow("SELECT username,password_hash,origin,generation FROM admin_account WHERE id=1").Scan(&a.Username, &a.PasswordHash, &a.Origin, &a.Generation)
	return a, err
}

// SetAdmin is CLI-only. No HTTP route can initialize or reset credentials.
func (s *Store) SetAdmin(username, password, origin string, reset bool) error {
	if err := validateAdminCredentials(username, password); err != nil {
		return err
	}
	origin, err := AdminOrigin(origin)
	if err != nil {
		return err
	}
	hash, err := hashAdminPassword(password)
	if err != nil {
		return err
	}
	generation, err := randomAdminSecret()
	if err != nil {
		return err
	}
	if reset {
		result, err := s.db.Exec("UPDATE admin_account SET username=?,password_hash=?,origin=?,generation=? WHERE id=1", username, hash, origin, generation)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err == nil && n != 1 {
			return fmt.Errorf("admin is not initialized; use admin-init")
		}
		return err
	}
	_, err = s.db.Exec("INSERT INTO admin_account(id,username,password_hash,origin,generation) VALUES(1,?,?,?,?)", username, hash, origin, generation)
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint") {
		return fmt.Errorf("admin already exists; use admin-reset")
	}
	return err
}

func (s *Store) AdminDefaults() (string, string, error) {
	a, err := s.adminAccount()
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return a.Username, a.Origin, err
}

func (a *adminState) allowLogin(address string, now time.Time) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for key, attempt := range a.attempts {
		if now.Sub(attempt.Started) >= time.Minute {
			delete(a.attempts, key)
		}
	}
	if now.Sub(a.global.Started) >= time.Minute {
		a.global = adminAttempts{Started: now}
	}
	if a.global.Count >= 20 {
		return false
	}
	a.global.Count++
	entry, ok := a.attempts[host]
	if !ok {
		if len(a.attempts) >= 128 {
			return false
		}
		entry.Started = now
	}
	entry.Count++
	a.attempts[host] = entry
	return entry.Count <= 5
}

func (a *adminState) createSession(account adminAccount, now time.Time) (string, adminSession, error) {
	token, err := randomAdminSecret()
	if err != nil {
		return "", adminSession{}, err
	}
	csrf, err := randomAdminSecret()
	if err != nil {
		return "", adminSession{}, err
	}
	session := adminSession{Username: account.Username, Generation: account.Generation, CSRF: csrf, Expires: now.Add(adminSessionLifetime)}
	a.mu.Lock()
	defer a.mu.Unlock()
	for key, value := range a.sessions {
		if !now.Before(value.Expires) || value.Generation != account.Generation {
			delete(a.sessions, key)
		}
	}
	if len(a.sessions) >= 32 {
		return "", adminSession{}, fmt.Errorf("too many admin sessions; log out an existing session or wait for expiration")
	}
	a.sessions[sha256.Sum256([]byte(token))] = session
	return token, session, nil
}

func (a *adminState) session(token string, account adminAccount, now time.Time) (adminSession, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	key := sha256.Sum256([]byte(token))
	value, ok := a.sessions[key]
	if !ok || !now.Before(value.Expires) || value.Generation != account.Generation {
		delete(a.sessions, key)
		return adminSession{}, false
	}
	return value, true
}

func (a *adminState) logout(token string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, sha256.Sum256([]byte(token)))
}
