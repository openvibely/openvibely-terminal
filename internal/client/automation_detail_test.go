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
	detail, err := parseAutomationDetailFromString(`<div id="automation-live" data-automation-id="au1" data-project-id="p1" data-automation-lifecycle-state="active"><div data-automation-graph-panel><g data-automation-live-node="n1" data-automation-node-count-running="not-a-number"><strong>Node</strong></g></div><div data-automation-live-metrics data-automation-active-invocations="NaN" data-automation-active-work-items="2"></div></div>`)
	if err != nil {
		t.Fatal(err)
	}
	if !detail.GraphAvailable || !detail.Partial {
		t.Fatalf("malformed optional detail = %+v", detail)
	}
	if detail.ActiveInvocationsAvailable || !detail.ActiveWorkItemsAvailable || detail.ActiveWorkItems != 2 {
		t.Fatalf("malformed runtime availability = invocations %t, work %d/%t", detail.ActiveInvocationsAvailable, detail.ActiveWorkItems, detail.ActiveWorkItemsAvailable)
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

func parseAutomationDetailFromString(source string) (AutomationDetail, error) {
	root, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return AutomationDetail{}, err
	}
	return parseAutomationDetail(root)
}
