package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/auth"
)

type userContextKeyType struct{}

var userContextKey = userContextKeyType{}

const (
	sessionCookieName = "timothy_session"
	sessionDuration   = 7 * 24 * time.Hour
)

// UserFromContext extracts the authenticated user claims from the request context, if any.
func UserFromContext(ctx context.Context) *auth.Claims {
	claims, _ := ctx.Value(userContextKey).(*auth.Claims)
	return claims
}

type authAPI struct {
	store   AuthStore
	keyPair *auth.KeyPair
	log     *slog.Logger
}

func (a *API) registerAuth(handle func(pattern string, h http.Handler), authStore AuthStore, keyPair *auth.KeyPair) {
	h := &authAPI{store: authStore, keyPair: keyPair, log: a.log}

	// Public unauthenticated routes
	handle("GET /v1/auth/status", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.status(w, r, a)
	}))
	handle("POST /v1/auth/setup", http.HandlerFunc(h.setup))
	handle("POST /v1/auth/login", http.HandlerFunc(h.login))
	handle("POST /v1/auth/logout", http.HandlerFunc(h.logout))
	handle("POST /v1/auth/reset-password", http.HandlerFunc(h.resetPassword))

	// Protected routes
	handle("GET /v1/auth/me", a.auth(http.HandlerFunc(h.me)))
}

func (h *authAPI) status(w http.ResponseWriter, r *http.Request, a *API) {
	var setupRequired bool
	if h.store != nil {
		hasUsers, err := h.store.HasUsers(r.Context())
		if err == nil {
			setupRequired = !hasUsers
		}
	}

	// Check if current request is authenticated
	var authenticated bool
	var userClaims *auth.Claims

	if h.keyPair != nil {
		if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie.Value != "" {
			claims, err := h.keyPair.Verify(cookie.Value)
			if err == nil && claims != nil {
				authenticated = true
				userClaims = claims
			}
		}
	}

	if !authenticated && h.keyPair != nil {
		if got, ok := bearerToken(r); ok && got != "" {
			claims, err := h.keyPair.Verify(got)
			if err == nil && claims != nil {
				authenticated = true
				userClaims = claims
			}
		}
	}

	if !authenticated && a.token != "" {
		if got, ok := bearerToken(r); ok && subtle.ConstantTimeCompare([]byte(got), []byte(a.token)) == 1 {
			authenticated = true
			userClaims = &auth.Claims{
				Subject:  "system-api-token",
				Username: "api-client",
				Role:     "admin",
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"setup_required": setupRequired,
		"authenticated":  authenticated,
		"user":           userClaims,
	})
}

func (h *authAPI) setup(w http.ResponseWriter, r *http.Request) {
	if h.store == nil || h.keyPair == nil {
		jsonError(w, http.StatusServiceUnavailable, "auth_unavailable", "authentication service is unavailable")
		return
	}

	hasUsers, err := h.store.HasUsers(r.Context())
	if err != nil {
		failInternal(w, h.log, "check users", err)
		return
	}
	if hasUsers {
		jsonError(w, http.StatusForbidden, "setup_already_completed", "initial admin account already exists")
		return
	}

	var req struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	user, err := h.store.CreateUser(r.Context(), req.Username, req.Email, req.Password, "admin")
	if err != nil {
		if errors.Is(err, auth.ErrInvalidInput) {
			jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		failInternal(w, h.log, "create admin", err)
		return
	}

	claims := auth.Claims{
		Subject:   user.ID,
		Username:  user.Username,
		Email:     user.Email,
		Role:      user.Role,
		ExpiresAt: time.Now().Add(sessionDuration).Unix(),
	}

	token, err := h.keyPair.Sign(claims)
	if err != nil {
		failInternal(w, h.log, "sign token", err)
		return
	}

	setSessionCookie(w, r, token, sessionDuration)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"user": user,
	})
}

func (h *authAPI) login(w http.ResponseWriter, r *http.Request) {
	if h.store == nil || h.keyPair == nil {
		jsonError(w, http.StatusServiceUnavailable, "auth_unavailable", "authentication service is unavailable")
		return
	}

	var req struct {
		Username string `json:"username"` // username or email
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	user, err := h.store.Authenticate(r.Context(), req.Username, req.Password)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			jsonError(w, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
			return
		}
		failInternal(w, h.log, "authenticate", err)
		return
	}

	claims := auth.Claims{
		Subject:   user.ID,
		Username:  user.Username,
		Email:     user.Email,
		Role:      user.Role,
		ExpiresAt: time.Now().Add(sessionDuration).Unix(),
	}

	token, err := h.keyPair.Sign(claims)
	if err != nil {
		failInternal(w, h.log, "sign token", err)
		return
	}

	setSessionCookie(w, r, token, sessionDuration)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"user": user,
	})
}

func (h *authAPI) logout(w http.ResponseWriter, r *http.Request) {
	setSessionCookie(w, r, "", -1*time.Hour)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *authAPI) resetPassword(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		jsonError(w, http.StatusServiceUnavailable, "auth_unavailable", "authentication service is unavailable")
		return
	}

	var req struct {
		Token       string `json:"token"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if err := h.store.ResetPasswordWithToken(r.Context(), req.Token, req.NewPassword); err != nil {
		if errors.Is(err, auth.ErrInvalidRecovery) {
			jsonError(w, http.StatusBadRequest, "invalid_token", "invalid or expired recovery token")
			return
		}
		if errors.Is(err, auth.ErrInvalidInput) {
			jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		failInternal(w, h.log, "reset password", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *authAPI) me(w http.ResponseWriter, r *http.Request) {
	claims := UserFromContext(r.Context())
	if claims == nil {
		// Authenticated via API token
		writeJSON(w, http.StatusOK, map[string]any{
			"authenticated": true,
			"user": map[string]any{
				"username": "api-client",
				"role":     "admin",
			},
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"user":          claims,
	})
}

func setSessionCookie(w http.ResponseWriter, _ *http.Request, token string, ttl time.Duration) {
	maxAge := int(ttl.Seconds())
	if ttl <= 0 {
		maxAge = -1
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}
