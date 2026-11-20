package mywhoosh

import (
	"encoding/base64"
	"testing"
	"time"
)

// fakeJWT builds an unsigned JWT with the given payload JSON.
func fakeJWT(payload string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"typ":"JWT","alg":"HS256"}`)) + "." + enc([]byte(payload)) + ".sig"
}

func TestTokenExpiry(t *testing.T) {
	tests := []struct {
		name  string
		token string
		want  time.Time
	}{
		{"exp claim", fakeJWT(`{"sub":"x","exp":1796481627}`), time.Unix(1796481627, 0)},
		{"padded payload", "h." + base64.URLEncoding.EncodeToString([]byte(`{"exp": 1796481627}`)) + ".s", time.Unix(1796481627, 0)},
		{"no exp claim", fakeJWT(`{"sub":"x"}`), time.Time{}},
		{"payload not JSON", fakeJWT(`not json`), time.Time{}},
		{"not a JWT", "opaque-token", time.Time{}},
		{"bad base64", "a.!!!.c", time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TokenExpiry(tt.token); !got.Equal(tt.want) {
				t.Errorf("TokenExpiry() = %v, want %v", got, tt.want)
			}
		})
	}
}
