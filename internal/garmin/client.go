package garmin

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
)

const (
	// Device-sync style upload as used by the Garmin Connect mobile app;
	// the import-style /upload/fit endpoint yields activities Garmin's
	// analytics treat differently.
	uploadURL       = "https://connectapi.garmin.com/upload-service/upload"
	uploadUserAgent = "GCM-iOS-5.19.1.2"
)

type ErrDuplicate struct{}

func (e ErrDuplicate) Error() string { return "activity already exists in Garmin Connect" }

type ErrRateLimited struct{}

func (e ErrRateLimited) Error() string { return "Garmin rate limit exceeded" }

func UploadFIT(o2 *OAuth2Token, data []byte, filename string) error {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return fmt.Errorf("write form data: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close form: %w", err)
	}

	req, err := http.NewRequest("POST", uploadURL, &buf)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+o2.AccessToken)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("User-Agent", uploadUserAgent)
	req.Header.Set("DI-Backend", "connectapi.garmin.com")
	req.Header.Set("NK", "NT")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusAccepted:
		return nil
	case http.StatusConflict:
		return ErrDuplicate{}
	case http.StatusTooManyRequests:
		return ErrRateLimited{}
	default:
		return fmt.Errorf("upload failed HTTP %d: %s", resp.StatusCode, truncate(body, 300))
	}
}
