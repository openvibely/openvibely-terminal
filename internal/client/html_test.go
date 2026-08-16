package client

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// ---------------------------------------------------------------------------
// Benchmarks
//
// Run with: go test -bench=. -benchmem ./internal/client/
//
// BenchmarkGetHTML exercises the full getHTML path with bytes.NewReader.
// BenchmarkStringReaderOld and BenchmarkBytesReaderNew isolate the specific
// allocation difference between the old and new patterns, so the reduction
// is visible without a separate before/after binary.
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

// BenchmarkStringReaderOld isolates the old extra-copy pattern:
// string(body) allocates a full N-byte copy; strings.NewReader wraps it.
// This is what each HTML fetch cost before the fix.
func BenchmarkStringReaderOld(b *testing.B) {
	const bodySize = 16 * 1024
	body := []byte(strings.Repeat("x", bodySize))
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		r := strings.NewReader(string(body)) // 1 alloc: copies bodySize bytes
		_ = r
	}
}

// BenchmarkBytesReaderNew isolates the new zero-copy pattern:
// bytes.NewReader wraps the existing slice with no allocation.
func BenchmarkBytesReaderNew(b *testing.B) {
	const bodySize = 16 * 1024
	body := []byte(strings.Repeat("x", bodySize))
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		r := bytes.NewReader(body) // 1 alloc for the Reader struct, no body copy
		_ = r
	}
}
