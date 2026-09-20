package app_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	backendApp "cpa-helper/backend/internal/app"
)

type userSuperAdminStateResponse struct {
	ID           int        `json:"id"`
	IsSuperAdmin bool       `json:"is_super_admin"`
	DisabledAt   *time.Time `json:"disabled_at"`
}

type userAPIKeyVisibilityResponse struct {
	ID      int `json:"id"`
	APIKeys []struct {
		APIKeyHash string  `json:"api_key_hash"`
		APIKey     *string `json:"api_key"`
	} `json:"api_keys"`
}

type observedAPIKeyVisibilityResponse struct {
	APIKeyHash string  `json:"api_key_hash"`
	APIKey     *string `json:"api_key"`
	UserID     *int    `json:"user_id"`
}

type userBoundAPIKeyResponse struct {
	APIKeyHash string `json:"api_key_hash"`
}

type concurrentUserRequest struct {
	Method  string
	Path    string
	Body    any
	Cookies []*http.Cookie
}

func TestSystemSettingsRequireSuperAdmin(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	app, err := backendApp.New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	handler := app.Routes()
	rootCookies := requestJSON(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "root",
		"password": "root-password",
		"nickname": "Root",
	}, nil, nil)
	manager := userRoleResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/users", map[string]any{
		"username": "manager",
		"password": "manager-password",
		"nickname": "Manager",
		"is_admin": true,
	}, rootCookies, &manager)
	managerCookies := requestJSON(t, handler, http.MethodPost, "/api/auth/login", map[string]any{
		"username": "manager",
		"password": "manager-password",
	}, nil, nil)

	requestJSONExpectStatus(t, handler, http.MethodGet, "/api/settings", nil, managerCookies, http.StatusForbidden)
	requestJSONExpectStatus(t, handler, http.MethodPut, "/api/settings", map[string]any{
		"cliaproxy_url": "https://attacker.example",
	}, managerCookies, http.StatusForbidden)
	requestJSON(t, handler, http.MethodGet, "/api/settings", nil, rootCookies, nil)
}

func TestRegularAdminCannotOperateOnSuperAdminAccount(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	app, err := backendApp.New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	handler := app.Routes()
	rootCookies := requestJSON(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "root",
		"password": "root-password",
		"nickname": "Root",
	}, nil, nil)

	manager := userRoleResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/users", map[string]any{
		"username": "manager",
		"password": "manager-password",
		"nickname": "Manager",
		"is_admin": true,
	}, rootCookies, &manager)
	protected := userRoleResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/users", map[string]any{
		"username": "protected",
		"password": "protected-password",
		"nickname": "Protected",
		"is_admin": true,
	}, rootCookies, &protected)
	requestJSON(t, handler, http.MethodPut, "/api/users/"+strconv.Itoa(protected.ID), map[string]any{
		"username":       "protected",
		"nickname":       "Protected",
		"is_admin":       true,
		"is_super_admin": true,
	}, rootCookies, nil)

	managerCookies := requestJSON(t, handler, http.MethodPost, "/api/auth/login", map[string]any{
		"username": "manager",
		"password": "manager-password",
	}, nil, nil)
	requestJSON(t, handler, http.MethodPost, "/api/auth/change-credentials", map[string]any{
		"current_password": "manager-password",
		"password":         "manager-new-password",
	}, managerCookies, nil)
	requestJSONExpectStatus(t, handler, http.MethodPut, "/api/users/"+strconv.Itoa(protected.ID), map[string]any{
		"username":       "protected",
		"password":       "attacker-password",
		"nickname":       "Protected",
		"is_admin":       true,
		"is_super_admin": true,
	}, managerCookies, http.StatusForbidden)
	requestJSONExpectStatus(t, handler, http.MethodPost, "/api/auth/login", map[string]any{
		"username": "protected",
		"password": "attacker-password",
	}, nil, http.StatusUnauthorized)
	requestJSON(t, handler, http.MethodPost, "/api/auth/login", map[string]any{
		"username": "protected",
		"password": "protected-password",
	}, nil, nil)

	requestJSONExpectStatus(t, handler, http.MethodPost, "/api/users/"+strconv.Itoa(protected.ID)+"/disable", nil, managerCookies, http.StatusForbidden)
	requestJSON(t, handler, http.MethodPost, "/api/users/"+strconv.Itoa(protected.ID)+"/disable", nil, rootCookies, nil)
	requestJSONExpectStatus(t, handler, http.MethodPost, "/api/users/"+strconv.Itoa(protected.ID)+"/enable", nil, managerCookies, http.StatusForbidden)
}

func TestRegularAdminCannotReadOrMutateSuperAdminAPIKeys(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	var remoteKeys []string
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Management-Key") != "test-management-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(remoteKeys)
		case http.MethodPut:
			if err := json.NewDecoder(r.Body).Decode(&remoteKeys); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer cpa.Close()

	app, err := backendApp.New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	handler := app.Routes()
	rootCookies := requestJSON(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "root",
		"password": "root-password",
		"nickname": "Root",
	}, nil, nil)
	requestJSON(t, handler, http.MethodPut, "/api/settings", map[string]any{
		"cliaproxy_url":  cpa.URL,
		"management_key": "test-management-key",
	}, rootCookies, nil)
	manager := userRoleResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/users", map[string]any{
		"username": "manager",
		"password": "manager-password",
		"nickname": "Manager",
		"is_admin": true,
	}, rootCookies, &manager)
	protected := userRoleResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/users", map[string]any{
		"username": "protected",
		"password": "protected-password",
		"nickname": "Protected",
		"is_admin": true,
	}, rootCookies, &protected)
	requestJSON(t, handler, http.MethodPut, "/api/users/"+strconv.Itoa(protected.ID), map[string]any{
		"username":       "protected",
		"nickname":       "Protected",
		"is_admin":       true,
		"is_super_admin": true,
	}, rootCookies, nil)

	const protectedKey = "sk-super-admin-key"
	bound := userBoundAPIKeyResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/users/"+strconv.Itoa(protected.ID)+"/api-keys", map[string]any{
		"api_key":     protectedKey,
		"description": "Protected key",
	}, rootCookies, &bound)
	const ordinaryKey = "sk-ordinary-admin-visible-only-as-metadata"
	ordinaryBound := userBoundAPIKeyResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/users/"+strconv.Itoa(manager.ID)+"/api-keys", map[string]any{
		"api_key":     ordinaryKey,
		"description": "Ordinary key",
	}, rootCookies, &ordinaryBound)

	managerCookies := requestJSON(t, handler, http.MethodPost, "/api/auth/login", map[string]any{
		"username": "manager",
		"password": "manager-password",
	}, nil, nil)
	requestJSON(t, handler, http.MethodPost, "/api/auth/change-credentials", map[string]any{
		"current_password": "manager-password",
		"password":         "manager-new-password",
	}, managerCookies, nil)
	protectedCookies := requestJSON(t, handler, http.MethodPost, "/api/auth/login", map[string]any{
		"username": "protected",
		"password": "protected-password",
	}, nil, nil)
	requestJSON(t, handler, http.MethodPost, "/api/auth/change-credentials", map[string]any{
		"current_password": "protected-password",
		"password":         "protected-new-password",
	}, protectedCookies, nil)

	var managerUsers []userAPIKeyVisibilityResponse
	requestJSON(t, handler, http.MethodGet, "/api/users", nil, managerCookies, &managerUsers)
	managerSawProtectedUser := false
	for _, user := range managerUsers {
		if user.ID == protected.ID {
			managerSawProtectedUser = true
			if len(user.APIKeys) != 0 {
				t.Fatalf("regular-admin user list exposes super-admin keys: %#v", user.APIKeys)
			}
		}
		if user.ID == manager.ID {
			for _, key := range user.APIKeys {
				if key.APIKey != nil {
					t.Fatalf("regular-admin user list exposes ordinary-user API key plaintext: %#v", key)
				}
			}
		}
	}
	if !managerSawProtectedUser {
		t.Fatal("regular-admin user list omitted the protected user instead of redacting its keys")
	}
	var observed []observedAPIKeyVisibilityResponse
	requestJSON(t, handler, http.MethodGet, "/api/users/observed-api-keys", nil, managerCookies, &observed)
	for _, key := range observed {
		if (key.UserID != nil && *key.UserID == protected.ID) || (key.APIKey != nil && *key.APIKey == protectedKey) {
			t.Fatalf("regular-admin observed-key list exposes super-admin key: %#v", key)
		}
		if key.UserID != nil && *key.UserID == manager.ID && key.APIKey != nil {
			t.Fatalf("regular-admin observed-key list exposes ordinary-user API key plaintext: %#v", key)
		}
	}

	var rootUsers []userAPIKeyVisibilityResponse
	requestJSON(t, handler, http.MethodGet, "/api/users", nil, rootCookies, &rootUsers)
	var rootSawProtectedKey bool
	for _, user := range rootUsers {
		if user.ID != protected.ID {
			continue
		}
		for _, key := range user.APIKeys {
			if key.APIKeyHash == bound.APIKeyHash {
				if key.APIKey != nil {
					t.Fatalf("super-admin management list exposes plaintext API key: %#v", key)
				}
				rootSawProtectedKey = true
			}
		}
	}
	if !rootSawProtectedKey {
		t.Fatal("super admin management list omitted protected API key metadata")
	}
	var rootObserved []observedAPIKeyVisibilityResponse
	requestJSON(t, handler, http.MethodGet, "/api/users/observed-api-keys", nil, rootCookies, &rootObserved)
	rootSawObservedProtectedKey := false
	for _, key := range rootObserved {
		if key.UserID != nil && *key.UserID == protected.ID {
			if key.APIKey != nil {
				t.Fatalf("super-admin observed-key list exposes plaintext API key: %#v", key)
			}
			rootSawObservedProtectedKey = true
		}
	}
	if !rootSawObservedProtectedKey {
		t.Fatal("super admin did not receive its protected API key in the observed-key list")
	}
	var ownKeys []observedAPIKeyVisibilityResponse
	requestJSON(t, handler, http.MethodGet, "/api/api-keys", nil, protectedCookies, &ownKeys)
	if len(ownKeys) != 1 || ownKeys[0].APIKey == nil || *ownKeys[0].APIKey != protectedKey {
		t.Fatalf("current user key list = %#v, want protected key", ownKeys)
	}

	requestJSONExpectStatus(t, handler, http.MethodPost, "/api/users/"+strconv.Itoa(protected.ID)+"/api-keys", map[string]any{
		"api_key":     "sk-forbidden-rebind",
		"description": "Forbidden",
	}, managerCookies, http.StatusForbidden)
	requestJSONExpectStatus(t, handler, http.MethodPost, "/api/users/"+strconv.Itoa(manager.ID)+"/api-keys", map[string]any{
		"api_key_hash": bound.APIKeyHash,
		"description":  "Forbidden rebind",
	}, managerCookies, http.StatusForbidden)
	requestJSONExpectStatus(t, handler, http.MethodDelete, "/api/users/"+strconv.Itoa(protected.ID)+"/api-keys/"+bound.APIKeyHash, nil, managerCookies, http.StatusForbidden)
	requestJSON(t, handler, http.MethodDelete, "/api/users/"+strconv.Itoa(protected.ID)+"/api-keys/"+bound.APIKeyHash, nil, rootCookies, nil)
}

func TestConcurrentSuperAdminDemotionsKeepOneActive(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	app, err := backendApp.New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	handler := app.Routes()
	rootCookies := requestJSON(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "root",
		"password": "root-password",
		"nickname": "Root",
	}, nil, nil)
	second := userRoleResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/users", map[string]any{
		"username": "second",
		"password": "second-password",
		"nickname": "Second",
		"is_admin": true,
	}, rootCookies, &second)
	requestJSON(t, handler, http.MethodPut, "/api/users/"+strconv.Itoa(second.ID), map[string]any{
		"username":       "second",
		"nickname":       "Second",
		"is_admin":       true,
		"is_super_admin": true,
	}, rootCookies, nil)
	secondCookies := requestJSON(t, handler, http.MethodPost, "/api/auth/login", map[string]any{
		"username": "second",
		"password": "second-password",
	}, nil, nil)
	requestJSON(t, handler, http.MethodPost, "/api/auth/change-credentials", map[string]any{
		"current_password": "second-password",
		"password":         "second-new-password",
	}, secondCookies, nil)

	statuses := concurrentUserRequestStatuses(t, handler,
		concurrentUserRequest{
			Method: http.MethodPut,
			Path:   "/api/users/1",
			Body: map[string]any{
				"username":       "root",
				"nickname":       "Root",
				"is_admin":       true,
				"is_super_admin": false,
			},
			Cookies: secondCookies,
		},
		concurrentUserRequest{
			Method: http.MethodPut,
			Path:   "/api/users/" + strconv.Itoa(second.ID),
			Body: map[string]any{
				"username":       "second",
				"nickname":       "Second",
				"is_admin":       true,
				"is_super_admin": false,
			},
			Cookies: rootCookies,
		},
	)
	assertConcurrentUserMutationStatuses(t, statuses, http.StatusOK, http.StatusConflict)
	assertOneActiveSuperAdmin(t, handler, rootCookies)
}

func TestConcurrentSuperAdminDisablesKeepOneActive(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	app, err := backendApp.New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	handler := app.Routes()
	rootCookies := requestJSON(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "root",
		"password": "root-password",
		"nickname": "Root",
	}, nil, nil)
	first := userRoleResponse{}
	second := userRoleResponse{}
	for _, user := range []struct {
		username string
		password string
		nickname string
		target   *userRoleResponse
	}{
		{"first", "first-password", "First", &first},
		{"second", "second-password", "Second", &second},
	} {
		requestJSON(t, handler, http.MethodPost, "/api/users", map[string]any{
			"username": user.username,
			"password": user.password,
			"nickname": user.nickname,
			"is_admin": true,
		}, rootCookies, user.target)
		requestJSON(t, handler, http.MethodPut, "/api/users/"+strconv.Itoa(user.target.ID), map[string]any{
			"username":       user.username,
			"nickname":       user.nickname,
			"is_admin":       true,
			"is_super_admin": true,
		}, rootCookies, nil)
	}
	firstCookies := requestJSON(t, handler, http.MethodPost, "/api/auth/login", map[string]any{
		"username": "first",
		"password": "first-password",
	}, nil, nil)
	requestJSON(t, handler, http.MethodPost, "/api/auth/change-credentials", map[string]any{
		"current_password": "first-password",
		"password":         "first-new-password",
	}, firstCookies, nil)
	secondCookies := requestJSON(t, handler, http.MethodPost, "/api/auth/login", map[string]any{
		"username": "second",
		"password": "second-password",
	}, nil, nil)
	requestJSON(t, handler, http.MethodPost, "/api/auth/change-credentials", map[string]any{
		"current_password": "second-password",
		"password":         "second-new-password",
	}, secondCookies, nil)
	requestJSON(t, handler, http.MethodPut, "/api/users/1", map[string]any{
		"username":       "root",
		"nickname":       "Root",
		"is_admin":       true,
		"is_super_admin": false,
	}, firstCookies, nil)

	statuses := concurrentUserRequestStatuses(t, handler,
		concurrentUserRequest{
			Method:  http.MethodPost,
			Path:    "/api/users/" + strconv.Itoa(first.ID) + "/disable",
			Cookies: secondCookies,
		},
		concurrentUserRequest{
			Method:  http.MethodPost,
			Path:    "/api/users/" + strconv.Itoa(second.ID) + "/disable",
			Cookies: firstCookies,
		},
	)
	assertConcurrentUserMutationStatuses(t, statuses, http.StatusNoContent, http.StatusConflict)
	// Demoting root revokes its earlier session. Reauthenticate before using the
	// ordinary-admin session to inspect the remaining super administrator.
	rootCookies = requestJSON(t, handler, http.MethodPost, "/api/auth/login", map[string]any{
		"username": "root",
		"password": "root-password",
	}, nil, nil)
	assertOneActiveSuperAdmin(t, handler, rootCookies)
}

func concurrentUserRequestStatuses(t *testing.T, handler http.Handler, specs ...concurrentUserRequest) []int {
	t.Helper()
	requests := make([]*http.Request, 0, len(specs))
	for _, spec := range specs {
		var body *bytes.Reader
		if spec.Body != nil {
			encoded, err := json.Marshal(spec.Body)
			if err != nil {
				t.Fatalf("marshal %s %s request body: %v", spec.Method, spec.Path, err)
			}
			body = bytes.NewReader(encoded)
		} else {
			body = bytes.NewReader(nil)
		}
		request := httptest.NewRequest(spec.Method, spec.Path, body)
		if spec.Body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		for _, cookie := range spec.Cookies {
			request.AddCookie(cookie)
		}
		requests = append(requests, request)
	}

	start := make(chan struct{})
	statuses := make(chan int, len(requests))
	for _, request := range requests {
		go func(request *http.Request) {
			<-start
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			statuses <- recorder.Code
		}(request)
	}
	close(start)

	result := make([]int, 0, len(requests))
	for range requests {
		result = append(result, <-statuses)
	}
	return result
}

func assertConcurrentUserMutationStatuses(t *testing.T, statuses []int, expected ...int) {
	t.Helper()
	if len(statuses) != len(expected) {
		t.Fatalf("concurrent statuses = %v, want %v", statuses, expected)
	}
	counts := map[int]int{}
	for _, status := range statuses {
		counts[status]++
	}
	for _, status := range expected {
		counts[status]--
	}
	for _, count := range counts {
		if count != 0 {
			t.Fatalf("concurrent statuses = %v, want %v", statuses, expected)
		}
	}
}

func assertOneActiveSuperAdmin(t *testing.T, handler http.Handler, cookies []*http.Cookie) {
	t.Helper()
	var users []userSuperAdminStateResponse
	requestJSON(t, handler, http.MethodGet, "/api/users", nil, cookies, &users)
	activeCount := 0
	for _, user := range users {
		if user.IsSuperAdmin && user.DisabledAt == nil {
			activeCount++
		}
	}
	if activeCount != 1 {
		t.Fatalf("active super admin count = %d, want 1; users = %#v", activeCount, users)
	}
}

func TestMustChangePasswordLifecycle(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	app, err := backendApp.New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	handler := app.Routes()
	rootCookies := requestJSON(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "root",
		"password": "root-password",
		"nickname": "Root",
	}, nil, nil)

	created := struct {
		ID int `json:"id"`
	}{}
	requestJSON(t, handler, http.MethodPost, "/api/users", map[string]any{
		"username": "newbie",
		"password": "newbie-password",
		"nickname": "Newbie",
	}, rootCookies, &created)

	loginResp := struct {
		ID                 int  `json:"id"`
		MustChangePassword bool `json:"must_change_password"`
	}{}
	newbieCookies := requestJSON(t, handler, http.MethodPost, "/api/auth/login", map[string]any{
		"username": "newbie",
		"password": "newbie-password",
	}, nil, &loginResp)

	if !loginResp.MustChangePassword {
		t.Fatalf("newly created user must_change_password = false, want true")
	}

	requestJSONExpectStatus(t, handler, http.MethodGet, "/api/account/models", nil, newbieCookies, http.StatusForbidden)
	requestJSONExpectStatus(t, handler, http.MethodPost, "/api/auth/change-credentials", map[string]any{
		"password": "root-new-password",
	}, rootCookies, http.StatusForbidden)

	requestJSONExpectStatus(t, handler, http.MethodPost, "/api/auth/change-credentials", map[string]any{
		"password": "newbie-password",
	}, newbieCookies, http.StatusUnprocessableEntity)

	changeResp := struct {
		MustChangePassword bool `json:"must_change_password"`
	}{}
	oldCookies := append([]*http.Cookie(nil), newbieCookies...)
	newbieCookies = requestJSON(t, handler, http.MethodPost, "/api/auth/change-credentials", map[string]any{
		"password": "newbie-new-password",
	}, newbieCookies, &changeResp)

	if changeResp.MustChangePassword {
		t.Fatalf("after changing credentials must_change_password = true, want false")
	}

	requestJSONExpectStatus(t, handler, http.MethodGet, "/api/account/models", nil, newbieCookies, http.StatusOK)
	requestJSONExpectStatus(t, handler, http.MethodPost, "/api/auth/change-credentials", map[string]any{
		"password": "another-new-password",
	}, oldCookies, http.StatusUnauthorized)
	requestJSONExpectStatus(t, handler, http.MethodPost, "/api/auth/change-credentials", map[string]any{
		"password": "another-new-password",
	}, newbieCookies, http.StatusForbidden)
	requestJSONExpectStatus(t, handler, http.MethodPost, "/api/auth/login", map[string]any{
		"username": "newbie",
		"password": "newbie-password",
	}, nil, http.StatusUnauthorized)
	requestJSON(t, handler, http.MethodPost, "/api/auth/login", map[string]any{
		"username": "newbie",
		"password": "newbie-new-password",
	}, nil, &loginResp)
	if loginResp.MustChangePassword {
		t.Fatal("signing in with the new password must not require another password change")
	}
}
