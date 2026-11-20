package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"html/template"
	"log"
	"net/http"

	"github.com/tommartensen/3rdparty2garmin/internal/db"
	"github.com/tommartensen/3rdparty2garmin/internal/wahoo"
)

type AuthHandler struct {
	client  *wahoo.Client
	store   *db.Store
	session *SessionManager
	tmpl    *template.Template
}

func NewAuthHandler(client *wahoo.Client, store *db.Store, session *SessionManager, tmpl *template.Template) *AuthHandler {
	return &AuthHandler{client: client, store: store, session: session, tmpl: tmpl}
}

func (h *AuthHandler) Login(w http.ResponseWriter, _ *http.Request) {
	state := randomState()
	http.SetCookie(w, &http.Cookie{
		Name:     "oauth_state",
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
	data := map[string]string{
		"AuthURL": h.client.AuthCodeURL(state),
	}
	if err := h.tmpl.ExecuteTemplate(w, "login.html", data); err != nil {
		log.Printf("render login: %v", err)
	}
}

func (h *AuthHandler) Callback(w http.ResponseWriter, r *http.Request) {
	stateCookie, err := r.Cookie("oauth_state")
	if err != nil || stateCookie.Value != r.URL.Query().Get("state") {
		http.Error(w, "invalid OAuth state", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "oauth_state", MaxAge: -1, Path: "/"})

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing authorization code", http.StatusBadRequest)
		return
	}

	token, err := h.client.Exchange(r.Context(), code)
	if err != nil {
		log.Printf("token exchange: %v", err)
		http.Error(w, "token exchange failed", http.StatusInternalServerError)
		return
	}

	wahooUserID, err := h.client.GetUser(r.Context(), token)
	if err != nil {
		log.Printf("get wahoo user: %v", err)
		http.Error(w, "failed to get Wahoo user", http.StatusInternalServerError)
		return
	}

	userID, err := h.store.UpsertUser(wahooUserID, token.AccessToken, token.RefreshToken, token.Expiry)
	if err != nil {
		log.Printf("upsert user: %v", err)
		http.Error(w, "failed to save user", http.StatusInternalServerError)
		return
	}

	h.session.SetUserID(w, userID)
	http.Redirect(w, r, "/workouts?refresh=1", http.StatusSeeOther)
}

// RootHandler serves as both the OAuth callback (when ?code= is present) and
// the default index page.
func (h *AuthHandler) RootHandler(index http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("code") != "" {
			h.Callback(w, r)
			return
		}
		index(w, r)
	}
}

func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	h.session.Clear(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func randomState() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
