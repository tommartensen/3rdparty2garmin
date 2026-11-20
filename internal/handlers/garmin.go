package handlers

import (
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/tommartensen/wahoo2garmin/internal/db"
	"github.com/tommartensen/wahoo2garmin/internal/fit"
	"github.com/tommartensen/wahoo2garmin/internal/garmin"
)

type GarminHandler struct {
	store   *db.Store
	session *SessionManager
	tmpl    *template.Template
}

func NewGarminHandler(store *db.Store, session *SessionManager, tmpl *template.Template) *GarminHandler {
	return &GarminHandler{store: store, session: session, tmpl: tmpl}
}

func (h *GarminHandler) LoginForm(w http.ResponseWriter, r *http.Request) {
	if err := h.tmpl.ExecuteTemplate(w, "garmin_login.html", nil); err != nil {
		log.Printf("render garmin login: %v", err)
	}
}

func (h *GarminHandler) Login(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromRequest(h.session, r)

	email := r.FormValue("email")
	password := r.FormValue("password")
	if email == "" || password == "" {
		http.Error(w, "email and password are required", http.StatusBadRequest)
		return
	}

	o1, o2, err := garmin.Login(email, password)
	if err != nil {
		log.Printf("garmin login: %v", err)
		http.Error(w, "Garmin login failed: "+err.Error(), http.StatusUnauthorized)
		return
	}

	if err := h.store.SaveGarminTokens(userID, o1.Token, o1.TokenSecret, o2.AccessToken, o2.RefreshToken, o2.ExpiresAt); err != nil {
		log.Printf("save garmin tokens: %v", err)
		http.Error(w, "failed to save Garmin credentials", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/workouts", http.StatusSeeOther)
}

// prepareForGarmin rewrites a FIT file's device identity so Garmin's
// first-party analytics (Load, Readiness, Training Status, ...) compute
// for it. Returns an error if the file can't be processed, so the user
// can fix or replace it instead of uploading a file Garmin would ignore.
func (h *GarminHandler) prepareForGarmin(data []byte, wahooID int) ([]byte, error) {
	fixed, err := fit.Fix(data)
	if err != nil {
		return nil, fmt.Errorf("fix fit %d: %w", wahooID, err)
	}
	return fixed, nil
}

func (h *GarminHandler) Upload(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromRequest(h.session, r)
	wahooID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid workout id", http.StatusBadRequest)
		return
	}

	data, err := h.store.GetFitFile(userID, wahooID)
	if err != nil || data == nil {
		http.Error(w, "FIT file not found — sync the workout first", http.StatusNotFound)
		return
	}
	data, err = h.prepareForGarmin(data, wahooID)
	if err != nil {
		http.Error(w, "could not process FIT file: "+err.Error(), http.StatusInternalServerError)
		return
	}

	tokens, err := h.store.GetGarminTokens(userID)
	if err != nil {
		http.Error(w, "Garmin not connected — connect first", http.StatusUnauthorized)
		return
	}

	o1 := &garmin.OAuth1Token{Token: tokens.OAuth1Token, TokenSecret: tokens.OAuth1TokenSecret}
	o2 := &garmin.OAuth2Token{AccessToken: tokens.OAuth2AccessToken, RefreshToken: tokens.OAuth2RefreshToken, ExpiresAt: tokens.OAuth2Expiry}

	if o2.Expired() {
		newO2, err := garmin.RefreshOAuth2(o1)
		if err != nil {
			log.Printf("refresh garmin oauth2: %v", err)
			http.Error(w, "Garmin token refresh failed — try reconnecting", http.StatusUnauthorized)
			return
		}
		if err := h.store.UpdateGarminOAuth2(userID, newO2.AccessToken, newO2.RefreshToken, newO2.ExpiresAt); err != nil {
			log.Printf("update garmin oauth2: %v", err)
		}
		o2 = newO2
	}

	filename := strconv.Itoa(wahooID) + ".fit"
	err = garmin.UploadFIT(o2, data, filename)
	if err != nil && !errors.As(err, &garmin.ErrDuplicate{}) {
		// Token may have expired mid-flight despite the check above; retry once after refresh.
		newO2, refreshErr := garmin.RefreshOAuth2(o1)
		if refreshErr != nil {
			log.Printf("upload to garmin: %v (refresh also failed: %v)", err, refreshErr)
			http.Error(w, "upload failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if saveErr := h.store.UpdateGarminOAuth2(userID, newO2.AccessToken, newO2.RefreshToken, newO2.ExpiresAt); saveErr != nil {
			log.Printf("update garmin oauth2: %v", saveErr)
		}
		err = garmin.UploadFIT(newO2, data, filename)
		if err != nil && !errors.As(err, &garmin.ErrDuplicate{}) {
			log.Printf("upload to garmin (retry): %v", err)
			http.Error(w, "upload failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if errors.As(err, &garmin.ErrDuplicate{}) {
		log.Printf("workout %d already exists in Garmin Connect", wahooID)
	}

	if err := h.store.MarkGarminUploaded(userID, wahooID); err != nil {
		log.Printf("mark uploaded: %v", err)
	}

	http.Redirect(w, r, "/workouts", http.StatusSeeOther)
}

func (h *GarminHandler) UploadAll(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromRequest(h.session, r)

	wahooIDs, err := h.store.ListUnuploadedWorkouts(userID)
	if err != nil {
		log.Printf("list unuploaded: %v", err)
		http.Error(w, "failed to list workouts", http.StatusInternalServerError)
		return
	}
	if len(wahooIDs) == 0 {
		http.Redirect(w, r, "/workouts", http.StatusSeeOther)
		return
	}

	tokens, err := h.store.GetGarminTokens(userID)
	if err != nil {
		http.Error(w, "Garmin not connected — connect first", http.StatusUnauthorized)
		return
	}

	o1 := &garmin.OAuth1Token{Token: tokens.OAuth1Token, TokenSecret: tokens.OAuth1TokenSecret}
	o2 := &garmin.OAuth2Token{AccessToken: tokens.OAuth2AccessToken, RefreshToken: tokens.OAuth2RefreshToken, ExpiresAt: tokens.OAuth2Expiry}

	if o2.Expired() {
		newO2, err := garmin.RefreshOAuth2(o1)
		if err != nil {
			log.Printf("refresh garmin oauth2: %v", err)
			http.Error(w, "Garmin token refresh failed — try reconnecting", http.StatusUnauthorized)
			return
		}
		if err := h.store.UpdateGarminOAuth2(userID, newO2.AccessToken, newO2.RefreshToken, newO2.ExpiresAt); err != nil {
			log.Printf("update garmin oauth2: %v", err)
		}
		o2 = newO2
	}

	uploaded, duplicates, failed := 0, 0, 0
	for i, wahooID := range wahooIDs {
		if i > 0 {
			time.Sleep(1 * time.Second)
		}

		data, err := h.store.GetFitFile(userID, wahooID)
		if err != nil || data == nil {
			failed++
			continue
		}
		data, err = h.prepareForGarmin(data, wahooID)
		if err != nil {
			log.Printf("skipping upload: %v", err)
			failed++
			continue
		}

		filename := strconv.Itoa(wahooID) + ".fit"
		err = garmin.UploadFIT(o2, data, filename)
		if err != nil {
			if errors.As(err, &garmin.ErrRateLimited{}) {
				log.Printf("upload all: rate limited after %d uploads, stopping", uploaded)
				break
			}
			if errors.As(err, &garmin.ErrDuplicate{}) {
				duplicates++
			} else {
				newO2, refreshErr := garmin.RefreshOAuth2(o1)
				if refreshErr != nil {
					log.Printf("upload %d failed: %v (refresh also failed: %v)", wahooID, err, refreshErr)
					failed++
					continue
				}
				if saveErr := h.store.UpdateGarminOAuth2(userID, newO2.AccessToken, newO2.RefreshToken, newO2.ExpiresAt); saveErr != nil {
					log.Printf("update garmin oauth2: %v", saveErr)
				}
				o2 = newO2
				err = garmin.UploadFIT(o2, data, filename)
				if err != nil && !errors.As(err, &garmin.ErrDuplicate{}) {
					log.Printf("upload %d failed (retry): %v", wahooID, err)
					failed++
					continue
				}
				if errors.As(err, &garmin.ErrDuplicate{}) {
					duplicates++
				} else {
					uploaded++
				}
			}
		} else {
			uploaded++
		}

		if markErr := h.store.MarkGarminUploaded(userID, wahooID); markErr != nil {
			log.Printf("mark uploaded %d: %v", wahooID, markErr)
		}
		log.Printf("upload progress: %d/%d (uploaded: %d, duplicates: %d, failed: %d)", i+1, len(wahooIDs), uploaded, duplicates, failed)
	}

	log.Printf("upload all done: %d uploaded, %d duplicates, %d failed, %d total", uploaded, duplicates, failed, len(wahooIDs))
	http.Redirect(w, r, "/workouts", http.StatusSeeOther)
}
