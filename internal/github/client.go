// Package github accesses the official GitHub REST API.
package github

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// apiBase is a variable so package tests can exercise the client without a network.
var apiBase = "https://api.github.com"

const maxBody = 2 << 20
const maxPages = 10
const maxRateWait = 2 * time.Minute

type Client struct {
	token            string
	base             *url.URL
	http             *http.Client
	mu               sync.Mutex
	eventCache       map[string]eventCacheEntry
	eventOrder       []string
	rateLimitedUntil time.Time
}

// HTTPStatusError reports a GitHub API status without retaining response URLs,
// bodies, or credentials. Callers can distinguish unsupported API routes.
type HTTPStatusError struct {
	StatusCode int
	// RetryAfter is a bounded server-advised cooldown; it contains no response body or URL.
	RetryAfter time.Duration
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("GitHub API returned status %d", e.StatusCode)
}

func New(token string) (*Client, error) {
	if strings.TrimSpace(token) == "" {
		token = ""
	}
	return newClient(token)
}

// NewPublic constructs an unauthenticated client for public GitHub endpoints.
func NewPublic() (*Client, error) { return newClient("") }

func newClient(token string) (*Client, error) {
	base, err := url.Parse(apiBase)
	if err != nil || base.Scheme == "" || base.Host == "" || base.User != nil || base.Path != "" || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("invalid API base")
	}
	return &Client{token: token, base: base, http: &http.Client{
		Timeout: 30 * time.Second,
		// Redirects are not trusted with credentials, even on the same host.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (c *Client) validURL(u *url.URL) bool {
	return u != nil && u.Scheme == c.base.Scheme && u.Host == c.base.Host && u.User == nil && u.Fragment == ""
}

// GetPages fetches at most ten pages. The starting path must be relative to the API origin.
func (c *Client) GetPages(ctx context.Context, path string) ([][]byte, error) {
	u, err := url.Parse(path)
	if err != nil || u.IsAbs() || u.Host != "" || u.User != nil || u.Fragment != "" || !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(path, "//") {
		return nil, errors.New("invalid API path")
	}
	u = c.base.ResolveReference(u)
	var pages [][]byte
	for len(pages) < maxPages {
		if !c.validURL(u) {
			return nil, errors.New("invalid pagination destination")
		}
		body, link, err := c.getPage(ctx, u)
		if err != nil {
			return nil, err
		}
		pages = append(pages, body)
		next, err := nextLink(link)
		if err != nil {
			return nil, err
		}
		if next == "" {
			break
		}
		parsed, err := url.Parse(next)
		if err != nil {
			return nil, errors.New("invalid pagination link")
		}
		// GitHub normally supplies absolute links; allow relative links only if they resolve on this origin.
		parsed = u.ResolveReference(parsed)
		if !c.validURL(parsed) {
			return nil, errors.New("invalid pagination destination")
		}
		u = parsed
	}
	return pages, nil
}

func nextLink(header string) (string, error) {
	for _, item := range strings.Split(header, ",") {
		parts := strings.Split(item, ";")
		if len(parts) < 2 {
			continue
		}
		next := false
		for _, param := range parts[1:] {
			p := strings.SplitN(strings.TrimSpace(param), "=", 2)
			if len(p) == 2 && strings.EqualFold(strings.TrimSpace(p[0]), "rel") {
				for _, rel := range strings.Fields(strings.Trim(strings.TrimSpace(p[1]), "\"")) {
					if rel == "next" {
						next = true
					}
				}
			}
		}
		if !next {
			continue
		}
		target := strings.TrimSpace(parts[0])
		if len(target) < 3 || target[0] != '<' || target[len(target)-1] != '>' {
			return "", errors.New("invalid pagination link")
		}
		return target[1 : len(target)-1], nil
	}
	return "", nil
}

func (c *Client) getPage(ctx context.Context, u *url.URL) ([]byte, string, error) {
	return c.getPageWithRetries(ctx, u, 2)
}

func (c *Client) getPageWithRetries(ctx context.Context, u *url.URL, retries int) ([]byte, string, error) {
	body, header, _, err := c.getPageResponse(ctx, u, retries, "", false)
	return body, header.Get("Link"), err
}

func (c *Client) getPageManaged(ctx context.Context, u *url.URL) ([]byte, string, error) {
	body, header, _, err := c.getPageResponse(ctx, u, 0, "", true)
	return body, header.Get("Link"), err
}

// getPageResponse is the shared bounded HTTP owner for regular and conditional requests.
func (c *Client) getPageResponse(ctx context.Context, u *url.URL, retries int, etag string, managed bool) ([]byte, http.Header, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, 0, err
	}
	if managed {
		c.mu.Lock()
		delay := time.Until(c.rateLimitedUntil)
		c.mu.Unlock()
		if delay > 0 {
			return nil, nil, 0, &HTTPStatusError{StatusCode: http.StatusTooManyRequests, RetryAfter: delay}
		}
	}
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, nil, 0, errors.New("cannot construct API request")
		}
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "github-archive")
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, 0, ctx.Err()
			}
			return nil, nil, 0, networkError(err)
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
		closeErr := resp.Body.Close()
		if readErr != nil || closeErr != nil {
			return nil, nil, 0, errors.New("cannot read GitHub API response")
		}
		if len(body) > maxBody {
			return nil, nil, 0, errors.New("GitHub API response exceeds 2 MiB")
		}
		if resp.StatusCode == http.StatusNotModified && etag != "" {
			return nil, resp.Header, resp.StatusCode, nil
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return body, resp.Header, resp.StatusCode, nil
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusServiceUnavailable {
			if managed {
				isRateLimit := resp.StatusCode == http.StatusTooManyRequests || (resp.StatusCode == http.StatusForbidden && (resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != ""))
				var cooldown time.Duration
				if isRateLimit {
					cooldown = responseCooldown(resp.Header, time.Now())
					c.mu.Lock()
					until := time.Now().Add(cooldown)
					if until.After(c.rateLimitedUntil) {
						c.rateLimitedUntil = until
					}
					c.mu.Unlock()
				} else if resp.StatusCode == http.StatusServiceUnavailable && resp.Header.Get("Retry-After") != "" {
					cooldown = responseCooldown(resp.Header, time.Now())
				}
				return nil, nil, 0, &HTTPStatusError{StatusCode: resp.StatusCode, RetryAfter: cooldown}
			}
			if retries == 0 {
				return nil, nil, 0, &HTTPStatusError{StatusCode: resp.StatusCode, RetryAfter: responseCooldown(resp.Header, time.Now())}
			}
			wait, retry, err := retryDelay(resp.Header, time.Now())
			if err != nil {
				return nil, nil, 0, err
			}
			if retry && attempt < retries {
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return nil, nil, 0, ctx.Err()
				case <-timer.C:
				}
				continue
			}
		}
		return nil, nil, 0, &HTTPStatusError{StatusCode: resp.StatusCode}
	}
	return nil, nil, 0, errors.New("GitHub API retry limit reached")
}

// networkError classifies common failures without leaking request URLs or credentials.
func networkError(err error) error {
	var dns *net.DNSError
	var cert x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var network net.Error
	switch {
	case errors.As(err, &dns):
		return errors.New("GitHub API request failed: DNS lookup failed")
	case errors.As(err, &cert), errors.As(err, &hostname):
		return errors.New("GitHub API request failed: TLS certificate verification failed")
	case errors.As(err, &network) && network.Timeout():
		return errors.New("GitHub API request failed: network timeout")
	case errors.Is(err, syscall.ECONNREFUSED):
		return errors.New("GitHub API request failed: connection refused")
	default:
		return errors.New("GitHub API request failed: network or TLS connection error")
	}
}

// responseCooldown clamps untrusted API headers and does not sleep.
func responseCooldown(h http.Header, now time.Time) time.Duration {
	var delay time.Duration
	if value := h.Get("Retry-After"); value != "" {
		if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 {
			if seconds > 3600 {
				seconds = 3600
			}
			delay = time.Duration(seconds) * time.Second
		} else if at, err := http.ParseTime(value); err == nil {
			delay = at.Sub(now)
		}
	}
	if (h.Get("X-RateLimit-Remaining") == "" || h.Get("X-RateLimit-Remaining") == "0") && h.Get("X-RateLimit-Reset") != "" {
		if epoch, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			reset := time.Unix(epoch, 0).Sub(now)
			if reset > delay {
				delay = reset
			}
		}
	}
	if delay <= 0 {
		delay = time.Minute
	}
	if delay > time.Hour {
		delay = time.Hour
	}
	return delay
}

func retryDelay(h http.Header, now time.Time) (time.Duration, bool, error) {
	var delay time.Duration
	var found bool
	if value := h.Get("Retry-After"); value != "" {
		if n, err := strconv.ParseInt(value, 10, 64); err == nil {
			if n < 0 || n > int64(maxRateWait/time.Second) {
				return 0, false, errors.New("GitHub retry delay exceeds limit")
			}
			delay, found = time.Duration(n)*time.Second, true
		} else if t, err := http.ParseTime(value); err == nil {
			delay, found = t.Sub(now), true
		}
	}
	// GitHub also sends reset timestamps on ordinary authorization failures.
	// A positive remaining quota means that timestamp is not a retry signal.
	if value := h.Get("X-RateLimit-Reset"); value != "" && (h.Get("X-RateLimit-Remaining") == "" || h.Get("X-RateLimit-Remaining") == "0") {
		if n, err := strconv.ParseInt(value, 10, 64); err == nil {
			reset := time.Unix(n, 0).Sub(now)
			if reset > maxRateWait {
				return 0, false, errors.New("GitHub rate limit reset exceeds limit")
			}
			if !found {
				delay, found = reset, true
			}
		}
	}
	if delay > maxRateWait {
		return 0, false, errors.New("GitHub retry delay exceeds limit")
	}
	if delay < 0 {
		delay = 0
	}
	return delay, found, nil
}
