package github

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxEventValidators = 512

type eventCacheEntry struct {
	etag, cursor string
	next         time.Time
}

// PollDeferredError indicates GitHub's event endpoint requested a slower poll.
// No network wait is performed; the caller can continue with another target.
type PollDeferredError struct{ RetryAfter time.Duration }

func (e *PollDeferredError) Error() string {
	return "GitHub event poll deferred for the server-advised interval"
}

func (c *Client) eventEntry(key string) eventCacheEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.eventCache[key]
}

func (c *Client) updateEventEntry(key string, update func(*eventCacheEntry)) {
	if len(key) > 2048 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.eventCache == nil {
		c.eventCache = make(map[string]eventCacheEntry)
	}
	entry, exists := c.eventCache[key]
	if !exists {
		if len(c.eventCache) >= maxEventValidators {
			delete(c.eventCache, c.eventOrder[0])
			c.eventOrder = c.eventOrder[1:]
		}
		c.eventOrder = append(c.eventOrder, key)
	}
	update(&entry)
	c.eventCache[key] = entry
}

func (c *Client) noteEventInterval(key, header string) {
	seconds, err := strconv.ParseInt(header, 10, 64)
	if err != nil || seconds <= 0 {
		return
	}
	// Prevent malicious or malformed headers from overflowing time.Duration.
	if seconds > int64((7*24*time.Hour)/time.Second) {
		seconds = int64((7 * 24 * time.Hour) / time.Second)
	}
	next := time.Now().Add(time.Duration(seconds) * time.Second)
	c.updateEventEntry(key, func(entry *eventCacheEntry) { entry.next = next })
}

// getEvents fetches only through the cursor and uses a conditional first page
// only after the caller has persisted that page's newest ID as its cursor.
func (c *Client) getEvents(ctx context.Context, path, cursor string) ([]Event, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	u, err := url.Parse(path)
	if err != nil || u.IsAbs() || u.Host != "" || u.User != nil || u.Fragment != "" || !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(path, "//") {
		return nil, "", errors.New("invalid API path")
	}
	u = c.base.ResolveReference(u)
	entry := c.eventEntry(path)
	if delay := time.Until(entry.next); delay > 0 {
		return nil, "", &PollDeferredError{RetryAfter: delay}
	}
	validator := ""
	if cursor != "" && cursor == entry.cursor {
		validator = entry.etag
	}
	var events []Event
	var newest, candidateETag string
	for page := 0; page < maxPages; page++ {
		if !c.validURL(u) {
			return nil, "", errors.New("invalid pagination destination")
		}
		conditional := ""
		if page == 0 {
			conditional = validator
		}
		body, header, status, err := c.getPageResponse(ctx, u, 0, conditional, true)
		if err != nil {
			return nil, "", err
		}
		if page == 0 {
			c.noteEventInterval(path, header.Get("X-Poll-Interval"))
			candidateETag = header.Get("ETag")
			if status == 304 {
				return nil, cursor, nil
			}
		}
		parsed, pageNewest, err := Since([][]byte{body}, "")
		if err != nil {
			return nil, "", err
		}
		if newest == "" {
			newest = pageNewest
		}
		found := false
		for _, event := range parsed {
			if cursor != "" && event.ID == cursor {
				found = true
				break
			}
			events = append(events, event)
		}
		next, err := nextLink(header.Get("Link"))
		if err != nil {
			return nil, "", err
		}
		if next != "" {
			parsedURL, err := url.Parse(next)
			if err != nil {
				return nil, "", errors.New("invalid pagination link")
			}
			parsedURL = u.ResolveReference(parsedURL)
			if !c.validURL(parsedURL) {
				return nil, "", errors.New("invalid pagination destination")
			}
			u = parsedURL
		}
		if found || next == "" {
			break
		}
	}
	if len(candidateETag) > 1024 || strings.ContainsAny(candidateETag, "\r\n") {
		candidateETag = ""
	}
	// Keep only a safe candidate tied to the cursor the caller can commit. If
	// its transaction rolls back, the old cursor prevents a conditional request.
	if newest != "" && len(newest) <= 256 {
		c.updateEventEntry(path, func(entry *eventCacheEntry) { entry.etag, entry.cursor = candidateETag, newest })
	}
	return events, newest, nil
}
