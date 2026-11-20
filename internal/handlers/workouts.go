package handlers

import (
	"html/template"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/tommartensen/wahoo2garmin/internal/db"
	"github.com/tommartensen/wahoo2garmin/internal/wahoo"

	"golang.org/x/oauth2"
)

type WorkoutHandler struct {
	client  *wahoo.Client
	store   *db.Store
	session *SessionManager
	tmpl    *template.Template
}

func NewWorkoutHandler(client *wahoo.Client, store *db.Store, session *SessionManager, tmpl *template.Template) *WorkoutHandler {
	return &WorkoutHandler{client: client, store: store, session: session, tmpl: tmpl}
}

func (h *WorkoutHandler) Index(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.session.GetUserID(r); ok {
		http.Redirect(w, r, "/workouts", http.StatusSeeOther)
	} else {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
}

func (h *WorkoutHandler) List(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromRequest(h.session, r)

	// Only fetch from Wahoo API on explicit refresh (?refresh=1)
	if r.URL.Query().Get("refresh") == "1" {
		h.refreshFromWahoo(r, userID)
	}

	workouts, err := h.store.ListWorkouts(userID)
	if err != nil {
		log.Printf("list workouts from db: %v", err)
		http.Error(w, "failed to list workouts", http.StatusInternalServerError)
		return
	}

	synced, uploaded := 0, 0
	for _, w := range workouts {
		if w.HasFitFile {
			synced++
		}
		if w.GarminUploadedAt != nil {
			uploaded++
		}
	}

	data := struct {
		Workouts        []db.Workout
		GarminConnected bool
		TotalWorkouts   int
		TotalSynced     int
		TotalUploaded   int
	}{
		Workouts:        workouts,
		GarminConnected: h.store.HasGarminTokens(userID),
		TotalWorkouts:   len(workouts),
		TotalSynced:     synced,
		TotalUploaded:   uploaded,
	}

	if err := h.tmpl.ExecuteTemplate(w, "workouts.html", data); err != nil {
		log.Printf("render workouts: %v", err)
	}
}

func (h *WorkoutHandler) refreshFromWahoo(r *http.Request, userID int) {
	user, err := h.store.GetUser(userID)
	if err != nil {
		log.Printf("get user: %v", err)
		return
	}

	token := &oauth2.Token{
		AccessToken:  user.AccessToken,
		RefreshToken: user.RefreshToken,
		Expiry:       user.Expiry,
		TokenType:    "Bearer",
	}

	if token.Expiry.Before(time.Now()) {
		src := h.client.OAuthConfig.TokenSource(r.Context(), token)
		newToken, err := src.Token()
		if err != nil {
			log.Printf("refresh token: %v", err)
			return
		}
		if err := h.store.UpdateTokens(userID, newToken.AccessToken, newToken.RefreshToken, newToken.Expiry); err != nil {
			log.Printf("update tokens: %v", err)
		}
		token = newToken
	}

	count, _ := h.store.WorkoutCount(userID)

	// Initial fill: fetch from last page backwards so DB fills oldest-first.
	// Incremental sync: fetch newest-first and stop when we hit a known workout.
	opts := wahoo.ListOptions{OldestFirst: count == 0}

	newCount := 0
	err = h.client.ListWorkoutsPageByPage(r.Context(), token, opts, func(page wahoo.WorkoutPage) bool {
		allKnown := true
		for _, ww := range page.Workouts {
			if ww.WorkoutSummary == nil {
				continue
			}
			if count > 0 && h.store.HasWorkout(userID, ww.ID) {
				continue
			}
			allKnown = false
			duration := float64(ww.WorkoutSummary.DurationActiveAccum)
			distance := float64(ww.WorkoutSummary.DistanceAccum)
			calories := float64(ww.WorkoutSummary.CaloriesAccum)
			var fileURL string
			if ww.WorkoutSummary.File != nil {
				fileURL = ww.WorkoutSummary.File.URL
			}
			workoutType := wahoo.WorkoutTypeName(ww.WorkoutTypeID)
			if err := h.store.UpsertWorkout(
				userID, ww.ID, ww.Name, workoutType, ww.Starts.Time,
				duration, distance, calories, fileURL,
			); err != nil {
				log.Printf("upsert workout %d: %v", ww.ID, err)
			}
			newCount++
		}
		log.Printf("synced page %d (%d new workouts, %d total on server)", page.Page, newCount, page.Total)
		// In incremental mode, stop if every workout on this page was already known.
		if count > 0 && allKnown {
			return false
		}
		return true
	})
	if err != nil {
		log.Printf("list workouts from wahoo: %v", err)
	}
}

func (h *WorkoutHandler) Sync(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromRequest(h.session, r)
	wahooID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid workout id", http.StatusBadRequest)
		return
	}

	fileURL, err := h.store.GetFileURL(userID, wahooID)
	if err != nil || fileURL == "" {
		http.Error(w, "no FIT file URL available for this workout — try refreshing first", http.StatusNotFound)
		return
	}

	data, err := h.client.DownloadFitFile(r.Context(), fileURL)
	if err != nil {
		log.Printf("download fit file: %v", err)
		http.Error(w, "failed to download FIT file", http.StatusInternalServerError)
		return
	}

	if err := h.store.SaveFitFile(userID, wahooID, data); err != nil {
		log.Printf("save fit file: %v", err)
		http.Error(w, "failed to save FIT file", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/workouts", http.StatusSeeOther)
}

func (h *WorkoutHandler) SyncAll(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromRequest(h.session, r)

	unsynced, err := h.store.ListUnsyncedWorkouts(userID)
	if err != nil {
		log.Printf("list unsynced: %v", err)
		http.Error(w, "failed to list unsynced workouts", http.StatusInternalServerError)
		return
	}

	synced, failed := 0, 0
	for _, uw := range unsynced {
		data, err := h.client.DownloadFitFile(r.Context(), uw.FileURL)
		if err != nil {
			log.Printf("download fit %d: %v", uw.WahooID, err)
			failed++
			continue
		}
		if err := h.store.SaveFitFile(userID, uw.WahooID, data); err != nil {
			log.Printf("save fit %d: %v", uw.WahooID, err)
			failed++
			continue
		}
		synced++
	}
	log.Printf("sync all: %d synced, %d failed, %d total", synced, failed, len(unsynced))
	http.Redirect(w, r, "/workouts", http.StatusSeeOther)
}

func (h *WorkoutHandler) Download(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromRequest(h.session, r)
	wahooID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid workout id", http.StatusBadRequest)
		return
	}

	data, err := h.store.GetFitFile(userID, wahooID)
	if err != nil || data == nil {
		http.Error(w, "FIT file not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+strconv.Itoa(wahooID)+".fit\"")
	w.Write(data)
}
