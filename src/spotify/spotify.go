package spotify

// Package: spotify
// Author: hfyeomans
// License: GPL-3.0 or later
//
// A small Spotify Web API client that mirrors the user's currently-playing
// track (across any of their devices) onto the XENEON EDGE, plus basic
// transport controls. Credentials and the long-lived refresh token are stored
// server-side (never exposed through the API); the access token is cached in
// memory and refreshed on demand.

import (
	"OpenLinkHub/src/config"
	"OpenLinkHub/src/logger"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	authorizeURL = "https://accounts.spotify.com/authorize"
	tokenURL     = "https://accounts.spotify.com/api/token"
	apiBase      = "https://api.spotify.com/v1"
	scopes       = "user-read-currently-playing user-read-playback-state user-modify-playback-state"
)

// credentials is the persisted secret state. It is written to the config
// directory and is intentionally kept out of the API surface.
type credentials struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	RefreshToken string `json:"refreshToken"`
}

// NowPlaying is the normalized, API-safe view rendered by the widget.
type NowPlaying struct {
	Playing    bool   `json:"playing"`
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	Album      string `json:"album"`
	ArtURL     string `json:"artUrl"`
	Device     string `json:"device"`
	ProgressMs int64  `json:"progressMs"`
	DurationMs int64  `json:"durationMs"`
}

// Status is the connection state shown on the config page.
type Status struct {
	HasCredentials bool   `json:"hasCredentials"`
	Connected      bool   `json:"connected"`
	RedirectURI    string `json:"redirectUri"`
}

var (
	mu          sync.RWMutex
	creds       credentials
	accessToken string
	accessExp   time.Time
	httpClient  = &http.Client{Timeout: 10 * time.Second}
)

func credentialsPath() string {
	return config.GetConfig().ConfigPath + "/database/spotify.json"
}

// RedirectURI is the loopback callback the user registers on their Spotify app.
func RedirectURI() string {
	return fmt.Sprintf("http://127.0.0.1:%d/api/spotify/callback", config.GetConfig().ListenPort)
}

// Init loads persisted credentials (if any) at startup.
func Init() {
	mu.Lock()
	defer mu.Unlock()

	data, err := os.ReadFile(credentialsPath())
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logger.Log(logger.Fields{"error": err}).Warn("Unable to read Spotify credentials")
		}
		return
	}
	if err = json.Unmarshal(data, &creds); err != nil {
		logger.Log(logger.Fields{"error": err}).Warn("Unable to parse Spotify credentials")
	}
}

// Stop clears the in-memory access token.
func Stop() {
	mu.Lock()
	defer mu.Unlock()
	accessToken = ""
	accessExp = time.Time{}
}

func saveLocked() error {
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(credentialsPath(), data, 0600)
}

// GetStatus reports whether credentials and a refresh token are present.
func GetStatus() Status {
	mu.RLock()
	defer mu.RUnlock()
	return Status{
		HasCredentials: creds.ClientID != "" && creds.ClientSecret != "",
		Connected:      creds.RefreshToken != "",
		RedirectURI:    RedirectURI(),
	}
}

// SetCredentials stores the user's Spotify app client id and secret. Changing
// the app invalidates any prior authorization.
func SetCredentials(clientID, clientSecret string) error {
	mu.Lock()
	defer mu.Unlock()
	clientID = strings.TrimSpace(clientID)
	clientSecret = strings.TrimSpace(clientSecret)
	if clientID == "" || clientSecret == "" {
		return errors.New("client id and secret are required")
	}
	if clientID != creds.ClientID || clientSecret != creds.ClientSecret {
		creds.RefreshToken = ""
		accessToken = ""
		accessExp = time.Time{}
	}
	creds.ClientID = clientID
	creds.ClientSecret = clientSecret
	return saveLocked()
}

// AuthURL builds the Spotify authorization URL the user opens once to grant access.
func AuthURL() (string, error) {
	mu.RLock()
	defer mu.RUnlock()
	if creds.ClientID == "" {
		return "", errors.New("set client id and secret first")
	}
	q := url.Values{}
	q.Set("client_id", creds.ClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", RedirectURI())
	q.Set("scope", scopes)
	return authorizeURL + "?" + q.Encode(), nil
}

// Exchange trades an authorization code for a refresh token and stores it.
func Exchange(code string) error {
	mu.Lock()
	defer mu.Unlock()

	code = strings.TrimSpace(code)
	if code == "" {
		return errors.New("authorization code is required")
	}
	if creds.ClientID == "" || creds.ClientSecret == "" {
		return errors.New("set client id and secret first")
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", RedirectURI())

	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := tokenRequest(form, &tok); err != nil {
		return err
	}
	if tok.RefreshToken == "" {
		return errors.New("spotify did not return a refresh token")
	}
	creds.RefreshToken = tok.RefreshToken
	accessToken = tok.AccessToken
	accessExp = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	return saveLocked()
}

// Disconnect forgets the stored refresh token (keeps app credentials).
func Disconnect() error {
	mu.Lock()
	defer mu.Unlock()
	creds.RefreshToken = ""
	accessToken = ""
	accessExp = time.Time{}
	return saveLocked()
}

// tokenRequest performs a client-authenticated POST to the token endpoint.
// The caller holds mu.
func tokenRequest(form url.Values, out interface{}) error {
	req, err := http.NewRequest(http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	basic := base64.StdEncoding.EncodeToString([]byte(creds.ClientID + ":" + creds.ClientSecret))
	req.Header.Set("Authorization", "Basic "+basic)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("spotify token endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, out)
}

// bearer returns a valid access token, refreshing it when necessary.
func bearer() (string, error) {
	mu.RLock()
	if creds.RefreshToken == "" {
		mu.RUnlock()
		return "", errors.New("spotify is not connected")
	}
	if accessToken != "" && time.Now().Before(accessExp.Add(-30*time.Second)) {
		tok := accessToken
		mu.RUnlock()
		return tok, nil
	}
	mu.RUnlock()

	mu.Lock()
	defer mu.Unlock()
	// Re-check after acquiring the write lock (another goroutine may have refreshed).
	if accessToken != "" && time.Now().Before(accessExp.Add(-30*time.Second)) {
		return accessToken, nil
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", creds.RefreshToken)

	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := tokenRequest(form, &tok); err != nil {
		return "", err
	}
	accessToken = tok.AccessToken
	accessExp = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	if tok.RefreshToken != "" && tok.RefreshToken != creds.RefreshToken {
		creds.RefreshToken = tok.RefreshToken
		_ = saveLocked()
	}
	return accessToken, nil
}

// apiRequest issues an authenticated request to the Spotify Web API.
func apiRequest(method, path string) (int, []byte, error) {
	token, err := bearer()
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequest(method, apiBase+path, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body, nil
}

// GetNowPlaying returns the user's active playback across all devices.
func GetNowPlaying() (NowPlaying, error) {
	status, body, err := apiRequest(http.MethodGet, "/me/player")
	if err != nil {
		return NowPlaying{}, err
	}
	// 204 = no active device / nothing playing.
	if status == http.StatusNoContent || len(body) == 0 {
		return NowPlaying{Playing: false}, nil
	}
	if status != http.StatusOK {
		return NowPlaying{}, fmt.Errorf("spotify player endpoint returned %d: %s", status, strings.TrimSpace(string(body)))
	}

	var pl struct {
		IsPlaying  bool  `json:"is_playing"`
		ProgressMs int64 `json:"progress_ms"`
		Device     struct {
			Name string `json:"name"`
		} `json:"device"`
		Item struct {
			Name       string `json:"name"`
			DurationMs int64  `json:"duration_ms"`
			Artists    []struct {
				Name string `json:"name"`
			} `json:"artists"`
			Album struct {
				Name   string `json:"name"`
				Images []struct {
					URL string `json:"url"`
				} `json:"images"`
			} `json:"album"`
		} `json:"item"`
	}
	if err = json.Unmarshal(body, &pl); err != nil {
		return NowPlaying{}, err
	}

	np := NowPlaying{
		Playing:    pl.IsPlaying,
		Title:      pl.Item.Name,
		Album:      pl.Item.Album.Name,
		Device:     pl.Device.Name,
		ProgressMs: pl.ProgressMs,
		DurationMs: pl.Item.DurationMs,
	}
	artists := make([]string, 0, len(pl.Item.Artists))
	for _, a := range pl.Item.Artists {
		artists = append(artists, a.Name)
	}
	np.Artist = strings.Join(artists, ", ")
	if len(pl.Item.Album.Images) > 0 {
		np.ArtURL = pl.Item.Album.Images[0].URL
	}
	return np, nil
}

// Control issues a transport command (play, pause, next, previous). Requires
// Spotify Premium and an active device.
func Control(action string) error {
	var method, path string
	switch action {
	case "play":
		method, path = http.MethodPut, "/me/player/play"
	case "pause":
		method, path = http.MethodPut, "/me/player/pause"
	case "next":
		method, path = http.MethodPost, "/me/player/next"
	case "previous":
		method, path = http.MethodPost, "/me/player/previous"
	default:
		return errors.New("invalid control action")
	}
	status, body, err := apiRequest(method, path)
	if err != nil {
		return err
	}
	// 2xx = success (204 No Content is typical).
	if status < 200 || status >= 300 {
		return fmt.Errorf("spotify control returned %d: %s", status, strings.TrimSpace(string(body)))
	}
	return nil
}
