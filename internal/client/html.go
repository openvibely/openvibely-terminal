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
	"encoding/json"
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
	root, _, err := c.getHTMLPage(ctx, path)
	return root, err
}

const (
	cardPageSize       = 50
	maxCardPages       = 200
	maxPaginatedCards  = 10000
	cardPageMoreHeader = "X-OpenVibely-Card-Page-Has-More"
)

type htmlPage struct {
	root *html.Node
}

func (c *Client) getHTMLPage(ctx context.Context, path string) (*html.Node, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Accept", "text/html")
	req.Header.Set("HX-Request", "true")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("GET %s: %w", path, err)
	}
	defer drainAndClose(resp.Body)

	if isReadAuthResponse(resp) {
		return nil, false, newAuthRequiredError(http.MethodGet, path, resp)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, apiError(resp)
	}
	root, err := html.Parse(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, false, err
	}
	rawHasMore := strings.TrimSpace(resp.Header.Get(cardPageMoreHeader))
	if rawHasMore != "" {
		hasMore, parseErr := strconv.ParseBool(rawHasMore)
		if parseErr != nil {
			return nil, false, fmt.Errorf("invalid %s header %q: %w", cardPageMoreHeader, rawHasMore, parseErr)
		}
		return root, hasMore, nil
	}
	paginationRoot := findNode(root, func(n *html.Node) bool { return hasHTMLAttr(n, "data-card-pagination-root") })
	return root, paginationRoot != nil && attr(paginationRoot, "data-card-pagination-has-more") == "true", nil
}

// getCardPages follows the backend's card-page continuation contract. The first
// response remains the normal full page so non-paginated cards and empty-state
// markup are preserved; later requests ask for the largest supported fragment.
func (c *Client) getCardPages(ctx context.Context, path string) ([]htmlPage, error) {
	root, hasMore, err := c.getHTMLPage(ctx, path)
	if err != nil {
		return nil, err
	}
	return c.getCardPagesFromInitial(ctx, path, root, hasMore)
}

// getCardPagesFromInitial follows card continuations after an already-loaded
// first page, such as the refreshed list returned by an HTMX mutation.
func (c *Client) getCardPagesFromInitial(ctx context.Context, path string, root *html.Node, hasMore bool) ([]htmlPage, error) {
	pages, _, err := c.getCardPagesFromInitialUntil(ctx, path, root, hasMore, nil)
	return pages, err
}

// getCardPagesFromInitialUntil follows the same bounded, validated traversal as
// getCardPagesFromInitial, but permits a caller to stop after an accepted page.
func (c *Client) getCardPagesFromInitialUntil(ctx context.Context, path string, root *html.Node, hasMore bool, stop func(*html.Node) bool) ([]htmlPage, bool, error) {
	paginationRoot := findNode(root, func(n *html.Node) bool { return hasHTMLAttr(n, "data-card-pagination-root") })
	if paginationRoot == nil {
		if hasMore {
			return nil, false, fmt.Errorf("card pagination reported more cards without pagination metadata")
		}
		return []htmlPage{{root: root}}, stop != nil && stop(root), nil
	}
	pages := []htmlPage{{root: root}}
	selector := attr(paginationRoot, "data-card-pagination-card-selector")
	keyAttr := attr(paginationRoot, "data-card-pagination-key")
	if hasMore {
		marker, _ := paginationSelector(selector)
		if marker == "" || strings.TrimSpace(keyAttr) == "" {
			return nil, false, fmt.Errorf("card pagination reported more cards with invalid pagination metadata")
		}
	}
	offset := countPaginationCards(root, selector, keyAttr)
	if stop != nil && stop(root) {
		return pages, true, nil
	}

	for page := 1; hasMore; page++ {
		if page >= maxCardPages || offset >= maxPaginatedCards {
			return nil, false, fmt.Errorf("card pagination exceeded safety limit after %d cards", offset)
		}
		continuation, err := cardContinuationPath(path, page, offset)
		if err != nil {
			return nil, false, err
		}
		next, nextHasMore, err := c.getHTMLPage(ctx, continuation)
		if err != nil {
			return nil, false, fmt.Errorf("loading card page %d after %d cards: %w", page+1, offset, err)
		}
		count := countPaginationCards(next, selector, keyAttr)
		if count == 0 && nextHasMore {
			return nil, false, fmt.Errorf("loading card page %d: backend reported more cards but returned none", page+1)
		}
		if offset+count > maxPaginatedCards {
			return nil, false, fmt.Errorf("card pagination exceeded safety limit after %d cards", offset)
		}
		pages = append(pages, htmlPage{root: next})
		offset += count
		hasMore = nextHasMore
		if stop != nil && stop(next) {
			return pages, true, nil
		}
	}
	return pages, false, nil
}

func cardContinuationPath(path string, page, offset int) (string, error) {
	u, err := url.Parse(path)
	if err != nil {
		return "", fmt.Errorf("parsing card pagination path: %w", err)
	}
	q := u.Query()
	q.Set("card_page", "1")
	q.Set("page", strconv.Itoa(page))
	q.Set("page_size", strconv.Itoa(cardPageSize))
	q.Set("offset", strconv.Itoa(offset))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func countPaginationCards(root *html.Node, selector, keyAttr string) int {
	seen := make(map[string]struct{})
	count := 0
	for _, node := range paginationCardNodes(root, selector) {
		key := attr(node, keyAttr)
		if key != "" {
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
		}
		count++
	}
	return count
}

func paginationCardNodes(root *html.Node, selector string) []*html.Node {
	marker, value := paginationSelector(selector)
	if marker == "" {
		return nil
	}
	return findAll(root, func(n *html.Node) bool {
		if !hasHTMLAttr(n, marker) {
			return false
		}
		return value == "" || attr(n, marker) == value
	})
}

func paginationSelector(selector string) (marker, value string) {
	selector = strings.TrimSpace(selector)
	if len(selector) < 3 || selector[0] != '[' || selector[len(selector)-1] != ']' {
		return "", ""
	}
	body := selector[1 : len(selector)-1]
	parts := strings.SplitN(body, "=", 2)
	marker = strings.TrimSpace(parts[0])
	if len(parts) == 2 {
		value = strings.Trim(strings.TrimSpace(parts[1]), "'\"")
	}
	return marker, value
}

// doForm performs a form-encoded mutation. The backend answers HTMX requests
// with a re-rendered fragment (200) or no content (204); both count as success.
func (c *Client) doForm(ctx context.Context, method, path string, form url.Values) error {
	resp, err := c.doFormResponse(ctx, method, path, form)
	if err != nil {
		return err
	}
	defer drainAndClose(resp.Body)
	return nil
}

// doFormHTML performs a form mutation and parses the returned HTML fragment.
func (c *Client) doFormHTML(ctx context.Context, method, path string, form url.Values) (*html.Node, error) {
	resp, err := c.doFormResponse(ctx, method, path, form)
	if err != nil {
		return nil, err
	}
	defer drainAndClose(resp.Body)
	return html.Parse(io.LimitReader(resp.Body, 8<<20))
}

func (c *Client) doFormResponse(ctx context.Context, method, path string, form url.Values) (*http.Response, error) {
	return c.doFormResponseWithPolicy(ctx, method, path, form, mutationRedirectUnexpectedError)
}

// doProjectFormResponse retains a non-login redirect for CreateProject, whose
// endpoint uses the redirect target to return the backend-assigned project ID.
func (c *Client) doProjectFormResponse(ctx context.Context, method, path string, form url.Values) (*http.Response, error) {
	return c.doFormResponseWithPolicy(ctx, method, path, form, mutationRedirectReturn)
}

func (c *Client) doFormResponseWithPolicy(ctx context.Context, method, path string, form url.Values, redirectPolicy mutationRedirectPolicy) (*http.Response, error) {
	var body string
	if form != nil {
		body = form.Encode()
	}
	contentType := ""
	if form != nil {
		contentType = "application/x-www-form-urlencoded"
	}
	return c.doHTMXMutation(ctx, method, path, strings.NewReader(body), contentType, redirectPolicy)
}

type mutationRedirectPolicy uint8

const (
	mutationRedirectAPIError mutationRedirectPolicy = iota
	mutationRedirectUnexpectedError
	mutationRedirectReturn
)

// doHTMXMutation builds and executes an HTMX mutation request. An accepted
// response is returned to the caller, which owns its body; failed responses are
// consumed and closed here. Form mutations retain their specific unexpected
// redirect error while JSON and multipart mutations use the generic API error.
type contentLengthReader interface {
	ContentLength() int64
}

func (c *Client) doHTMXMutation(ctx context.Context, method, path string, body io.Reader, contentType string, redirectPolicy mutationRedirectPolicy) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if bodyWithLength, ok := body.(contentLengthReader); ok {
		req.ContentLength = bodyWithLength.ContentLength()
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("HX-Request", "true")
	req.Header.Set("Accept", "text/html, application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}

	if isAuthResponse(resp) {
		drainAndClose(resp.Body)
		return nil, newAuthRequiredError(method, path, resp)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		switch redirectPolicy {
		case mutationRedirectReturn:
			return resp, nil
		case mutationRedirectUnexpectedError:
			err := fmt.Errorf("%s %s: unexpected redirect status %d", method, path, resp.StatusCode)
			drainAndClose(resp.Body)
			return nil, err
		}
	}
	err = apiError(resp)
	drainAndClose(resp.Body)
	return nil, err
}

// doJSON performs a JSON mutation against an HTMX-rendered route. The backend
// still answers with HTML, so the request keeps the same HTMX and Accept headers
// as form mutations while using the JSON request contract.
func (c *Client) doJSON(ctx context.Context, method, path string, payload any) error {
	resp, err := c.doJSONResponse(ctx, method, path, payload)
	if err != nil {
		return err
	}
	drainAndClose(resp.Body)
	return nil
}

// doJSONResponse performs a JSON mutation and retains a successful response
// body for callers whose endpoint returns a JSON resource. Non-2xx responses
// use the same authentication and API-error classification as doJSON.
func (c *Client) doJSONResponse(ctx context.Context, method, path string, payload any) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return c.doHTMXMutation(ctx, method, path, bytes.NewReader(body), "application/json", mutationRedirectAPIError)
}

var cardNodeText = NodeText

type scrapedCard struct {
	attrs map[string]string
	node  *html.Node
}

// scrapeCards collects every element carrying the given data-* marker
// attribute (e.g. "data-task-id") and returns its attributes and text.
func scrapeCards(root *html.Node, marker string) []Card {
	matches := scrapeCardNodes(root, marker)
	out := make([]Card, 0, len(matches))
	for _, m := range matches {
		out = append(out, Card{Attrs: m.attrs, Text: cardNodeText(m.node)})
	}
	return out
}

func scrapeCardNodes(root *html.Node, marker string) []scrapedCard {
	nodes := findAll(root, func(e *html.Node) bool { return attr(e, marker) != "" })
	out := make([]scrapedCard, 0, len(nodes))
	for _, n := range nodes {
		attrs := map[string]string{}
		for _, a := range n.Attr {
			if strings.HasPrefix(a.Key, "data-") {
				attrs[a.Key] = a.Val
			}
		}
		out = append(out, scrapedCard{attrs: attrs, node: n})
	}
	return out
}

func dedupeScrapedCards(cards []scrapedCard, marker string) []scrapedCard {
	best := map[string]int{}
	var order []string
	for i, c := range cards {
		id := c.attrs[marker]
		if id == "" {
			continue
		}
		prev, seen := best[id]
		if !seen {
			best[id] = i
			order = append(order, id)
			continue
		}
		if len(c.attrs) > len(cards[prev].attrs) {
			best[id] = i
		}
	}
	out := make([]scrapedCard, 0, len(order))
	for _, id := range order {
		out = append(out, cards[best[id]])
	}
	return out
}

func scrapedToCards(cards []scrapedCard, includeText bool) []Card {
	out := make([]Card, 0, len(cards))
	for _, c := range cards {
		card := Card{Attrs: c.attrs}
		if includeText {
			card.Text = cardNodeText(c.node)
		}
		out = append(out, card)
	}
	return out
}

// dedupedCards is a convenience wrapper that scrapes and deduplicates in one step.
func dedupedCards(root *html.Node, marker string) []Card {
	return scrapedToCards(dedupeScrapedCards(scrapeCardNodes(root, marker), marker), true)
}

// dedupedCardsWithoutText scrapes and deduplicates cards without populating Text.
func dedupedCardsWithoutText(root *html.Node, marker string) []Card {
	return scrapedToCards(dedupeScrapedCards(scrapeCardNodes(root, marker), marker), false)
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
