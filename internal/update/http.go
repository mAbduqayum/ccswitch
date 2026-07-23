package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// latestReleaseURL is the GitHub API endpoint for the newest published release.
const latestReleaseURL = "https://api.github.com/repos/mAbduqayum/ccswitch/releases/latest"

// httpReleaser is the real network-backed Releaser. It is the only type in
// ccswitch that opens a network connection.
type httpReleaser struct {
	client *http.Client
	apiURL string
}

// NewHTTPReleaser builds a Releaser that talks to GitHub Releases.
func NewHTTPReleaser() Releaser {
	return &httpReleaser{
		client: &http.Client{Timeout: 60 * time.Second},
		apiURL: latestReleaseURL,
	}
}

func (h *httpReleaser) Latest(ctx context.Context) (Release, error) {
	body, err := h.get(ctx, h.apiURL)
	if err != nil {
		return Release{}, err
	}
	var payload struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Release{}, fmt.Errorf("decode release metadata: %w", err)
	}
	rel := Release{Tag: payload.TagName}
	for _, a := range payload.Assets {
		rel.Assets = append(rel.Assets, Asset{Name: a.Name, URL: a.URL})
	}
	return rel, nil
}

func (h *httpReleaser) Fetch(ctx context.Context, url string) ([]byte, error) {
	return h.get(ctx, url)
}

func (h *httpReleaser) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// GitHub's API rejects requests without a User-Agent.
	req.Header.Set("User-Agent", "ccswitch-update")
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxDownloadSize))
}
