package db

import (
	"path/filepath"
	"testing"
	"time"
)

func TestUpsertUserReturnsCorrectIDOnConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	expiry := time.Now().Add(time.Hour)

	idA, err := s.UpsertUser(1001, "tokA", "refA", expiry)
	if err != nil {
		t.Fatal(err)
	}
	idB, err := s.UpsertUser(1002, "tokB", "refB", expiry)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("user A (wahoo 1001) = %d, user B (wahoo 1002) = %d", idA, idB)

	// Returning login for user A: hits ON CONFLICT(wahoo_user_id) DO UPDATE.
	idAgain, err := s.UpsertUser(1001, "tokA2", "refA2", expiry)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("re-login of wahoo 1001 returned userID %d (want %d)", idAgain, idA)
	if idAgain != idA {
		t.Fatalf("re-login of wahoo 1001 returned userID %d, expected %d", idAgain, idA)
	}
}
