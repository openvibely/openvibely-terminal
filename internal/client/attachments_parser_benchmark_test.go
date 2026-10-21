package client

import (
	"fmt"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// legacyParseTaskAttachmentsForProject is the pre-optimization implementation,
// kept as a parser-only benchmark reference so both paths use the same parsed DOM.
func legacyParseTaskAttachmentsForProject(root *html.Node, taskID, projectID string) ([]Attachment, error) {
	if strings.TrimSpace(projectID) == "" {
		return parseTaskAttachments(root, taskID, projectID), nil
	}
	if actual := taskDetailProjectID(root); actual != "" && actual != projectID {
		return nil, fmt.Errorf("task attachment response belongs to project %q, not selected project %q", actual, projectID)
	}
	list := findByID(root, "attachment-list")
	if list == nil {
		return make([]Attachment, 0), nil
	}
	for _, control := range findAll(list, func(e *html.Node) bool {
		return attachmentDeleteURL(attr(e, "hx-delete")) != ""
	}) {
		if actual := attachmentDeleteProjectID(attr(control, "hx-delete")); actual != "" && actual != projectID {
			return nil, fmt.Errorf("attachment response belongs to project %q, not selected project %q", actual, projectID)
		}
	}
	return parseTaskAttachments(root, taskID, projectID), nil
}

func attachmentParserBenchmarkDOM(rows int) *html.Node {
	var markup strings.Builder
	markup.Grow(rows * 220)
	markup.WriteString(`<div data-task-project-id="p1"><section><div id="attachment-list" data-project-id="p1">`)
	for i := 0; i < rows; i++ {
		fmt.Fprintf(&markup, `<div class="attachment-row" data-file-path="/uploads/file-%d" data-media-type="text/plain"><div><p class="text-sm font-medium">file-%d.txt</p><p class="text-xs">%d B</p></div><button hx-delete="/attachments/att-%d?project_id=p1"></button></div>`, i, i, i+1, i)
	}
	markup.WriteString(`</div></section></div>`)
	root, err := html.Parse(strings.NewReader(markup.String()))
	if err != nil {
		panic(err)
	}
	return root
}

func BenchmarkTaskAttachmentProjectParser(b *testing.B) {
	for _, rows := range []int{10, 100, 500} {
		root := attachmentParserBenchmarkDOM(rows)
		for _, parser := range []struct {
			name string
			fn   func(*html.Node, string, string) ([]Attachment, error)
		}{
			{name: "baseline", fn: legacyParseTaskAttachmentsForProject},
			{name: "changed", fn: parseTaskAttachmentsForProject},
		} {
			b.Run(fmt.Sprintf("rows-%d/%s", rows, parser.name), func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := parser.fn(root, "task-1", "p1"); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
