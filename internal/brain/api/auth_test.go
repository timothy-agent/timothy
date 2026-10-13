package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/auth"
)

type fakeAuthStore struct {
	hasUsers    bool
	users       map[string]*auth.User // keyed by username and email
	passwords   map[string]string     // keyed by user ID
	recoveryTok map[string]string     // token -> email
}

func newFakeAuthStore() *fakeAuthStore {
	return &fakeAuthStore{
		users:       make(map[string]*auth.User),
		passwords:   make(map[string]string),
		recoveryTok: make(map[string]string),
	}
}

func (s *fakeAuthStore) HasUsers(ctx context.Context) (bool, error) {
	return s.hasUsers, nil
}

func (s *fakeAuthStore) CreateUser(ctx context.Context, username, email, password, role string) (*auth.User, error) {
	if s.hasUsers {
		return nil, errors.New("admin already exists")
	}
	u, err := auth.ValidateUsername(username)
	if err != nil {
		return nil, err
	}
	e, err := auth.ValidateEmail(email)
	if err != nil {
		return nil, err
	}
	if err := auth.ValidatePassword(password); err != nil {
		return nil, err
	}
	user := &auth.User{
		ID:        "user-123",
		Username:  u,
		Email:     e,
		Role:      role,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	s.users[u] = user
	s.users[e] = user
	s.passwords[user.ID] = password
	s.hasUsers = true
	return user, nil
}

func (s *fakeAuthStore) Authenticate(ctx context.Context, usernameOrEmail, password string) (*auth.User, error) {
	u, ok := s.users[usernameOrEmail]
	if !ok {
		return nil, auth.ErrInvalidCredentials
	}
	if s.passwords[u.ID] != password {
		return nil, auth.ErrInvalidCredentials
	}
	return u, nil
}

func (s *fakeAuthStore) ResetPasswordWithToken(ctx context.Context, token, newPassword string) error {
	email, ok := s.recoveryTok[token]
	if !ok {
		return auth.ErrInvalidRecovery
	}
	if err := auth.ValidatePassword(newPassword); err != nil {
		return err
	}
	u := s.users[email]
	s.passwords[u.ID] = newPassword
	delete(s.recoveryTok, token)
	return nil
}

func testKeyPair(t *testing.T) *auth.KeyPair {
	t.Helper()
	kp, err := auth.DeriveKeyPair([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatalf("derive test key pair: %v", err)
	}
	return kp
}

func TestAuthStatus(t *testing.T) {
	t.Parallel()

	t.Run("setup required when no users exist", func(t *testing.T) {
		store := newFakeAuthStore()
		kp := testKeyPair(t)
		a := &API{token: "tok", log: discard(), authStore: store, keyPair: kp}
		mux := http.NewServeMux()
		a.registerAuth(mux.Handle, store, kp)

		req := httptest.NewRequest(http.MethodGet, "/v1/auth/status", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		var resp map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if resp["setup_required"] != true {
			t.Errorf("expected setup_required true, got %v", resp["setup_required"])
		}
		if resp["authenticated"] != false {
			t.Errorf("expected authenticated false, got %v", resp["authenticated"])
		}
	})

	t.Run("authenticated with valid cookie", func(t *testing.T) {
		store := newFakeAuthStore()
		kp := testKeyPair(t)
		a := &API{token: "tok", log: discard(), authStore: store, keyPair: kp}
		mux := http.NewServeMux()
		a.registerAuth(mux.Handle, store, kp)

		token, err := auth.Sign(auth.Claims{
			Subject:   "usr-1",
			Username:  "admin",
			Email:     "admin@example.com",
			Role:      "admin",
			ExpiresAt: time.Now().Add(time.Hour).Unix(),
		}, kp.PrivateKey)
		if err != nil {
			t.Fatalf("sign token: %v", err)
		}

		req := httptest.NewRequest(http.MethodGet, "/v1/auth/status", nil)
		req.AddCookie(&http.Cookie{
			Name:     sessionCookieName,
			Value:    token,
			Secure:   true,
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		var resp map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if resp["authenticated"] != true {
			t.Errorf("expected authenticated true, got %v", resp["authenticated"])
		}
	})

	t.Run("authenticated with valid post-quantum hybrid token", func(t *testing.T) {
		store := newFakeAuthStore()
		kp := testKeyPair(t)
		a := &API{token: "tok", log: discard(), authStore: store, keyPair: kp}
		mux := http.NewServeMux()
		a.registerAuth(mux.Handle, store, kp)

		pqToken, err := kp.SignPQ(auth.Claims{
			Subject:   "usr-pq",
			Username:  "admin",
			Email:     "admin@example.com",
			Role:      "admin",
			ExpiresAt: time.Now().Add(time.Hour).Unix(),
		})
		if err != nil {
			t.Fatalf("sign pq token: %v", err)
		}

		req := httptest.NewRequest(http.MethodGet, "/v1/auth/status", nil)
		req.AddCookie(&http.Cookie{
			Name:     sessionCookieName,
			Value:    pqToken,
			Secure:   true,
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		var resp map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if resp["authenticated"] != true {
			t.Errorf("expected authenticated true, got %v", resp["authenticated"])
		}
	})

	t.Run("authenticated with valid bearer JWT", func(t *testing.T) {
		store := newFakeAuthStore()
		kp := testKeyPair(t)
		a := &API{token: "tok", log: discard(), authStore: store, keyPair: kp}
		mux := http.NewServeMux()
		a.registerAuth(mux.Handle, store, kp)

		bearerTok, err := kp.Sign(auth.Claims{
			Subject:   "usr-bearer",
			Username:  "admin",
			Email:     "admin@example.com",
			Role:      "admin",
			ExpiresAt: time.Now().Add(time.Hour).Unix(),
		})
		if err != nil {
			t.Fatalf("sign token: %v", err)
		}

		req := httptest.NewRequest(http.MethodGet, "/v1/auth/status", nil)
		req.Header.Set("Authorization", "Bearer "+bearerTok)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		var resp map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if resp["authenticated"] != true {
			t.Errorf("expected authenticated true, got %v", resp["authenticated"])
		}
	})
}

func TestAuthSetup(t *testing.T) {
	t.Parallel()

	store := newFakeAuthStore()
	kp := testKeyPair(t)
	a := &API{token: "tok", log: discard(), authStore: store, keyPair: kp}
	mux := http.NewServeMux()
	a.registerAuth(mux.Handle, store, kp)

	// 1. Initial setup succeeds
	body := `{"username":"admin","email":"admin@example.com","password":"StrongPassword123!"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/setup", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == sessionCookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil || sessionCookie.Value == "" {
		t.Fatal("expected timothy_session cookie to be set")
	}

	// 2. Second setup attempt is rejected
	req2 := httptest.NewRequest(http.MethodPost, "/v1/auth/setup", bytes.NewBufferString(body))
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)
	if w2.Code != http.StatusForbidden {
		t.Fatalf("expected 403 on second setup, got %d", w2.Code)
	}
}

func TestAuthLoginAndLogout(t *testing.T) {
	t.Parallel()

	store := newFakeAuthStore()
	kp := testKeyPair(t)
	_, _ = store.CreateUser(context.Background(), "alice", "alice@example.com", "Password123!", "admin")

	a := &API{token: "tok", log: discard(), authStore: store, keyPair: kp}
	mux := http.NewServeMux()
	a.registerAuth(mux.Handle, store, kp)

	// Login with invalid credentials
	badLogin := `{"username":"alice","password":"WrongPassword"}`
	reqBad := httptest.NewRequest(http.MethodPost, "/v1/auth/login", bytes.NewBufferString(badLogin))
	wBad := httptest.NewRecorder()
	mux.ServeHTTP(wBad, reqBad)
	if wBad.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for bad password, got %d", wBad.Code)
	}

	// Login with username
	loginUser := `{"username":"alice","password":"Password123!"}`
	reqUser := httptest.NewRequest(http.MethodPost, "/v1/auth/login", bytes.NewBufferString(loginUser))
	wUser := httptest.NewRecorder()
	mux.ServeHTTP(wUser, reqUser)
	if wUser.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid username, got %d", wUser.Code)
	}

	// Login with email
	loginEmail := `{"username":"alice@example.com","password":"Password123!"}`
	reqEmail := httptest.NewRequest(http.MethodPost, "/v1/auth/login", bytes.NewBufferString(loginEmail))
	wEmail := httptest.NewRecorder()
	mux.ServeHTTP(wEmail, reqEmail)
	if wEmail.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid email, got %d", wEmail.Code)
	}

	// Logout clears cookie
	reqLogout := httptest.NewRequest(http.MethodPost, "/v1/auth/logout", nil)
	wLogout := httptest.NewRecorder()
	mux.ServeHTTP(wLogout, reqLogout)
	if wLogout.Code != http.StatusOK {
		t.Fatalf("expected 200 for logout, got %d", wLogout.Code)
	}
	logoutCookies := wLogout.Result().Cookies()
	var cleared bool
	for _, c := range logoutCookies {
		if c.Name == sessionCookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("expected session cookie to be cleared with MaxAge < 0")
	}
}

func TestAuthMiddleware(t *testing.T) {
	t.Parallel()

	store := newFakeAuthStore()
	kp := testKeyPair(t)
	a := &API{token: "secret-token", log: discard(), authStore: store, keyPair: kp}

	handler := a.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	// Case 1: No users exist -> returns setup_required (401)
	req1 := httptest.NewRequest(http.MethodGet, "/v1/sessions", nil)
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)
	if w1.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w1.Code)
	}
	if !bytes.Contains(w1.Body.Bytes(), []byte("setup_required")) {
		t.Errorf("expected setup_required in body, got: %s", w1.Body.String())
	}

	// Case 2: User created, request has no credentials -> returns unauthorized
	_, _ = store.CreateUser(context.Background(), "bob", "bob@example.com", "Password123!", "admin")
	req2 := httptest.NewRequest(http.MethodGet, "/v1/sessions", nil)
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w2.Code)
	}
	if bytes.Contains(w2.Body.Bytes(), []byte("setup_required")) {
		t.Errorf("did not expect setup_required after users created, got: %s", w2.Body.String())
	}

	// Case 3: Request with valid bearer token succeeds
	req3 := httptest.NewRequest(http.MethodGet, "/v1/sessions", nil)
	req3.Header.Set("Authorization", "Bearer secret-token")
	w3 := httptest.NewRecorder()
	handler.ServeHTTP(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("expected 200 with valid bearer token, got %d", w3.Code)
	}

	// Case 4: Request with valid cookie succeeds
	token, _ := auth.Sign(auth.Claims{
		Subject:   "usr-bob",
		Username:  "bob",
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}, kp.PrivateKey)
	req4 := httptest.NewRequest(http.MethodGet, "/v1/sessions", nil)
	req4.AddCookie(&http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	w4 := httptest.NewRecorder()
	handler.ServeHTTP(w4, req4)
	if w4.Code != http.StatusOK {
		t.Fatalf("expected 200 with valid cookie, got %d", w4.Code)
	}
}
