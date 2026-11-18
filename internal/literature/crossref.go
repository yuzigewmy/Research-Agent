// Package literature retrieves bibliographic metadata from Crossref.
package literature

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	crossrefOrigin  = "https://api.crossref.org"
	requestTimeout  = 8 * time.Second
	maxResponseSize = 4 << 20
	maxRows         = 80
	maxQueryBytes   = 1024
)

var (
	// ErrInvalidDOI indicates an unsupported or malformed DOI input.
	ErrInvalidDOI = errors.New("invalid DOI")
	// ErrNotFound means Crossref has no record for the requested DOI.
	ErrNotFound = errors.New("DOI not found in Crossref")
	// ErrUnavailable indicates Crossref could not provide a usable response.
	ErrUnavailable = errors.New("Crossref temporarily unavailable")
	// ErrInvalidQuery indicates an invalid search query or result limit.
	ErrInvalidQuery = errors.New("invalid literature search query")
)

// Client accesses the public Crossref metadata API. Construct it with New and
// reuse it for multiple requests. Its methods may be called concurrently.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New constructs a client for the fixed HTTPS Crossref API. Requests use
// https://www.crossref.org/documentation/retrieve-metadata/rest-api/#77001.
func New() *Client {
	return &Client{
		baseURL: crossrefOrigin,
		httpClient: &http.Client{
			Timeout: requestTimeout,
			Transport: &http.Transport{
				ForceAttemptHTTP2:      true,
				IdleConnTimeout:        30 * time.Second,
				MaxResponseHeaderBytes: 64 << 10,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Search returns one page of Crossref's ranked metadata matches, with at most
// limit distinct, normalized DOIs. The limit must be between 1 and 80.
func (c *Client) Search(ctx context.Context, query string, limit int) ([]Work, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(query) > maxQueryBytes || !utf8.ValidString(query) {
		return nil, ErrInvalidQuery
	}
	if limit < 1 || limit > maxRows {
		return nil, fmt.Errorf("%w: limit must be between 1 and %d", ErrInvalidQuery, maxRows)
	}
	params := url.Values{"query": {query}, "rows": {strconv.Itoa(limit)}}
	// Crossref includes language in works but does not allow it in select.
	var result struct {
		Items []crossrefWork `json:"items"`
	}
	if err := c.get(ctx, "/works?"+params.Encode(), "work-list", &result); err != nil {
		return nil, err
	}
	if result.Items == nil {
		return nil, fmt.Errorf("%w: missing works list", ErrUnavailable)
	}
	works := make([]Work, 0, min(limit, len(result.Items)))
	seen := make(map[string]bool, cap(works))
	for _, item := range result.Items {
		doi, err := NormalizeDOI(item.DOI)
		if err != nil || seen[doi] {
			continue
		}
		seen[doi] = true
		works = append(works, item.work(doi))
		if len(works) == limit {
			break
		}
	}
	return works, nil
}

// Verify looks up the DOI at Crossref and confirms that its metadata matches
// the requested identifier. The result is a record of deposited metadata.
func (c *Client) Verify(ctx context.Context, doi string) (Work, error) {
	doi, err := NormalizeDOI(doi)
	if err != nil {
		return Work{}, err
	}
	var result crossrefWork
	// Encode the complete identifier as in Crossref's Swagger singleton example.
	if err := c.get(ctx, "/works/"+url.PathEscape(doi), "work", &result); err != nil {
		return Work{}, err
	}
	if returnedDOI, err := NormalizeDOI(result.DOI); err != nil || returnedDOI != doi {
		return Work{}, fmt.Errorf("%w: metadata DOI did not match the request", ErrUnavailable)
	}
	return result.work(doi), nil
}

func (c *Client) get(ctx context.Context, path, messageType string, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("%w: could not construct request", ErrUnavailable)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ResearchAgent/1.0")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: request failed", ErrUnavailable)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound && messageType == "work" {
		return ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: HTTP %d", ErrUnavailable, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: reading response failed", ErrUnavailable)
	}
	if len(body) > maxResponseSize {
		return fmt.Errorf("%w: response exceeds size limit", ErrUnavailable)
	}
	var response struct {
		Status      string          `json:"status"`
		MessageType string          `json:"message-type"`
		Message     json.RawMessage `json:"message"`
	}
	if json.Unmarshal(body, &response) != nil || response.Status != "ok" || response.MessageType != messageType {
		return fmt.Errorf("%w: invalid response", ErrUnavailable)
	}
	if json.Unmarshal(response.Message, result) != nil {
		return fmt.Errorf("%w: invalid metadata", ErrUnavailable)
	}
	return nil
}
