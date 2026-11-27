package garmin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Activity is one entry of the Connect activity list.
type Activity struct {
	ID           int64       `json:"activityId"`
	Name         string      `json:"activityName"`
	StartTimeGMT string      `json:"startTimeGMT"`
	DeviceID     json.Number `json:"deviceId"`
}

func setAPIHeaders(req *http.Request, accessToken string) {
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", uploadUserAgent)
	req.Header.Set("DI-Backend", "connectapi.garmin.com")
	req.Header.Set("NK", "NT")
}

// ListActivities returns the account's activities, paginated.
func ListActivities(o2 *OAuth2Token) ([]Activity, error) {
	var all []Activity
	const limit = 100
	for start := 0; ; start += limit {
		url := fmt.Sprintf("https://connectapi.garmin.com/activitylist-service/activities?limit=%d&start=%d", limit, start)
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		setAPIHeaders(req, o2.AccessToken)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("list activities: %w", err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("list activities: HTTP %d: %s", resp.StatusCode, truncate(body, 300))
		}
		var page struct {
			ActivityList []Activity `json:"activityList"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("parse activities: %w", err)
		}
		all = append(all, page.ActivityList...)
		if len(page.ActivityList) < limit {
			return all, nil
		}
	}
}

// DeleteActivity removes an activity from Garmin Connect.
func DeleteActivity(o2 *OAuth2Token, id int64) error {
	req, err := http.NewRequest(http.MethodDelete, fmt.Sprintf("https://connectapi.garmin.com/activity-service/activity/%d", id), nil)
	if err != nil {
		return err
	}
	setAPIHeaders(req, o2.AccessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("delete activity %d: %w", id, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete activity %d: HTTP %d: %s", id, resp.StatusCode, truncate(body, 300))
	}
	return nil
}
