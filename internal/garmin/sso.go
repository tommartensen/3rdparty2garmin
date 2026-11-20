package garmin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/dghubble/oauth1"
)

const (
	oauthConsumerURL = "https://thegarth.s3.amazonaws.com/oauth_consumer.json"
	ssoUserAgent     = "com.garmin.android.apps.connectmobile"
	garminDomain     = "garmin.com"
)

var (
	csrfRe   = regexp.MustCompile(`name="_csrf"\s+value="(.+?)"`)
	titleRe  = regexp.MustCompile(`<title>(.+?)</title>`)
	ticketRe = regexp.MustCompile(`embed\?ticket=([^"]+)"`)
)

type oauthConsumer struct {
	ConsumerKey    string `json:"consumer_key"`
	ConsumerSecret string `json:"consumer_secret"`
}

type ssoSession struct {
	client  *http.Client
	lastURL string
}

func newSSOSession() *ssoSession {
	jar, _ := cookiejar.New(nil)
	return &ssoSession{
		client: &http.Client{
			Jar:     jar,
			Timeout: 30 * time.Second,
		},
	}
}

func (s *ssoSession) get(reqURL string, useReferer bool) (string, error) {
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", ssoUserAgent)
	if useReferer && s.lastURL != "" {
		req.Header.Set("Referer", s.lastURL)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	s.lastURL = resp.Request.URL.String()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(body, 300))
	}
	return string(body), nil
}

func (s *ssoSession) post(reqURL string, form url.Values, useReferer bool) (string, error) {
	req, err := http.NewRequest("POST", reqURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", ssoUserAgent)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if useReferer && s.lastURL != "" {
		req.Header.Set("Referer", s.lastURL)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	s.lastURL = resp.Request.URL.String()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(body, 300))
	}
	return string(body), nil
}

func Login(email, password string) (*OAuth1Token, *OAuth2Token, error) {
	consumer, err := fetchConsumer()
	if err != nil {
		return nil, nil, fmt.Errorf("fetch consumer: %w", err)
	}

	sess := newSSOSession()
	ssoBase := fmt.Sprintf("https://sso.%s", garminDomain)
	ssoEmbed := ssoBase + "/sso/embed"

	embedParams := url.Values{
		"id":          {"gauth-widget"},
		"embedWidget": {"true"},
		"gauthHost":   {ssoBase + "/sso"},
	}
	signinParams := url.Values{
		"id":                              {"gauth-widget"},
		"embedWidget":                     {"true"},
		"gauthHost":                       {ssoEmbed},
		"service":                         {ssoEmbed},
		"source":                          {ssoEmbed},
		"redirectAfterAccountLoginUrl":    {ssoEmbed},
		"redirectAfterAccountCreationUrl": {ssoEmbed},
	}

	// Step 1: GET embed page to set cookies
	if _, err = sess.get(ssoEmbed+"?"+embedParams.Encode(), false); err != nil {
		return nil, nil, fmt.Errorf("sso embed: %w", err)
	}

	// Step 2: GET signin page to extract CSRF token
	signinURL := ssoBase + "/sso/signin?" + signinParams.Encode()
	body, err := sess.get(signinURL, true)
	if err != nil {
		return nil, nil, fmt.Errorf("sso signin page: %w", err)
	}

	csrf := csrfRe.FindStringSubmatch(body)
	if csrf == nil {
		return nil, nil, fmt.Errorf("CSRF token not found in signin page")
	}

	// Step 3: POST credentials
	formData := url.Values{
		"username": {email},
		"password": {password},
		"embed":    {"true"},
		"_csrf":    {csrf[1]},
	}
	body, err = sess.post(ssoBase+"/sso/signin?"+signinParams.Encode(), formData, true)
	if err != nil {
		return nil, nil, fmt.Errorf("sso login: %w", err)
	}

	titleMatch := titleRe.FindStringSubmatch(body)
	if titleMatch == nil {
		return nil, nil, fmt.Errorf("no title in response — login may have failed")
	}
	title := titleMatch[1]
	if strings.Contains(title, "MFA") {
		return nil, nil, fmt.Errorf("MFA is required but not supported — disable MFA temporarily in Garmin account settings")
	}
	if title != "Success" {
		return nil, nil, fmt.Errorf("login failed: %q (check credentials)", title)
	}

	// Step 4: Extract ticket
	ticketMatch := ticketRe.FindStringSubmatch(body)
	if ticketMatch == nil {
		return nil, nil, fmt.Errorf("ticket not found in response")
	}
	ticket := ticketMatch[1]

	// Step 5: Exchange ticket for OAuth1 token
	oauth1Token, err := getOAuth1Token(consumer, ticket)
	if err != nil {
		return nil, nil, fmt.Errorf("oauth1 token: %w", err)
	}

	// Step 6: Exchange OAuth1 for OAuth2
	oauth2Token, err := exchangeOAuth2(consumer, oauth1Token)
	if err != nil {
		return nil, nil, fmt.Errorf("oauth2 exchange: %w", err)
	}

	return oauth1Token, oauth2Token, nil
}

func RefreshOAuth2(o1 *OAuth1Token) (*OAuth2Token, error) {
	consumer, err := fetchConsumer()
	if err != nil {
		return nil, fmt.Errorf("fetch consumer: %w", err)
	}
	return exchangeOAuth2(consumer, o1)
}

func fetchConsumer() (*oauthConsumer, error) {
	resp, err := http.Get(oauthConsumerURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("consumer fetch HTTP %d", resp.StatusCode)
	}
	var c oauthConsumer
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		return nil, err
	}
	return &c, nil
}

func getOAuth1Token(consumer *oauthConsumer, ticket string) (*OAuth1Token, error) {
	config := oauth1.NewConfig(consumer.ConsumerKey, consumer.ConsumerSecret)
	token := oauth1.NewToken("", "")
	httpClient := config.Client(context.Background(), token)
	httpClient.Timeout = 30 * time.Second

	apiBase := fmt.Sprintf("https://connectapi.%s", garminDomain)
	loginURL := fmt.Sprintf("https://sso.%s/sso/embed", garminDomain)
	reqURL := fmt.Sprintf(
		"%s/oauth-service/oauth/preauthorized?ticket=%s&login-url=%s&accepts-mfa-tokens=true",
		apiBase, url.QueryEscape(ticket), url.QueryEscape(loginURL),
	)

	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ssoUserAgent)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("preauthorized HTTP %d: %s", resp.StatusCode, truncate(body, 300))
	}

	values, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, fmt.Errorf("parse oauth1 response: %w", err)
	}

	return &OAuth1Token{
		Token:       values.Get("oauth_token"),
		TokenSecret: values.Get("oauth_token_secret"),
	}, nil
}

func exchangeOAuth2(consumer *oauthConsumer, o1 *OAuth1Token) (*OAuth2Token, error) {
	config := oauth1.NewConfig(consumer.ConsumerKey, consumer.ConsumerSecret)
	token := oauth1.NewToken(o1.Token, o1.TokenSecret)
	httpClient := config.Client(context.Background(), token)
	httpClient.Timeout = 30 * time.Second

	apiBase := fmt.Sprintf("https://connectapi.%s", garminDomain)
	reqURL := fmt.Sprintf("%s/oauth-service/oauth/exchange/user/2.0", apiBase)

	req, err := http.NewRequest("POST", reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ssoUserAgent)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("exchange HTTP %d: %s", resp.StatusCode, truncate(respBody, 300))
	}

	var raw struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(respBody, &raw); err != nil {
		return nil, fmt.Errorf("parse oauth2: %w", err)
	}

	return &OAuth2Token{
		AccessToken:  raw.AccessToken,
		RefreshToken: raw.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(raw.ExpiresIn) * time.Second),
	}, nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n])
}
