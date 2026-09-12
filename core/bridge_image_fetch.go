package core

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// bridgeImageFetchLimits bound control-plane image URL fetches. Attachments
// published through the canonical image policy are ~a few hundred KB; the cap
// leaves headroom without letting a bad URL stall or OOM the engine.
const (
	bridgeImageFetchTimeout = 20 * time.Second
	bridgeImageMaxBytes     = 25 << 20
)

var bridgeImageFetchClient = &http.Client{Timeout: bridgeImageFetchTimeout}

// fetchBridgeImage downloads an image attachment published by the control
// plane. Only http(s) object URLs are accepted.
func fetchBridgeImage(rawURL string) ([]byte, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid image url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("image url scheme must be http(s), got %q", parsed.Scheme)
	}
	response, err := bridgeImageFetchClient.Get(rawURL)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("image url status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, bridgeImageMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("image url returned an empty body")
	}
	if len(data) > bridgeImageMaxBytes {
		return nil, fmt.Errorf("image url body exceeds %d bytes", bridgeImageMaxBytes)
	}
	return data, nil
}
