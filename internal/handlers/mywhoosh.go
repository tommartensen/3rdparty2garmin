package handlers

import (
	"html/template"
	"log"
	"net/http"

	"github.com/tommartensen/wahoo2garmin/internal/db"
	"github.com/tommartensen/wahoo2garmin/internal/mywhoosh"
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

func (h *MyWhooshHandler) LoginForm(w http.ResponseWriter, r *http.Request) {
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

	accessToken, whooshID, err := h.client.Login(email, password)
	if err != nil {
		log.Printf("mywhoosh login: %v", err)
		http.Error(w, "MyWhoosh login failed: "+err.Error(), http.StatusUnauthorized)
		return
	}

	if err := h.store.SaveMyWhooshTokens(userID, accessToken, whooshID); err != nil {
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
