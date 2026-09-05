package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/pbkdf2"
)

const (
	sessionCookieName    = "cpa_helper_session"
	sessionMaxAgeSeconds = 60 * 60 * 24 * 7
)

type Identity struct {
	UserID         *int
	Username       *string
	SessionVersion int
}

func CreateSalt() (string, error) {
	return randomHex(16)
}

func CreateSecret() (string, error) {
	bytes := make([]byte, 48)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func HashPassword(password, salt string) string {
	digest := pbkdf2.Key([]byte(password), []byte(salt), 260000, 32, sha256.New)
	return base64.URLEncoding.EncodeToString(digest)
}

func VerifyPassword(password, salt, expected string) bool {
	return hmac.Equal([]byte(HashPassword(password, salt)), []byte(expected))
}

func HashAPIKey(apiKey string) string {
	normalized := strings.TrimSpace(apiKey)
	if normalized == "" {
		normalized = "unknown"
	}
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

func MaskSecret(value *string) string {
	if value == nil {
		return "unknown"
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return "unknown"
	}
	if normalized == "unknown" {
		return "unknown"
	}
	if len(normalized) <= 4 {
		return "****"
	}
	if len(normalized) <= 8 {
		return normalized[:1] + "..." + normalized[len(normalized)-1:]
	}
	return normalized[:6] + "..." + normalized[len(normalized)-4:]
}

func SetSessionCookie(w http.ResponseWriter, userID int, secret string) error {
	return SetSessionCookieWithVersion(w, userID, secret, 0)
}

func SetSessionCookieWithVersion(w http.ResponseWriter, userID int, secret string, sessionVersion int) error {
	return SetSessionCookieWithVersionAndSecure(w, userID, secret, sessionVersion, true)
}

func SetSessionCookieWithVersionAndSecure(w http.ResponseWriter, userID int, secret string, sessionVersion int, secure bool) error {
	token, err := createSessionToken(userID, secret, sessionVersion)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   sessionMaxAgeSeconds,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func ClearSessionCookie(w http.ResponseWriter) {
	ClearSessionCookieWithSecure(w, true)
}

func ClearSessionCookieWithSecure(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func ReadSessionToken(token, secret string) (*Identity, bool) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return nil, false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(parts[0]))
	expected := mac.Sum(nil)
	actual, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		actual, err = base64.URLEncoding.DecodeString(parts[1])
	}
	if err != nil || !hmac.Equal(actual, expected) {
		return nil, false
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		payloadBytes, err = base64.URLEncoding.DecodeString(parts[0])
	}
	if err != nil {
		return nil, false
	}
	var payload map[string]any
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return nil, false
	}
	expFloat, ok := payload["exp"].(float64)
	if !ok || time.Now().Unix() > int64(expFloat) {
		return nil, false
	}
	sub, _ := payload["sub"].(string)
	typ, _ := payload["typ"].(string)
	if typ == "user_id" {
		id, err := strconv.Atoi(sub)
		if err != nil {
			return nil, false
		}
		return &Identity{UserID: &id, SessionVersion: sessionVersion(payload)}, true
	}
	if sub != "" {
		return &Identity{Username: &sub, SessionVersion: sessionVersion(payload)}, true
	}
	return nil, false
}

func SessionCookieName() string {
	return sessionCookieName
}

func randomHex(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func createSessionToken(userID int, secret string, sessionVersion int) (string, error) {
	payload := map[string]any{
		"sub": strconv.Itoa(userID),
		"typ": "user_id",
		"exp": time.Now().Add(sessionMaxAgeSeconds * time.Second).Unix(),
		"ver": sessionVersion,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	payloadPart := base64.RawURLEncoding.EncodeToString(payloadBytes)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payloadPart))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payloadPart + "." + signature, nil
}

func sessionVersion(payload map[string]any) int {
	value, ok := payload["ver"].(float64)
	if !ok || value < 0 {
		return 0
	}
	return int(value)
}
