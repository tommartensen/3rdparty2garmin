package garmin

import "time"

type OAuth1Token struct {
	Token       string
	TokenSecret string
}

type OAuth2Token struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

func (t *OAuth2Token) Expired() bool {
	return time.Now().After(t.ExpiresAt)
}
