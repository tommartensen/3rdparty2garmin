package wahoo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/oauth2"
)

const baseURL = "https://api.wahooligan.com"

type Client struct {
	OAuthConfig *oauth2.Config
}

func NewClient(clientID, clientSecret, redirectURL string) *Client {
	return &Client{
		OAuthConfig: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  redirectURL,
			Endpoint: oauth2.Endpoint{
				AuthURL:  baseURL + "/oauth/authorize",
				TokenURL: baseURL + "/oauth/token",
			},
			Scopes: []string{"user_read", "workouts_read"},
		},
	}
}

func (c *Client) AuthCodeURL(state string) string {
	return c.OAuthConfig.AuthCodeURL(state)
}

func (c *Client) Exchange(ctx context.Context, code string) (*oauth2.Token, error) {
	return c.OAuthConfig.Exchange(ctx, code)
}

type userResponse struct {
	User struct {
		ID int `json:"id"`
	} `json:"user"`
}

func (c *Client) GetUser(ctx context.Context, token *oauth2.Token) (int, error) {
	client := c.OAuthConfig.Client(ctx, token)
	resp, err := client.Get(baseURL + "/v1/user")
	if err != nil {
		return 0, fmt.Errorf("get user: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("get user: status %d: %s", resp.StatusCode, body)
	}
	var u userResponse
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return 0, fmt.Errorf("decode user: %w", err)
	}
	return u.User.ID, nil
}

type WorkoutSummary struct {
	CaloriesAccum       stringFloat `json:"calories_accum"`
	DistanceAccum       stringFloat `json:"distance_accum"`
	DurationActiveAccum stringFloat `json:"duration_active_accum"`
	File                *struct {
		URL string `json:"url"`
	} `json:"file"`
}

type Workout struct {
	ID             int             `json:"id"`
	Name           string          `json:"name"`
	WorkoutTypeID  int             `json:"workout_type_id"`
	Starts         wahooTime       `json:"starts"`
	WorkoutSummary *WorkoutSummary `json:"workout_summary"`
}

// stringFloat parses JSON strings like "765.0" into float64.
type stringFloat float64

func (sf *stringFloat) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		var f float64
		if err2 := json.Unmarshal(data, &f); err2 != nil {
			return err
		}
		*sf = stringFloat(f)
		return nil
	}
	if s == "" {
		*sf = 0
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("parse float %q: %w", s, err)
	}
	*sf = stringFloat(f)
	return nil
}

type wahooTime struct {
	time.Time
}

func (t *wahooTime) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	if s == "" {
		return nil
	}
	for _, layout := range []string{
		time.RFC3339,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05Z",
	} {
		if parsed, err := time.Parse(layout, s); err == nil {
			t.Time = parsed
			return nil
		}
	}
	return fmt.Errorf("cannot parse time %q", s)
}

type workoutsResponse struct {
	Workouts []Workout `json:"workouts"`
	Total    int       `json:"total"`
	Page     int       `json:"page"`
	PerPage  int       `json:"per_page"`
}

var workoutTypeNames = map[int]string{
	0:  "Cycling",
	1:  "Running",
	2:  "Swimming",
	3:  "Strength",
	4:  "Walking",
	5:  "Cardio",
	6:  "E-Bike",
	12: "Indoor Cycling",
	61: "KICKR",
}

func WorkoutTypeName(id int) string {
	if name, ok := workoutTypeNames[id]; ok {
		return name
	}
	return fmt.Sprintf("Type %d", id)
}

// WorkoutPage holds one page of results from the Wahoo API.
type WorkoutPage struct {
	Workouts []Workout
	Total    int
	Page     int
}

type ListOptions struct {
	// OldestFirst fetches from the last page backwards so the DB fills chronologically.
	OldestFirst bool
}

// ListWorkoutsPageByPage calls onPage for each page of workouts fetched from
// the API. It stops when all pages are consumed or onPage returns false.
// If a rate limit (429) is hit, it stops and returns nil — already-delivered
// pages have been handed to onPage and can be persisted by the caller.
func (c *Client) ListWorkoutsPageByPage(ctx context.Context, token *oauth2.Token, opts ListOptions, onPage func(WorkoutPage) bool) error {
	client := c.OAuthConfig.Client(ctx, token)
	perPage := 50

	if opts.OldestFirst {
		return c.listOldestFirst(client, perPage, onPage)
	}
	return c.listNewestFirst(client, perPage, onPage)
}

func (c *Client) fetchPage(client *http.Client, page, perPage int) (*workoutsResponse, error) {
	url := fmt.Sprintf("%s/v1/workouts?page=%d&per_page=%d", baseURL, page, perPage)
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("list workouts page %d: %w", page, err)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		resp.Body.Close()
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("list workouts: status %d: %s", resp.StatusCode, body)
	}
	var wr workoutsResponse
	err = json.NewDecoder(resp.Body).Decode(&wr)
	resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("decode workouts page %d: %w", page, err)
	}
	return &wr, nil
}

func (c *Client) listNewestFirst(client *http.Client, perPage int, onPage func(WorkoutPage) bool) error {
	page := 1
	fetched := 0
	for {
		wr, err := c.fetchPage(client, page, perPage)
		if err != nil {
			return err
		}
		if wr == nil {
			return nil
		}
		fetched += len(wr.Workouts)
		if !onPage(WorkoutPage{Workouts: wr.Workouts, Total: wr.Total, Page: page}) {
			break
		}
		if fetched >= wr.Total || len(wr.Workouts) == 0 {
			break
		}
		page++
	}
	return nil
}

func (c *Client) listOldestFirst(client *http.Client, perPage int, onPage func(WorkoutPage) bool) error {
	// Fetch page 1 just to learn the total, then start from the last page.
	first, err := c.fetchPage(client, 1, perPage)
	if err != nil {
		return err
	}
	if first == nil || first.Total == 0 {
		return nil
	}

	lastPage := (first.Total + perPage - 1) / perPage
	for page := lastPage; page >= 1; page-- {
		var wr *workoutsResponse
		if page == 1 {
			wr = first
		} else {
			wr, err = c.fetchPage(client, page, perPage)
			if err != nil {
				return err
			}
			if wr == nil {
				return nil
			}
		}
		if !onPage(WorkoutPage{Workouts: wr.Workouts, Total: wr.Total, Page: page}) {
			break
		}
	}
	return nil
}

func (c *Client) DownloadFitFile(ctx context.Context, fileURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download fit: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download fit: status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
