package main

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"embed"
	"encoding/pem"
	"fmt"
	"html/template"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/tommartensen/3rdparty2garmin/internal/db"
	"github.com/tommartensen/3rdparty2garmin/internal/handlers"
	"github.com/tommartensen/3rdparty2garmin/internal/mywhoosh"
	"github.com/tommartensen/3rdparty2garmin/internal/wahoo"
)

//go:embed templates
var templateFS embed.FS

func main() {
	loadEnv(".env")

	clientID := requireEnv("WAHOO_CLIENT_ID")
	clientSecret := requireEnv("WAHOO_CLIENT_SECRET")
	baseURL := envDefault("BASE_URL", "https://localhost:8080")
	listenAddr := envDefault("LISTEN_ADDR", ":8080")

	store, err := db.New("3rdparty2garmin.db")
	if err != nil {
		log.Fatalf("open database: %v", err)
	}

	client := wahoo.NewClient(clientID, clientSecret, baseURL)

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
	loginTmpl := template.Must(template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/layout.html", "templates/login.html"))
	workoutsTmpl := template.Must(template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/layout.html", "templates/workouts.html"))
	garminLoginTmpl := template.Must(template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/layout.html", "templates/garmin_login.html"))
	mywhooshLoginTmpl := template.Must(template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/layout.html", "templates/mywhoosh_login.html"))

	session := handlers.NewSessionManager(clientSecret)
	whoosh := mywhoosh.NewClient()
	auth := handlers.NewAuthHandler(client, store, session, loginTmpl)
	workouts := handlers.NewWorkoutHandler(client, whoosh, store, session, workoutsTmpl)
	garminHandler := handlers.NewGarminHandler(client, whoosh, store, session, garminLoginTmpl)
	mywhooshHandler := handlers.NewMyWhooshHandler(whoosh, store, session, mywhooshLoginTmpl)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", auth.RootHandler(workouts.Index))
	mux.HandleFunc("GET /login", auth.Login)
	mux.HandleFunc("POST /logout", auth.Logout)
	mux.HandleFunc("GET /workouts", session.RequireAuth(workouts.List))
	mux.HandleFunc("GET /garmin/login", session.RequireAuth(garminHandler.LoginForm))
	mux.HandleFunc("POST /garmin/login", session.RequireAuth(garminHandler.Login))
	mux.HandleFunc("GET /mywhoosh/login", session.RequireAuth(mywhooshHandler.LoginForm))
	mux.HandleFunc("POST /mywhoosh/login", session.RequireAuth(mywhooshHandler.Login))
	mux.HandleFunc("POST /mywhoosh/disconnect", session.RequireAuth(mywhooshHandler.Disconnect))
	mux.HandleFunc("POST /workouts/upload-all", session.RequireAuth(garminHandler.UploadAll))
	mux.HandleFunc("POST /workouts/{source}/{id}/upload", session.RequireAuth(garminHandler.Upload))

	if err := ensureTLSCerts("cert.pem", "key.pem", baseURL); err != nil {
		log.Fatalf("tls certs: %v", err)
	}

	log.Printf("starting server on %s", baseURL)
	err = http.ListenAndServeTLS(listenAddr, "cert.pem", "key.pem", mux)
	// Close explicitly: log.Fatalf exits without running deferred calls.
	if closeErr := store.Close(); closeErr != nil {
		log.Printf("close database: %v", closeErr)
	}
	log.Fatalf("server: %v", err)
}

// ensureTLSCerts generates a self-signed certificate for the configured
// host when the PEM files are missing, so the standalone binary starts
// without manual openssl steps. Existing files are left untouched.
func ensureTLSCerts(certFile, keyFile, baseURL string) error {
	_, certErr := os.Stat(certFile)
	_, keyErr := os.Stat(keyFile)
	if certErr == nil && keyErr == nil {
		return nil
	}

	host := "localhost"
	if u, err := url.Parse(baseURL); err == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	dnsNames := []string{host}
	if host != "localhost" {
		dnsNames = append(dnsNames, "localhost")
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("generate serial: %w", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("create certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal key: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", certFile, err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", keyFile, err)
	}
	log.Printf("generated self-signed TLS certificate for %s (%s, %s)", host, certFile, keyFile)
	return nil
}

func envDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
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
