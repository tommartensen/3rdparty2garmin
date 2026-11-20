package main

import (
	"bufio"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/tommartensen/3rdparty2garmin/internal/db"
	"github.com/tommartensen/3rdparty2garmin/internal/handlers"
	"github.com/tommartensen/3rdparty2garmin/internal/mywhoosh"
	"github.com/tommartensen/3rdparty2garmin/internal/wahoo"
)

func main() {
	loadEnv(".env")

	clientID := requireEnv("WAHOO_CLIENT_ID")
	clientSecret := requireEnv("WAHOO_CLIENT_SECRET")

	store, err := db.New("3rdparty2garmin.db")
	if err != nil {
		log.Fatalf("open database: %v", err)
	}

	client := wahoo.NewClient(clientID, clientSecret, "https://localhost:8080")

	funcMap := template.FuncMap{
		"divf": func(a, b float64) float64 { return a / b },
		"fmtDuration": func(seconds float64) string {
			total := int(seconds) / 60
			h, m := total/60, total%60
			if h > 0 {
				return fmt.Sprintf("%dh%02dm", h, m)
			}
			return fmt.Sprintf("%dm", m)
		},
	}
	loginTmpl := template.Must(template.New("").Funcs(funcMap).ParseFiles("templates/layout.html", "templates/login.html"))
	workoutsTmpl := template.Must(template.New("").Funcs(funcMap).ParseFiles("templates/layout.html", "templates/workouts.html"))
	garminLoginTmpl := template.Must(template.New("").Funcs(funcMap).ParseFiles("templates/layout.html", "templates/garmin_login.html"))
	mywhooshLoginTmpl := template.Must(template.New("").Funcs(funcMap).ParseFiles("templates/layout.html", "templates/mywhoosh_login.html"))

	session := handlers.NewSessionManager(clientSecret)
	auth := handlers.NewAuthHandler(client, store, session, loginTmpl)
	workouts := handlers.NewWorkoutHandler(client, mywhoosh.NewClient(), store, session, workoutsTmpl)
	garminHandler := handlers.NewGarminHandler(store, session, garminLoginTmpl)
	mywhooshHandler := handlers.NewMyWhooshHandler(mywhoosh.NewClient(), store, session, mywhooshLoginTmpl)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", auth.RootHandler(workouts.Index))
	mux.HandleFunc("GET /login", auth.Login)
	mux.HandleFunc("POST /logout", auth.Logout)
	mux.HandleFunc("GET /workouts", session.RequireAuth(workouts.List))
	mux.HandleFunc("POST /workouts/sync-all", session.RequireAuth(workouts.SyncAll))
	mux.HandleFunc("POST /workouts/{source}/{id}/sync", session.RequireAuth(workouts.Sync))
	mux.HandleFunc("GET /workouts/{source}/{id}/download", session.RequireAuth(workouts.Download))
	mux.HandleFunc("GET /garmin/login", session.RequireAuth(garminHandler.LoginForm))
	mux.HandleFunc("POST /garmin/login", session.RequireAuth(garminHandler.Login))
	mux.HandleFunc("GET /mywhoosh/login", session.RequireAuth(mywhooshHandler.LoginForm))
	mux.HandleFunc("POST /mywhoosh/login", session.RequireAuth(mywhooshHandler.Login))
	mux.HandleFunc("POST /mywhoosh/disconnect", session.RequireAuth(mywhooshHandler.Disconnect))
	mux.HandleFunc("POST /workouts/upload-all", session.RequireAuth(garminHandler.UploadAll))
	mux.HandleFunc("POST /workouts/{source}/{id}/upload", session.RequireAuth(garminHandler.Upload))

	log.Println("starting server on https://localhost:8080")
	err = http.ListenAndServeTLS(":8080", "cert.pem", "key.pem", mux)
	// Close explicitly: log.Fatalf exits without running deferred calls.
	if closeErr := store.Close(); closeErr != nil {
		log.Printf("close database: %v", closeErr)
	}
	log.Fatalf("server: %v", err)
}

func loadEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if err := os.Setenv(strings.TrimSpace(k), strings.TrimSpace(v)); err != nil {
			log.Printf("set %s from %s: %v", strings.TrimSpace(k), path, err)
		}
	}
	if err := scanner.Err(); err != nil {
		log.Printf("reading %s: %v", path, err)
	}
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required environment variable %s is not set", key)
	}
	return v
}
