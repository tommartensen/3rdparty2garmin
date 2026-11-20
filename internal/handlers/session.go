package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
)

const cookieName = "3rdparty2garmin_session"

type SessionManager struct {
	key []byte
}

func NewSessionManager(secret string) *SessionManager {
	h := sha256.Sum256([]byte(secret))
	return &SessionManager{key: h[:]}
}

func (sm *SessionManager) SetUserID(w http.ResponseWriter, userID int) {
	value := strconv.Itoa(userID)
	sig := sm.sign(value)
	cookie := &http.Cookie{
		Name:     cookieName,
		Value:    value + "." + sig,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
	http.SetCookie(w, cookie)
}

func (sm *SessionManager) GetUserID(r *http.Request) (int, bool) {
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return 0, false
	}
	parts := strings.SplitN(cookie.Value, ".", 2)
	if len(parts) != 2 {
		return 0, false
	}
	if !sm.verify(parts[0], parts[1]) {
		return 0, false
	}
	id, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, false
	}
	return id, true
}

func (sm *SessionManager) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
	})
}

func (sm *SessionManager) sign(value string) string {
	mac := hmac.New(sha256.New, sm.key)
	mac.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (sm *SessionManager) verify(value, sig string) bool {
	expected := sm.sign(value)
	return hmac.Equal([]byte(expected), []byte(sig))
}

func (sm *SessionManager) RequireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := sm.GetUserID(r); !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// UserIDFromRequest is a convenience for handlers that already passed RequireAuth.
func UserIDFromRequest(sm *SessionManager, r *http.Request) int {
	id, _ := sm.GetUserID(r)
	if id == 0 {
		panic("UserIDFromRequest called without auth check")
	}
	return id
}
