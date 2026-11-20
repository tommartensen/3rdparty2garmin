package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
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
	WahooID           int
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
			wahoo_id          INTEGER  NOT NULL,
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
			UNIQUE(user_id, wahoo_id)
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
	`)
	return err
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

func (s *Store) WorkoutCount(userID int) (int, error) {
	row := s.db.QueryRow("SELECT COUNT(*) FROM workouts WHERE user_id = ?", userID)
	var count int
	err := row.Scan(&count)
	return count, err
}

func (s *Store) HasWorkout(userID, wahooID int) bool {
	row := s.db.QueryRow("SELECT 1 FROM workouts WHERE user_id = ? AND wahoo_id = ?", userID, wahooID)
	var x int
	return row.Scan(&x) == nil
}

func (s *Store) UpsertWorkout(userID, wahooID int, name, workoutType string, createdAt time.Time, duration, distance, calories float64, fileURL string) error {
	_, err := s.db.Exec(`
		INSERT INTO workouts (user_id, wahoo_id, name, workout_type, created_at, duration_seconds, distance_meters, calories, file_url)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, wahoo_id) DO UPDATE SET
			name = excluded.name,
			workout_type = excluded.workout_type,
			created_at = excluded.created_at,
			duration_seconds = excluded.duration_seconds,
			distance_meters = excluded.distance_meters,
			calories = excluded.calories,
			file_url = excluded.file_url
	`, userID, wahooID, name, workoutType, createdAt, duration, distance, calories, fileURL)
	return err
}

func (s *Store) GetFileURL(userID, wahooID int) (string, error) {
	row := s.db.QueryRow("SELECT file_url FROM workouts WHERE user_id = ? AND wahoo_id = ?", userID, wahooID)
	var url string
	if err := row.Scan(&url); err != nil {
		return "", fmt.Errorf("get file url: %w", err)
	}
	return url, nil
}

type UnsyncedWorkout struct {
	WahooID int
	FileURL string
}

func (s *Store) ListUnsyncedWorkouts(userID int) ([]UnsyncedWorkout, error) {
	rows, err := s.db.Query(
		"SELECT wahoo_id, file_url FROM workouts WHERE user_id = ? AND fit_file IS NULL AND file_url != ''",
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UnsyncedWorkout
	for rows.Next() {
		var w UnsyncedWorkout
		if err := rows.Scan(&w.WahooID, &w.FileURL); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) ListUnuploadedWorkouts(userID int) ([]int, error) {
	rows, err := s.db.Query(
		"SELECT wahoo_id FROM workouts WHERE user_id = ? AND fit_file IS NOT NULL AND garmin_uploaded_at IS NULL ORDER BY created_at ASC",
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) SaveFitFile(userID, wahooID int, data []byte) error {
	_, err := s.db.Exec(
		"UPDATE workouts SET fit_file = ?, synced_at = ? WHERE user_id = ? AND wahoo_id = ?",
		data, time.Now(), userID, wahooID,
	)
	return err
}

func (s *Store) GetFitFile(userID, wahooID int) ([]byte, error) {
	row := s.db.QueryRow("SELECT fit_file FROM workouts WHERE user_id = ? AND wahoo_id = ?", userID, wahooID)
	var data []byte
	if err := row.Scan(&data); err != nil {
		return nil, fmt.Errorf("get fit file: %w", err)
	}
	return data, nil
}

func (s *Store) ListWorkouts(userID int) ([]Workout, error) {
	rows, err := s.db.Query(`
		SELECT id, user_id, wahoo_id, name, workout_type, created_at,
		       duration_seconds, distance_meters, calories, COALESCE(file_url, ''),
		       fit_file IS NOT NULL, synced_at, garmin_uploaded_at
		FROM workouts
		WHERE user_id = ?
		ORDER BY created_at DESC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list workouts: %w", err)
	}
	defer rows.Close()

	var workouts []Workout
	for rows.Next() {
		var w Workout
		if err := rows.Scan(
			&w.ID, &w.UserID, &w.WahooID, &w.Name, &w.WorkoutType, &w.CreatedAt,
			&w.DurationSeconds, &w.DistanceMeters, &w.Calories, &w.FileURL,
			&w.HasFitFile, &w.SyncedAt, &w.GarminUploadedAt,
		); err != nil {
			return nil, fmt.Errorf("scan workout: %w", err)
		}
		workouts = append(workouts, w)
	}
	return workouts, rows.Err()
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

func (s *Store) MarkGarminUploaded(userID, wahooID int) error {
	_, err := s.db.Exec(
		"UPDATE workouts SET garmin_uploaded_at = ? WHERE user_id = ? AND wahoo_id = ?",
		time.Now(), userID, wahooID,
	)
	return err
}
