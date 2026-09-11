package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// newHTMLClient creates a test Client whose handler always serves the given
// HTML bytes with a 200 status. Works with both *testing.T and *testing.B.
func newHTMLClient(tb testing.TB, body []byte) *Client {
	tb.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		w.Write(body) //nolint:errcheck
	}))
	tb.Cleanup(srv.Close)
	c, err := New(srv.URL)
	if err != nil {
		tb.Fatalf("New: %v", err)
	}
	return c
}

// TestGetHTMLEmptyBody checks that an empty response is accepted and returns
// a valid html.DocumentNode without error.
func TestGetHTMLEmptyBody(t *testing.T) {
	c := newHTMLClient(t, []byte{})
	node, err := c.getHTML(context.Background(), "/")
	if err != nil {
		t.Fatalf("empty body: unexpected error: %v", err)
	}
	if node == nil {
		t.Fatal("empty body: expected non-nil node")
	}
	if node.Type != html.DocumentNode {
		t.Errorf("empty body: got node type %v, want DocumentNode", node.Type)
	}
}

// TestGetHTMLSimple verifies that a minimal HTML page is parsed and scraped
// correctly using the current bytes.NewReader path.
func TestGetHTMLSimple(t *testing.T) {
	src := []byte(`<html><body><p data-test="hello">world</p></body></html>`)
	c := newHTMLClient(t, src)

	node, err := c.getHTML(context.Background(), "/")
	if err != nil {
		t.Fatalf("simple HTML: unexpected error: %v", err)
	}

	cards := scrapeCards(node, "data-test")
	if len(cards) != 1 {
		t.Fatalf("simple HTML: want 1 card, got %d", len(cards))
	}
	if cards[0].Get("test") != "hello" {
		t.Errorf("simple HTML: attr = %q, want %q", cards[0].Get("test"), "hello")
	}
	if got := strings.TrimSpace(cards[0].Text); got != "world" {
		t.Errorf("simple HTML: text = %q, want %q", got, "world")
	}
}

// TestGetHTMLMultiByteUTF8 verifies that multi-byte UTF-8 (emoji, CJK,
// combining characters) is preserved correctly through bytes.NewReader.
func TestGetHTMLMultiByteUTF8(t *testing.T) {
	utf8Text := "Hello 🌍 世界 café مرحبا ∑"
	src := []byte(fmt.Sprintf(
		`<html><body><p data-utf8="yes">%s</p></body></html>`, utf8Text,
	))
	c := newHTMLClient(t, src)

	node, err := c.getHTML(context.Background(), "/")
	if err != nil {
		t.Fatalf("UTF-8: unexpected error: %v", err)
	}

	cards := scrapeCards(node, "data-utf8")
	if len(cards) != 1 {
		t.Fatalf("UTF-8: want 1 card, got %d", len(cards))
	}
	text := cards[0].Text
	for _, want := range []string{"🌍", "世界", "café"} {
		if !strings.Contains(text, want) {
			t.Errorf("UTF-8: text %q does not contain %q", text, want)
		}
	}
}

// TestGetHTMLLargeBody verifies that a ~4 MB response (below the 8 MB cap)
// is read and parsed correctly.
func TestGetHTMLLargeBody(t *testing.T) {
	const targetBytes = 4 * 1024 * 1024 // 4 MB

	var buf bytes.Buffer
	buf.WriteString(`<html><body>`)
	chunk := []byte(`<p data-row="yes">` + strings.Repeat("A", 200) + `</p>`)
	for buf.Len() < targetBytes {
		buf.Write(chunk)
	}
	buf.WriteString(`</body></html>`)

	c := newHTMLClient(t, buf.Bytes())
	node, err := c.getHTML(context.Background(), "/")
	if err != nil {
		t.Fatalf("large body (%d bytes): unexpected error: %v", buf.Len(), err)
	}
	if node == nil {
		t.Fatal("large body: expected non-nil node")
	}
	cards := scrapeCards(node, "data-row")
	if len(cards) == 0 {
		t.Error("large body: expected at least one scraped card")
	}
}

// TestGetHTMLUnauthorized checks that 302 and 401 responses produce an
// "unauthorized" error rather than a nil node.
func TestGetHTMLUnauthorized(t *testing.T) {
	for _, code := range []int{http.StatusFound, http.StatusUnauthorized} {
		code := code
		t.Run(fmt.Sprintf("status_%d", code), func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
			}))
			_, err := c.getHTML(context.Background(), "/")
			if err == nil {
				t.Fatalf("status %d: expected error", code)
			}
			if !strings.Contains(err.Error(), "unauthorized") {
				t.Errorf("status %d: error %q does not mention 'unauthorized'", code, err.Error())
			}
		})
	}
}

func TestDoJSONAuthenticationAndRedirectStatuses(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		location    string
		wantErr     bool
		wantAuthErr bool
	}{
		{name: "login redirect", status: http.StatusFound, location: "/login?next=%2Fskills", wantErr: true, wantAuthErr: true},
		{name: "unauthorized", status: http.StatusUnauthorized, wantErr: true, wantAuthErr: true},
		{name: "near login help", status: http.StatusFound, location: "/login-help", wantErr: true},
		{name: "near login numeric", status: http.StatusFound, location: "/login2", wantErr: true},
		{name: "other redirect", status: http.StatusTemporaryRedirect, location: "/skills", wantErr: true},
		{name: "no content", status: http.StatusNoContent, wantErr: false},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/skills" {
					t.Errorf("request = %s %s, want POST /skills", r.Method, r.URL.Path)
				}
				if tc.location != "" {
					w.Header().Set("Location", tc.location)
				}
				w.WriteHeader(tc.status)
			}))

			err := c.CreateSkill(context.Background(), "p1", "demo", "", "body")
			if tc.wantErr && err == nil {
				t.Fatalf("status %d: expected error", tc.status)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("status %d: unexpected error: %v", tc.status, err)
			}
			if tc.wantAuthErr && (err == nil || !strings.Contains(err.Error(), "unauthorized")) {
				t.Fatalf("status %d: error = %v, want unauthorized", tc.status, err)
			}
			if !tc.wantAuthErr && err != nil && strings.Contains(err.Error(), "unauthorized") {
				t.Fatalf("status %d: non-login redirect was reported as unauthorized: %v", tc.status, err)
			}
		})
	}
}

func TestDoFormMutationRedirectClassification(t *testing.T) {
	for _, tc := range []struct {
		name     string
		location string
		wantAuth bool
	}{
		{name: "login", location: "/login?next=%2Fmutate", wantAuth: true},
		{name: "login help", location: "/login-help"},
		{name: "login numeric", location: "/login2"},
		{name: "other", location: "/after-mutation"},
		{name: "absolute login", location: "https://backend.example/login?next=%2Fmutate", wantAuth: true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", tc.location)
				w.WriteHeader(http.StatusFound)
			}))

			err := c.doForm(context.Background(), http.MethodPost, "/mutate", nil)
			if err == nil {
				t.Fatalf("redirect %q returned nil error", tc.location)
			}
			if gotAuth := IsAuthRequired(err); gotAuth != tc.wantAuth {
				t.Fatalf("redirect %q auth = %t, want %t: %v", tc.location, gotAuth, tc.wantAuth, err)
			}
		})
	}
}

func TestDoFormAuthenticationAndRedirectStatuses(t *testing.T) {
	operations := []struct {
		name string
		call func(*Client) error
	}{
		{
			name: "form",
			call: func(c *Client) error {
				return c.doForm(context.Background(), http.MethodPost, "/mutate", nil)
			},
		},
		{
			name: "form_html",
			call: func(c *Client) error {
				_, err := c.doFormHTML(context.Background(), http.MethodPost, "/mutate", nil)
				return err
			},
		},
	}

	for _, status := range []int{
		http.StatusMovedPermanently,
		http.StatusFound,
		http.StatusSeeOther,
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect,
	} {
		for _, operation := range operations {
			status, operation := status, operation
			t.Run(fmt.Sprintf("login_redirect_%s_%d", operation.name, status), func(t *testing.T) {
				c, body := newTrackedFormResponseClient(status, "/login?next=%2Fmutate", "session expired")
				err := operation.call(c)
				if err == nil {
					t.Fatalf("status %d: expected error", status)
				}
				if !strings.Contains(err.Error(), "unauthorized") {
					t.Errorf("status %d: error %q does not mention unauthorized", status, err)
				}
				if !body.closed {
					t.Errorf("status %d: response body was not closed", status)
				}
			})
		}
	}
}

func TestDoFormRejectsUnexpectedRedirects(t *testing.T) {
	operations := []struct {
		name string
		call func(*Client) error
	}{
		{
			name: "form",
			call: func(c *Client) error {
				return c.doForm(context.Background(), http.MethodPost, "/mutate", nil)
			},
		},
		{
			name: "form_html",
			call: func(c *Client) error {
				_, err := c.doFormHTML(context.Background(), http.MethodPost, "/mutate", nil)
				return err
			},
		},
	}

	for _, status := range []int{
		http.StatusMovedPermanently,
		http.StatusFound,
		http.StatusSeeOther,
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect,
	} {
		for _, operation := range operations {
			status, operation := status, operation
			t.Run(fmt.Sprintf("unexpected_redirect_%s_%d", operation.name, status), func(t *testing.T) {
				c, body := newTrackedFormResponseClient(status, "/mutate/next", "unexpected redirect")
				err := operation.call(c)
				if err == nil {
					t.Fatalf("status %d: expected error", status)
				}
				if strings.Contains(err.Error(), "unauthorized") {
					t.Errorf("status %d: non-login redirect was reported as unauthorized: %v", status, err)
				}
				if !body.closed {
					t.Errorf("status %d: response body was not closed", status)
				}
			})
		}
	}
}

func TestDoFormAcceptsSuccessfulResponsesAndCleansBodies(t *testing.T) {
	operations := []struct {
		name string
		call func(*Client) error
	}{
		{
			name: "form",
			call: func(c *Client) error {
				return c.doForm(context.Background(), http.MethodPost, "/mutate", nil)
			},
		},
		{
			name: "form_html",
			call: func(c *Client) error {
				node, err := c.doFormHTML(context.Background(), http.MethodPost, "/mutate", nil)
				if err == nil && node == nil {
					return fmt.Errorf("expected parsed HTML document")
				}
				return err
			},
		},
	}

	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "fragment", status: http.StatusOK, body: `<div data-result="ok">updated</div>`},
		{name: "no_content", status: http.StatusNoContent},
	} {
		for _, operation := range operations {
			tc, operation := tc, operation
			t.Run(tc.name+"_"+operation.name, func(t *testing.T) {
				c, body := newTrackedFormResponseClient(tc.status, "", tc.body)
				if err := operation.call(c); err != nil {
					t.Fatalf("status %d: unexpected error: %v", tc.status, err)
				}
				if !body.closed {
					t.Errorf("status %d: response body was not closed", tc.status)
				}
			})
		}
	}
}

type trackedResponseBody struct {
	*strings.Reader
	closed bool
}

func (b *trackedResponseBody) Close() error {
	b.closed = true
	return nil
}

type htmlTestRoundTripper func(*http.Request) (*http.Response, error)

func (f htmlTestRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func newTrackedFormResponseClient(status int, location, bodyText string) (*Client, *trackedResponseBody) {
	body := &trackedResponseBody{Reader: strings.NewReader(bodyText)}
	transport := htmlTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		header := make(http.Header)
		if location != "" {
			header.Set("Location", location)
		}
		return &http.Response{
			StatusCode: status,
			Status:     fmt.Sprintf("%d test response", status),
			Header:     header,
			Body:       body,
			Request:    r,
		}, nil
	})
	return &Client{
		baseURL: "http://backend.test",
		http: &http.Client{
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, body
}

func TestDoFormLoginRedirectIsNotFollowed(t *testing.T) {
	for _, status := range []int{
		http.StatusFound,
		http.StatusSeeOther,
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect,
	} {
		status := status
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			var mutationRequests, loginRequests int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/mutate":
					mutationRequests++
					w.Header().Set("Location", "/login")
					w.WriteHeader(status)
				case "/login":
					loginRequests++
					w.WriteHeader(http.StatusOK)
				default:
					t.Errorf("unexpected request path %q", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()

			c, err := New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			err = c.doForm(context.Background(), http.MethodPost, "/mutate", nil)
			if err == nil || !strings.Contains(err.Error(), "unauthorized") {
				t.Fatalf("status %d: error = %v, want unauthorized", status, err)
			}
			if mutationRequests != 1 {
				t.Errorf("mutation requests = %d, want 1", mutationRequests)
			}
			if loginRequests != 0 {
				t.Errorf("login requests = %d, want 0; redirect was followed", loginRequests)
			}
		})
	}
}

func TestHTMXMutationPathsPreserveHeadersBodiesAndSuccessfulResponseHandling(t *testing.T) {
	operations := []struct {
		name        string
		contentType string
		wantBody    string
		call        func(*Client) error
	}{
		{
			name:        "form",
			contentType: "application/x-www-form-urlencoded",
			wantBody:    "name=Ada&note=hello",
			call: func(c *Client) error {
				return c.doForm(context.Background(), http.MethodPost, "/mutate", url.Values{
					"name": {"Ada"},
					"note": {"hello"},
				})
			},
		},
		{
			name:        "json",
			contentType: "application/json",
			wantBody:    `{"name":"Ada"}`,
			call: func(c *Client) error {
				return c.doJSON(context.Background(), http.MethodPut, "/mutate", struct {
					Name string `json:"name"`
				}{Name: "Ada"})
			},
		},
		{
			name:        "multipart",
			contentType: "multipart/form-data; boundary=test-boundary",
			wantBody:    "multipart-body",
			call: func(c *Client) error {
				_, err := c.doMultipartHTML(context.Background(), http.MethodPost, "/mutate", strings.NewReader("multipart-body"), "multipart/form-data; boundary=test-boundary")
				return err
			},
		},
	}

	for _, operation := range operations {
		for _, status := range []int{http.StatusOK, http.StatusCreated, 299, http.StatusNoContent} {
			operation, status := operation, status
			t.Run(fmt.Sprintf("%s_status_%d", operation.name, status), func(t *testing.T) {
				responseBody := &trackedResponseBody{Reader: strings.NewReader(`<div>updated</div>`)}
				var gotRequest *http.Request
				var gotBody []byte
				transport := htmlTestRoundTripper(func(r *http.Request) (*http.Response, error) {
					gotRequest = r
					var err error
					gotBody, err = io.ReadAll(r.Body)
					if err != nil {
						t.Errorf("read request body: %v", err)
					}
					return &http.Response{
						StatusCode: status,
						Status:     fmt.Sprintf("%d test response", status),
						Header:     make(http.Header),
						Body:       responseBody,
						Request:    r,
					}, nil
				})
				c := &Client{
					baseURL: "http://backend.test",
					http: &http.Client{
						Transport: transport,
						CheckRedirect: func(req *http.Request, via []*http.Request) error {
							return http.ErrUseLastResponse
						},
					},
				}

				if err := operation.call(c); err != nil {
					t.Fatalf("status %d: unexpected error: %v", status, err)
				}
				if gotRequest == nil {
					t.Fatal("mutation did not issue a request")
				}
				if gotRequest.Header.Get("HX-Request") != "true" {
					t.Errorf("HX-Request = %q, want true", gotRequest.Header.Get("HX-Request"))
				}
				if gotRequest.Header.Get("Accept") != "text/html, application/json" {
					t.Errorf("Accept = %q, want text/html, application/json", gotRequest.Header.Get("Accept"))
				}
				if gotRequest.Header.Get("Content-Type") != operation.contentType {
					t.Errorf("Content-Type = %q, want %q", gotRequest.Header.Get("Content-Type"), operation.contentType)
				}
				if got := string(gotBody); got != operation.wantBody {
					t.Errorf("request body = %q, want %q", got, operation.wantBody)
				}
				if !responseBody.closed {
					t.Errorf("status %d: response body was not closed", status)
				}
			})
		}
	}
}

func TestHTMXMutationPathsClassifyExactLoginAndOrdinaryRedirects(t *testing.T) {
	operations := []struct {
		name string
		call func(*Client) error
	}{
		{
			name: "form",
			call: func(c *Client) error {
				return c.doForm(context.Background(), http.MethodPost, "/mutate", nil)
			},
		},
		{
			name: "json",
			call: func(c *Client) error {
				return c.doJSON(context.Background(), http.MethodPost, "/mutate", map[string]string{"value": "one"})
			},
		},
		{
			name: "multipart",
			call: func(c *Client) error {
				_, err := c.doMultipartHTML(context.Background(), http.MethodPost, "/mutate", strings.NewReader("body"), "text/plain")
				return err
			},
		},
	}
	redirects := []struct {
		name      string
		location  string
		wantAuth  bool
		wantError string
	}{
		{name: "exact login", location: "/login?next=%2Fmutate", wantAuth: true},
		{name: "absolute exact login", location: "https://backend.example/login?next=%2Fmutate", wantAuth: true},
		{name: "login help", location: "/login-help", wantError: "server error (302)"},
		{name: "login numeric", location: "/login2", wantError: "server error (302)"},
		{name: "ordinary", location: "/after-mutation", wantError: "server error (302)"},
	}

	for _, operation := range operations {
		for _, redirect := range redirects {
			operation, redirect := operation, redirect
			t.Run(operation.name+"/"+redirect.name, func(t *testing.T) {
				responseBody := &trackedResponseBody{Reader: strings.NewReader("redirect body")}
				transport := htmlTestRoundTripper(func(r *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusFound,
						Status:     "302 test response",
						Header:     http.Header{"Location": []string{redirect.location}},
						Body:       responseBody,
						Request:    r,
					}, nil
				})
				c := &Client{
					baseURL: "http://backend.test",
					http: &http.Client{
						Transport: transport,
						CheckRedirect: func(req *http.Request, via []*http.Request) error {
							return http.ErrUseLastResponse
						},
					},
				}

				err := operation.call(c)
				if err == nil {
					t.Fatal("redirect returned nil error")
				}
				if got := IsAuthRequired(err); got != redirect.wantAuth {
					t.Fatalf("auth classification = %t, want %t: %v", got, redirect.wantAuth, err)
				}
				if redirect.wantError != "" {
					wantError := redirect.wantError
					if operation.name == "form" {
						wantError = "unexpected redirect status 302"
					}
					if !strings.Contains(err.Error(), wantError) {
						t.Errorf("error = %q, want %q", err, wantError)
					}
				}
				if redirect.name == "exact login" || redirect.name == "absolute exact login" {
					if !strings.Contains(err.Error(), "unauthorized") {
						t.Errorf("exact login error = %q, want unauthorized", err)
					}
				}
				if !responseBody.closed {
					t.Error("redirect response body was not closed")
				}
			})
		}
	}
}

func TestHTMXMutationPathsDoNotFollowLoginRedirects(t *testing.T) {
	operations := []struct {
		name string
		call func(*Client) error
	}{
		{
			name: "form",
			call: func(c *Client) error {
				return c.doForm(context.Background(), http.MethodPost, "/mutate", nil)
			},
		},
		{
			name: "json",
			call: func(c *Client) error {
				return c.doJSON(context.Background(), http.MethodPost, "/mutate", map[string]string{"value": "one"})
			},
		},
		{
			name: "multipart",
			call: func(c *Client) error {
				_, err := c.doMultipartHTML(context.Background(), http.MethodPost, "/mutate", strings.NewReader("body"), "text/plain")
				return err
			},
		},
	}

	for _, operation := range operations {
		for _, status := range []int{http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
			operation, status := operation, status
			t.Run(fmt.Sprintf("%s_status_%d", operation.name, status), func(t *testing.T) {
				var mutationRequests, loginRequests int
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/mutate":
						mutationRequests++
						w.Header().Set("Location", "/login?next=%2Fmutate")
						w.WriteHeader(status)
					case "/login":
						loginRequests++
						w.WriteHeader(http.StatusOK)
					default:
						t.Errorf("unexpected request path %q", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				defer srv.Close()

				c, err := New(srv.URL)
				if err != nil {
					t.Fatal(err)
				}
				if err := operation.call(c); err == nil || !IsAuthRequired(err) {
					t.Fatalf("status %d: error = %v, want authentication error", status, err)
				}
				if mutationRequests != 1 {
					t.Errorf("mutation requests = %d, want 1", mutationRequests)
				}
				if loginRequests != 0 {
					t.Errorf("login requests = %d, want 0; redirect was followed", loginRequests)
				}
			})
		}
	}
}

func TestHTMXMutationPathsHandleAPIErrorsAndTransportFailures(t *testing.T) {
	operations := []struct {
		name string
		call func(*Client) error
	}{
		{
			name: "form",
			call: func(c *Client) error {
				return c.doForm(context.Background(), http.MethodPost, "/mutate", nil)
			},
		},
		{
			name: "json",
			call: func(c *Client) error {
				return c.doJSON(context.Background(), http.MethodPost, "/mutate", map[string]string{"value": "one"})
			},
		},
		{
			name: "multipart",
			call: func(c *Client) error {
				_, err := c.doMultipartHTML(context.Background(), http.MethodPost, "/mutate", strings.NewReader("body"), "text/plain")
				return err
			},
		},
	}

	for _, operation := range operations {
		operation := operation
		t.Run(operation.name+"/api_error", func(t *testing.T) {
			responseBody := &trackedResponseBody{Reader: strings.NewReader(`{"error":"mutation rejected"}`)}
			transport := htmlTestRoundTripper(func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusUnprocessableEntity,
					Status:     "422 test response",
					Header:     make(http.Header),
					Body:       responseBody,
					Request:    r,
				}, nil
			})
			c := &Client{
				baseURL: "http://backend.test",
				http: &http.Client{
					Transport: transport,
					CheckRedirect: func(req *http.Request, via []*http.Request) error {
						return http.ErrUseLastResponse
					},
				},
			}

			err := operation.call(c)
			if err == nil || !strings.Contains(err.Error(), "mutation rejected") {
				t.Fatalf("error = %v, want API error message", err)
			}
			if !responseBody.closed {
				t.Error("API error response body was not closed")
			}
		})

		t.Run(operation.name+"/transport_error", func(t *testing.T) {
			transport := htmlTestRoundTripper(func(*http.Request) (*http.Response, error) {
				return nil, fmt.Errorf("transport unavailable")
			})
			c := &Client{
				baseURL: "http://backend.test",
				http: &http.Client{
					Transport: transport,
					CheckRedirect: func(req *http.Request, via []*http.Request) error {
						return http.ErrUseLastResponse
					},
				},
			}

			err := operation.call(c)
			if err == nil || !strings.Contains(err.Error(), "transport unavailable") {
				t.Fatalf("error = %v, want transport error", err)
			}
			if !strings.Contains(err.Error(), "POST /mutate") {
				t.Errorf("error = %q, want method and path context", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Benchmarks
//
// Run with: go test -bench=. -benchmem ./internal/client/
//
// BenchmarkGetHTML exercises the full getHTML path with bytes.NewReader.
// ---------------------------------------------------------------------------

// BenchmarkGetHTML measures the allocation profile of a complete getHTML call.
func BenchmarkGetHTML(b *testing.B) {
	const bodySize = 16 * 1024 // 16 KB — representative task/alert page
	body := []byte(`<html><body>` + strings.Repeat("<p>x</p>", bodySize/7) + `</body></html>`)
	c := newHTMLClient(b, body)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		node, err := c.getHTML(context.Background(), "/")
		if err != nil || node == nil {
			b.Fatalf("getHTML: %v node=%v", err, node)
		}
	}
}

// TestDedupedCards verifies that dedupedCards scrapes and deduplicates in one step.
func TestDedupedCards(t *testing.T) {
	// Two elements share the same marker value; a third has a unique value.
	// The duplicate pair should be collapsed to one card (the one with more attrs).
	const body = `<!DOCTYPE html><html><body>
		<div data-skill-handle="alpha" data-skill-name="A">discarded alpha</div>
		<div data-skill-handle="alpha" data-skill-name="A" data-skill-extra="yes">kept alpha</div>
		<div data-skill-handle="beta" data-skill-name="B">kept beta</div>
	</body></html>`
	root, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("html.Parse: %v", err)
	}
	cards := dedupedCards(root, "data-skill-handle")
	if got, want := len(cards), 2; got != want {
		t.Fatalf("len(cards) = %d, want %d", got, want)
	}
	if cards[0].Attrs["data-skill-handle"] != "alpha" {
		t.Errorf("cards[0] handle = %q, want %q", cards[0].Attrs["data-skill-handle"], "alpha")
	}
	// The richer duplicate (with data-skill-extra) should be kept.
	if cards[0].Attrs["data-skill-extra"] != "yes" {
		t.Errorf("cards[0] missing data-skill-extra; got attrs %v", cards[0].Attrs)
	}
	if cards[0].Text != "kept alpha" {
		t.Errorf("cards[0] text = %q, want %q", cards[0].Text, "kept alpha")
	}
	if cards[1].Attrs["data-skill-handle"] != "beta" {
		t.Errorf("cards[1] handle = %q, want %q", cards[1].Attrs["data-skill-handle"], "beta")
	}
	if cards[1].Text != "kept beta" {
		t.Errorf("cards[1] text = %q, want %q", cards[1].Text, "kept beta")
	}
}

func TestDedupedCardsDefersTextUntilAfterDedup(t *testing.T) {
	const body = `<!DOCTYPE html><html><body>
		<div data-model-id="alpha" data-model-name="A">discarded alpha 1</div>
		<div data-model-id="alpha" data-model-name="A" data-model-provider="p">kept alpha</div>
		<button data-model-id="alpha">discarded alpha action</button>
		<div data-model-id="beta" data-model-name="B">kept beta</div>
		<button data-model-id="beta">discarded beta action</button>
	</body></html>`
	root, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("html.Parse: %v", err)
	}

	old := cardNodeText
	calls := 0
	cardNodeText = func(n *html.Node) string {
		calls++
		return NodeText(n)
	}
	defer func() { cardNodeText = old }()

	cards := dedupedCards(root, "data-model-id")
	if got, want := len(cards), 2; got != want {
		t.Fatalf("len(cards) = %d, want %d", got, want)
	}
	if calls != len(cards) {
		t.Fatalf("NodeText calls = %d, want retained card count %d", calls, len(cards))
	}
	rawMatches := len(findAll(root, func(e *html.Node) bool { return attr(e, "data-model-id") != "" }))
	if rawMatches < 2*calls {
		t.Fatalf("raw matches %d did not demonstrate at least 2x fewer text extractions than %d retained cards", rawMatches, calls)
	}
	if cards[0].Text != "kept alpha" || cards[1].Text != "kept beta" {
		t.Fatalf("texts = %q, %q; want kept card text", cards[0].Text, cards[1].Text)
	}
}

func TestDedupedCardsWithoutTextSkipsNodeText(t *testing.T) {
	const body = `<!DOCTYPE html><html><body>
		<div data-agent-id="a1" data-agent-name="Reviewer">Reviewer text</div>
		<button data-agent-id="a1">delete</button>
	</body></html>`
	root, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("html.Parse: %v", err)
	}

	old := cardNodeText
	cardNodeText = func(n *html.Node) string {
		t.Fatalf("NodeText called for no-text dedupe path")
		return ""
	}
	defer func() { cardNodeText = old }()

	cards := dedupedCardsWithoutText(root, "data-agent-id")
	if got, want := len(cards), 1; got != want {
		t.Fatalf("len(cards) = %d, want %d", got, want)
	}
	if cards[0].Text != "" {
		t.Errorf("card text = %q, want empty", cards[0].Text)
	}
	if cards[0].Get("agent-name") != "Reviewer" {
		t.Errorf("agent name = %q, want Reviewer", cards[0].Get("agent-name"))
	}
}

// BenchmarkBytesReader measures the zero-copy reader used by getHTML.
func BenchmarkBytesReader(b *testing.B) {
	const bodySize = 16 * 1024
	body := []byte(strings.Repeat("x", bodySize))
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		r := bytes.NewReader(body) // 1 alloc for the Reader struct, no body copy
		_ = r
	}
}
