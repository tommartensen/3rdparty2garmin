package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Workout sources.
const (
	SourceWahoo    = "wahoo"
	SourceMyWhoosh = "mywhoosh"
)

type User struct {
	ID           int
	WahooUserID  int
	AccessToken  string
	RefreshToken string
	Expiry       time.Time
}

type Workout struct {
	ID                int
	UserID            int
	Source            string
	ExternalID        string
	Name              string
	WorkoutType       string
	CreatedAt         time.Time
	DurationSeconds   float64
	DistanceMeters    float64
	Calories          float64
	FileURL           string
	HasFitFile        bool
	SyncedAt          *time.Time
	GarminUploadedAt  *time.Time
}

type Store struct {
	db *sql.DB
}

func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	if err := s.migrateWorkoutsSource(); err != nil {
		return err
	}
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			id             INTEGER  PRIMARY KEY AUTOINCREMENT,
			wahoo_user_id  INTEGER  UNIQUE NOT NULL,
			access_token   TEXT     NOT NULL,
			refresh_token  TEXT     NOT NULL,
			expiry         DATETIME NOT NULL
		);

		CREATE TABLE IF NOT EXISTS workouts (
			id                INTEGER  PRIMARY KEY AUTOINCREMENT,
			user_id           INTEGER  NOT NULL REFERENCES users(id),
			source            TEXT     NOT NULL DEFAULT 'wahoo',
			external_id       TEXT     NOT NULL,
			name              TEXT,
			workout_type      TEXT,
			created_at        DATETIME,
			duration_seconds  REAL,
			distance_meters   REAL,
			calories          REAL,
			file_url          TEXT,
			fit_file          BLOB,
			synced_at         DATETIME,
			garmin_uploaded_at DATETIME,
			UNIQUE(user_id, source, external_id)
		);

		CREATE TABLE IF NOT EXISTS garmin_tokens (
			id                  INTEGER  PRIMARY KEY AUTOINCREMENT,
			user_id             INTEGER  UNIQUE NOT NULL REFERENCES users(id),
			oauth1_token        TEXT     NOT NULL,
			oauth1_token_secret TEXT     NOT NULL,
			oauth2_access_token TEXT     NOT NULL,
			oauth2_refresh_token TEXT    NOT NULL,
			oauth2_expiry       DATETIME NOT NULL
		);

		CREATE TABLE IF NOT EXISTS mywhoosh_tokens (
			user_id      INTEGER PRIMARY KEY REFERENCES users(id),
			access_token TEXT    NOT NULL,
			whoosh_id    TEXT
		);
	`)
	return err
}

// migrateWorkoutsSource migrates the original wahoo-only workouts table
// (keyed by integer wahoo_id) to the multi-source schema (source +
// external_id TEXT). Rebuilding the table is required to change the
// unique constraint; the guard makes it a no-op once migrated.
func (s *Store) migrateWorkoutsSource() error {
	var hasSource int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('workouts') WHERE name = 'source'`).Scan(&hasSource)
	if err != nil {
		return fmt.Errorf("inspect workouts schema: %w", err)
	}
	if hasSource > 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`
		CREATE TABLE workouts_new (
			id                INTEGER  PRIMARY KEY AUTOINCREMENT,
			user_id           INTEGER  NOT NULL REFERENCES users(id),
			source            TEXT     NOT NULL DEFAULT 'wahoo',
			external_id       TEXT     NOT NULL,
			name              TEXT,
			workout_type      TEXT,
			created_at        DATETIME,
			duration_seconds  REAL,
			distance_meters   REAL,
			calories          REAL,
			file_url          TEXT,
			fit_file          BLOB,
			synced_at         DATETIME,
			garmin_uploaded_at DATETIME,
			UNIQUE(user_id, source, external_id)
		);

		INSERT INTO workouts_new (user_id, source, external_id, name, workout_type, created_at,
			duration_seconds, distance_meters, calories, file_url, fit_file, synced_at, garmin_uploaded_at)
		SELECT user_id, 'wahoo', CAST(wahoo_id AS TEXT), name, workout_type, created_at,
			duration_seconds, distance_meters, calories, file_url, fit_file, synced_at, garmin_uploaded_at
		FROM workouts;

		DROP TABLE workouts;
		ALTER TABLE workouts_new RENAME TO workouts;
	`)
	if err != nil {
		return fmt.Errorf("migrate workouts to multi-source schema: %w", err)
	}
	return tx.Commit()
}

func (s *Store) UpsertUser(wahooUserID int, accessToken, refreshToken string, expiry time.Time) (int, error) {
	res, err := s.db.Exec(`
		INSERT INTO users (wahoo_user_id, access_token, refresh_token, expiry)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(wahoo_user_id) DO UPDATE SET
			access_token = excluded.access_token,
			refresh_token = excluded.refresh_token,
			expiry = excluded.expiry
	`, wahooUserID, accessToken, refreshToken, expiry)
	if err != nil {
		return 0, fmt.Errorf("upsert user: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil || id == 0 {
		row := s.db.QueryRow("SELECT id FROM users WHERE wahoo_user_id = ?", wahooUserID)
		var userID int
		if err := row.Scan(&userID); err != nil {
			return 0, fmt.Errorf("get user id: %w", err)
		}
		return userID, nil
	}
	return int(id), nil
}

func (s *Store) GetUser(userID int) (*User, error) {
	row := s.db.QueryRow("SELECT id, wahoo_user_id, access_token, refresh_token, expiry FROM users WHERE id = ?", userID)
	u := &User{}
	if err := row.Scan(&u.ID, &u.WahooUserID, &u.AccessToken, &u.RefreshToken, &u.Expiry); err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	return u, nil
}

func (s *Store) UpdateTokens(userID int, accessToken, refreshToken string, expiry time.Time) error {
	_, err := s.db.Exec(
		"UPDATE users SET access_token = ?, refresh_token = ?, expiry = ? WHERE id = ?",
		accessToken, refreshToken, expiry, userID,
	)
	return err
}

func (s *Store) WorkoutCount(userID int, source string) (int, error) {
	row := s.db.QueryRow("SELECT COUNT(*) FROM workouts WHERE user_id = ? AND source = ?", userID, source)
	var count int
	err := row.Scan(&count)
	return count, err
}

func (s *Store) HasWorkout(userID int, source, externalID string) bool {
	row := s.db.QueryRow("SELECT 1 FROM workouts WHERE user_id = ? AND source = ? AND external_id = ?", userID, source, externalID)
	var x int
	return row.Scan(&x) == nil
}

func (s *Store) UpsertWorkout(userID int, source, externalID, name, workoutType string, createdAt time.Time, duration, distance, calories float64, fileURL string) error {
	_, err := s.db.Exec(`
		INSERT INTO workouts (user_id, source, external_id, name, workout_type, created_at, duration_seconds, distance_meters, calories, file_url)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, source, external_id) DO UPDATE SET
			name = excluded.name,
			workout_type = excluded.workout_type,
			created_at = excluded.created_at,
			duration_seconds = excluded.duration_seconds,
			distance_meters = excluded.distance_meters,
			calories = excluded.calories,
			file_url = excluded.file_url
	`, userID, source, externalID, name, workoutType, createdAt, duration, distance, calories, fileURL)
	return err
}

func (s *Store) GetFileURL(userID int, source, externalID string) (string, error) {
	row := s.db.QueryRow("SELECT file_url FROM workouts WHERE user_id = ? AND source = ? AND external_id = ?", userID, source, externalID)
	var url string
	if err := row.Scan(&url); err != nil {
		return "", fmt.Errorf("get file url: %w", err)
	}
	return url, nil
}

func (s *Store) SaveFitFile(userID int, source, externalID string, data []byte) error {
	_, err := s.db.Exec(
		"UPDATE workouts SET fit_file = ?, synced_at = ? WHERE user_id = ? AND source = ? AND external_id = ?",
		data, time.Now(), userID, source, externalID,
	)
	return err
}

func (s *Store) GetFitFile(userID int, source, externalID string) ([]byte, error) {
	row := s.db.QueryRow("SELECT fit_file FROM workouts WHERE user_id = ? AND source = ? AND external_id = ?", userID, source, externalID)
	var data []byte
	if err := row.Scan(&data); err != nil {
		return nil, fmt.Errorf("get fit file: %w", err)
	}
	return data, nil
}

func (s *Store) GetWorkout(userID int, source, externalID string) (*Workout, error) {
	row := s.db.QueryRow(`
		SELECT id, user_id, source, external_id, name, workout_type, created_at,
		       duration_seconds, distance_meters, calories, COALESCE(file_url, ''),
		       fit_file IS NOT NULL, synced_at, garmin_uploaded_at
		FROM workouts
		WHERE user_id = ? AND source = ? AND external_id = ?
	`, userID, source, externalID)
	w := &Workout{}
	if err := row.Scan(
		&w.ID, &w.UserID, &w.Source, &w.ExternalID, &w.Name, &w.WorkoutType, &w.CreatedAt,
		&w.DurationSeconds, &w.DistanceMeters, &w.Calories, &w.FileURL,
		&w.HasFitFile, &w.SyncedAt, &w.GarminUploadedAt,
	); err != nil {
		return nil, fmt.Errorf("get workout: %w", err)
	}
	return w, nil
}

// ListWorkouts returns the user's workouts, newest first. A limit <= 0
// returns all rows; otherwise limit/offset paginate.
func (s *Store) ListWorkouts(userID int, limit, offset int) ([]Workout, error) {
	query := `
		SELECT id, user_id, source, external_id, name, workout_type, created_at,
		       duration_seconds, distance_meters, calories, COALESCE(file_url, ''),
		       fit_file IS NOT NULL, synced_at, garmin_uploaded_at
		FROM workouts
		WHERE user_id = ?
		ORDER BY created_at DESC`
	args := []any{userID}
	if limit > 0 {
		query += " LIMIT ? OFFSET ?"
		args = append(args, limit, offset)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list workouts: %w", err)
	}
	defer rows.Close()

	var workouts []Workout
	for rows.Next() {
		var w Workout
		if err := rows.Scan(
			&w.ID, &w.UserID, &w.Source, &w.ExternalID, &w.Name, &w.WorkoutType, &w.CreatedAt,
			&w.DurationSeconds, &w.DistanceMeters, &w.Calories, &w.FileURL,
			&w.HasFitFile, &w.SyncedAt, &w.GarminUploadedAt,
		); err != nil {
			return nil, fmt.Errorf("scan workout: %w", err)
		}
		workouts = append(workouts, w)
	}
	return workouts, rows.Err()
}

// WorkoutStats returns total, locally synced, and Garmin-uploaded
// workout counts across all sources.
func (s *Store) WorkoutStats(userID int) (total, synced, uploaded int, err error) {
	row := s.db.QueryRow(`
		SELECT COUNT(*),
		       COALESCE(SUM(fit_file IS NOT NULL), 0),
		       COALESCE(SUM(garmin_uploaded_at IS NOT NULL), 0)
		FROM workouts
		WHERE user_id = ?
	`, userID)
	err = row.Scan(&total, &synced, &uploaded)
	if err != nil {
		err = fmt.Errorf("workout stats: %w", err)
	}
	return
}

func (s *Store) SaveGarminTokens(userID int, o1Token, o1Secret, o2Access, o2Refresh string, o2Expiry time.Time) error {
	_, err := s.db.Exec(`
		INSERT INTO garmin_tokens (user_id, oauth1_token, oauth1_token_secret, oauth2_access_token, oauth2_refresh_token, oauth2_expiry)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			oauth1_token = excluded.oauth1_token,
			oauth1_token_secret = excluded.oauth1_token_secret,
			oauth2_access_token = excluded.oauth2_access_token,
			oauth2_refresh_token = excluded.oauth2_refresh_token,
			oauth2_expiry = excluded.oauth2_expiry
	`, userID, o1Token, o1Secret, o2Access, o2Refresh, o2Expiry)
	return err
}

type GarminTokens struct {
	OAuth1Token        string
	OAuth1TokenSecret  string
	OAuth2AccessToken  string
	OAuth2RefreshToken string
	OAuth2Expiry       time.Time
}

func (s *Store) GetGarminTokens(userID int) (*GarminTokens, error) {
	row := s.db.QueryRow(
		"SELECT oauth1_token, oauth1_token_secret, oauth2_access_token, oauth2_refresh_token, oauth2_expiry FROM garmin_tokens WHERE user_id = ?",
		userID,
	)
	t := &GarminTokens{}
	if err := row.Scan(&t.OAuth1Token, &t.OAuth1TokenSecret, &t.OAuth2AccessToken, &t.OAuth2RefreshToken, &t.OAuth2Expiry); err != nil {
		return nil, err
	}
	return t, nil
}

func (s *Store) UpdateGarminOAuth2(userID int, accessToken, refreshToken string, expiry time.Time) error {
	_, err := s.db.Exec(
		"UPDATE garmin_tokens SET oauth2_access_token = ?, oauth2_refresh_token = ?, oauth2_expiry = ? WHERE user_id = ?",
		accessToken, refreshToken, expiry, userID,
	)
	return err
}

func (s *Store) HasGarminTokens(userID int) bool {
	row := s.db.QueryRow("SELECT 1 FROM garmin_tokens WHERE user_id = ?", userID)
	var x int
	return row.Scan(&x) == nil
}

func (s *Store) MarkGarminUploaded(userID int, source, externalID string) error {
	_, err := s.db.Exec(
		"UPDATE workouts SET garmin_uploaded_at = ? WHERE user_id = ? AND source = ? AND external_id = ?",
		time.Now(), userID, source, externalID,
	)
	return err
}

type MyWhooshTokens struct {
	AccessToken string
	WhooshID    string
}

func (s *Store) SaveMyWhooshTokens(userID int, accessToken, whooshID string) error {
	_, err := s.db.Exec(`
		INSERT INTO mywhoosh_tokens (user_id, access_token, whoosh_id)
		VALUES (?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			access_token = excluded.access_token,
			whoosh_id = excluded.whoosh_id
	`, userID, accessToken, whooshID)
	return err
}

func (s *Store) GetMyWhooshTokens(userID int) (*MyWhooshTokens, error) {
	row := s.db.QueryRow("SELECT access_token, whoosh_id FROM mywhoosh_tokens WHERE user_id = ?", userID)
	t := &MyWhooshTokens{}
	if err := row.Scan(&t.AccessToken, &t.WhooshID); err != nil {
		return nil, fmt.Errorf("get mywhoosh tokens: %w", err)
	}
	return t, nil
}

func (s *Store) HasMyWhooshTokens(userID int) bool {
	row := s.db.QueryRow("SELECT 1 FROM mywhoosh_tokens WHERE user_id = ?", userID)
	var x int
	return row.Scan(&x) == nil
}

func (s *Store) DeleteMyWhooshTokens(userID int) error {
	_, err := s.db.Exec("DELETE FROM mywhoosh_tokens WHERE user_id = ?", userID)
	return err
}
