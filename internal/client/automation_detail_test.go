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
			<line data-automation-live-edge="e1" class="automation-graph-edge" aria-label="Start → Review, 4 transitions, 2 recent"></line>
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

func TestParseAutomationDetailDoesNotMergeDuplicateEndpointEdgesWithoutUniqueIdentity(t *testing.T) {
	detail, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au-duplicate-endpoints" data-project-id="p1" data-automation-lifecycle-state="active">
		<div data-automation-graph-panel><svg>
			<line class="automation-graph-edge" data-source-node-name="Start" data-target-node-name="Review" aria-label="approved, 3 transitions, 0 recent"></line>
			<line class="automation-graph-edge" data-source-node-name="Start" data-target-node-name="Review" aria-label="rejected, 4 transitions, 0 recent"></line>
		</svg></div>
		<div data-automation-live-details-panel><div data-automation-live-edge-details>
			<div data-automation-live-edge-detail="e2" data-source-node-name="Start" data-target-node-name="Review"><div>Start → Review</div><p>rejected</p></div>
			<div data-automation-live-edge-detail="e1" data-source-node-name="Start" data-target-node-name="Review"><div>Start → Review</div><p>approved</p></div>
		</div></div>
	</div>`)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Edges) != 4 {
		t.Fatalf("duplicate endpoint edges were merged: %+v", detail.Edges)
	}
	graphRecords, detailRecords := 0, 0
	for _, edge := range detail.Edges {
		if edge.EdgeKey == "" {
			graphRecords++
			continue
		}
		detailRecords++
		if edge.TransitionCountAvailable || edge.RecentTransitionCountAvailable {
			t.Errorf("detail edge received counts without a unique join: %+v", edge)
		}
	}
	if graphRecords != 2 || detailRecords != 2 {
		t.Fatalf("graph records=%d detail records=%d, edges=%+v", graphRecords, detailRecords, detail.Edges)
	}
}

func TestParseAutomationDetailDoesNotMergeWhenDetailEndpointIsDuplicated(t *testing.T) {
	detail, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au-duplicate-detail-endpoint" data-project-id="p1" data-automation-lifecycle-state="active">
		<div data-automation-graph-panel><svg><line class="automation-graph-edge" data-source-node-name="Start" data-target-node-name="Review" aria-label="approved, 3 transitions, 0 recent"></line></svg></div>
		<div data-automation-live-details-panel><div data-automation-live-edge-details>
			<div data-automation-live-edge-detail="e2" data-source-node-name="Start" data-target-node-name="Review"><div>Start → Review</div><p>approved</p></div>
			<div data-automation-live-edge-detail="e1" data-source-node-name="Start" data-target-node-name="Review"><div>Start → Review</div><p>approved</p></div>
		</div></div>
	</div>`)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Edges) != 3 {
		t.Fatalf("duplicate detail endpoint was merged with the graph edge: %+v", detail.Edges)
	}
	for _, edge := range detail.Edges {
		if edge.EdgeKey != "" && (edge.TransitionCountAvailable || edge.RecentTransitionCountAvailable) {
			t.Errorf("detail edge received an ambiguous graph count: %+v", edge)
		}
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
	if len(detail.Edges) != 7 || !detail.EdgeCountsAvailable {
		t.Fatalf("edges = %+v, available=%t", detail.Edges, detail.EdgeCountsAvailable)
	}
	graphTransitions := []int{}
	detailKeys := map[string]bool{}
	for _, edge := range detail.Edges {
		if edge.EdgeKey == "" {
			if edge.SourceName != "" || edge.TargetName != "" {
				t.Errorf("uncorrelated graph edge was assigned endpoints: %+v", edge)
			}
			if !edge.TransitionCountAvailable {
				t.Errorf("graph edge lost transition count: %+v", edge)
			}
			graphTransitions = append(graphTransitions, edge.TransitionCount)
			continue
		}
		detailKeys[edge.EdgeKey] = true
		if edge.TransitionCountAvailable || edge.RecentTransitionCountAvailable {
			t.Errorf("detail edge received unverified graph counts: %+v", edge)
		}
	}
	if len(graphTransitions) != 4 || graphTransitions[0] != 3 || graphTransitions[1] != 4 || graphTransitions[2] != 0 || graphTransitions[3] != 0 {
		t.Fatalf("graph transition records = %v", graphTransitions)
	}
	if len(detailKeys) != 3 || !detailKeys["e1"] || !detailKeys["e2"] || !detailKeys["e3"] {
		t.Fatalf("detail edge records = %+v", detailKeys)
	}
	if !strings.Contains(strings.Join(detail.Warnings, "\n"), "edge") {
		t.Fatalf("missing edge-correlation warning: %v", detail.Warnings)
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
	if !detail.GraphAvailable || !detail.NodesAvailable || !detail.EdgesAvailable || len(detail.Nodes) != 2 || len(detail.Edges) != 2 {
		t.Fatalf("actual graph = %+v", detail)
	}
	if len(detail.UnmatchedNodeDetails) != 2 || detail.UnmatchedNodeDetails[0].Name != "Start" || detail.UnmatchedNodeDetails[1].Name != "Review" {
		t.Fatalf("unmatched node details = %+v, want both detail records retained separately", detail.UnmatchedNodeDetails)
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

func TestParseAutomationDetailUsesPerMetricCountProvenance(t *testing.T) {
	detail, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au-count-provenance" data-project-id="p1" data-automation-lifecycle-state="active">
		<div data-automation-graph-panel>
			<g data-automation-live-node="n1" data-automation-node-key="shared" data-counts='{"waiting":2}'><strong>Shared</strong><small>9 running · 1 waiting</small></g>
		</div>
		<div data-automation-live-details-panel>
			<section data-automation-live-node-detail="shared"><h3>Shared</h3><span data-automation-node-counts='{"running":0}'></span></section>
		</div>
	</div>`)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Nodes) != 1 {
		t.Fatalf("nodes = %+v, want one correlated node", detail.Nodes)
	}
	counts := detail.Nodes[0].Counts
	if !counts.RunningAvailable || counts.Running != 0 || !counts.WaitingAvailable || counts.Waiting != 2 {
		t.Fatalf("mixed count provenance = %+v, want structured running=0 and waiting=2", counts)
	}
}

func TestParseAutomationDetailUnknownExternalFreshnessIsUnavailable(t *testing.T) {
	detail, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au-external" data-project-id="p1" data-automation-lifecycle-state="active">
		<div data-automation-graph-panel></div>
		<div data-automation-external-state data-automation-external-stale="mystery"></div>
	</div>`)
	if err != nil {
		t.Fatal(err)
	}
	if !detail.ExternalStateAvailable || detail.ExternalState.StaleAvailable || detail.ExternalState.Stale {
		t.Fatalf("unknown freshness availability = %+v/%t, want section available but freshness unavailable", detail.ExternalState, detail.ExternalStateAvailable)
	}
	if !detail.Partial || !strings.Contains(strings.Join(detail.Warnings, "\n"), "external state freshness is malformed") {
		t.Fatalf("unknown freshness should be partial with a warning: %+v", detail)
	}
}

func TestParseAutomationDetailPreservesResourceRelations(t *testing.T) {
	detail, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au-resources" data-project-id="p1" data-automation-lifecycle-state="active">
		<div data-automation-resources>
			<div data-automation-resource-row data-automation-resource-node-key="shared" data-automation-resource-type="task" data-automation-resource-id="task-1" data-automation-resource-relation="parent" data-automation-resource-name="Parent"></div>
			<div data-automation-resource-row data-automation-resource-node-key="shared" data-automation-resource-type="task" data-automation-resource-id="task-1" data-automation-resource-relation="child" data-automation-resource-name="Child"></div>
		</div>
	</div>`)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Resources) != 2 {
		t.Fatalf("resources = %+v, want both relation variants", detail.Resources)
	}
	relations := map[string]bool{}
	for _, resource := range detail.Resources {
		relations[resource.Relation] = true
	}
	if !relations["parent"] || !relations["child"] {
		t.Fatalf("resource relations = %+v", relations)
	}
}

func TestParseAutomationDetailRetainsAllDetailOnlyNodes(t *testing.T) {
	detail, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au-detail-nodes" data-project-id="p1" data-automation-lifecycle-state="active">
		<div data-automation-live-details-panel>
			<section data-automation-live-node-detail="first"><h3>First</h3><p>first · trigger</p></section>
			<section data-automation-live-node-detail="second"><h3>Second</h3><p>second · action</p></section>
		</div>
	</div>`)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Nodes) != 2 {
		t.Fatalf("detail-only nodes = %+v, want both records", detail.Nodes)
	}
	if detail.Nodes[0].NodeKey != "first" || detail.Nodes[0].Name != "First" || detail.Nodes[1].NodeKey != "second" || detail.Nodes[1].Name != "Second" {
		t.Fatalf("detail-only node records = %+v", detail.Nodes)
	}
}

func TestParseAutomationDetailDoesNotGuessGraphDetailEdgeCorrelation(t *testing.T) {
	detail, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au-edge-correlation" data-project-id="p1" data-automation-lifecycle-state="active">
		<div data-automation-graph-panel><svg>
			<line class="automation-graph-edge" aria-label="approved, 3 transitions, 0 recent"></line>
			<line class="automation-graph-edge" aria-label="approved, 4 transitions, 0 recent"></line>
		</svg></div>
		<div data-automation-live-details-panel><div data-automation-live-edge-details>
			<div data-automation-live-edge-detail="e2"><div>Start → Deploy</div><p>approved</p></div>
			<div data-automation-live-edge-detail="e1"><div>Start → Review</div><p>approved</p></div>
		</div></div>
	</div>`)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Edges) != 4 {
		t.Fatalf("edges = %+v, want separate graph and detail records", detail.Edges)
	}
	graphRecords, detailRecords := 0, 0
	for _, edge := range detail.Edges {
		if edge.EdgeKey == "" {
			graphRecords++
			if !edge.TransitionCountAvailable {
				t.Errorf("graph edge lost its count: %+v", edge)
			}
			continue
		}
		detailRecords++
		if edge.TransitionCountAvailable || edge.RecentTransitionCountAvailable {
			t.Errorf("unverified graph/detail join supplied counts to %q: %+v", edge.EdgeKey, edge)
		}
	}
	if graphRecords != 2 || detailRecords != 2 {
		t.Fatalf("graph records=%d detail records=%d, edges=%+v", graphRecords, detailRecords, detail.Edges)
	}
	if !strings.Contains(strings.Join(detail.Warnings, "\n"), "edge") {
		t.Fatalf("missing edge-correlation warning: %v", detail.Warnings)
	}
}

func TestParseAutomationDetailRejectsUnknownExternalStateValues(t *testing.T) {
	tests := []struct {
		name     string
		section  string
		wantWarn string
	}{
		{
			name:     "unknown status attribute",
			section:  `<div data-automation-external-state data-automation-external-status="mystery"></div>`,
			wantWarn: "external state",
		},
		{
			name:     "unknown status text",
			section:  `<div data-automation-external-state>status: mystery</div>`,
			wantWarn: "freshness",
		},
		{
			name:     "negated freshness text",
			section:  `<div data-automation-external-state>not stale</div>`,
			wantWarn: "freshness",
		},
		{
			name:     "multi-word negated freshness text",
			section:  `<div data-automation-external-state>not currently stale</div>`,
			wantWarn: "freshness",
		},
		{
			name:     "mixed positive and qualified freshness text",
			section:  `<div data-automation-external-state><div>stale</div><div>maybe stale</div></div>`,
			wantWarn: "freshness",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			detail, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au-external-values" data-project-id="p1" data-automation-lifecycle-state="active"><div data-automation-graph-panel></div>` + tc.section + `</div>`)
			if err != nil {
				t.Fatal(err)
			}
			if !detail.ExternalStateAvailable || detail.ExternalState.StaleAvailable || detail.ExternalState.Stale || detail.ExternalState.Status != "" {
				t.Fatalf("external state = %+v available=%t, want no confident freshness value", detail.ExternalState, detail.ExternalStateAvailable)
			}
			warnings := strings.Join(detail.Warnings, "\n")
			if !detail.Partial || !strings.Contains(warnings, tc.wantWarn) {
				t.Fatalf("warnings=%v partial=%t, want warning containing %q", detail.Warnings, detail.Partial, tc.wantWarn)
			}
		})
	}
}

func TestParseAutomationDetailAcceptsClearlyLabeledFreshnessText(t *testing.T) {
	detail, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au-external-label" data-project-id="p1" data-automation-lifecycle-state="active"><div data-automation-graph-panel></div><div data-automation-external-state><div>status = fresh</div></div></div>`)
	if err != nil {
		t.Fatal(err)
	}
	if detail.ExternalState.Status != "fresh" || !detail.ExternalState.StaleAvailable || detail.ExternalState.Stale {
		t.Fatalf("labeled freshness = %+v, want fresh", detail.ExternalState)
	}
}

func TestParseAutomationDetailTreatsUnknownGraphAvailabilityAsUncertain(t *testing.T) {
	detail, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au-graph-availability" data-project-id="p1" data-automation-lifecycle-state="active" data-automation-graph-available="maybe">
		<div data-automation-graph-panel><g data-automation-live-node="n1"><strong>Start</strong></g></div>
	</div>`)
	if err != nil {
		t.Fatal(err)
	}
	if !detail.GraphAvailable || !detail.NodesAvailable || !detail.EdgesAvailable {
		t.Fatalf("unknown graph availability discarded structural evidence: %+v", detail)
	}
	if !detail.Partial || !strings.Contains(strings.Join(detail.Warnings, "\n"), "graph availability") {
		t.Fatalf("unknown graph availability = partial=%t warnings=%v", detail.Partial, detail.Warnings)
	}
}

func TestParseAutomationDetailUsesPerMetricEdgeCountProvenance(t *testing.T) {
	detail, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au-edge-provenance" data-project-id="p1" data-automation-lifecycle-state="active">
		<div data-automation-graph-panel><svg><line data-automation-live-edge="e1" class="automation-graph-edge" aria-label="approved, 3 transitions, 1 recent"></line></svg></div>
		<div data-automation-live-details-panel><div data-automation-live-edge-details>
			<div data-automation-live-edge-detail="e1" data-automation-transition-count="7"><div>Start → Review</div><p>approved</p></div>
		</div></div>
	</div>`)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Edges) != 1 {
		t.Fatalf("edges = %+v, want one correlated edge", detail.Edges)
	}
	edge := detail.Edges[0]
	if edge.TransitionCount != 7 || !edge.TransitionCountAvailable || edge.RecentTransitionCount != 1 || !edge.RecentTransitionCountAvailable {
		t.Fatalf("edge counts = %+v, want structured transition=7 and aria recent=1", edge)
	}
}

func TestParseAutomationDetailRejectsMalformedAutomationCounts(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "trailing JSON data",
			body: `<g data-automation-live-node="n1" data-counts='{"running":2} trailing'><strong>Node</strong></g>`,
		},
		{
			name: "negative text count",
			body: `<g data-automation-live-node="n1"><strong>Node</strong><small>running -2</small></g>`,
		},
		{
			name: "text count trailing word",
			body: `<g data-automation-live-node="n1"><strong>Node</strong><small>running 2oops</small></g>`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			detail, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au-malformed-counts" data-project-id="p1" data-automation-lifecycle-state="active"><div data-automation-graph-panel>` + tc.body + `</div></div>`)
			if err != nil {
				t.Fatal(err)
			}
			if len(detail.Nodes) != 1 {
				t.Fatalf("nodes = %+v", detail.Nodes)
			}
			counts := detail.Nodes[0].Counts
			if counts.RunningAvailable || counts.Running != 0 {
				t.Fatalf("malformed running count was accepted: %+v", counts)
			}
			if !strings.Contains(strings.Join(detail.Warnings, "\n"), "node counts") {
				t.Fatalf("missing malformed-count warning: %v", detail.Warnings)
			}
		})
	}
}

func TestParseAutomationDetailRetainsUnidentifiedNodeDetails(t *testing.T) {
	detail, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au-unidentified-detail" data-project-id="p1" data-automation-lifecycle-state="active"><div data-automation-graph-panel><g data-automation-live-node="n1"><strong>Graph</strong></g></div><section data-automation-live-node-detail><div data-counts='{"running":1}'></div></section></div>`)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.UnmatchedNodeDetails) != 1 {
		t.Fatalf("unidentified detail record was dropped: %+v", detail.UnmatchedNodeDetails)
	}
	unmatched := detail.UnmatchedNodeDetails[0]
	if unmatched.Counts.Running != 1 || !unmatched.Counts.RunningAvailable {
		t.Fatalf("unidentified detail record lost its available fields: %+v", unmatched)
	}
	if !detail.Partial || !strings.Contains(strings.Join(detail.Warnings, "\n"), "node detail") {
		t.Fatalf("unidentified detail did not remain explicitly partial: partial=%t warnings=%v", detail.Partial, detail.Warnings)
	}
}
func TestParseAutomationDetailDoesNotRecoverMalformedStructuredCounts(t *testing.T) {
	detail, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au-strict-counts" data-project-id="p1" data-automation-lifecycle-state="active">
		<div data-automation-graph-panel><svg>
			<g data-automation-live-node="n1" data-counts='{"running":2} trailing'><strong>Node</strong><small>9 running</small></g>
			<g data-automation-live-node="n2"><strong>Negative</strong><small>-2 running</small></g>
		</svg></div>
		<line data-automation-live-edge="e1" data-transition-count="not-a-number" aria-label="approved, 8 transitions, 1 recent"></line>
	</div>`)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Nodes) != 2 || len(detail.Edges) != 1 {
		t.Fatalf("parsed records = nodes=%d edges=%d, detail=%+v", len(detail.Nodes), len(detail.Edges), detail)
	}
	for _, node := range detail.Nodes {
		if node.Counts.RunningAvailable || node.Counts.Running != 0 {
			t.Fatalf("malformed node running count became available for %q: %+v", node.Name, node.Counts)
		}
	}
	edge := detail.Edges[0]
	if edge.TransitionCountAvailable || edge.TransitionCount != 0 {
		t.Fatalf("malformed edge transition count recovered from ARIA: %+v", edge)
	}
	if !edge.RecentTransitionCountAvailable || edge.RecentTransitionCount != 1 {
		t.Fatalf("valid independent ARIA recent count was lost: %+v", edge)
	}
	warnings := strings.Join(detail.Warnings, "\n")
	if !strings.Contains(warnings, "node counts") || !strings.Contains(warnings, "edge transition") {
		t.Fatalf("strict count warnings = %v", detail.Warnings)
	}
}

func TestParseAutomationDetailRejectsConcatenatedTextCounts(t *testing.T) {
	for _, text := range []string{"running2", "2running", "not no active work", "running 2-foo", "running 2 trailing", "2 running trailing", "not 2 running"} {
		t.Run(text, func(t *testing.T) {
			detail, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au-boundary" data-project-id="p1" data-automation-lifecycle-state="active"><div data-automation-graph-panel><g data-automation-live-node="n1"><strong>Node</strong><small>` + text + `</small></g></div></div>`)
			if err != nil {
				t.Fatal(err)
			}
			if len(detail.Nodes) != 1 {
				t.Fatalf("nodes = %+v", detail.Nodes)
			}
			if detail.Nodes[0].Counts.RunningAvailable {
				t.Fatalf("concatenated count %q was accepted: %+v", text, detail.Nodes[0].Counts)
			}
		})
	}
}

func TestParseAutomationDetailInvalidExternalStatusSuppressesDerivedFreshness(t *testing.T) {
	detail, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au-external-status" data-project-id="p1" data-automation-lifecycle-state="active"><div data-automation-graph-panel></div><div data-automation-external-state data-automation-external-status="mystery">stale</div></div>`)
	if err != nil {
		t.Fatal(err)
	}
	if !detail.ExternalStateAvailable || detail.ExternalState.Status != "" || detail.ExternalState.StaleAvailable {
		t.Fatalf("invalid structured status became confident freshness: %+v", detail.ExternalState)
	}
	warnings := strings.Join(detail.Warnings, "\n")
	if !detail.Partial || !strings.Contains(warnings, "external state status is malformed") {
		t.Fatalf("invalid status warnings = %v partial=%t", detail.Warnings, detail.Partial)
	}
}

func TestParseAutomationDetailDoesNotRecoverMalformedAggregateCounts(t *testing.T) {
	detail, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au-aggregate-counts" data-project-id="p1" data-automation-lifecycle-state="active">
		<div data-automation-graph-panel></div>
		<div data-automation-live-metrics data-automation-active-invocations="not-a-number" data-automation-active-work-items="2">7 active invocations · 8 active work items</div>
		<div data-automation-external-state data-automation-external-tracked-resources="not-a-number">9 tracked resources</div>
	</div>`)
	if err != nil {
		t.Fatal(err)
	}
	if detail.ActiveInvocationsAvailable || detail.ActiveInvocations != 0 {
		t.Fatalf("malformed active invocations recovered from text: %d/%t", detail.ActiveInvocations, detail.ActiveInvocationsAvailable)
	}
	if !detail.ActiveWorkItemsAvailable || detail.ActiveWorkItems != 2 {
		t.Fatalf("valid active work items changed by malformed sibling: %d/%t", detail.ActiveWorkItems, detail.ActiveWorkItemsAvailable)
	}
	if !detail.ExternalStateAvailable || detail.ExternalState.TrackedResourcesAvailable || detail.ExternalState.TrackedResources != 0 {
		t.Fatalf("malformed tracked resources recovered from text: %+v", detail.ExternalState)
	}
	warnings := strings.Join(detail.Warnings, "\n")
	for _, want := range []string{"active invocations count is malformed", "tracked resources count is malformed"} {
		if !strings.Contains(warnings, want) {
			t.Errorf("warnings=%v, missing %q", detail.Warnings, want)
		}
	}
}

func TestParseAutomationDetailRejectsMalformedAndOverflowTextCounts(t *testing.T) {
	overflow := strings.Repeat("9", 64)
	detail, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au-count-boundaries" data-project-id="p1" data-automation-lifecycle-state="active">
		<div data-automation-graph-panel><svg>
			<g data-automation-live-node="n1"><strong>Trailing</strong><small>running 2 trailing</small></g>
			<g data-automation-live-node="n2"><strong>Overflow</strong><small>running ` + overflow + `</small></g>
			<line data-automation-live-edge="e1" class="automation-graph-edge" aria-label="approved, ` + overflow + ` transitions, 1 recent"></line>
		</svg></div>
		<div data-automation-live-metrics>6 active invocations trailing</div>
	</div>`)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Nodes) != 2 || len(detail.Edges) != 1 {
		t.Fatalf("records = nodes=%d edges=%d", len(detail.Nodes), len(detail.Edges))
	}
	for _, node := range detail.Nodes {
		if node.Counts.RunningAvailable || node.Counts.Running != 0 {
			t.Fatalf("malformed text count was accepted for %q: %+v", node.Name, node.Counts)
		}
	}
	if detail.ActiveInvocationsAvailable || detail.ActiveInvocations != 0 {
		t.Fatalf("malformed runtime text count was accepted: %d/%t", detail.ActiveInvocations, detail.ActiveInvocationsAvailable)
	}
	edge := detail.Edges[0]
	if edge.TransitionCountAvailable || edge.TransitionCount != 0 {
		t.Fatalf("overflow ARIA transition count was accepted: %+v", edge)
	}
	if !edge.RecentTransitionCountAvailable || edge.RecentTransitionCount != 1 {
		t.Fatalf("valid independent ARIA count was lost: %+v", edge)
	}
	warnings := strings.Join(detail.Warnings, "\n")
	for _, want := range []string{"node counts are malformed", "active invocations count is malformed", "edge transition count is malformed"} {
		if !strings.Contains(warnings, want) {
			t.Errorf("warnings=%v, missing %q", detail.Warnings, want)
		}
	}
}

func TestParseAutomationDetailPreservesIndependentMetricsAfterMalformedStructuredCounts(t *testing.T) {
	detail, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au-independent-counts" data-project-id="p1" data-automation-lifecycle-state="active">
		<div data-automation-graph-panel><g data-automation-live-node="n1" data-counts='{"running":2} trailing'><strong>Node</strong><small>9 running · 3 failed</small></g></div>
	</div>`)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Nodes) != 1 {
		t.Fatalf("nodes = %+v", detail.Nodes)
	}
	counts := detail.Nodes[0].Counts
	if counts.RunningAvailable || counts.Running != 0 {
		t.Fatalf("malformed structured running count recovered: %+v", counts)
	}
	if !counts.FailedAvailable || counts.Failed != 3 {
		t.Fatalf("independent valid failed count was suppressed: %+v", counts)
	}
}

func TestParseAutomationDetailPreservesIndependentMetricsAfterTruncatedStructuredCounts(t *testing.T) {
	detail, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au-truncated-counts" data-project-id="p1" data-automation-lifecycle-state="active">
		<div data-automation-graph-panel><g data-automation-live-node="n1" data-counts='{"running":2,'><strong>Node</strong><small>9 running · 3 failed</small></g></div>
	</div>`)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Nodes) != 1 {
		t.Fatalf("nodes = %+v", detail.Nodes)
	}
	counts := detail.Nodes[0].Counts
	if counts.RunningAvailable || counts.Running != 0 {
		t.Fatalf("truncated structured running count recovered: %+v", counts)
	}
	if !counts.FailedAvailable || counts.Failed != 3 {
		t.Fatalf("independent failed count was suppressed by truncated payload: %+v", counts)
	}
}

func TestParseAutomationDetailRetainsIdentitylessMarkedGraphNodes(t *testing.T) {
	detail, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au-identityless-node" data-project-id="p1" data-automation-lifecycle-state="active">
		<div data-automation-graph-panel><g data-automation-live-node data-counts='{"running":1}'></g></div>
	</div>`)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Nodes) != 1 {
		t.Fatalf("identity-less marked graph node was dropped: %+v", detail.Nodes)
	}
	if detail.Nodes[0].ID != "" || detail.Nodes[0].NodeKey != "" || detail.Nodes[0].Name != "" {
		t.Fatalf("identity-less graph node gained fabricated identity: %+v", detail.Nodes[0])
	}
	if !detail.Nodes[0].Counts.RunningAvailable || detail.Nodes[0].Counts.Running != 1 {
		t.Fatalf("identity-less graph node lost counts: %+v", detail.Nodes[0].Counts)
	}
}

func TestParseAutomationDetailDoesNotUseUnmatchedDetailMetricsForGraphNodes(t *testing.T) {
	detail, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au-unmatched-counts" data-project-id="p1" data-automation-lifecycle-state="active">
		<div data-automation-graph-panel><g data-automation-live-node="n1"><strong>Graph</strong></g></div>
		<div data-automation-live-details-panel><section data-automation-live-node-detail="detail-only"><h3>Detail only</h3><span data-counts='{"running":5}'></span></section></div>
	</div>`)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Nodes) != 1 || len(detail.UnmatchedNodeDetails) != 1 {
		t.Fatalf("mixed graph/detail records = nodes=%+v unmatched=%+v", detail.Nodes, detail.UnmatchedNodeDetails)
	}
	if detail.NodeCountsAvailable {
		t.Fatalf("unmatched detail metric made graph node counts available: %+v", detail)
	}
	if detail.Nodes[0].Counts.RunningAvailable {
		t.Fatalf("graph node inherited unmatched detail metric: %+v", detail.Nodes[0].Counts)
	}
	if !detail.UnmatchedNodeDetails[0].Counts.RunningAvailable || detail.UnmatchedNodeDetails[0].Counts.Running != 5 {
		t.Fatalf("unmatched detail metric was lost: %+v", detail.UnmatchedNodeDetails[0].Counts)
	}
}

func TestParseAutomationDetailRejectsMalformedTextCountContinuations(t *testing.T) {
	for _, text := range []string{"running 2,garbage", "2 running;garbage", "not running 2"} {
		t.Run(text, func(t *testing.T) {
			detail, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au-count-continuation" data-project-id="p1" data-automation-lifecycle-state="active"><div data-automation-graph-panel><g data-automation-live-node="n1"><strong>Node</strong><small>` + text + `</small></g></div></div>`)
			if err != nil {
				t.Fatal(err)
			}
			if len(detail.Nodes) != 1 || detail.Nodes[0].Counts.RunningAvailable {
				t.Fatalf("malformed count %q was accepted: %+v", text, detail)
			}
			if !detail.Partial || !strings.Contains(strings.Join(detail.Warnings, "\n"), "node counts are malformed") {
				t.Fatalf("malformed count %q did not produce a partial warning: partial=%t warnings=%v", text, detail.Partial, detail.Warnings)
			}
		})
	}
}

func TestParseAutomationDetailWarnsOnMalformedARIAEdgeCounts(t *testing.T) {
	for _, aria := range []string{
		"approved, -1 transitions, 1 recent",
		"approved, nope transitions, 1 recent",
		"approved, 3 transitions, 1 recent trailing",
	} {
		t.Run(aria, func(t *testing.T) {
			detail, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au-aria-counts" data-project-id="p1" data-automation-lifecycle-state="active"><div data-automation-graph-panel><svg><line class="automation-graph-edge" aria-label="` + aria + `"></line></svg></div></div>`)
			if err != nil {
				t.Fatal(err)
			}
			if len(detail.Edges) != 1 {
				t.Fatalf("malformed ARIA edge was dropped: %+v", detail.Edges)
			}
			edge := detail.Edges[0]
			if edge.TransitionCountAvailable || edge.RecentTransitionCountAvailable {
				t.Fatalf("malformed ARIA counts became available: %+v", edge)
			}
			warnings := strings.Join(detail.Warnings, "\n")
			if !detail.Partial || !strings.Contains(warnings, "edge transition") {
				t.Fatalf("malformed ARIA counts lacked a partial warning: partial=%t warnings=%v", detail.Partial, detail.Warnings)
			}
		})
	}
}

func TestParseAutomationDetailInvalidatesAllClaimedMetricsInPartiallyDecodedCounts(t *testing.T) {
	detail, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au-partial-counts" data-project-id="p1" data-automation-lifecycle-state="active">
		<div data-automation-graph-panel><g data-automation-live-node="n1" data-counts='{"running":2,"failed":'><strong>Node</strong><small>9 running · 3 failed · 4 waiting</small></g></div>
	</div>`)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Nodes) != 1 {
		t.Fatalf("nodes = %+v", detail.Nodes)
	}
	counts := detail.Nodes[0].Counts
	if counts.RunningAvailable || counts.Running != 0 || counts.FailedAvailable || counts.Failed != 0 {
		t.Fatalf("claimed metrics recovered from partial JSON: %+v", counts)
	}
	if !counts.WaitingAvailable || counts.Waiting != 4 {
		t.Fatalf("independent waiting metric was not retained: %+v", counts)
	}
	if !detail.Partial || !strings.Contains(strings.Join(detail.Warnings, "\n"), "node counts are malformed") {
		t.Fatalf("partial JSON lacked warning: partial=%t warnings=%v", detail.Partial, detail.Warnings)
	}
}

func TestParseAutomationDetailRetainsMalformedIdentitylessGraphEdges(t *testing.T) {
	detail, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au-identityless-edge" data-project-id="p1" data-automation-lifecycle-state="active">
		<div data-automation-graph-panel><svg><line class="automation-graph-edge" data-transition-count="not-a-number"></line></svg></div>
	</div>`)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Edges) != 1 {
		t.Fatalf("identityless malformed edge was dropped: %+v", detail.Edges)
	}
	edge := detail.Edges[0]
	if edge.TransitionCountAvailable || edge.RecentTransitionCountAvailable {
		t.Fatalf("malformed identityless edge counts became available: %+v", edge)
	}
	warnings := strings.Join(detail.Warnings, "\n")
	if !detail.Partial || !strings.Contains(warnings, "edge record has no stable identity") {
		t.Fatalf("identityless edge lacked retention warning: partial=%t warnings=%v", detail.Partial, detail.Warnings)
	}
}

func parseAutomationDetailFromString(source string) (AutomationDetail, error) {
	root, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return AutomationDetail{}, err
	}
	return parseAutomationDetail(root)
}
