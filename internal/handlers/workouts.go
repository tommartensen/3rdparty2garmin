package handlers

import (
	"errors"
	"fmt"
	"html/template"
	"log"
	"math"
	"net/http"
	"strconv"
	"time"
	"unicode"

	"github.com/tommartensen/3rdparty2garmin/internal/db"
	"github.com/tommartensen/3rdparty2garmin/internal/mywhoosh"
	"github.com/tommartensen/3rdparty2garmin/internal/wahoo"

	"golang.org/x/oauth2"
)

type WorkoutHandler struct {
	client  *wahoo.Client
	whoosh  *mywhoosh.Client
	store   *db.Store
	session *SessionManager
	tmpl    *template.Template
}

func NewWorkoutHandler(client *wahoo.Client, whoosh *mywhoosh.Client, store *db.Store, session *SessionManager, tmpl *template.Template) *WorkoutHandler {
	return &WorkoutHandler{client: client, whoosh: whoosh, store: store, session: session, tmpl: tmpl}
}

func (h *WorkoutHandler) Index(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.session.GetUserID(r); ok {
		http.Redirect(w, r, "/workouts", http.StatusSeeOther)
	} else {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
}

// pageConfig describes the current workouts-list view: which page of
// which size. Size <= 0 means "all". Shared by the list, sync-all, and
// upload-all handlers so bulk actions apply to the workouts in view.
type pageConfig struct {
	Page       int
	Size       int
	SizeParam  string
	TotalCount int
	TotalPages int
	HasPrev    bool
	HasNext    bool
	PrevPage   int
	NextPage   int
}

func (p pageConfig) Offset() int {
	if p.Size <= 0 {
		return 0
	}
	return (p.Page - 1) * p.Size
}

// QueryString renders the view parameters as URL query, so pagination
// links and post-action redirects keep the user in place.
func (p pageConfig) QueryString() string {
	return fmt.Sprintf("page=%d&size=%s", p.Page, p.SizeParam)
}

// parsePageConfig reads page/size from the request (query string for
// GET, form body for POST) and clamps them against the total count.
func parsePageConfig(r *http.Request, total int) pageConfig {
	p := pageConfig{Page: 1, Size: 25, SizeParam: "25"}
	if sp := r.FormValue("size"); sp != "" {
		if sp == "all" {
			p.Size, p.SizeParam = 0, "all"
		} else if n, err := strconv.Atoi(sp); err == nil && n > 0 {
			p.Size, p.SizeParam = n, sp
		}
	}
	if pp := r.FormValue("page"); pp != "" {
		if n, err := strconv.Atoi(pp); err == nil && n > 0 {
			p.Page = n
		}
	}

	p.TotalCount = total
	if p.Size > 0 {
		p.TotalPages = (total + p.Size - 1) / p.Size
	}
	if p.TotalPages < 1 {
		p.TotalPages = 1
	}
	if p.Page > p.TotalPages {
		p.Page = p.TotalPages
	}
	p.HasPrev = p.Page > 1
	p.HasNext = p.Page < p.TotalPages
	p.PrevPage = p.Page - 1
	p.NextPage = p.Page + 1
	return p
}

// workoutsInView returns the workouts on the requested page plus the
// page configuration, using the same selection as the list view.
func workoutsInView(store *db.Store, r *http.Request, userID int) ([]db.Workout, pageConfig, error) {
	total, _, _, err := store.WorkoutStats(userID)
	if err != nil {
		return nil, pageConfig{}, err
	}
	cfg := parsePageConfig(r, total)
	workouts, err := store.ListWorkouts(userID, cfg.Size, cfg.Offset())
	if err != nil {
		return nil, cfg, err
	}
	return workouts, cfg, nil
}

// donutStat feeds the stats widget's radial meters: a filled arc (the
// count) on a lighter same-ramp track (the remainder), with the value
// labeled in the center.
type donutStat struct {
	Label         string
	Count         int
	Total         int
	Percent       int
	Circumference float64
	Dash          float64
	RoundCap      bool
}

func newDonutStat(label string, count, total int) donutStat {
	const radius = 52
	d := donutStat{
		Label:         label,
		Count:         count,
		Total:         total,
		Circumference: 2 * math.Pi * radius,
	}
	if total > 0 {
		d.Percent = int(math.Round(float64(count) / float64(total) * 100))
		d.Dash = d.Circumference * float64(count) / float64(total)
	}
	// A round cap would render a dot at zero and overlap itself on a
	// full circle; only arc ends get it.
	d.RoundCap = count > 0 && count < total
	return d
}

// capitalizeFirst uppercases the first rune, so MyWhoosh's lowercase
// sport types ("cycling") match Wahoo's capitalized ones ("Cycling").
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return string(unicode.ToUpper(r[0])) + string(r[1:])
}

func (h *WorkoutHandler) List(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromRequest(h.session, r)

	// Only fetch from the source APIs on explicit refresh (?refresh=1)
	if r.URL.Query().Get("refresh") == "1" {
		h.refreshFromWahoo(r, userID)
		h.refreshFromMyWhoosh(userID)
	}

	workouts, cfg, err := workoutsInView(h.store, r, userID)
	if err != nil {
		log.Printf("list workouts from db: %v", err)
		http.Error(w, "failed to list workouts", http.StatusInternalServerError)
		return
	}

	total, synced, uploaded, err := h.store.WorkoutStats(userID)
	if err != nil {
		log.Printf("workout stats: %v", err)
	}

	data := struct {
		Workouts          []db.Workout
		GarminConnected   bool
		MyWhooshConnected bool
		TotalWorkouts     int
		Donuts            [2]donutStat
		PageConfig        pageConfig
	}{
		Workouts:          workouts,
		GarminConnected:   h.store.HasGarminTokens(userID),
		MyWhooshConnected: h.store.HasMyWhooshTokens(userID),
		TotalWorkouts:     total,
		Donuts: [2]donutStat{
			newDonutStat("Synced locally", synced, total),
			newDonutStat("Uploaded to Garmin", uploaded, total),
		},
		PageConfig: cfg,
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

	count, _ := h.store.WorkoutCount(userID, db.SourceWahoo)

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
			if count > 0 && h.store.HasWorkout(userID, db.SourceWahoo, strconv.Itoa(ww.ID)) {
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
				userID, db.SourceWahoo, strconv.Itoa(ww.ID), ww.Name, workoutType, ww.Starts.Time,
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

func (h *WorkoutHandler) refreshFromMyWhoosh(userID int) {
	tokens, err := h.store.GetMyWhooshTokens(userID)
	if err != nil {
		return // not connected
	}

	activities, err := h.whoosh.GetActivities(tokens.AccessToken)
	if err != nil {
		log.Printf("list activities from mywhoosh: %v", err)
		return
	}

	newCount := 0
	for _, act := range activities {
		if h.store.HasWorkout(userID, db.SourceMyWhoosh, act.ID) {
			continue
		}
		start, err := act.StartTime()
		if err != nil {
			log.Printf("activity %s: %v", act.ID, err)
			continue
		}
		workoutType := act.SportType
		if workoutType == "" {
			workoutType = "Virtual Cycling"
		} else {
			workoutType = capitalizeFirst(workoutType)
		}
		name := act.Name
		if name == "" && act.RouteName != "" {
			name = act.RouteName
		}
		if err := h.store.UpsertWorkout(
			userID, db.SourceMyWhoosh, act.ID, name, workoutType, start,
			act.DurationSeconds(), act.Distance*1000, 0, act.ActivityFileID,
		); err != nil {
			log.Printf("upsert mywhoosh activity %s: %v", act.ID, err)
			continue
		}
		newCount++
	}
	log.Printf("synced mywhoosh: %d new activities, %d total on server", newCount, len(activities))
}

// downloadFit fetches the FIT file for a workout from its source.
// fileRef is source-specific: a download URL for Wahoo, an activity
// file ID for MyWhoosh.
func (h *WorkoutHandler) downloadFit(r *http.Request, userID int, source, fileRef string) ([]byte, error) {
	switch source {
	case db.SourceWahoo:
		return h.client.DownloadFitFile(r.Context(), fileRef)
	case db.SourceMyWhoosh:
		tokens, err := h.store.GetMyWhooshTokens(userID)
		if err != nil {
			return nil, errors.New("MyWhoosh not connected — connect first")
		}
		data, err := h.whoosh.DownloadFitFile(tokens.AccessToken, fileRef)
		var unauthorized mywhoosh.ErrUnauthorized
		if errors.As(err, &unauthorized) {
			return nil, errors.New("MyWhoosh session expired — reconnect to MyWhoosh")
		}
		return data, err
	default:
		return nil, errors.New("unknown source: " + source)
	}
}

func (h *WorkoutHandler) Sync(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromRequest(h.session, r)
	source, externalID := r.PathValue("source"), r.PathValue("id")
	if !validSource(source) {
		http.Error(w, "invalid source", http.StatusBadRequest)
		return
	}

	fileRef, err := h.store.GetFileURL(userID, source, externalID)
	if err != nil || fileRef == "" {
		http.Error(w, "no FIT file available for this workout — try refreshing first", http.StatusNotFound)
		return
	}

	data, err := h.downloadFit(r, userID, source, fileRef)
	if err != nil {
		log.Printf("download fit file: %v", err)
		http.Error(w, "failed to download FIT file: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := h.store.SaveFitFile(userID, source, externalID, data); err != nil {
		log.Printf("save fit file: %v", err)
		http.Error(w, "failed to save FIT file", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/workouts", http.StatusSeeOther)
}

// SyncAll syncs the workouts currently in view (per the page/size form
// parameters), not the entire history.
func (h *WorkoutHandler) SyncAll(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromRequest(h.session, r)

	workouts, cfg, err := workoutsInView(h.store, r, userID)
	if err != nil {
		log.Printf("list workouts: %v", err)
		http.Error(w, "failed to list workouts", http.StatusInternalServerError)
		return
	}

	synced, failed := 0, 0
	for _, wk := range workouts {
		if wk.HasFitFile || wk.FileURL == "" {
			continue
		}
		data, err := h.downloadFit(r, userID, wk.Source, wk.FileURL)
		if err != nil {
			log.Printf("download fit %s/%s: %v", wk.Source, wk.ExternalID, err)
			failed++
			continue
		}
		if err := h.store.SaveFitFile(userID, wk.Source, wk.ExternalID, data); err != nil {
			log.Printf("save fit %s/%s: %v", wk.Source, wk.ExternalID, err)
			failed++
			continue
		}
		synced++
	}
	log.Printf("sync all (page %d, size %s): %d synced, %d failed", cfg.Page, cfg.SizeParam, synced, failed)
	http.Redirect(w, r, "/workouts?"+cfg.QueryString(), http.StatusSeeOther)
}

func (h *WorkoutHandler) Download(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromRequest(h.session, r)
	source, externalID := r.PathValue("source"), r.PathValue("id")
	if !validSource(source) {
		http.Error(w, "invalid source", http.StatusBadRequest)
		return
	}

	data, err := h.store.GetFitFile(userID, source, externalID)
	if err != nil || data == nil {
		http.Error(w, "FIT file not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+externalID+".fit\"")
	if _, err := w.Write(data); err != nil {
		log.Printf("write fit download: %v", err)
	}
}
