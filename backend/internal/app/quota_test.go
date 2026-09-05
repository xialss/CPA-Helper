package app_test

import (
	"net/http"
	"strconv"
	"testing"

	backendApp "cpa-helper/backend/internal/app"
)

type quotaAPIUserResponse struct {
	ID    int `json:"id"`
	Quota struct {
		Unlimited        bool     `json:"unlimited"`
		CanCreateKeys    bool     `json:"can_create_keys"`
		LifetimeQuotaUSD *float64 `json:"lifetime_quota_usd"`
	} `json:"quota"`
}

type quotaAPIStatusResponse struct {
	Unlimited        bool     `json:"unlimited"`
	LifetimeQuotaUSD *float64 `json:"lifetime_quota_usd"`
	MonthlyQuotaUSD  *float64 `json:"monthly_quota_usd"`
	CanCreateKeys    bool     `json:"can_create_keys"`
}

type userRoleResponse struct {
	ID           int  `json:"id"`
	IsAdmin      bool `json:"is_admin"`
	IsSuperAdmin bool `json:"is_super_admin"`
}

func TestQuotaAPIPermissionsAndAccountStatus(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	app, err := backendApp.New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	handler := app.Routes()
	adminCookies := requestJSON(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "Admin",
	}, nil, nil)

	member := quotaAPIUserResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/users", map[string]any{
		"username": "member",
		"password": "member-password",
		"nickname": "Member",
		"is_admin": false,
	}, adminCookies, &member)
	if !member.Quota.Unlimited || !member.Quota.CanCreateKeys {
		t.Fatalf("new user quota = %#v, want unlimited and creatable", member.Quota)
	}

	lifetime := 1.25
	monthly := 0.5
	updated := quotaAPIStatusResponse{}
	requestJSON(t, handler, http.MethodPut, "/api/users/"+strconv.Itoa(member.ID)+"/quota", map[string]any{
		"lifetime_quota_usd": lifetime,
		"monthly_quota_usd":  monthly,
	}, adminCookies, &updated)
	if updated.Unlimited || updated.LifetimeQuotaUSD == nil || *updated.LifetimeQuotaUSD != lifetime || updated.MonthlyQuotaUSD == nil || *updated.MonthlyQuotaUSD != monthly {
		t.Fatalf("updated quota = %#v, want configured lifetime and monthly quota", updated)
	}

	memberCookies := requestJSON(t, handler, http.MethodPost, "/api/auth/login", map[string]any{
		"username": "member",
		"password": "member-password",
	}, nil, nil)
	requestJSON(t, handler, http.MethodPost, "/api/auth/change-credentials", map[string]any{
		"current_password": "member-password",
		"password":         "member-new-password",
	}, memberCookies, nil)

	accountQuota := quotaAPIStatusResponse{}
	requestJSON(t, handler, http.MethodGet, "/api/account/quota", nil, memberCookies, &accountQuota)
	if accountQuota.LifetimeQuotaUSD == nil || *accountQuota.LifetimeQuotaUSD != lifetime || accountQuota.MonthlyQuotaUSD == nil || *accountQuota.MonthlyQuotaUSD != monthly {
		t.Fatalf("account quota = %#v, want member quota", accountQuota)
	}

	requestJSONExpectStatus(t, handler, http.MethodPut, "/api/users/"+strconv.Itoa(member.ID)+"/quota", map[string]any{
		"lifetime_quota_usd": nil,
	}, memberCookies, http.StatusForbidden)
}

func TestQuotaExhaustedAccountCannotCreateAPIKey(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	app, err := backendApp.New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	handler := app.Routes()
	cookies := requestJSON(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "Admin",
	}, nil, nil)

	zero := 0
	requestJSON(t, handler, http.MethodPut, "/api/users/1/quota", map[string]any{
		"lifetime_quota_usd": zero,
	}, cookies, nil)

	requestJSONExpectStatus(t, handler, http.MethodPost, "/api/api-keys", map[string]any{
		"description": "VSCode",
	}, cookies, http.StatusConflict)
}

func TestSuperAdminControlsQuotaAndSuperAdminRole(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	app, err := backendApp.New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	handler := app.Routes()
	superAdminCookies := requestJSON(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "root",
		"password": "test-password",
		"nickname": "Root",
	}, nil, nil)

	manager := userRoleResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/users", map[string]any{
		"username": "manager",
		"password": "manager-password",
		"nickname": "Manager",
		"is_admin": true,
	}, superAdminCookies, &manager)
	if !manager.IsAdmin || manager.IsSuperAdmin {
		t.Fatalf("new manager role = %#v, want admin-only", manager)
	}

	managerCookies := requestJSON(t, handler, http.MethodPost, "/api/auth/login", map[string]any{
		"username": "manager",
		"password": "manager-password",
	}, nil, nil)
	requestJSON(t, handler, http.MethodPost, "/api/auth/change-credentials", map[string]any{
		"current_password": "manager-password",
		"password":         "manager-new-password",
	}, managerCookies, nil)
	requestJSONExpectStatus(t, handler, http.MethodPut, "/api/users/1/quota", map[string]any{
		"lifetime_quota_usd": 1.25,
	}, managerCookies, http.StatusForbidden)
	requestJSONExpectStatus(t, handler, http.MethodPut, "/api/users/2", map[string]any{
		"username":       "manager",
		"nickname":       "Manager",
		"is_admin":       true,
		"is_super_admin": true,
	}, managerCookies, http.StatusForbidden)

	promoted := userRoleResponse{}
	requestJSON(t, handler, http.MethodPut, "/api/users/2", map[string]any{
		"username":       "manager",
		"nickname":       "Manager",
		"is_admin":       true,
		"is_super_admin": true,
	}, superAdminCookies, &promoted)
	if !promoted.IsAdmin || !promoted.IsSuperAdmin {
		t.Fatalf("promoted manager role = %#v, want super admin", promoted)
	}

	managerCookies = requestJSON(t, handler, http.MethodPost, "/api/auth/login", map[string]any{
		"username": "manager",
		"password": "manager-new-password",
	}, nil, nil)
	updated := quotaAPIStatusResponse{}
	requestJSON(t, handler, http.MethodPut, "/api/users/1/quota", map[string]any{
		"lifetime_quota_usd": 1.25,
	}, managerCookies, &updated)
	if updated.LifetimeQuotaUSD == nil || *updated.LifetimeQuotaUSD != 1.25 {
		t.Fatalf("super admin quota = %#v, want 1.25 lifetime quota", updated)
	}

	requestJSON(t, handler, http.MethodPut, "/api/users/1", map[string]any{
		"username":       "root",
		"nickname":       "Root",
		"is_admin":       true,
		"is_super_admin": false,
	}, managerCookies, nil)
	requestJSONExpectStatus(t, handler, http.MethodPost, "/api/users/2/disable", nil, managerCookies, http.StatusConflict)
}
