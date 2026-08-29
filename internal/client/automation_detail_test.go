package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

const automationDetailPublishedPage = `<div id="automation-live"
	data-automation-id="au1" data-project-id="p2" data-automation-name="Nightly review"
	data-automation-description="Review the repository every night"
	data-automation-type="github_sdlc" data-automation-lifecycle-state="active"
	data-automation-health-state="healthy" data-automation-health-reason="all checks passing"
	data-automation-stable-key="nightly-review" data-automation-published-version-id="v7"
	data-automation-version-id="v7" data-automation-version-number="7"
	data-automation-version-state="published" data-automation-version-source="template"
	data-automation-adapter-key="github_sdlc" data-automation-schema-version="2">
	<div data-automation-live-header><div data-automation-breadcrumb><h2>Nightly review</h2></div><p>Review the repository every night</p></div>
	<span data-automation-live-status data-state="active">active</span>
	<span data-automation-live-health data-state="healthy">healthy</span>
	<div data-automation-graph-panel>
		<svg data-automation-canvas>
			<line class="automation-graph-edge" aria-label="Start → Review, 4 transitions, 2 recent"></line>
			<g data-automation-live-node="n1" data-automation-node-key="start" data-automation-node-type="trigger" data-automation-node-role="trigger" class="automation-graph-node--running"><strong>Start</strong><span class="automation-node-state--running">running</span><small>No active work</small></g>
			<g data-automation-live-node="n2" data-automation-node-key="review" data-automation-node-type="task" data-automation-node-role="implementation" class="automation-graph-node--waiting"><strong>Review</strong><span class="automation-node-state--waiting">waiting</span><small>2 running · 1 failed</small></g>
		</svg>
	</div>
	<div data-automation-live-details-panel>
		<section data-automation-live-node-detail="start"><h3>Start</h3><p>start · trigger</p><span data-automation-node-counts='{"completed_recently":2}'></span></section>
		<section data-automation-live-node-detail="review"><h3>Review</h3><p>review · implementation</p></section>
		<div data-automation-live-edge-details><div data-automation-live-edge-detail="e1"><div>Start → Review</div><p>approved</p></div></div>
	</div>
	<div data-automation-live-metrics data-automation-active-invocations="3" data-automation-active-work-items="5">3 active invocations · 5 active work items</div>
	<div data-automation-resources><div data-automation-resource-row data-automation-resource-node-key="review" data-automation-resource-type="repository" data-automation-resource-id="repo1" data-automation-resource-name="openvibely" data-automation-resource-relation="input" data-automation-resource-status="ready"></div></div>
	<div data-automation-external-state data-automation-external-status="fresh" data-automation-external-stale="fresh" data-automation-external-tracked-resources="1" data-automation-external-last-updated="2025-01-02T03:04:05Z"></div>
</div>`

func TestGetAutomationDetailUsesSelectedProjectAndParsesLiveFragment(t *testing.T) {
	var requests int
	var gotMethod, gotPath, gotProject, gotAccept, gotHX string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotProject = r.URL.Query().Get("project_id")
		gotAccept = r.Header.Get("Accept")
		gotHX = r.Header.Get("HX-Request")
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, automationDetailPublishedPage)
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := c.GetAutomationDetail(context.Background(), "p2", "au1")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 || gotMethod != http.MethodGet || gotPath != "/automations/au1" || gotProject != "p2" {
		t.Fatalf("request = %d %s %s?project_id=%s, want one GET /automations/au1?project_id=p2", requests, gotMethod, gotPath, gotProject)
	}
	if gotAccept != "text/html" || gotHX != "true" {
		t.Fatalf("headers Accept=%q HX-Request=%q", gotAccept, gotHX)
	}
	if detail.Automation.ID != "au1" || detail.Automation.ProjectID != "p2" || detail.Automation.Name != "Nightly review" {
		t.Fatalf("automation = %+v", detail.Automation)
	}
	if detail.Version.Version != 7 || detail.Version.State != "published" || detail.Version.AdapterKey != "github_sdlc" {
		t.Fatalf("version = %+v", detail.Version)
	}
	if !detail.GraphAvailable || !detail.NodesAvailable || !detail.EdgesAvailable || detail.Partial {
		t.Fatalf("availability graph=%t nodes=%t edges=%t partial=%t warnings=%v", detail.GraphAvailable, detail.NodesAvailable, detail.EdgesAvailable, detail.Partial, detail.Warnings)
	}
	if len(detail.Nodes) != 2 || !detail.NodeCountsAvailable {
		t.Fatalf("nodes = %+v, available=%t", detail.Nodes, detail.NodeCountsAvailable)
	}
	if detail.Nodes[0].Counts.Running+detail.Nodes[1].Counts.Running != 2 || detail.Nodes[1].Counts.Failed != 1 || detail.Nodes[0].Counts.CompletedRecently != 2 {
		t.Fatalf("node counts = %+v", detail.Nodes)
	}
	if len(detail.Edges) != 1 || detail.Edges[0].SourceName != "Start" || detail.Edges[0].TargetName != "Review" || detail.Edges[0].TransitionCount != 4 || detail.Edges[0].RecentTransitionCount != 2 {
		t.Fatalf("edges = %+v", detail.Edges)
	}
	if !detail.ActiveInvocationsAvailable || !detail.ActiveWorkItemsAvailable || detail.ActiveInvocations != 3 || detail.ActiveWorkItems != 5 {
		t.Fatalf("runtime = invocations %d/%t work %d/%t", detail.ActiveInvocations, detail.ActiveInvocationsAvailable, detail.ActiveWorkItems, detail.ActiveWorkItemsAvailable)
	}
	if !detail.ResourcesAvailable || len(detail.Resources) != 1 || detail.Resources[0].ResourceID != "repo1" {
		t.Fatalf("resources = %+v available=%t", detail.Resources, detail.ResourcesAvailable)
	}
	if !detail.ExternalStateAvailable || !detail.ExternalState.TrackedResourcesAvailable || detail.ExternalState.TrackedResources != 1 || !detail.ExternalState.StaleAvailable || detail.ExternalState.Stale {
		t.Fatalf("external state = %+v available=%t", detail.ExternalState, detail.ExternalStateAvailable)
	}
}

func TestParseAutomationDetailKeepsEmptyGraphAndOptionalSectionsDistinct(t *testing.T) {
	root, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au-empty" data-project-id="p1" data-automation-name="Empty" data-automation-lifecycle-state="active"><div data-automation-graph-panel></div></div>`)
	if err != nil {
		t.Fatal(err)
	}
	if !root.GraphAvailable || !root.NodesAvailable || !root.EdgesAvailable || len(root.Nodes) != 0 || len(root.Edges) != 0 {
		t.Fatalf("empty graph = %+v", root)
	}
	if root.CountsAvailable || root.ActiveInvocationsAvailable || root.ActiveWorkItemsAvailable || root.ResourcesAvailable || root.ExternalStateAvailable {
		t.Fatalf("optional availability falsely reported: %+v", root)
	}
	if !root.Partial {
		t.Fatal("empty optional sections should make a loaded graph partial")
	}
}

func TestParseAutomationDetailDraftHasNoLiveGraph(t *testing.T) {
	root, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au-draft" data-project-id="p1" data-automation-name="Draft" data-automation-lifecycle-state="draft" data-automation-version-state="draft"><div data-automation-graph-panel><g data-automation-live-node="n1"><strong>Draft node</strong></g></div></div>`)
	if err != nil {
		t.Fatal(err)
	}
	if root.GraphAvailable || root.Version.State != "draft" {
		t.Fatalf("draft detail = %+v", root)
	}
	if len(root.Nodes) != 1 {
		t.Fatalf("draft parser should retain parsed source data for diagnostics, nodes=%+v", root.Nodes)
	}
}

func TestParseAutomationDetailMalformedOptionalValuesArePartial(t *testing.T) {
	detail, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au1" data-project-id="p1" data-automation-lifecycle-state="active"><div data-automation-graph-panel><g data-automation-live-node="n1" data-automation-node-count-running="not-a-number"><strong>Node</strong></g></div><div data-automation-live-metrics data-automation-active-invocations="NaN" data-automation-active-work-items="2"></div><div data-automation-external-state data-automation-external-tracked-resources="NaN"></div></div>`)
	if err != nil {
		t.Fatal(err)
	}
	if !detail.GraphAvailable || !detail.Partial {
		t.Fatalf("malformed optional detail = %+v", detail)
	}
	if detail.ActiveInvocationsAvailable || !detail.ActiveWorkItemsAvailable || detail.ActiveWorkItems != 2 {
		t.Fatalf("malformed runtime availability = invocations %t, work %d/%t", detail.ActiveInvocationsAvailable, detail.ActiveWorkItems, detail.ActiveWorkItemsAvailable)
	}
	if !detail.ExternalStateAvailable || detail.ExternalState.TrackedResourcesAvailable {
		t.Fatalf("malformed external availability = %+v/%t", detail.ExternalState, detail.ExternalStateAvailable)
	}
	if !strings.Contains(strings.Join(detail.Warnings, "\n"), "tracked resources count is malformed") {
		t.Fatalf("malformed external count warning missing: %v", detail.Warnings)
	}
	if len(detail.Warnings) == 0 {
		t.Fatal("malformed optional values should produce warnings")
	}
}

func TestGetAutomationDetailMalformedNotFoundAndBackendErrors(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		body         string
		wantErr      string
		wantNotFound bool
	}{
		{name: "malformed", status: http.StatusOK, body: `<div data-project-id="p1">no identity</div>`, wantErr: "malformed fragment"},
		{name: "not found", status: http.StatusNotFound, body: `missing`, wantErr: "automation \"au1\" not found", wantNotFound: true},
		{name: "backend", status: http.StatusBadGateway, body: `{"error":"upstream unavailable"}`, wantErr: "server error (502): upstream unavailable"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			c, err := New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.GetAutomationDetail(context.Background(), "p1", "au1")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
			}
			if tc.wantNotFound && !errors.Is(err, ErrAutomationNotFound) {
				t.Fatalf("error %v does not unwrap to ErrAutomationNotFound", err)
			}
		})
	}
}

func TestParseAutomationDetailTracksPerFieldAvailabilityAndRecentState(t *testing.T) {
	detail, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au-counts" data-project-id="p1" data-automation-lifecycle-state="active">
		<div data-automation-graph-panel>
			<g data-automation-live-node="n1" data-automation-node-key="first" data-counts='{"running":0,"failed":2}'><strong>First</strong></g>
			<g data-automation-live-node="n2" data-automation-node-key="second"><strong>Second</strong><small>1 blocked</small></g>
			<g data-automation-live-node="n3" data-automation-node-key="third" class="automation-graph-node--completed"><strong>Third</strong></g>
			<g data-automation-live-node="n4" data-automation-node-key="fourth" data-counts='{"running":0}'><strong>Fourth</strong><small>9 running · 2 failed</small></g>
		</div>
	</div>`)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Nodes) != 4 || !detail.NodeCountsAvailable {
		t.Fatalf("nodes = %+v, available=%t", detail.Nodes, detail.NodeCountsAvailable)
	}
	first, second, third, fourth := detail.Nodes[0], detail.Nodes[1], detail.Nodes[2], detail.Nodes[3]
	if !first.Counts.RunningAvailable || first.Counts.Running != 0 || !first.Counts.FailedAvailable || first.Counts.Failed != 2 {
		t.Fatalf("first counts = %+v", first.Counts)
	}
	if first.Counts.WaitingAvailable || first.Counts.BlockedAvailable || first.Counts.CompletedRecentlyAvailable {
		t.Fatalf("missing first count fields reported: %+v", first.Counts)
	}
	if !second.Counts.BlockedAvailable || second.Counts.Blocked != 1 || second.Counts.RunningAvailable || second.Counts.FailedAvailable {
		t.Fatalf("second counts = %+v", second.Counts)
	}
	if third.DisplayState != "recently_completed" {
		t.Fatalf("third state = %q, want recently_completed", third.DisplayState)
	}
	if !fourth.Counts.RunningAvailable || fourth.Counts.Running != 0 || !fourth.Counts.FailedAvailable || fourth.Counts.Failed != 2 || fourth.Counts.WaitingAvailable {
		t.Fatalf("structured counts should win per field over text counts: %+v", fourth.Counts)
	}
}

func TestParseAutomationDetailPreservesDistinctAndUnlabelledEdges(t *testing.T) {
	detail, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au-edges" data-project-id="p1" data-automation-lifecycle-state="active">
		<div data-automation-graph-panel><svg>
			<line class="automation-graph-edge" aria-label="approved, 3 transitions, 1 recent"></line>
			<line class="automation-graph-edge" aria-label="approved, 4 transitions, 0 recent"></line>
			<line class="automation-graph-edge" aria-label=", 0 transitions, 0 recent"></line>
			<line class="automation-graph-edge" data-transition-count="0"></line>
		</svg></div>
		<div data-automation-live-details-panel><div data-automation-live-edge-details>
			<div data-automation-live-edge-detail="e1"><div>Start → Review</div><p>approved</p></div>
			<div data-automation-live-edge-detail="e2"><div>Start → Deploy</div><p>approved</p></div>
			<div data-automation-live-edge-detail="e3"><div>Review → Archive</div></div>
		</div></div>
	</div>`)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Edges) != 4 || !detail.EdgeCountsAvailable {
		t.Fatalf("edges = %+v, available=%t", detail.Edges, detail.EdgeCountsAvailable)
	}
	byTopology := make(map[string]AutomationLiveEdge, len(detail.Edges))
	for _, edge := range detail.Edges {
		byTopology[edge.SourceName+"→"+edge.TargetName] = edge
	}
	startReview, ok := byTopology["Start→Review"]
	if !ok || startReview.EdgeKey != "e1" || startReview.Label != "approved" || startReview.TransitionCount != 3 || startReview.RecentTransitionCount != 1 {
		t.Fatalf("start/review edge = %+v", startReview)
	}
	startDeploy, ok := byTopology["Start→Deploy"]
	if !ok || startDeploy.EdgeKey != "e2" || startDeploy.TransitionCount != 4 || !startDeploy.RecentTransitionCountAvailable || startDeploy.RecentTransitionCount != 0 {
		t.Fatalf("start/deploy edge = %+v", startDeploy)
	}
	reviewArchive, ok := byTopology["Review→Archive"]
	if !ok || reviewArchive.EdgeKey != "e3" || reviewArchive.Label != "" || reviewArchive.TransitionCount != 0 || !reviewArchive.TransitionCountAvailable {
		t.Fatalf("review/archive edge = %+v", reviewArchive)
	}
	countOnly := 0
	for _, edge := range detail.Edges {
		if edge.SourceName == "" && edge.TargetName == "" && edge.EdgeKey == "" {
			if edge.TransitionCountAvailable && edge.TransitionCount == 0 {
				countOnly++
			}
		}
	}
	if countOnly != 1 {
		t.Fatalf("count-only edges = %d, edges=%+v", countOnly, detail.Edges)
	}
}

func TestParseActualAutomationLiveRouteMarksOmittedSectionsUnavailable(t *testing.T) {
	const actualFragment = `<div id="automation-live" data-automation-id="au-actual" data-project-id="p1">
		<div data-automation-live-header><nav data-automation-breadcrumb><h2>Actual automation</h2></nav><p>Live description</p></div>
		<div data-automation-readonly-canvas><div data-automation-live-status>active</div><div data-automation-live-health>healthy</div>
			<div data-automation-graph-panel><svg data-automation-canvas>
				<line class="automation-graph-edge" aria-label="approved, 2 transitions, 1 recent"></line>
				<g data-automation-live-node="n1"><rect class="automation-graph-node--completed"></rect><foreignObject><div><strong>Start</strong><span class="automation-node-state--completed">Recently completed</span><small>2 recent</small></div></foreignObject></g>
				<g data-automation-live-node="n2"><rect class="automation-graph-node--waiting"></rect><foreignObject><div><strong>Review</strong><span class="automation-node-state--waiting">Waiting</span><small>No active work</small></div></foreignObject></g>
			</svg></div>
			<div data-automation-live-details-panel><div data-automation-live-node-details>
				<section data-automation-live-node-detail="start"><div><h3>Start</h3><p>start · trigger</p></div></section>
				<section data-automation-live-node-detail="review"><div><h3>Review</h3><p>review · implementation</p></div></section>
			</div><div data-automation-live-edge-details><div data-automation-live-edge-detail="e1"><div>Start → Review</div><p>approved</p></div></div></div>
		</div>
	</div>`
	detail, err := parseAutomationDetailFromString(actualFragment)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Automation.ID != "au-actual" || detail.Automation.ProjectID != "p1" || detail.Automation.Name != "Actual automation" || detail.Automation.LifecycleState != "active" || detail.Automation.HealthState != "healthy" {
		t.Fatalf("metadata = %+v", detail.Automation)
	}
	if !detail.GraphAvailable || !detail.NodesAvailable || !detail.EdgesAvailable || len(detail.Nodes) != 2 || len(detail.Edges) != 1 {
		t.Fatalf("actual graph = %+v", detail)
	}
	if detail.Nodes[0].DisplayState != "recently_completed" {
		t.Fatalf("actual completed state = %q", detail.Nodes[0].DisplayState)
	}
	if !detail.NodeCountsAvailable || !detail.Nodes[0].Counts.CompletedRecentlyAvailable || detail.Nodes[0].Counts.CompletedRecently != 2 {
		t.Fatalf("actual node counts = %+v", detail.Nodes[0].Counts)
	}
	if detail.ActiveInvocationsAvailable || detail.ActiveWorkItemsAvailable || detail.ResourcesAvailable || detail.ExternalStateAvailable {
		t.Fatalf("omitted live sections falsely available: runtime=%t/%t resources=%t external=%t", detail.ActiveInvocationsAvailable, detail.ActiveWorkItemsAvailable, detail.ResourcesAvailable, detail.ExternalStateAvailable)
	}
	if !detail.Partial || len(detail.Warnings) == 0 {
		t.Fatalf("actual omitted sections should be partial with a warning: %+v", detail)
	}
}

func parseAutomationDetailFromString(source string) (AutomationDetail, error) {
	root, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return AutomationDetail{}, err
	}
	return parseAutomationDetail(root)
}
