package client

// Generic HTML transport for the OpenVibely backend.
//
// Most backend screens (tasks, alerts, skills, models, agents, schedules,
// workers, channels, personality, pulse, reflection, insights) are rendered as
// HTMX/templ HTML rather than JSON. This file provides the shared plumbing:
//
//   - getHTML   fetches a page/fragment and parses it
//   - doForm    performs a form-encoded mutation with HTMX headers
//   - Card      a data-* attribute bag scraped out of a rendered card element
//
// Sending "HX-Request: true" on mutations makes the backend answer with the
// re-rendered fragment and a 2xx status instead of a browser redirect, which
// is the most reliable success signal available to a native client.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// Card is one scraped UI element: its data-* attributes plus its text.
type Card struct {
	Attrs map[string]string
	Text  string
}

// Get returns the value of a data-* attribute (without the "data-" prefix).
func (c Card) Get(name string) string { return c.Attrs["data-"+name] }

// Int returns a data-* attribute parsed as an int.
func (c Card) Int(name string) int {
	n, _ := strconv.Atoi(c.Get(name))
	return n
}

// Bool returns a data-* attribute parsed as a bool ("true"/"1").
func (c Card) Bool(name string) bool {
	v := c.Get(name)
	return v == "true" || v == "1"
}

// getHTML fetches path and parses the response as HTML.
func (c *Client) getHTML(ctx context.Context, path string) (*html.Node, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/html")
	req.Header.Set("HX-Request", "true")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	defer drainAndClose(resp.Body)

	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("GET %s: unauthorized (server auth enabled; provide credentials)", path)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, apiError(resp)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return html.Parse(bytes.NewReader(body))
}

// doForm performs a form-encoded mutation. The backend answers HTMX requests
// with a re-rendered fragment (200) or no content (204); both count as success.
func (c *Client) doForm(ctx context.Context, method, path string, form url.Values) error {
	var body string
	if form != nil {
		body = form.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, strings.NewReader(body))
	if err != nil {
		return err
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("HX-Request", "true")
	req.Header.Set("Accept", "text/html, application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer drainAndClose(resp.Body)

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("%s %s: unauthorized (server auth enabled; provide credentials)", method, path)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		return nil
	}
	return apiError(resp)
}

// scrapeCards collects every element carrying the given data-* marker
// attribute (e.g. "data-task-id") and returns its attributes and text.
func scrapeCards(root *html.Node, marker string) []Card {
	nodes := findAll(root, func(e *html.Node) bool { return attr(e, marker) != "" })
	out := make([]Card, 0, len(nodes))
	for _, n := range nodes {
		attrs := map[string]string{}
		for _, a := range n.Attr {
			if strings.HasPrefix(a.Key, "data-") {
				attrs[a.Key] = a.Val
			}
		}
		out = append(out, Card{Attrs: attrs, Text: NodeText(n)})
	}
	return out
}

// dedupeCards keeps the first card for each marker value, preferring the one
// with the most data-* attributes (nested buttons repeat the id attribute).
func dedupeCards(cards []Card, marker string) []Card {
	best := map[string]int{}
	var order []string
	for i, c := range cards {
		id := c.Attrs[marker]
		if id == "" {
			continue
		}
		prev, seen := best[id]
		if !seen {
			best[id] = i
			order = append(order, id)
			continue
		}
		if len(c.Attrs) > len(cards[prev].Attrs) {
			best[id] = i
		}
	}
	out := make([]Card, 0, len(order))
	for _, id := range order {
		out = append(out, cards[best[id]])
	}
	return out
}

// dedupedCards is a convenience wrapper that scrapes and deduplicates in one step.
func dedupedCards(root *html.Node, marker string) []Card {
	return dedupeCards(scrapeCards(root, marker), marker)
}

// firstLine returns the first non-empty line of a card's text, which is how
// the templates render a card's primary label.
func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// query builds a "?k=v" suffix, omitting empty values.
func query(pairs ...string) string {
	v := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i+1] != "" {
			v.Set(pairs[i], pairs[i+1])
		}
	}
	if len(v) == 0 {
		return ""
	}
	return "?" + v.Encode()
}
