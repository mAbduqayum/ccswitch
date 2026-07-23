package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
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

// NewHTTPReleaser builds a Releaser that talks to GitHub Releases. It bounds
// the connect, TLS, and response-header phases rather than the whole exchange:
// a blanket client timeout would guillotine a large download on a slow link
// (the release archive is a few megabytes). Overall cancellation rides on the
// caller's context.
func NewHTTPReleaser() Releaser {
	return &httpReleaser{
		client: &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
				TLSHandshakeTimeout:   30 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
			},
		},
		apiURL: latestReleaseURL,
	}
}

func (h *httpReleaser) Latest(ctx context.Context) (Release, error) {
	body, err := h.get(ctx, h.apiURL, nil)
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

func (h *httpReleaser) Fetch(ctx context.Context, url string, progress func(done, total int64)) ([]byte, error) {
	return h.get(ctx, url, progress)
}

func (h *httpReleaser) get(ctx context.Context, url string, progress func(done, total int64)) ([]byte, error) {
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
	reader := io.LimitReader(resp.Body, maxDownloadSize)
	if progress != nil {
		reader = &progressReader{r: reader, total: resp.ContentLength, report: progress}
	}
	return io.ReadAll(reader)
}

// progressReader reports cumulative bytes read to a callback as a download
// streams. total is the Content-Length (0 when the server didn't send one).
type progressReader struct {
	r      io.Reader
	total  int64
	done   int64
	report func(done, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.done += int64(n)
		total := p.total
		if total < 0 {
			total = 0
		}
		p.report(p.done, total)
	}
	return n, err
}
