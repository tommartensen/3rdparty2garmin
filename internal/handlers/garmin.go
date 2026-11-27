package handlers

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"time"

	"github.com/tommartensen/3rdparty2garmin/internal/db"
	"github.com/tommartensen/3rdparty2garmin/internal/fit"
	"github.com/tommartensen/3rdparty2garmin/internal/garmin"
	"github.com/tommartensen/3rdparty2garmin/internal/mywhoosh"
	"github.com/tommartensen/3rdparty2garmin/internal/wahoo"
)

type GarminHandler struct {
	wahoo   *wahoo.Client
	whoosh  *mywhoosh.Client
	store   *db.Store
	session *SessionManager
	tmpl    *template.Template
}

func NewGarminHandler(wahooClient *wahoo.Client, whoosh *mywhoosh.Client, store *db.Store, session *SessionManager, tmpl *template.Template) *GarminHandler {
	return &GarminHandler{wahoo: wahooClient, whoosh: whoosh, store: store, session: session, tmpl: tmpl}
}

func (h *GarminHandler) LoginForm(w http.ResponseWriter, _ *http.Request) {
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
// first-party analytics compute for it.
func (h *GarminHandler) prepareForGarmin(data []byte, source, externalID string) ([]byte, error) {
	opts := fit.Options{StripTemperature: source == db.SourceMyWhoosh}
	fixed, err := fit.Fix(data, opts)
	if err != nil {
		return nil, fmt.Errorf("fix fit %s/%s: %w", source, externalID, err)
	}
	return fixed, nil
}

// downloadFitFile fetches the workout's FIT file from its source; fileRef
// is a download URL for Wahoo, an activity file ID for MyWhoosh.
func (h *GarminHandler) downloadFitFile(ctx context.Context, userID int, source, fileRef string) ([]byte, error) {
	switch source {
	case db.SourceWahoo:
		return h.wahoo.DownloadFitFile(ctx, fileRef)
	case db.SourceMyWhoosh:
		var data []byte
		err := withMyWhooshToken(h.whoosh, h.store, userID, func(accessToken string) error {
			var err error
			data, err = h.whoosh.DownloadFitFile(accessToken, fileRef)
			return err
		})
		return data, err
	default:
		return nil, errors.New("unknown source: " + source)
	}
}

// upload syncs a single workout to Garmin, downloading its FIT file on demand.
func (h *GarminHandler) upload(ctx context.Context, userID int, source, externalID string) error {
	fileRef, err := h.store.GetFileURL(userID, source, externalID)
	if err != nil || fileRef == "" {
		return errors.New("no FIT file available for this workout — try refreshing first")
	}

	data, err := h.downloadFitFile(ctx, userID, source, fileRef)
	if err != nil {
		return fmt.Errorf("download FIT file: %w", err)
	}
	data, err = h.prepareForGarmin(data, source, externalID)
	if err != nil {
		return err
	}

	tokens, err := h.store.GetGarminTokens(userID)
	if err != nil {
		return errors.New("garmin not connected — connect first")
	}

	o1 := &garmin.OAuth1Token{Token: tokens.OAuth1Token, TokenSecret: tokens.OAuth1TokenSecret}
	o2 := &garmin.OAuth2Token{AccessToken: tokens.OAuth2AccessToken, RefreshToken: tokens.OAuth2RefreshToken, ExpiresAt: tokens.OAuth2Expiry}

	if o2.Expired() {
		newO2, err := garmin.RefreshOAuth2(o1)
		if err != nil {
			return errors.New("garmin token refresh failed — try reconnecting")
		}
		if err := h.store.UpdateGarminOAuth2(userID, newO2.AccessToken, newO2.RefreshToken, newO2.ExpiresAt); err != nil {
			log.Printf("update garmin oauth2: %v", err)
		}
		o2 = newO2
	}

	filename := externalID + ".fit"
	err = garmin.UploadFIT(o2, data, filename)
	if err != nil && !errors.As(err, &garmin.ErrDuplicate{}) {
		// Token may have expired mid-flight despite the check above; retry once after refresh.
		newO2, refreshErr := garmin.RefreshOAuth2(o1)
		if refreshErr != nil {
			return fmt.Errorf("upload to garmin: %v (refresh also failed: %v)", err, refreshErr)
		}
		if saveErr := h.store.UpdateGarminOAuth2(userID, newO2.AccessToken, newO2.RefreshToken, newO2.ExpiresAt); saveErr != nil {
			log.Printf("update garmin oauth2: %v", saveErr)
		}
		err = garmin.UploadFIT(newO2, data, filename)
		if err != nil && !errors.As(err, &garmin.ErrDuplicate{}) {
			return err
		}
	}
	if errors.As(err, &garmin.ErrDuplicate{}) {
		log.Printf("workout %s/%s already exists in Garmin Connect", source, externalID)
	}

	if err := h.store.MarkGarminUploaded(userID, source, externalID); err != nil {
		log.Printf("mark uploaded: %v", err)
	}
	return nil
}

func (h *GarminHandler) Upload(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromRequest(h.session, r)
	source, externalID := r.PathValue("source"), r.PathValue("id")
	if !validSource(source) {
		http.Error(w, "invalid source", http.StatusBadRequest)
		return
	}

	if err := h.upload(r.Context(), userID, source, externalID); err != nil {
		log.Printf("upload: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/workouts", http.StatusSeeOther)
}

// UploadAll uploads the workouts currently in view (per page/size), not the entire history.
func (h *GarminHandler) UploadAll(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromRequest(h.session, r)

	if _, err := h.store.GetGarminTokens(userID); err != nil {
		http.Error(w, "Garmin not connected — connect first", http.StatusUnauthorized)
		return
	}

	workouts, cfg, err := workoutsInView(h.store, r, userID)
	if err != nil {
		log.Printf("list workouts: %v", err)
		http.Error(w, "failed to list workouts", http.StatusInternalServerError)
		return
	}

	var inView []db.Workout
	for _, wk := range workouts {
		// Skip workouts the source has no FIT file for; they can never upload.
		if wk.GarminUploadedAt == nil && wk.FileURL != "" {
			inView = append(inView, wk)
		}
	}
	if len(inView) == 0 {
		http.Redirect(w, r, "/workouts?"+cfg.QueryString(), http.StatusSeeOther)
		return
	}

	uploaded, failed := 0, 0
	for i, wk := range inView {
		if i > 0 {
			time.Sleep(1 * time.Second)
		}

		err := h.upload(r.Context(), userID, wk.Source, wk.ExternalID)
		if err != nil {
			if errors.As(err, &garmin.ErrRateLimited{}) {
				log.Printf("upload all: rate limited after %d uploads, stopping", uploaded)
				break
			}
			log.Printf("upload %s/%s failed: %v", wk.Source, wk.ExternalID, err)
			failed++
			continue
		}
		uploaded++
		log.Printf("upload progress: %d/%d (uploaded: %d, failed: %d)", i+1, len(inView), uploaded, failed)
	}

	log.Printf("upload all (page %d, size %s): %d uploaded, %d failed, %d in view", cfg.Page, cfg.SizeParam, uploaded, failed, len(inView))
	http.Redirect(w, r, "/workouts?"+cfg.QueryString(), http.StatusSeeOther)
}

func validSource(source string) bool {
	return source == db.SourceWahoo || source == db.SourceMyWhoosh
}
