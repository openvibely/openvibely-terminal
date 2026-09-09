package client

import (
	"fmt"
	"strings"
	"testing"
)

// BenchmarkParseAutomationDetailSparseCorrelation exercises the complete HTML
// parser with independently rendered graph and detail records. The fixture
// intentionally has one unique identity and endpoint pair per record so it
// isolates correlation cost from ambiguous-record behavior covered by tests.
func BenchmarkParseAutomationDetailSparseCorrelation(b *testing.B) {
	for _, records := range []int{10, 100, 500} {
		fixture := automationDetailSparseCorrelationFixture(records)
		b.Run(fmt.Sprintf("records=%d", records), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(fixture)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				detail, err := parseAutomationDetailFromString(fixture)
				if err != nil {
					b.Fatal(err)
				}
				if len(detail.Nodes) != records || len(detail.Edges) != records {
					b.Fatalf("records=%d parsed nodes=%d edges=%d", records, len(detail.Nodes), len(detail.Edges))
				}
			}
		})
	}
}

func automationDetailSparseCorrelationFixture(records int) string {
	var source strings.Builder
	source.Grow(records * 700)
	fmt.Fprintf(&source, `<div id="automation-live" data-automation-id="benchmark-%d" data-project-id="project-benchmark" data-automation-lifecycle-state="active"><div data-automation-graph-panel><svg>`, records)
	for i := range records {
		fmt.Fprintf(&source, `<g data-automation-live-node="node-%03d" data-automation-node-key="key-%03d" data-counts='{"running":%d}'><strong>Node %03d</strong></g>`, i, i, i%5, i)
		fmt.Fprintf(&source, `<line class="automation-graph-edge" data-automation-live-edge-id="edge-%03d" data-automation-live-edge="edge-key-%03d" data-source-node-id="node-%03d" data-target-node-id="node-%03d" aria-label="Node %03d → Node %03d, %d transitions, %d recent"></line>`, i, i, i, (i+1)%records, i, (i+1)%records, i, i%3)
	}
	source.WriteString(`</svg></div><div data-automation-live-details-panel>`)
	for i := range records {
		fmt.Fprintf(&source, `<section data-automation-live-node-detail="key-%03d" data-automation-live-node-id="node-%03d" data-counts='{"completed_recently":%d}'><h3>Node %03d</h3><p>key-%03d · task</p></section>`, i, i, i%7, i, i)
		fmt.Fprintf(&source, `<div data-automation-live-edge-detail="edge-key-%03d" data-automation-live-edge-id="edge-%03d" data-source-node-id="node-%03d" data-target-node-id="node-%03d"><div>Node %03d → Node %03d</div><p>next</p></div>`, i, i, i, (i+1)%records, i, (i+1)%records)
	}
	source.WriteString(`</div></div>`)
	return source.String()
}
