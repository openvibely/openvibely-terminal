package client

// HTML-to-text conversion for backend pages that have no JSON representation.
// The OpenVibely backend renders most screens as HTMX/templ HTML fragments;
// the TUI fetches those fragments and renders a readable text version.

import (
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// skippedTags never contribute text output.
var skippedTags = map[string]bool{
	"script": true, "style": true, "svg": true, "noscript": true,
	"template": true, "dialog": true, "select": true, "option": true,
	"input": true, "textarea": true, "head": true,
}

// blockTags force a line break before and after their content.
var blockTags = map[string]bool{
	"div": true, "p": true, "h1": true, "h2": true, "h3": true, "h4": true,
	"h5": true, "h6": true, "li": true, "tr": true, "table": true,
	"ul": true, "ol": true, "section": true, "article": true, "form": true,
	"header": true, "footer": true, "blockquote": true, "pre": true,
	"details": true, "summary": true, "fieldset": true, "br": true, "hr": true,
}

var multiBlank = regexp.MustCompile(`\n{3,}`)

// HTMLToText renders an HTML document or fragment as plain text.
func HTMLToText(fragment string) string {
	root, err := html.Parse(strings.NewReader(fragment))
	if err != nil {
		return strings.TrimSpace(fragment)
	}
	var b strings.Builder
	renderNodeText(&b, root)
	return tidyText(b.String())
}

// NodeText renders a parsed subtree as plain text.
func NodeText(n *html.Node) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	renderNodeText(&b, n)
	return tidyText(b.String())
}

func renderNodeText(b *strings.Builder, n *html.Node) {
	switch n.Type {
	case html.TextNode:
		text := strings.Join(strings.Fields(n.Data), " ")
		if text != "" {
			b.WriteString(text)
			b.WriteString(" ")
		}
		return
	case html.ElementNode:
		if skippedTags[n.Data] {
			return
		}
		if blockTags[n.Data] {
			b.WriteString("\n")
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		renderNodeText(b, c)
	}
	if n.Type == html.ElementNode && blockTags[n.Data] {
		b.WriteString("\n")
	}
}

func tidyText(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(strings.TrimSpace(l), " ")
	}
	out := strings.Join(lines, "\n")
	out = multiBlank.ReplaceAllString(out, "\n\n")
	return strings.TrimSpace(out)
}

// --- node helpers used by the structured parsers ---

// findByID returns the first element with the given id attribute.
func findByID(n *html.Node, id string) *html.Node {
	return findNode(n, func(e *html.Node) bool { return attr(e, "id") == id })
}

// findAll collects every element matching the predicate.
func findAll(n *html.Node, match func(*html.Node) bool) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && match(node) {
			out = append(out, node)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

// findNode returns the first element matching the predicate.
func findNode(n *html.Node, match func(*html.Node) bool) *html.Node {
	if n.Type == html.ElementNode && match(n) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findNode(c, match); found != nil {
			return found
		}
	}
	return nil
}

// attr returns the value of the named attribute, or "".
func attr(n *html.Node, name string) string {
	if n == nil {
		return ""
	}
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

// hasHTMLAttr reports whether an element contains an attribute, including an
// explicitly empty attribute value such as data-personality-key="".
func hasHTMLAttr(n *html.Node, name string) bool {
	if n == nil {
		return false
	}
	for _, a := range n.Attr {
		if a.Key == name {
			return true
		}
	}
	return false
}

// prefix + "/<id>" (e.g. "/schedules/abc-123") and returns the unique ids
// in document order. Used to discover entity ids embedded in hx-* routes.
func extractIDs(n *html.Node, prefix string) []string {
	re := regexp.MustCompile(regexp.QuoteMeta(prefix) + `/([A-Za-z0-9_-]+)`)
	seen := map[string]bool{}
	var out []string
	for _, node := range findAll(n, func(e *html.Node) bool { return true }) {
		for _, a := range node.Attr {
			if !strings.HasPrefix(a.Key, "hx-") && a.Key != "href" && a.Key != "action" {
				continue
			}
			for _, mt := range re.FindAllStringSubmatch(a.Val, -1) {
				id := mt[1]
				if !seen[id] {
					seen[id] = true
					out = append(out, id)
				}
			}
		}
	}
	return out
}
