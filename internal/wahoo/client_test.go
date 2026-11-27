package wahoo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/oauth2"
)

func TestGetUser(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want    int
		wantErr bool
	}{
		{"documented top-level id", `{"id": 60462, "first": "Bob"}`, 60462, false},
		{"missing id must fail loudly", `{"first": "Bob", "email": "x@y.z"}`, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := &Client{OAuthConfig: &oauth2.Config{}, baseURL: srv.URL}
			id, err := c.GetUser(context.Background(), &oauth2.Token{AccessToken: "test"})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got id %d", id)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if id != tc.want {
				t.Fatalf("got id %d, want %d", id, tc.want)
			}
		})
	}
}
