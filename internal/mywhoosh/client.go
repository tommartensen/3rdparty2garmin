// Package mywhoosh talks to the MyWhoosh API: login, activity listing,
// and FIT file download. Tokens are passed in per call and persisted by
// the caller.
package mywhoosh

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	loginURL       = "https://services.mywhoosh.com/http-service/api/login"
	refreshURL     = "https://services.mywhoosh.com/http-service/api/tokens/generate"
	activitiesBase = "https://service14.mywhoosh.com/v2/"
)

// ErrUnauthorized indicates a rejected MyWhoosh token.
type ErrUnauthorized struct{}

func (e ErrUnauthorized) Error() string { return "MyWhoosh session expired — reconnect to MyWhoosh" }

type Activity struct {
	ID             string  `json:"id"`
	Name           string  `json:"title"`
	Date           float64 `json:"date"`
	ActivityFileID string  `json:"activityFileId"`
	Distance       float64 `json:"distance,omitempty"` // kilometers
	RideDuration   string  `json:"rideDuration,omitempty"`
	AvgPower       float64 `json:"watt,omitempty"`
	AvgHR          float64 `json:"heartrate,omitempty"`
	Elevation      float64 `json:"elevation,omitempty"`
	WattPerKg      float64 `json:"wattPerKg,omitempty"`
	RouteName      string  `json:"routeName,omitempty"`
	SportType      string  `json:"sportType,omitempty"`
	StartDatetime  string  `json:"startDatetime,omitempty"`
}

// StartTime prefers startDatetime over the epoch date field.
func (a *Activity) StartTime() (time.Time, error) {
	if a.StartDatetime != "" {
		if t, err := time.Parse("2006-01-02T15:04:05.000Z", a.StartDatetime); err == nil {
			return t, nil
		}
	}
	if a.Date != 0 {
		return time.Unix(int64(a.Date), 0), nil
	}
	return time.Time{}, fmt.Errorf("activity %s has no date", a.ID)
}

// DurationSeconds parses rideDuration ("01:31:54.000") into seconds; 0 if unparseable.
func (a *Activity) DurationSeconds() float64 {
	if a.RideDuration == "" {
		return 0
	}
	parts := strings.Split(strings.Split(a.RideDuration, ".")[0], ":")
	if len(parts) != 3 {
		return 0
	}
	var h, m, s int
	if _, err := fmt.Sscanf(strings.Join(parts, ":"), "%d:%d:%d", &h, &m, &s); err != nil {
		return 0
	}
	return float64(h*3600 + m*60 + s)
}

// Tokens is a MyWhoosh session; both are JWTs (see research/mywhoosh-api.md).
type Tokens struct {
	AccessToken  string
	RefreshToken string
	WhooshID     string
}

// TokenExpiry returns a MyWhoosh JWT's exp claim (unverified) or the zero time.
func TokenExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return time.Time{}
	}
	return time.Unix(claims.Exp, 0)
}

type Client struct {
	httpClient *http.Client
}

func NewClient() *Client {
	return &Client{httpClient: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) Login(email, password string) (*Tokens, error) {
	payload := map[string]any{
		"Username":      email,
		"Password":      password,
		"Platform":      "Android",
		"Action":        1001,
		"CorrelationId": uuid.New().String(),
		"DeviceId":      uuid.New().String(),
		"Authorization": "",
	}

	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal login payload: %w", err)
	}

	body, status, err := c.do(http.MethodPost, loginURL, "", jsonBody)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("login failed (HTTP %d): %s", status, truncate(body))
	}

	var result struct {
		Success      bool   `json:"Success"`
		Message      string `json:"Message"`
		AccessToken  string `json:"AccessToken"`
		RefreshToken string `json:"RefreshToken"`
		WhooshID     string `json:"WhooshId"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse login response: %w", err)
	}
	if !result.Success {
		msg := result.Message
		if msg == "" {
			msg = "unknown error"
		}
		return nil, fmt.Errorf("login failed: %s", msg)
	}
	if result.AccessToken == "" {
		return nil, fmt.Errorf("login failed: no access token in response")
	}

	return &Tokens{AccessToken: result.AccessToken, RefreshToken: result.RefreshToken, WhooshID: result.WhooshID}, nil
}

// Refresh exchanges a refresh token for a new session; ErrUnauthorized
// means the user needs to log in again.
func (c *Client) Refresh(refreshToken string) (*Tokens, error) {
	body, status, err := c.do(http.MethodGet, refreshURL, refreshToken, nil)
	if err != nil {
		return nil, err
	}
	if status >= 500 {
		return nil, fmt.Errorf("refresh failed (HTTP %d): %s", status, truncate(body))
	}
	// An invalid refresh token yields HTTP 426, not 401.
	if status != http.StatusOK {
		return nil, ErrUnauthorized{}
	}

	var result struct {
		AccessToken  string `json:"AccessToken"`
		RefreshToken string `json:"RefreshToken"`
		WhooshID     string `json:"WhooshId"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse refresh response: %w", err)
	}
	if result.AccessToken == "" || result.RefreshToken == "" {
		return nil, fmt.Errorf("refresh failed: no tokens in response")
	}
	return &Tokens{AccessToken: result.AccessToken, RefreshToken: result.RefreshToken, WhooshID: result.WhooshID}, nil
}

// GetActivities fetches all activities, newest first.
func (c *Client) GetActivities(token string) ([]Activity, error) {
	activitiesURL := activitiesBase + "rider/profile/activities"
	var allActivities []Activity
	page := 1

	for {
		payload := fmt.Sprintf(`{"sortDate":"DESC","page":%d}`, page)

		body, status, err := c.do(http.MethodPost, activitiesURL, token, []byte(payload))
		if err != nil {
			return nil, err
		}
		if status == http.StatusUnauthorized {
			return nil, ErrUnauthorized{}
		}
		if status >= 400 {
			return nil, fmt.Errorf("fetch activities failed (HTTP %d): %s", status, truncate(body))
		}

		var result struct {
			Data struct {
				Results    []json.RawMessage `json:"results"`
				TotalPages int               `json:"totalPages"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("parse activities: %w", err)
		}

		for _, rawAct := range result.Data.Results {
			var act Activity
			if err := json.Unmarshal(rawAct, &act); err == nil {
				allActivities = append(allActivities, act)
			}
		}

		if page >= result.Data.TotalPages {
			break
		}
		page++
	}

	return allActivities, nil
}

// DownloadFitFile fetches an activity's FIT file via its pre-signed S3 URL.
func (c *Client) DownloadFitFile(token, activityFileID string) ([]byte, error) {
	downloadURL := activitiesBase + "rider/profile/download-activity-file"
	payload := fmt.Sprintf(`{"fileId":"%s"}`, activityFileID)

	body, status, err := c.do(http.MethodPost, downloadURL, token, []byte(payload))
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized {
		return nil, ErrUnauthorized{}
	}
	if status >= 400 {
		return nil, fmt.Errorf("download failed (HTTP %d): %s", status, truncate(body))
	}

	var result struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse download response: %w", err)
	}
	if result.Data == "" {
		return nil, fmt.Errorf("no download URL in response")
	}

	fitResp, err := c.httpClient.Get(result.Data)
	if err != nil {
		return nil, fmt.Errorf("fetch FIT file: %w", err)
	}
	defer func() { _ = fitResp.Body.Close() }()

	fitData, err := io.ReadAll(fitResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read FIT file: %w", err)
	}
	if fitResp.StatusCode >= 400 {
		return nil, fmt.Errorf("FIT download failed (HTTP %d)", fitResp.StatusCode)
	}

	return fitData, nil
}

func (c *Client) do(method, url, token string, payload []byte) ([]byte, int, error) {
	req, err := http.NewRequest(method, url, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}
	return body, resp.StatusCode, nil
}

func truncate(body []byte) string {
	return string(body[:min(300, len(body))])
}
