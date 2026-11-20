package handlers

import (
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"time"

	"github.com/tommartensen/3rdparty2garmin/internal/db"
	"github.com/tommartensen/3rdparty2garmin/internal/mywhoosh"
)

type MyWhooshHandler struct {
	client  *mywhoosh.Client
	store   *db.Store
	session *SessionManager
	tmpl    *template.Template
}

func NewMyWhooshHandler(client *mywhoosh.Client, store *db.Store, session *SessionManager, tmpl *template.Template) *MyWhooshHandler {
	return &MyWhooshHandler{client: client, store: store, session: session, tmpl: tmpl}
}

func (h *MyWhooshHandler) LoginForm(w http.ResponseWriter, _ *http.Request) {
	if err := h.tmpl.ExecuteTemplate(w, "mywhoosh_login.html", nil); err != nil {
		log.Printf("render mywhoosh login: %v", err)
	}
}

func (h *MyWhooshHandler) Login(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromRequest(h.session, r)

	email := r.FormValue("email")
	password := r.FormValue("password")
	if email == "" || password == "" {
		http.Error(w, "email and password are required", http.StatusBadRequest)
		return
	}

	tokens, err := h.client.Login(email, password)
	if err != nil {
		log.Printf("mywhoosh login: %v", err)
		http.Error(w, "MyWhoosh login failed: "+err.Error(), http.StatusUnauthorized)
		return
	}

	if err := h.store.SaveMyWhooshTokens(userID, db.MyWhooshTokens{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		WhooshID:     tokens.WhooshID,
	}); err != nil {
		log.Printf("save mywhoosh tokens: %v", err)
		http.Error(w, "failed to save MyWhoosh credentials", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/workouts?refresh=1", http.StatusSeeOther)
}

func (h *MyWhooshHandler) Disconnect(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromRequest(h.session, r)

	if err := h.store.DeleteMyWhooshTokens(userID); err != nil {
		log.Printf("delete mywhoosh tokens: %v", err)
		http.Error(w, "failed to disconnect MyWhoosh", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/workouts", http.StatusSeeOther)
}

var errMyWhooshNotConnected = errors.New("MyWhoosh not connected — connect first")

// myWhooshRefreshMargin is how long before expiry the access token is
// refreshed proactively. It lasts 15 days, so a day of margin is plenty.
const myWhooshRefreshMargin = 24 * time.Hour

// withMyWhooshToken calls fn with the user's MyWhoosh access token,
// refreshing it first if it is about to expire, or after fn fails with
// mywhoosh.ErrUnauthorized (then fn is retried once). If the refresh
// token is rejected too, the stored tokens are deleted so the UI offers
// to reconnect, and mywhoosh.ErrUnauthorized is returned.
func withMyWhooshToken(client *mywhoosh.Client, store *db.Store, userID int, fn func(accessToken string) error) error {
	tokens, err := store.GetMyWhooshTokens(userID)
	if err != nil {
		return errMyWhooshNotConnected
	}

	exp := mywhoosh.TokenExpiry(tokens.AccessToken)
	if tokens.RefreshToken != "" && !exp.IsZero() && time.Until(exp) < myWhooshRefreshMargin {
		refreshed, err := refreshMyWhooshTokens(client, store, userID, tokens)
		var unauthorized mywhoosh.ErrUnauthorized
		switch {
		case errors.As(err, &unauthorized):
			return err
		case err != nil:
			// Transient failure: the old access token may still be valid.
			log.Printf("proactive mywhoosh token refresh: %v", err)
		default:
			tokens = refreshed
		}
	}

	err = fn(tokens.AccessToken)
	var unauthorized mywhoosh.ErrUnauthorized
	if !errors.As(err, &unauthorized) {
		return err
	}
	tokens, err = refreshMyWhooshTokens(client, store, userID, tokens)
	if err != nil {
		return err
	}
	return fn(tokens.AccessToken)
}

// refreshMyWhooshTokens exchanges the stored refresh token for a new
// session and saves it. If there is no refresh token (stored before
// refresh support) or it is rejected, the stored tokens are deleted.
func refreshMyWhooshTokens(client *mywhoosh.Client, store *db.Store, userID int, old *db.MyWhooshTokens) (*db.MyWhooshTokens, error) {
	if old.RefreshToken == "" {
		return nil, expireMyWhooshTokens(store, userID)
	}
	t, err := client.Refresh(old.RefreshToken)
	var unauthorized mywhoosh.ErrUnauthorized
	if errors.As(err, &unauthorized) {
		return nil, expireMyWhooshTokens(store, userID)
	}
	if err != nil {
		return nil, err
	}

	tokens := &db.MyWhooshTokens{AccessToken: t.AccessToken, RefreshToken: t.RefreshToken, WhooshID: t.WhooshID}
	if tokens.WhooshID == "" {
		tokens.WhooshID = old.WhooshID
	}
	if err := store.SaveMyWhooshTokens(userID, *tokens); err != nil {
		return nil, fmt.Errorf("save refreshed mywhoosh tokens: %w", err)
	}
	return tokens, nil
}

// expireMyWhooshTokens deletes a session that can no longer be
// refreshed and returns mywhoosh.ErrUnauthorized.
func expireMyWhooshTokens(store *db.Store, userID int) error {
	if err := store.DeleteMyWhooshTokens(userID); err != nil {
		log.Printf("delete expired mywhoosh tokens: %v", err)
	}
	return mywhoosh.ErrUnauthorized{}
}
