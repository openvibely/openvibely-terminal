package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// ErrAutomationNotFound identifies a project-scoped automation that the
// backend could not find. It is kept distinct from a generic backend error so
// a detail view can explain that no graph was loaded.
var ErrAutomationNotFound = errors.New("automation not found")

// AutomationNotFoundError carries the safe identity of a missing automation.
type AutomationNotFoundError struct {
	ID        string
	ProjectID string
}

func (e *AutomationNotFoundError) Error() string {
	if e == nil || e.ID == "" {
		return ErrAutomationNotFound.Error()
	}
	return fmt.Sprintf("automation %q not found", e.ID)
}

func (e *AutomationNotFoundError) Unwrap() error { return ErrAutomationNotFound }

// AutomationMetadata is the metadata rendered by the live automation route.
// It mirrors the backend's automation object without changing the smaller
// Automation card model used by list and lifecycle commands.
type AutomationMetadata struct {
	ID                 string `json:"id"`
	ProjectID          string `json:"project_id"`
	StableKey          string `json:"stable_key"`
	Name               string `json:"name"`
	Description        string `json:"description"`
	AutomationType     string `json:"automation_type"`
	LifecycleState     string `json:"lifecycle_state"`
	HealthState        string `json:"health_state"`
	HealthReason       string `json:"health_reason"`
	PublishedVersionID string `json:"published_version_id,omitempty"`
	TemplateRevision   string `json:"template_revision,omitempty"`
	CreatedVia         string `json:"created_via"`
	CreatedAt          string `json:"created_at"`
	UpdatedAt          string `json:"updated_at"`
	ArchivedAt         string `json:"archived_at,omitempty"`
}

// AutomationVersion is the version metadata exposed by the backend live graph.
type AutomationVersion struct {
	ID            string `json:"id"`
	ProjectID     string `json:"project_id"`
	AutomationID  string `json:"automation_id"`
	Version       int    `json:"version"`
	State         string `json:"state"`
	Source        string `json:"source"`
	AdapterKey    string `json:"adapter_key"`
	SchemaVersion int    `json:"schema_version"`
	CreatedAt     string `json:"created_at"`
	PublishedAt   string `json:"published_at,omitempty"`
}

// AutomationNode is the saved graph node shape used by the live node model.
type AutomationNode struct {
	ID           string  `json:"id"`
	ProjectID    string  `json:"project_id"`
	AutomationID string  `json:"automation_id"`
	VersionID    string  `json:"version_id"`
	NodeKey      string  `json:"node_key"`
	Name         string  `json:"name"`
	NodeType     string  `json:"node_type"`
	Role         string  `json:"role"`
	ConfigJSON   string  `json:"config_json,omitempty"`
	PositionX    float64 `json:"position_x,omitempty"`
	PositionY    float64 `json:"position_y,omitempty"`
}

// AutomationNodeCounts contains the meaningful runtime counts for one node.
type AutomationNodeCounts struct {
	Running           int `json:"running"`
	Waiting           int `json:"waiting"`
	Blocked           int `json:"blocked"`
	Failed            int `json:"failed"`
	CompletedRecently int `json:"completed_recently"`

	RunningAvailable           bool `json:"running_available"`
	WaitingAvailable           bool `json:"waiting_available"`
	BlockedAvailable           bool `json:"blocked_available"`
	FailedAvailable            bool `json:"failed_available"`
	CompletedRecentlyAvailable bool `json:"completed_recently_available"`

	// Per-field provenance is parser-only. Structured values are preferred over
	// compact visible labels independently for each metric.
	runningQuality, waitingQuality, blockedQuality, failedQuality, completedRecentlyQuality int
}

// AutomationLiveNode is one node in the current published graph.
type AutomationLiveNode struct {
	AutomationNode
	Counts       AutomationNodeCounts `json:"counts"`
	DisplayState string               `json:"display_state"`
}

// AutomationEdge is the saved graph edge shape used by the live edge model.
type AutomationEdge struct {
	ID            string `json:"id"`
	ProjectID     string `json:"project_id"`
	AutomationID  string `json:"automation_id"`
	VersionID     string `json:"version_id"`
	SourceNodeID  string `json:"source_node_id"`
	TargetNodeID  string `json:"target_node_id"`
	EdgeKey       string `json:"edge_key"`
	Label         string `json:"label"`
	ConditionJSON string `json:"condition_json,omitempty"`
	DisplayOrder  int    `json:"display_order,omitempty"`
}

// AutomationLiveEdge is one transition in the current published graph.
type AutomationLiveEdge struct {
	AutomationEdge
	TransitionCount       int    `json:"transition_count"`
	RecentTransitionCount int    `json:"recent_transition_count"`
	Highlighted           bool   `json:"highlighted,omitempty"`
	SourceName            string `json:"source_name,omitempty"`
	TargetName            string `json:"target_name,omitempty"`

	TransitionCountAvailable       bool `json:"transition_count_available"`
	RecentTransitionCountAvailable bool `json:"recent_transition_count_available"`

	transitionCountQuality       int
	recentTransitionCountQuality int

	// edgeSource identifies the graph/detail representation during parsing and
	// is intentionally omitted from machine-readable output.
	edgeSource int
}

// AutomationResourceSummary is one resource linked to a live graph node.
type AutomationResourceSummary struct {
	NodeID       string `json:"node_id"`
	NodeKey      string `json:"node_key"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	Relation     string `json:"relation"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	URL          string `json:"url,omitempty"`
}

// AutomationExternalState reports the freshness of externally tracked state.
type AutomationExternalState struct {
	TrackedResources          int    `json:"tracked_resources"`
	TrackedResourcesAvailable bool   `json:"tracked_resources_available"`
	LastUpdatedAt             string `json:"last_updated_at,omitempty"`
	Stale                     bool   `json:"stale"`
	StaleAvailable            bool   `json:"stale_available"`
	Status                    string `json:"status,omitempty"`
	StatusInvalid             bool   `json:"status_invalid,omitempty"`
}

// AutomationDetail is the project-scoped read-only live automation response.
// The availability fields are client-side contract information: HTML
// fragments can omit optional sections, and zero is otherwise ambiguous with
// an explicitly reported zero.
type AutomationDetail struct {
	Automation AutomationMetadata   `json:"automation"`
	Version    AutomationVersion    `json:"version"`
	Nodes      []AutomationLiveNode `json:"nodes"`
	// UnmatchedNodeDetails retains detail-panel records that cannot be safely
	// correlated with graph nodes. Keeping them separate prevents lost config
	// data without inflating the authoritative graph-node count.
	UnmatchedNodeDetails []AutomationLiveNode        `json:"unmatched_node_details,omitempty"`
	Edges                []AutomationLiveEdge        `json:"edges"`
	Resources            []AutomationResourceSummary `json:"resources"`
	ActiveInvocations    int                         `json:"active_invocations"`
	ActiveWorkItems      int                         `json:"active_work_items"`
	RecentCutoff         string                      `json:"recent_cutoff,omitempty"`
	ExternalState        AutomationExternalState     `json:"external_state"`

	GraphAvailable             bool     `json:"graph_available"`
	NodesAvailable             bool     `json:"nodes_available"`
	EdgesAvailable             bool     `json:"edges_available"`
	NodeCountsAvailable        bool     `json:"node_counts_available"`
	EdgeCountsAvailable        bool     `json:"edge_counts_available"`
	ActiveInvocationsAvailable bool     `json:"active_invocations_available"`
	ActiveWorkItemsAvailable   bool     `json:"active_work_items_available"`
	CountsAvailable            bool     `json:"counts_available"`
	ResourcesAvailable         bool     `json:"resources_available"`
	ExternalStateAvailable     bool     `json:"external_state_available"`
	Partial                    bool     `json:"partial"`
	Warnings                   []string `json:"warnings,omitempty"`
}

// GetAutomationDetail fetches and parses the selected project's live
// automation fragment. The route is intentionally the same route used by the
// web UI; it is not replaced with a guessed JSON endpoint.
func (c *Client) GetAutomationDetail(ctx context.Context, projectID, automationID string) (*AutomationDetail, error) {
	projectID = strings.TrimSpace(projectID)
	automationID = strings.TrimSpace(automationID)
	if automationID == "" {
		return nil, fmt.Errorf("automation ID is required")
	}
	path := "/automations/" + url.PathEscape(automationID) + query("project_id", projectID)
	root, err := c.getAutomationDetailHTML(ctx, path, projectID, automationID)
	if err != nil {
		return nil, err
	}

	detail, err := parseAutomationDetail(root)
	if err != nil {
		return nil, err
	}
	if detail.Automation.ID != automationID {
		return nil, fmt.Errorf("automation detail: response identity %q does not match requested automation %q", detail.Automation.ID, automationID)
	}
	if detail.Automation.ProjectID != "" && projectID != "" && detail.Automation.ProjectID != projectID {
		return nil, fmt.Errorf("automation detail: response project %q does not match selected project %q", detail.Automation.ProjectID, projectID)
	}
	return &detail, nil
}

func (c *Client) getAutomationDetailHTML(ctx context.Context, path, projectID, automationID string) (*html.Node, error) {
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

	if isReadAuthResponse(resp) {
		return nil, newAuthRequiredError(http.MethodGet, path, resp)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, &AutomationNotFoundError{ID: automationID, ProjectID: projectID}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, apiError(resp)
	}
	parsed, err := html.Parse(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("parsing automation detail: %w", err)
	}
	return parsed, nil
}

// parseAutomationDetail parses stable live-page markers and structured
// sections. It returns a structural error only when the automation identity is
// absent; malformed optional values become warnings and unavailable sections.
func parseAutomationDetail(root *html.Node) (AutomationDetail, error) {
	detail := AutomationDetail{
		Nodes:     make([]AutomationLiveNode, 0),
		Edges:     make([]AutomationLiveEdge, 0),
		Resources: make([]AutomationResourceSummary, 0),
		Warnings:  make([]string, 0),
	}
	live := findNode(root, func(n *html.Node) bool {
		return hasHTMLAttr(n, "data-automation-id") ||
			(attr(n, "id") == "automation-live" && (hasHTMLAttr(n, "data-project-id") || hasHTMLAttr(n, "data-refresh-url")))
	})
	if live == nil {
		return detail, fmt.Errorf("automation detail: malformed fragment: missing data-automation-id")
	}

	detail.Automation = parseAutomationMetadata(live)
	if detail.Automation.ID == "" {
		return detail, fmt.Errorf("automation detail: malformed fragment: empty automation ID")
	}

	parseAutomationVersion(&detail, live)
	parseAutomationGraph(&detail, live)
	parseAutomationRuntimeCounts(&detail, live)
	parseAutomationResources(&detail, live)
	parseAutomationExternalState(&detail, live)

	if detail.Automation.LifecycleState == "draft" || detail.Version.State == "draft" {
		detail.GraphAvailable = false
	}
	if detail.GraphAvailable && detail.Version.State == "" {
		// A successful live graph page is the published view even when the
		// current HTML fragment does not expose version metadata separately.
		detail.Version.State = "published"
	}
	if detail.GraphAvailable {
		detail.Partial = !detail.NodesAvailable || !detail.EdgesAvailable || !detail.CountsAvailable ||
			!detail.ActiveInvocationsAvailable || !detail.ActiveWorkItemsAvailable ||
			!detail.ResourcesAvailable || !detail.ExternalStateAvailable
	}
	if len(detail.Warnings) > 0 {
		detail.Partial = true
	}
	return detail, nil
}

func parseAutomationMetadata(live *html.Node) AutomationMetadata {
	meta := AutomationMetadata{
		ID:                 strings.TrimSpace(firstAutomationAttr(live, "data-automation-id", "data-id")),
		ProjectID:          strings.TrimSpace(firstAutomationAttr(live, "data-project-id", "data-automation-project-id")),
		StableKey:          strings.TrimSpace(firstAutomationAttr(live, "data-automation-stable-key", "data-stable-key")),
		Name:               strings.TrimSpace(firstAutomationAttr(live, "data-automation-name", "data-name")),
		Description:        strings.TrimSpace(firstAutomationAttr(live, "data-automation-description", "data-description")),
		AutomationType:     strings.TrimSpace(firstAutomationAttr(live, "data-automation-type", "data-automation-automation-type")),
		LifecycleState:     normalizeAutomationState(firstAutomationAttr(live, "data-automation-lifecycle-state", "data-automation-state", "data-automation-lifecycle", "data-lifecycle-state")),
		HealthState:        normalizeAutomationState(firstAutomationAttr(live, "data-automation-health-state", "data-health-state", "data-automation-health", "data-health")),
		HealthReason:       strings.TrimSpace(firstAutomationAttr(live, "data-automation-health-reason", "data-health-reason")),
		PublishedVersionID: strings.TrimSpace(firstAutomationAttr(live, "data-automation-published-version-id", "data-published-version-id")),
		TemplateRevision:   strings.TrimSpace(firstAutomationAttr(live, "data-automation-template-revision", "data-template-revision")),
		CreatedVia:         strings.TrimSpace(firstAutomationAttr(live, "data-automation-created-via", "data-created-via")),
		CreatedAt:          strings.TrimSpace(firstAutomationAttr(live, "data-automation-created-at", "data-created-at")),
		UpdatedAt:          strings.TrimSpace(firstAutomationAttr(live, "data-automation-updated-at", "data-updated-at")),
		ArchivedAt:         strings.TrimSpace(firstAutomationAttr(live, "data-automation-archived-at", "data-archived-at")),
	}

	metadataSection := findNode(live, func(n *html.Node) bool {
		return hasAnyHTMLAttr(n, "data-automation-metadata", "data-automation-live-metadata")
	})
	if metadataSection != nil {
		meta.ID = firstNonEmptyAutomation(meta.ID, firstAutomationAttrDeep(metadataSection, "data-automation-id", "data-id"))
		meta.ProjectID = firstNonEmptyAutomation(meta.ProjectID, firstAutomationAttrDeep(metadataSection, "data-project-id", "data-automation-project-id"))
		meta.StableKey = firstNonEmptyAutomation(meta.StableKey, firstAutomationAttrDeep(metadataSection, "data-automation-stable-key", "data-stable-key"))
		meta.Name = firstNonEmptyAutomation(meta.Name, firstAutomationAttrDeep(metadataSection, "data-automation-name", "data-name"))
		meta.Description = firstNonEmptyAutomation(meta.Description, firstAutomationAttrDeep(metadataSection, "data-automation-description", "data-description"))
		meta.AutomationType = firstNonEmptyAutomation(meta.AutomationType, firstAutomationAttrDeep(metadataSection, "data-automation-type", "data-automation-automation-type"))
		meta.LifecycleState = firstNonEmptyAutomation(meta.LifecycleState, normalizeAutomationState(firstAutomationAttrDeep(metadataSection, "data-automation-lifecycle-state", "data-automation-state", "data-lifecycle-state")))
		meta.HealthState = firstNonEmptyAutomation(meta.HealthState, normalizeAutomationState(firstAutomationAttrDeep(metadataSection, "data-automation-health-state", "data-health-state", "data-health")))
		meta.HealthReason = firstNonEmptyAutomation(meta.HealthReason, firstAutomationAttrDeep(metadataSection, "data-automation-health-reason", "data-health-reason"))
		meta.PublishedVersionID = firstNonEmptyAutomation(meta.PublishedVersionID, firstAutomationAttrDeep(metadataSection, "data-automation-published-version-id", "data-published-version-id"))
		meta.TemplateRevision = firstNonEmptyAutomation(meta.TemplateRevision, firstAutomationAttrDeep(metadataSection, "data-automation-template-revision", "data-template-revision"))
		meta.CreatedVia = firstNonEmptyAutomation(meta.CreatedVia, firstAutomationAttrDeep(metadataSection, "data-automation-created-via", "data-created-via"))
		meta.CreatedAt = firstNonEmptyAutomation(meta.CreatedAt, firstAutomationAttrDeep(metadataSection, "data-automation-created-at", "data-created-at"))
		meta.UpdatedAt = firstNonEmptyAutomation(meta.UpdatedAt, firstAutomationAttrDeep(metadataSection, "data-automation-updated-at", "data-updated-at"))
		meta.ArchivedAt = firstNonEmptyAutomation(meta.ArchivedAt, firstAutomationAttrDeep(metadataSection, "data-automation-archived-at", "data-archived-at"))
	}

	if meta.Name == "" {
		if breadcrumb := findNode(live, func(n *html.Node) bool { return hasHTMLAttr(n, "data-automation-breadcrumb") }); breadcrumb != nil {
			if heading := findNode(breadcrumb, func(n *html.Node) bool { return n.Data == "h2" }); heading != nil {
				meta.Name = strings.TrimSpace(NodeText(heading))
			}
		}
	}
	if meta.Name == "" {
		if title := findNode(live, func(n *html.Node) bool { return hasHTMLAttr(n, "data-openvibely-page-title") }); title != nil {
			meta.Name = strings.TrimSpace(strings.TrimSuffix(attr(title, "data-openvibely-page-title"), " - OpenVibely"))
		}
	}
	if meta.Name == "" {
		if heading := findNode(live, func(n *html.Node) bool { return n.Data == "h1" || n.Data == "h2" }); heading != nil {
			meta.Name = strings.TrimSpace(NodeText(heading))
		}
	}
	if meta.Description == "" {
		if header := findNode(live, func(n *html.Node) bool { return hasHTMLAttr(n, "data-automation-live-header") }); header != nil {
			if paragraph := findNode(header, func(n *html.Node) bool { return n.Data == "p" }); paragraph != nil {
				meta.Description = strings.TrimSpace(NodeText(paragraph))
			}
		}
	}
	if meta.LifecycleState == "" {
		if status := findNode(live, func(n *html.Node) bool { return hasHTMLAttr(n, "data-automation-live-status") }); status != nil {
			meta.LifecycleState = normalizeAutomationState(firstAutomationAttr(status, "data-state", "data-value", "data-status"))
			if meta.LifecycleState == "" {
				meta.LifecycleState = normalizeAutomationState(NodeText(status))
			}
		}
	}
	if meta.HealthState == "" {
		if health := findNode(live, func(n *html.Node) bool { return hasHTMLAttr(n, "data-automation-live-health") }); health != nil {
			meta.HealthState = normalizeAutomationState(firstAutomationAttr(health, "data-state", "data-value", "data-status"))
			if meta.HealthState == "" {
				meta.HealthState = normalizeAutomationState(NodeText(health))
			}
		}
	}
	return meta
}

func parseAutomationVersion(detail *AutomationDetail, live *html.Node) {
	version := &detail.Version
	versionRoot := findNode(live, func(n *html.Node) bool {
		return hasAnyHTMLAttr(n, "data-automation-version", "data-automation-version-id", "data-version-id", "data-automation-version-state", "data-version-state")
	})
	if versionRoot == nil {
		versionRoot = live
	}
	version.ID = strings.TrimSpace(firstAutomationAttrDeep(versionRoot, "data-automation-version-id", "data-version-id"))
	version.ProjectID = strings.TrimSpace(firstAutomationAttrDeep(versionRoot, "data-automation-version-project-id", "data-version-project-id"))
	version.AutomationID = strings.TrimSpace(firstAutomationAttrDeep(versionRoot, "data-automation-version-automation-id", "data-version-automation-id"))
	version.State = normalizeAutomationState(firstAutomationAttrDeep(versionRoot, "data-automation-version-state", "data-version-state"))
	version.Source = strings.TrimSpace(firstAutomationAttrDeep(versionRoot, "data-automation-version-source", "data-version-source"))
	version.AdapterKey = strings.TrimSpace(firstAutomationAttrDeep(versionRoot, "data-automation-adapter-key", "data-adapter-key"))
	version.CreatedAt = strings.TrimSpace(firstAutomationAttrDeep(versionRoot, "data-automation-version-created-at", "data-version-created-at"))
	version.PublishedAt = strings.TrimSpace(firstAutomationAttrDeep(versionRoot, "data-automation-version-published-at", "data-version-published-at"))
	version.Version = parseAutomationIntDeep(detail, versionRoot, "version", "data-automation-version-number", "data-version-number", "data-automation-version")
	version.SchemaVersion = parseAutomationIntDeep(detail, versionRoot, "schema version", "data-automation-schema-version", "data-schema-version")

	if version.State == "" {
		if published := firstAutomationAttr(versionRoot, "data-automation-published", "data-published"); parseAutomationBool(published) {
			version.State = "published"
		} else if draft := firstAutomationAttr(versionRoot, "data-automation-draft", "data-draft"); parseAutomationBool(draft) {
			version.State = "draft"
		}
	}
}

func parseAutomationGraph(detail *AutomationDetail, live *html.Node) {
	explicitAvailable, explicitFound, explicitValid := firstAutomationBoolValueAttrDeep(live,
		"data-automation-graph-available", "data-automation-live-graph-available", "data-has-live-graph")
	graphPanel := findNode(live, func(n *html.Node) bool {
		return hasAnyHTMLAttr(n, "data-automation-graph-panel", "data-automation-canvas", "data-automation-live-graph")
	})
	nodes, nodesPresent, nodeCountsPresent := parseAutomationLiveNodes(detail, live)
	edges, edgesPresent, edgeCountsPresent := parseAutomationLiveEdges(detail, live, nodes)
	detail.Nodes = nodes
	detail.Edges = edges
	detail.NodesAvailable = nodesPresent
	detail.EdgesAvailable = edgesPresent
	if graphPanel != nil {
		// A graph panel is a complete, possibly empty graph section. This lets
		// an empty graph say "empty" instead of looking like a parser failure.
		detail.NodesAvailable = true
		detail.EdgesAvailable = true
	}
	if explicitFound && explicitValid {
		detail.GraphAvailable = explicitAvailable
	} else {
		if explicitFound && !explicitValid {
			detail.Warnings = append(detail.Warnings, "graph availability is malformed")
		}
		detail.GraphAvailable = graphPanel != nil || nodesPresent || edgesPresent
	}
	if detail.Version.State == "draft" || detail.Automation.LifecycleState == "draft" {
		detail.GraphAvailable = false
	}
	detail.NodeCountsAvailable = nodeCountsPresent
	detail.EdgeCountsAvailable = edgeCountsPresent
	if nodeCountsPresent || edgeCountsPresent {
		detail.CountsAvailable = true
	}
}

func parseAutomationLiveNodes(detail *AutomationDetail, live *html.Node) ([]AutomationLiveNode, bool, bool) {
	var out = make([]AutomationLiveNode, 0)
	present := false
	countsPresent := false
	liveNodes := findAll(live, func(n *html.Node) bool {
		return hasAnyHTMLAttr(n, "data-automation-live-node", "data-automation-node")
	})
	if len(liveNodes) > 0 {
		present = true
	}
	for _, node := range liveNodes {
		parsed, hasCounts := parseAutomationLiveNode(detail, node)
		if hasCounts {
			countsPresent = true
		}
		if parsed.ID == "" && parsed.NodeKey == "" && parsed.Name == "" {
			// The marker itself is authoritative graph topology. Retain a record
			// even when malformed markup provides no recoverable identity.
			out = append(out, parsed)
			detail.Warnings = append(detail.Warnings, "graph node record has no stable identity")
			continue
		}
		out = append(out, parsed)
	}

	detailNodes := findAll(live, func(n *html.Node) bool {
		return hasAnyHTMLAttr(n, "data-automation-live-node-detail", "data-automation-node-detail")
	})
	if len(detailNodes) > 0 {
		present = true
	}
	graphNodesPresent := len(liveNodes) > 0
	for _, node := range detailNodes {
		parsed, hasCounts := parseAutomationNodeDetail(detail, node)
		if !graphNodesPresent {
			// Without graph node markers, detail records are the complete node
			// representation. Retain every distinct stable node key, including
			// identity-less marked records.
			if parsed.ID == "" && parsed.NodeKey == "" && parsed.Name == "" {
				detail.Warnings = append(detail.Warnings, "node detail record has no stable identity")
			}
			if hasCounts {
				countsPresent = true
			}
			mergeAutomationLiveNode(&out, parsed)
			continue
		}
		if automationLiveNodeHasStableMatch(out, parsed) {
			// A correlated detail record is authoritative for any metrics that
			// were absent from the graph representation.
			if hasCounts {
				countsPresent = true
			}
			mergeAutomationLiveNode(&out, parsed)
			continue
		}
		// The current live route uses node IDs for SVG graph shapes and
		// node keys for the details cards, so an unmatched pair cannot be
		// correlated safely. Keep the graph rows authoritative, retain the
		// detail record separately, and surface the lost correlation.
		detail.UnmatchedNodeDetails = append(detail.UnmatchedNodeDetails, parsed)
		detail.Warnings = append(detail.Warnings, "node detail records could not be correlated with graph nodes")
	}
	return out, present, countsPresent
}

func automationLiveNodeHasStableMatch(nodes []AutomationLiveNode, parsed AutomationLiveNode) bool {
	if parsed.ID == "" && parsed.NodeKey == "" {
		return false
	}
	for _, current := range nodes {
		if parsed.ID != "" && current.ID != "" && strings.EqualFold(current.ID, parsed.ID) {
			return true
		}
		if parsed.NodeKey != "" && current.NodeKey != "" && strings.EqualFold(current.NodeKey, parsed.NodeKey) {
			return true
		}
	}
	return false
}

func parseAutomationLiveNode(detail *AutomationDetail, node *html.Node) (AutomationLiveNode, bool) {
	parsed := AutomationLiveNode{}
	parsed.ID = strings.TrimSpace(firstAutomationAttr(node, "data-automation-live-node", "data-automation-node", "data-automation-node-id", "data-node-id"))
	parsed.NodeKey = strings.TrimSpace(firstAutomationAttr(node, "data-automation-node-key", "data-node-key"))
	parsed.Name = strings.TrimSpace(firstAutomationAttr(node, "data-automation-node-name", "data-node-name"))
	parsed.NodeType = strings.TrimSpace(firstAutomationAttr(node, "data-automation-node-type", "data-node-type"))
	parsed.Role = strings.TrimSpace(firstAutomationAttr(node, "data-automation-node-role", "data-node-role"))
	parsed.DisplayState = normalizeAutomationDisplayState(firstAutomationAttr(node,
		"data-automation-live-node-state", "data-automation-node-state", "data-display-state", "data-state", "data-status"))
	if parsed.Name == "" {
		if strong := findNode(node, func(n *html.Node) bool { return n.Data == "strong" }); strong != nil {
			parsed.Name = strings.TrimSpace(NodeText(strong))
		}
	}
	if parsed.DisplayState == "" {
		parsed.DisplayState = automationStateFromClasses(node)
	}
	if parsed.DisplayState == "" {
		if state := findNode(node, func(n *html.Node) bool { return strings.Contains(attr(n, "class"), "automation-node-state") }); state != nil {
			parsed.DisplayState = normalizeAutomationDisplayState(NodeText(state))
		}
	}
	if parsed.Name == "" {
		parsed.Name = firstLine(NodeText(node))
	}
	countsPresent := parseAutomationNodeCounts(detail, node, &parsed.Counts)
	return parsed, countsPresent
}

func parseAutomationNodeDetail(detail *AutomationDetail, section *html.Node) (AutomationLiveNode, bool) {
	parsed := AutomationLiveNode{}
	parsed.ID = strings.TrimSpace(firstAutomationAttr(section, "data-automation-live-node-id", "data-automation-node-id", "data-node-id"))
	parsed.NodeKey = strings.TrimSpace(firstAutomationAttr(section, "data-automation-live-node-detail", "data-automation-node-detail", "data-automation-node-key", "data-node-key"))
	parsed.Name = strings.TrimSpace(firstAutomationAttr(section, "data-automation-node-name", "data-node-name"))
	parsed.NodeType = strings.TrimSpace(firstAutomationAttr(section, "data-automation-node-type", "data-node-type"))
	parsed.Role = strings.TrimSpace(firstAutomationAttr(section, "data-automation-node-role", "data-node-role"))
	parsed.DisplayState = normalizeAutomationDisplayState(firstAutomationAttr(section, "data-automation-node-state", "data-display-state"))
	if heading := findNode(section, func(n *html.Node) bool { return n.Data == "h3" || n.Data == "h4" || n.Data == "strong" }); heading != nil {
		if parsed.Name == "" {
			parsed.Name = strings.TrimSpace(NodeText(heading))
		}
	}
	if meta := findNode(section, func(n *html.Node) bool { return n.Data == "p" }); meta != nil {
		parts := strings.Split(NodeText(meta), "·")
		if parsed.NodeKey == "" && len(parts) > 0 && strings.TrimSpace(parts[0]) != "" {
			parsed.NodeKey = strings.TrimSpace(parts[0])
		}
		if parsed.Role == "" && len(parts) > 1 {
			parsed.Role = strings.TrimSpace(parts[len(parts)-1])
		}
	}
	if parsed.NodeType == "" {
		if badge := findNode(section, func(n *html.Node) bool { return strings.Contains(attr(n, "class"), "badge") }); badge != nil {
			parsed.NodeType = strings.TrimSpace(NodeText(badge))
		}
	}
	countsPresent := parseAutomationNodeCounts(detail, section, &parsed.Counts)
	return parsed, countsPresent
}

func mergeAutomationLiveNode(nodes *[]AutomationLiveNode, parsed AutomationLiveNode) {
	if parsed.ID == "" && parsed.NodeKey == "" && parsed.Name == "" {
		// A marked detail record is still meaningful even when no stable field
		// can be recovered. Keep it as an explicitly unidentified row instead
		// of silently dropping its counts or configuration.
		*nodes = append(*nodes, parsed)
		return
	}
	match := -1
	for i := range *nodes {
		current := &(*nodes)[i]
		if parsed.ID != "" && current.ID != "" && strings.EqualFold(current.ID, parsed.ID) {
			match = i
			break
		}
		if parsed.NodeKey != "" && current.NodeKey != "" && strings.EqualFold(current.NodeKey, parsed.NodeKey) {
			match = i
			break
		}
	}
	if match < 0 {
		*nodes = append(*nodes, parsed)
		return
	}
	current := &(*nodes)[match]
	if current.ID == "" {
		current.ID = parsed.ID
	}
	if current.NodeKey == "" {
		current.NodeKey = parsed.NodeKey
	}
	if current.Name == "" {
		current.Name = parsed.Name
	}
	if current.NodeType == "" {
		current.NodeType = parsed.NodeType
	}
	if current.Role == "" {
		current.Role = parsed.Role
	}
	if current.DisplayState == "" {
		current.DisplayState = parsed.DisplayState
	}
	mergeAutomationNodeCounts(&current.Counts, parsed.Counts)
}

func mergeAutomationNodeCounts(dst *AutomationNodeCounts, src AutomationNodeCounts) {
	merge := func(dstValue *int, dstAvailable *bool, dstQuality *int, srcValue int, srcAvailable bool, srcQuality int) {
		if srcQuality < 0 {
			// A malformed structured metric is authoritative about its presence:
			// do not let a lower-quality text value recover it. A later valid
			// structured value may still repair the same metric.
			if *dstQuality < 2 {
				*dstValue = 0
				*dstAvailable = false
				*dstQuality = srcQuality
			}
			return
		}
		if !srcAvailable {
			return
		}
		if *dstQuality < 0 && srcQuality < 2 {
			return
		}
		if !*dstAvailable || srcQuality > *dstQuality {
			*dstValue = srcValue
			*dstAvailable = true
			*dstQuality = srcQuality
		}
	}
	merge(&dst.Running, &dst.RunningAvailable, &dst.runningQuality, src.Running, src.RunningAvailable, src.runningQuality)
	merge(&dst.Waiting, &dst.WaitingAvailable, &dst.waitingQuality, src.Waiting, src.WaitingAvailable, src.waitingQuality)
	merge(&dst.Blocked, &dst.BlockedAvailable, &dst.blockedQuality, src.Blocked, src.BlockedAvailable, src.blockedQuality)
	merge(&dst.Failed, &dst.FailedAvailable, &dst.failedQuality, src.Failed, src.FailedAvailable, src.failedQuality)
	merge(&dst.CompletedRecently, &dst.CompletedRecentlyAvailable, &dst.completedRecentlyQuality, src.CompletedRecently, src.CompletedRecentlyAvailable, src.completedRecentlyQuality)
}

func markAutomationNodeCountsInvalid(counts *AutomationNodeCounts) {
	counts.Running = 0
	counts.Waiting = 0
	counts.Blocked = 0
	counts.Failed = 0
	counts.CompletedRecently = 0
	counts.RunningAvailable = false
	counts.WaitingAvailable = false
	counts.BlockedAvailable = false
	counts.FailedAvailable = false
	counts.CompletedRecentlyAvailable = false
	counts.runningQuality = -1
	counts.waitingQuality = -1
	counts.blockedQuality = -1
	counts.failedQuality = -1
	counts.completedRecentlyQuality = -1
}

func markAutomationNodeCountMapInvalid(values map[string]any, counts *AutomationNodeCounts) {
	for _, field := range []struct {
		keys      []string
		target    *int
		available *bool
		quality   *int
	}{
		{keys: []string{"running"}, target: &counts.Running, available: &counts.RunningAvailable, quality: &counts.runningQuality},
		{keys: []string{"waiting"}, target: &counts.Waiting, available: &counts.WaitingAvailable, quality: &counts.waitingQuality},
		{keys: []string{"blocked"}, target: &counts.Blocked, available: &counts.BlockedAvailable, quality: &counts.blockedQuality},
		{keys: []string{"failed"}, target: &counts.Failed, available: &counts.FailedAvailable, quality: &counts.failedQuality},
		{keys: []string{"completed_recently", "completed", "recent"}, target: &counts.CompletedRecently, available: &counts.CompletedRecentlyAvailable, quality: &counts.completedRecentlyQuality},
	} {
		for _, key := range field.keys {
			if _, found := values[key]; !found {
				continue
			}
			*field.target = 0
			*field.available = false
			*field.quality = -1
			break
		}
	}
}

var automationCountFieldKeyRE = regexp.MustCompile(`(?i)"(running|waiting|blocked|failed|completed_recently|completed|recent)"\s*:`)

func markAutomationNodeCountFieldsInvalidFromRaw(raw string, counts *AutomationNodeCounts) bool {
	matches := automationCountFieldKeyRE.FindAllStringSubmatch(raw, -1)
	if len(matches) == 0 {
		return false
	}
	values := make(map[string]any, len(matches))
	for _, match := range matches {
		values[strings.ToLower(match[1])] = nil
	}
	markAutomationNodeCountMapInvalid(values, counts)
	return true
}

func parseAutomationNodeCounts(detail *AutomationDetail, node *html.Node, counts *AutomationNodeCounts) bool {
	present := false
	countNode := node
	if nested := findNode(node, func(n *html.Node) bool {
		return hasAnyHTMLAttr(n, "data-automation-node-counts", "data-node-counts", "data-counts")
	}); nested != nil {
		countNode = nested
	}
	if raw, found := firstAutomationAttrFound(countNode, "data-automation-node-counts", "data-node-counts", "data-counts"); found {
		var values map[string]any
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		decodeErr := decoder.Decode(&values)
		if decodeErr == nil {
			var trailing any
			if err := decoder.Decode(&trailing); err != io.EOF {
				decodeErr = fmt.Errorf("trailing JSON data")
			}
		}
		if decodeErr != nil || values == nil {
			detail.Warnings = append(detail.Warnings, "node counts are malformed")
			if !markAutomationNodeCountFieldsInvalidFromRaw(raw, counts) {
				if values == nil {
					markAutomationNodeCountsInvalid(counts)
				} else {
					// A malformed object can still identify which metrics it was
					// attempting to provide. Invalidate only those fields so valid
					// independent metrics may still come from visible text or
					// explicit attributes.
					markAutomationNodeCountMapInvalid(values, counts)
				}
			}
		} else {
			mapPresent, malformed := applyAutomationCountMap(values, counts)
			if malformed {
				detail.Warnings = append(detail.Warnings, "node counts are malformed")
			}
			if mapPresent {
				present = true
			}
		}
	}
	for _, field := range []struct {
		name      string
		target    *int
		available *bool
		quality   *int
		attrs     []string
	}{
		{name: "running", target: &counts.Running, available: &counts.RunningAvailable, quality: &counts.runningQuality, attrs: []string{"data-automation-node-count-running", "data-node-running", "data-running-count", "data-running"}},
		{name: "waiting", target: &counts.Waiting, available: &counts.WaitingAvailable, quality: &counts.waitingQuality, attrs: []string{"data-automation-node-count-waiting", "data-node-waiting", "data-waiting-count", "data-waiting"}},
		{name: "blocked", target: &counts.Blocked, available: &counts.BlockedAvailable, quality: &counts.blockedQuality, attrs: []string{"data-automation-node-count-blocked", "data-node-blocked", "data-blocked-count", "data-blocked"}},
		{name: "failed", target: &counts.Failed, available: &counts.FailedAvailable, quality: &counts.failedQuality, attrs: []string{"data-automation-node-count-failed", "data-node-failed", "data-failed-count", "data-failed"}},
		{name: "completed recently", target: &counts.CompletedRecently, available: &counts.CompletedRecentlyAvailable, quality: &counts.completedRecentlyQuality, attrs: []string{"data-automation-node-count-completed-recently", "data-node-completed-recently", "data-completed-recently-count", "data-completed-recently", "data-recent-count"}},
	} {
		if raw, found := firstAutomationAttrFoundDeep(node, field.attrs...); found {
			value, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil || value < 0 {
				detail.Warnings = append(detail.Warnings, "node "+field.name+" count is malformed")
				*field.target = 0
				*field.available = false
				*field.quality = -1
				continue
			}
			*field.target = value
			*field.available = true
			*field.quality = 2
			present = true
		}
	}
	if label := firstAutomationAttrDeep(node, "data-automation-node-count-label", "data-count-label"); label != "" {
		if mergeAutomationCountText(detail, counts, label) {
			present = true
		}
	}
	if small := findNode(node, func(n *html.Node) bool { return n.Data == "small" }); small != nil {
		if mergeAutomationCountText(detail, counts, NodeText(small)) {
			present = true
		}
	}
	return present
}

func mergeAutomationCountText(detail *AutomationDetail, counts *AutomationNodeCounts, text string) bool {
	var parsed AutomationNodeCounts
	present, malformed := applyAutomationCountText(text, &parsed)
	if malformed {
		detail.Warnings = append(detail.Warnings, "node counts are malformed")
	}
	if !present {
		return false
	}
	mergeAutomationNodeCounts(counts, parsed)
	return true
}

const (
	automationEdgeSourceGraph   = 1
	automationEdgeSourceDetails = 2
)

func parseAutomationLiveEdges(detail *AutomationDetail, live *html.Node, nodes []AutomationLiveNode) ([]AutomationLiveEdge, bool, bool) {
	var out = make([]AutomationLiveEdge, 0)
	present := false
	countsPresent := false
	explicit := findAll(live, func(n *html.Node) bool {
		return hasAnyHTMLAttr(n, "data-automation-live-edge", "data-automation-edge", "data-edge") || strings.Contains(attr(n, "class"), "automation-graph-edge")
	})
	if len(explicit) > 0 {
		present = true
	}
	parsedExplicit := make([]AutomationLiveEdge, 0, len(explicit))
	for _, edge := range explicit {
		parsed, hasCounts := parseAutomationLiveEdge(detail, edge)
		if hasCounts {
			countsPresent = true
		}
		parsedExplicit = append(parsedExplicit, parsed)
		if automationEdgeHasNoStableIdentity(parsed) {
			detail.Warnings = append(detail.Warnings, "edge record has no stable identity")
		}
		mergeAutomationLiveEdge(&out, parsed, false)
	}

	detailEdges := findAll(live, func(n *html.Node) bool {
		return hasAnyHTMLAttr(n, "data-automation-live-edge-detail", "data-automation-edge-detail")
	})
	if len(detailEdges) > 0 {
		present = true
	}
	parsedDetails := make([]AutomationLiveEdge, 0, len(detailEdges))
	for _, edge := range detailEdges {
		parsed, hasCounts := parseAutomationEdgeDetail(detail, edge)
		if hasCounts {
			countsPresent = true
		}
		parsedDetails = append(parsedDetails, parsed)
		if automationEdgeHasNoStableIdentity(parsed) {
			detail.Warnings = append(detail.Warnings, "edge record has no stable identity")
		}
	}
	for _, parsed := range parsedDetails {
		mergeAutomationLiveEdge(&out, parsed, automationEdgeHasUniqueEndpointMatch(parsed, parsedExplicit, parsedDetails))
	}
	if len(explicit) > 0 && len(detailEdges) > 0 {
		for _, edge := range out {
			if edge.edgeSource != automationEdgeSourceGraph|automationEdgeSourceDetails {
				detail.Warnings = append(detail.Warnings, "edge records could not be correlated safely")
				break
			}
		}
	}
	for i := range out {
		resolveAutomationEdgeNames(&out[i], nodes)
	}
	return out, present, countsPresent
}

func automationEdgeHasUniqueEndpointMatch(parsed AutomationLiveEdge, explicit, details []AutomationLiveEdge) bool {
	key := automationEdgeEndpointKey(parsed)
	if key == "" || countAutomationEdgeEndpoint(explicit, key) != 1 || countAutomationEdgeEndpoint(details, key) != 1 {
		return false
	}
	return true
}

func countAutomationEdgeEndpoint(edges []AutomationLiveEdge, key string) int {
	count := 0
	for _, edge := range edges {
		if automationEdgeEndpointKey(edge) == key {
			count++
		}
	}
	return count
}

func automationEdgeEndpointKey(edge AutomationLiveEdge) string {
	if edge.SourceNodeID != "" && edge.TargetNodeID != "" {
		return "id\x00" + strings.ToLower(edge.SourceNodeID) + "\x00" + strings.ToLower(edge.TargetNodeID)
	}
	if edge.SourceName != "" && edge.TargetName != "" {
		return "name\x00" + strings.ToLower(edge.SourceName) + "\x00" + strings.ToLower(edge.TargetName)
	}
	return ""
}

var automationEdgeCountsRE = regexp.MustCompile(`^(.*?),?\s*(\d+)\s+transitions?,\s*(\d+)\s+recent$`)
var automationFreshnessWordRE = regexp.MustCompile(`(?i)\b(fresh|stale)\b`)

func automationEdgeCountsMatch(aria string) []string {
	match := automationEdgeCountsRE.FindStringSubmatch(aria)
	if match == nil {
		return nil
	}
	indexes := automationEdgeCountsRE.FindStringSubmatchIndex(aria)
	if len(indexes) < 6 {
		return nil
	}
	numberStart := indexes[4]
	if numberStart > 0 {
		previous, _ := utf8.DecodeLastRuneInString(aria[:numberStart])
		if !unicode.IsSpace(previous) && previous != ',' {
			return nil
		}
		beforeNumber := strings.TrimRightFunc(aria[:numberStart], unicode.IsSpace)
		if beforeNumber != "" {
			last, _ := utf8.DecodeLastRuneInString(beforeNumber)
			if strings.ContainsRune("+-−.", last) {
				return nil
			}
		}
	}
	return match
}

func automationEdgeARIAHasCountWords(aria string) bool {
	lower := strings.ToLower(aria)
	return strings.Contains(lower, "transition") || strings.Contains(lower, " recent") || strings.HasSuffix(lower, "recent")
}

func parseAutomationLiveEdge(detail *AutomationDetail, edge *html.Node) (AutomationLiveEdge, bool) {
	parsed := AutomationLiveEdge{edgeSource: automationEdgeSourceGraph}
	parsed.ID = strings.TrimSpace(firstAutomationAttr(edge, "data-automation-live-edge-id", "data-automation-edge-id", "data-edge-id"))
	parsed.EdgeKey = strings.TrimSpace(firstAutomationAttr(edge, "data-automation-live-edge", "data-automation-edge", "data-edge", "data-edge-key"))
	parsed.SourceNodeID = strings.TrimSpace(firstAutomationAttr(edge, "data-source-node-id", "data-automation-source-node-id", "data-edge-from", "data-source-node", "data-source"))
	parsed.TargetNodeID = strings.TrimSpace(firstAutomationAttr(edge, "data-target-node-id", "data-automation-target-node-id", "data-edge-to", "data-target-node", "data-target"))
	parsed.SourceName = strings.TrimSpace(firstAutomationAttr(edge, "data-source-node-name", "data-edge-from-name"))
	parsed.TargetName = strings.TrimSpace(firstAutomationAttr(edge, "data-target-node-name", "data-edge-to-name"))
	parsed.Label = strings.TrimSpace(firstAutomationAttr(edge, "data-automation-edge-label", "data-edge-label", "data-label"))
	parsed.ConditionJSON = strings.TrimSpace(firstAutomationAttr(edge, "data-automation-edge-condition", "data-edge-condition"))
	parsed.Highlighted = parseAutomationBool(firstAutomationAttr(edge, "data-automation-edge-highlighted", "data-highlighted"))

	transition, transitionFound, transitionInvalid := parseAutomationEdgeCount(detail, edge, "edge transition", "data-automation-transition-count", "data-transition-count", "data-transitions")
	recent, recentFound, recentInvalid := parseAutomationEdgeCount(detail, edge, "edge recent transition", "data-automation-recent-transition-count", "data-recent-transition-count", "data-recent-transitions", "data-recent")
	if transitionFound {
		parsed.TransitionCount = transition
		parsed.transitionCountQuality = 2
	} else if transitionInvalid {
		parsed.transitionCountQuality = -1
	}
	if recentFound {
		parsed.RecentTransitionCount = recent
		parsed.recentTransitionCountQuality = 2
	} else if recentInvalid {
		parsed.recentTransitionCountQuality = -1
	}
	if aria := strings.TrimSpace(attr(edge, "aria-label")); aria != "" {
		if match := automationEdgeCountsMatch(aria); match != nil {
			if parsed.Label == "" {
				parsed.Label = strings.TrimSpace(strings.TrimSuffix(match[1], ","))
			}
			if !transitionFound && !transitionInvalid {
				value, err := strconv.Atoi(match[2])
				if err != nil || value < 0 {
					detail.Warnings = append(detail.Warnings, "edge transition count is malformed")
					transitionInvalid = true
					parsed.transitionCountQuality = -1
				} else {
					parsed.TransitionCount = value
					parsed.transitionCountQuality = 1
					transitionFound = true
				}
			}
			if !recentFound && !recentInvalid {
				value, err := strconv.Atoi(match[3])
				if err != nil || value < 0 {
					detail.Warnings = append(detail.Warnings, "edge recent transition count is malformed")
					recentInvalid = true
					parsed.recentTransitionCountQuality = -1
				} else {
					parsed.RecentTransitionCount = value
					parsed.recentTransitionCountQuality = 1
					recentFound = true
				}
			}
		} else if automationEdgeARIAHasCountWords(aria) {
			detail.Warnings = append(detail.Warnings, "edge transition counts are malformed")
			if !transitionFound && !transitionInvalid {
				transitionInvalid = true
				parsed.transitionCountQuality = -1
			}
			if !recentFound && !recentInvalid {
				recentInvalid = true
				parsed.recentTransitionCountQuality = -1
			}
		}
	}
	parsed.TransitionCountAvailable = transitionFound
	parsed.RecentTransitionCountAvailable = recentFound
	if parsed.SourceName == "" || parsed.TargetName == "" {
		from, to := splitAutomationEdgeEndpoints(parsed.Label)
		if parsed.SourceName == "" {
			parsed.SourceName = from
		}
		if parsed.TargetName == "" {
			parsed.TargetName = to
		}
	}
	return parsed, transitionFound || recentFound
}

func parseAutomationEdgeDetail(detail *AutomationDetail, section *html.Node) (AutomationLiveEdge, bool) {
	parsed := AutomationLiveEdge{edgeSource: automationEdgeSourceDetails}
	parsed.ID = strings.TrimSpace(firstAutomationAttr(section, "data-automation-live-edge-id", "data-automation-edge-id", "data-edge-id"))
	parsed.EdgeKey = strings.TrimSpace(firstAutomationAttr(section, "data-automation-live-edge-detail", "data-automation-edge-detail", "data-edge-key", "data-edge"))
	parsed.SourceNodeID = strings.TrimSpace(firstAutomationAttr(section, "data-source-node-id", "data-automation-source-node-id", "data-edge-from", "data-source-node", "data-source"))
	parsed.TargetNodeID = strings.TrimSpace(firstAutomationAttr(section, "data-target-node-id", "data-automation-target-node-id", "data-edge-to", "data-target-node", "data-target"))
	parsed.SourceName = strings.TrimSpace(firstAutomationAttr(section, "data-source-node-name", "data-edge-from-name"))
	parsed.TargetName = strings.TrimSpace(firstAutomationAttr(section, "data-target-node-name", "data-edge-to-name"))
	parsed.Label = strings.TrimSpace(firstAutomationAttr(section, "data-automation-edge-label", "data-edge-label", "data-label"))
	parsed.ConditionJSON = strings.TrimSpace(firstAutomationAttr(section, "data-automation-edge-condition", "data-edge-condition"))
	if parsed.SourceName == "" || parsed.TargetName == "" {
		if heading := findNode(section, func(n *html.Node) bool { return n.Data == "div" || n.Data == "span" }); heading != nil {
			from, to := splitAutomationEdgeEndpoints(firstLine(NodeText(heading)))
			if parsed.SourceName == "" {
				parsed.SourceName = from
			}
			if parsed.TargetName == "" {
				parsed.TargetName = to
			}
		}
	}
	paragraphs := findAll(section, func(n *html.Node) bool { return n.Data == "p" })
	if parsed.Label == "" && len(paragraphs) > 0 {
		parsed.Label = strings.TrimSpace(NodeText(paragraphs[0]))
	}
	if parsed.ConditionJSON == "" && len(paragraphs) > 1 {
		parsed.ConditionJSON = strings.TrimSpace(NodeText(paragraphs[1]))
	}
	transition, transitionFound, transitionInvalid := parseAutomationEdgeCount(detail, section, "edge transition", "data-automation-transition-count", "data-transition-count", "data-transitions")
	recent, recentFound, recentInvalid := parseAutomationEdgeCount(detail, section, "edge recent transition", "data-automation-recent-transition-count", "data-recent-transition-count", "data-recent-transitions", "data-recent")
	if transitionFound {
		parsed.TransitionCount = transition
		parsed.transitionCountQuality = 2
	} else if transitionInvalid {
		parsed.transitionCountQuality = -1
	}
	if recentFound {
		parsed.RecentTransitionCount = recent
		parsed.recentTransitionCountQuality = 2
	} else if recentInvalid {
		parsed.recentTransitionCountQuality = -1
	}
	parsed.TransitionCountAvailable = transitionFound
	parsed.RecentTransitionCountAvailable = recentFound
	return parsed, transitionFound || recentFound
}

func parseAutomationEdgeCount(detail *AutomationDetail, node *html.Node, label string, attrs ...string) (int, bool, bool) {
	value, found := firstAutomationAttrFoundDeep(node, attrs...)
	if !found {
		return 0, false, false
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 0 {
		detail.Warnings = append(detail.Warnings, label+" count is malformed")
		return 0, false, true
	}
	return parsed, true, false
}

func automationEdgeHasNoStableIdentity(edge AutomationLiveEdge) bool {
	if edge.ID != "" || edge.EdgeKey != "" {
		return false
	}
	if edge.SourceNodeID != "" && edge.TargetNodeID != "" {
		return false
	}
	if edge.SourceName != "" && edge.TargetName != "" {
		return false
	}
	return true
}

func mergeAutomationLiveEdge(edges *[]AutomationLiveEdge, parsed AutomationLiveEdge, allowEndpointMerge bool) {
	match := -1
	for i := range *edges {
		if automationEdgesCanMerge((*edges)[i], parsed, allowEndpointMerge) {
			match = i
			break
		}
	}
	if match < 0 {
		*edges = append(*edges, parsed)
		return
	}

	current := &(*edges)[match]
	if current.ID == "" {
		current.ID = parsed.ID
	}
	if current.EdgeKey == "" {
		current.EdgeKey = parsed.EdgeKey
	}
	if current.SourceNodeID == "" {
		current.SourceNodeID = parsed.SourceNodeID
	}
	if current.TargetNodeID == "" {
		current.TargetNodeID = parsed.TargetNodeID
	}
	if current.SourceName == "" {
		current.SourceName = parsed.SourceName
	}
	if current.TargetName == "" {
		current.TargetName = parsed.TargetName
	}
	if current.Label == "" {
		current.Label = parsed.Label
	}
	if current.ConditionJSON == "" {
		current.ConditionJSON = parsed.ConditionJSON
	}
	mergeCount := func(currentValue *int, currentAvailable *bool, currentQuality *int, parsedValue int, parsedAvailable bool, parsedQuality int) {
		if parsedQuality < 0 {
			// A malformed structured metric must not be repaired by the
			// lower-quality representation on the same edge.
			if *currentQuality < 2 {
				*currentValue = 0
				*currentAvailable = false
				*currentQuality = parsedQuality
			}
			return
		}
		if !parsedAvailable || (*currentQuality < 0 && parsedQuality < 2) {
			return
		}
		if !*currentAvailable || parsedQuality > *currentQuality {
			*currentValue = parsedValue
			*currentAvailable = true
			*currentQuality = parsedQuality
		}
	}
	mergeCount(&current.TransitionCount, &current.TransitionCountAvailable, &current.transitionCountQuality,
		parsed.TransitionCount, parsed.TransitionCountAvailable, parsed.transitionCountQuality)
	mergeCount(&current.RecentTransitionCount, &current.RecentTransitionCountAvailable, &current.recentTransitionCountQuality,
		parsed.RecentTransitionCount, parsed.RecentTransitionCountAvailable, parsed.recentTransitionCountQuality)
	current.Highlighted = current.Highlighted || parsed.Highlighted
	current.edgeSource |= parsed.edgeSource
}

func automationEdgesCanMerge(current, parsed AutomationLiveEdge, allowEndpointMerge bool) bool {
	if current.ID != "" && parsed.ID != "" {
		if !strings.EqualFold(current.ID, parsed.ID) {
			return false
		}
		if current.EdgeKey != "" && parsed.EdgeKey != "" && !strings.EqualFold(current.EdgeKey, parsed.EdgeKey) {
			return false
		}
		return true
	}
	if current.EdgeKey != "" && parsed.EdgeKey != "" {
		if !strings.EqualFold(current.EdgeKey, parsed.EdgeKey) {
			return false
		}
		if current.ID != "" && parsed.ID != "" && !strings.EqualFold(current.ID, parsed.ID) {
			return false
		}
		return true
	}

	if current.edgeSource != 0 && parsed.edgeSource != 0 && current.edgeSource&parsed.edgeSource != 0 {
		// Multiple records from the same rendered representation are separate
		// topology rows unless the stable identity branch above matched them.
		return false
	}
	if automationEdgeEndpointConflict(current, parsed) {
		return false
	}
	if current.Label != "" && parsed.Label != "" && !strings.EqualFold(current.Label, parsed.Label) {
		return false
	}
	if allowEndpointMerge && automationEdgeEndpointsEqual(current, parsed) {
		return true
	}
	// Without a shared stable identity or complete matching endpoints, keep
	// graph and detail records separate rather than guessing from traversal order.
	return false
}

func automationEdgeEndpointConflict(a, b AutomationLiveEdge) bool {
	for _, pair := range [][2]string{
		{a.SourceNodeID, b.SourceNodeID},
		{a.TargetNodeID, b.TargetNodeID},
		{a.SourceName, b.SourceName},
		{a.TargetName, b.TargetName},
	} {
		if pair[0] != "" && pair[1] != "" && !strings.EqualFold(pair[0], pair[1]) {
			return true
		}
	}
	return false
}

func automationEdgeEndpointsEqual(a, b AutomationLiveEdge) bool {
	if a.SourceNodeID != "" && a.TargetNodeID != "" && b.SourceNodeID != "" && b.TargetNodeID != "" {
		return strings.EqualFold(a.SourceNodeID, b.SourceNodeID) && strings.EqualFold(a.TargetNodeID, b.TargetNodeID)
	}
	if a.SourceName != "" && a.TargetName != "" && b.SourceName != "" && b.TargetName != "" {
		return strings.EqualFold(a.SourceName, b.SourceName) && strings.EqualFold(a.TargetName, b.TargetName)
	}
	return false
}

func resolveAutomationEdgeNames(edge *AutomationLiveEdge, nodes []AutomationLiveNode) {
	if edge.SourceName == "" && edge.SourceNodeID != "" {
		for _, node := range nodes {
			if node.ID == edge.SourceNodeID || node.NodeKey == edge.SourceNodeID {
				edge.SourceName = firstNonEmptyAutomation(node.Name, node.NodeKey, node.ID)
				break
			}
		}
	}
	if edge.TargetName == "" && edge.TargetNodeID != "" {
		for _, node := range nodes {
			if node.ID == edge.TargetNodeID || node.NodeKey == edge.TargetNodeID {
				edge.TargetName = firstNonEmptyAutomation(node.Name, node.NodeKey, node.ID)
				break
			}
		}
	}
}

func parseAutomationRuntimeCountWithInvalid(detail *AutomationDetail, node *html.Node, label string, attrs ...string) (int, bool, bool) {
	value, found := firstAutomationAttrFoundDeep(node, attrs...)
	if !found {
		return 0, false, false
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 0 {
		detail.Warnings = append(detail.Warnings, label+" count is malformed")
		return 0, false, true
	}
	return parsed, true, false
}

func automationCountLabelPresent(text string, labels ...string) bool {
	text = strings.ToLower(text)
	for _, label := range labels {
		if strings.Contains(text, strings.ToLower(strings.TrimSpace(label))) {
			return true
		}
	}
	return false
}

func parseAutomationRuntimeCounts(detail *AutomationDetail, live *html.Node) {
	metrics := findNode(live, func(n *html.Node) bool {
		return hasAnyHTMLAttr(n, "data-automation-live-metrics", "data-automation-runtime-counts", "data-automation-live-counts", "data-automation-activity-summary", "data-automation-runtime", "data-automation-runtime-summary", "data-automation-counts")
	})
	if metrics == nil {
		metrics = live
	}
	if value := firstAutomationAttrDeep(metrics, "data-automation-recent-cutoff", "data-recent-cutoff", "data-automation-live-recent-cutoff"); value != "" {
		detail.RecentCutoff = value
	}
	invocations, invFound, invInvalid := parseAutomationRuntimeCountWithInvalid(detail, metrics, "active invocations",
		"data-automation-active-invocations", "data-active-invocations", "data-automation-live-active-invocations", "data-invocations-active", "data-active-invocation-count", "data-active-invocations-count")
	work, workFound, workInvalid := parseAutomationRuntimeCountWithInvalid(detail, metrics, "active work items",
		"data-automation-active-work-items", "data-active-work-items", "data-automation-live-active-work-items", "data-work-items-active", "data-active-work", "data-active-work-count", "data-active-work-items-count")
	if invFound {
		detail.ActiveInvocations = invocations
	}
	if workFound {
		detail.ActiveWorkItems = work
	}
	text := strings.TrimSpace(NodeText(metrics))
	if !invFound && !invInvalid {
		if value, found := namedAutomationCount(text, "active invocations", "active invocation"); found {
			detail.ActiveInvocations, invFound = value, true
		} else if automationCountLabelPresent(text, "active invocations", "active invocation") {
			detail.Warnings = append(detail.Warnings, "active invocations count is malformed")
		}
	}
	if !workFound && !workInvalid {
		if value, found := namedAutomationCount(text, "active work items", "active work item", "open work items", "open work item"); found {
			detail.ActiveWorkItems, workFound = value, true
		} else if automationCountLabelPresent(text, "active work items", "active work item", "open work items", "open work item") {
			detail.Warnings = append(detail.Warnings, "active work items count is malformed")
		}
	}
	if invFound {
		detail.ActiveInvocationsAvailable = true
	}
	if workFound {
		detail.ActiveWorkItemsAvailable = true
	}
	detail.CountsAvailable = detail.CountsAvailable || invFound || workFound
	if marker := findNode(live, func(n *html.Node) bool {
		return hasAnyHTMLAttr(n, "data-automation-live-metrics", "data-automation-runtime-counts", "data-automation-live-counts", "data-automation-activity-summary", "data-automation-runtime", "data-automation-runtime-summary", "data-automation-counts")
	}); marker != nil && !detail.CountsAvailable {
		detail.Partial = true
	}
}

func parseAutomationResources(detail *AutomationDetail, live *html.Node) {
	section := findNode(live, func(n *html.Node) bool {
		return hasAnyHTMLAttr(n, "data-automation-resources", "data-automation-live-resources", "data-automation-resource-list", "data-automation-node-resources")
	})
	resources := findAll(live, func(n *html.Node) bool {
		return hasAnyHTMLAttr(n, "data-automation-resource", "data-automation-live-resource", "data-automation-resource-row", "data-automation-resource-item", "data-resource-row")
	})
	detail.Resources = parseAutomationResourceNodes(resources)
	detail.ResourcesAvailable = section != nil || len(resources) > 0
}

func parseAutomationResourceNodes(resourceNodes []*html.Node) []AutomationResourceSummary {
	out := make([]AutomationResourceSummary, 0, len(resourceNodes))
	seen := map[string]bool{}
	for _, node := range resourceNodes {
		resource := AutomationResourceSummary{
			NodeID:       strings.TrimSpace(firstAutomationAttr(node, "data-automation-resource-node-id", "data-node-id")),
			NodeKey:      strings.TrimSpace(firstAutomationAttr(node, "data-automation-resource-node-key", "data-node-key")),
			ResourceType: strings.TrimSpace(firstAutomationAttr(node, "data-automation-resource-type", "data-resource-type")),
			ResourceID:   strings.TrimSpace(firstAutomationAttr(node, "data-automation-resource-id", "data-resource-id")),
			Relation:     strings.TrimSpace(firstAutomationAttr(node, "data-automation-resource-relation", "data-relation")),
			Name:         strings.TrimSpace(firstAutomationAttr(node, "data-automation-resource-name", "data-resource-name")),
			Status:       strings.TrimSpace(firstAutomationAttr(node, "data-automation-resource-status", "data-resource-status")),
			URL:          strings.TrimSpace(firstAutomationAttr(node, "data-automation-resource-url", "data-resource-url")),
		}
		if resource.Name == "" {
			resource.Name = firstLine(NodeText(node))
		}
		key := strings.Join([]string{resource.NodeID, resource.NodeKey, resource.ResourceType, resource.ResourceID, resource.Relation}, "\x00")
		if resource.NodeID == "" && resource.NodeKey == "" && resource.ResourceType == "" && resource.ResourceID == "" && resource.Relation == "" {
			continue
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, resource)
	}
	return out
}

func parseAutomationExternalState(detail *AutomationDetail, live *html.Node) {
	section := findNode(live, func(n *html.Node) bool {
		return hasAnyHTMLAttr(n, "data-automation-external-state", "data-automation-live-external-state", "data-external-state", "data-automation-external")
	})
	sectionPresent := section != nil
	if section == nil {
		section = live
	}
	tracked, trackedFound, trackedInvalid := parseAutomationRuntimeCountWithInvalid(detail, section, "tracked resources",
		"data-automation-external-tracked-resources", "data-tracked-resources", "data-automation-tracked-resources", "data-tracked-resource-count")
	staleRaw, staleRawFound := firstAutomationAttrFoundDeep(section,
		"data-automation-external-stale", "data-external-stale", "data-stale")
	stale := false
	staleFound := false
	if staleRawFound {
		stale, staleFound = parseAutomationFreshness(staleRaw)
		if !staleFound {
			detail.Warnings = append(detail.Warnings, "external state freshness is malformed")
		}
	}
	lastUpdated := strings.TrimSpace(firstAutomationAttrDeep(section,
		"data-automation-external-last-updated", "data-external-last-updated", "data-last-updated-at", "data-external-updated-at"))
	statusRaw, statusRawFound := firstAutomationAttrFoundDeep(section, "data-automation-external-status", "data-external-status")
	status := ""
	statusInvalid := false
	if statusRawFound {
		var statusValid bool
		status, statusValid = parseAutomationExternalStatus(statusRaw)
		if !statusValid {
			statusInvalid = true
			detail.Warnings = append(detail.Warnings, "external state status is malformed")
		}
	}
	if section != live {
		text := NodeText(section)
		if !staleFound && !staleRawFound && !statusRawFound {
			if parsedStale, found, ambiguous := parseAutomationFreshnessText(text); ambiguous {
				detail.Warnings = append(detail.Warnings, "external state freshness is ambiguous")
			} else if found {
				stale, staleFound = parsedStale, true
			}
		}
		if lastUpdated == "" {
			lastUpdated = externalLastUpdatedText(NodeText(section))
		}
		if status == "" && !statusRawFound && !statusInvalid {
			switch {
			case staleFound && stale:
				status = "stale"
			case staleFound:
				status = "fresh"
			}
		}
		if !trackedFound && !trackedInvalid {
			if value, found := namedAutomationCount(NodeText(section), "tracked resources", "tracked resource"); found {
				tracked, trackedFound = value, true
			} else if automationCountLabelPresent(text, "tracked resources", "tracked resource") {
				detail.Warnings = append(detail.Warnings, "tracked resources count is malformed")
			}
		}
	}
	if !sectionPresent && !trackedFound && !staleFound && lastUpdated == "" && status == "" {
		detail.ExternalStateAvailable = false
		return
	}
	detail.ExternalState.TrackedResources = tracked
	detail.ExternalState.TrackedResourcesAvailable = trackedFound
	detail.ExternalState.Stale = stale
	detail.ExternalState.StaleAvailable = staleFound
	detail.ExternalState.LastUpdatedAt = lastUpdated
	detail.ExternalState.Status = status
	detail.ExternalState.StatusInvalid = statusInvalid
	detail.ExternalStateAvailable = true
}

func externalLastUpdatedText(text string) string {
	lower := strings.ToLower(text)
	const marker = "last persisted update"
	idx := strings.Index(lower, marker)
	if idx < 0 {
		return ""
	}
	return strings.TrimSpace(text[idx+len(marker):])
}

func firstAutomationAttr(n *html.Node, names ...string) string {
	value, _ := firstAutomationAttrFound(n, names...)
	return value
}

func firstAutomationAttrFound(n *html.Node, names ...string) (string, bool) {
	if n == nil {
		return "", false
	}
	for _, name := range names {
		if hasHTMLAttr(n, name) {
			return strings.TrimSpace(attr(n, name)), true
		}
	}
	return "", false
}

func hasAnyHTMLAttr(n *html.Node, names ...string) bool {
	for _, name := range names {
		if hasHTMLAttr(n, name) {
			return true
		}
	}
	return false
}

func firstAutomationAttrDeep(n *html.Node, names ...string) string {
	value, _ := firstAutomationAttrFoundDeep(n, names...)
	return value
}

func firstAutomationAttrFoundDeep(n *html.Node, names ...string) (string, bool) {
	if value, found := firstAutomationAttrFound(n, names...); found {
		return value, true
	}
	if nested := findNode(n, func(e *html.Node) bool { return hasAnyHTMLAttr(e, names...) }); nested != nil {
		return firstAutomationAttrFound(nested, names...)
	}
	return "", false
}

func firstAutomationBoolValueAttrDeep(n *html.Node, names ...string) (bool, bool, bool) {
	value, found := firstAutomationAttrFoundDeep(n, names...)
	if !found {
		return false, false, false
	}
	parsed, valid := parseAutomationBoolValue(value)
	return parsed, true, valid
}

func firstAutomationIntAttr(n *html.Node, names ...string) (int, bool) {
	value, found := firstAutomationAttrFound(n, names...)
	if !found {
		return 0, false
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 0 {
		return 0, false
	}
	return parsed, true
}

func firstAutomationIntAttrDeep(n *html.Node, names ...string) (int, bool) {
	value, found := firstAutomationAttrFoundDeep(n, names...)
	if !found {
		return 0, false
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 0 {
		return 0, false
	}
	return parsed, true
}

func parseAutomationInt(detail *AutomationDetail, n *html.Node, label string, names ...string) int {
	value, found := firstAutomationAttrFound(n, names...)
	if !found || value == "" {
		return 0
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 0 {
		detail.Warnings = append(detail.Warnings, label+" is malformed")
		return 0
	}
	return parsed
}

func parseAutomationIntDeep(detail *AutomationDetail, n *html.Node, label string, names ...string) int {
	value, found := firstAutomationAttrFoundDeep(n, names...)
	if !found || value == "" {
		return 0
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 0 {
		detail.Warnings = append(detail.Warnings, label+" is malformed")
		return 0
	}
	return parsed
}

func parseAutomationFreshness(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "stale", "true", "1", "yes", "on":
		return true, true
	case "fresh", "false", "0", "no", "off":
		return false, true
	default:
		return false, false
	}
}

func parseAutomationExternalStatus(value string) (string, bool) {
	switch normalizeAutomationState(value) {
	case "fresh", "stale":
		return normalizeAutomationState(value), true
	default:
		return "", false
	}
}

// parseAutomationFreshnessText returns (stale, found, ambiguous). It accepts
// only an exact freshness label or a clearly labelled positive value; arbitrary
// prose is not strong enough to establish the external-state polarity.
func parseAutomationFreshnessText(text string) (bool, bool, bool) {
	normalize := func(value string) string {
		return strings.ToLower(strings.Join(strings.Fields(value), " "))
	}
	recognized := ""
	ambiguous := false
	for _, rawLine := range strings.Split(text, "\n") {
		line := normalize(rawLine)
		if line == "" {
			continue
		}
		compact := strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, line)
		state := ""
		switch compact {
		case "fresh", "status:fresh", "status=fresh", "state:fresh", "state=fresh", "freshness:fresh", "freshness=fresh", "externalstate:fresh", "externalstate=fresh":
			state = "fresh"
		case "stale", "status:stale", "status=stale", "state:stale", "state=stale", "freshness:stale", "freshness=stale", "externalstate:stale", "externalstate=stale":
			state = "stale"
		}
		if state != "" {
			if recognized != "" && recognized != state {
				ambiguous = true
			} else {
				recognized = state
			}
			continue
		}
		if automationFreshnessWordRE.MatchString(line) || strings.Contains(line, "unknown") || strings.Contains(line, "maybe") {
			ambiguous = true
			continue
		}
		for _, prefix := range []string{"status:", "status=", "state:", "state=", "freshness:", "freshness=", "externalstate:", "externalstate="} {
			if strings.HasPrefix(compact, prefix) {
				ambiguous = true
				break
			}
		}
	}
	if ambiguous {
		return false, false, true
	}
	switch recognized {
	case "fresh":
		return false, true, false
	case "stale":
		return true, true, false
	default:
		return false, false, false
	}
}

func parseAutomationBoolValue(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "yes", "on":
		return true, true
	case "false", "0", "no", "off":
		return false, true
	default:
		return false, false
	}
}

func parseAutomationBool(value string) bool {
	parsed, _ := parseAutomationBoolValue(value)
	return parsed
}

func normalizeAutomationState(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "_")
	value = strings.Join(strings.Fields(value), "_")
	return value
}

func normalizeAutomationDisplayState(value string) string {
	value = normalizeAutomationState(value)
	switch value {
	case "waiting", "waiting_human", "human_waiting":
		return "waiting_human"
	case "recent", "completed", "completed_recently", "recently_completed":
		return "recently_completed"
	default:
		return value
	}
}

func automationStateFromClasses(n *html.Node) string {
	var state string
	for _, node := range findAll(n, func(e *html.Node) bool { return true }) {
		for _, token := range strings.Fields(attr(node, "class")) {
			switch {
			case strings.HasPrefix(token, "automation-node-state--"):
				state = strings.TrimPrefix(token, "automation-node-state--")
			case strings.HasPrefix(token, "automation-graph-node--"):
				state = strings.TrimPrefix(token, "automation-graph-node--")
			}
			if state != "" {
				return normalizeAutomationDisplayState(state)
			}
		}
	}
	return ""
}

func applyAutomationCountMap(values map[string]any, counts *AutomationNodeCounts) (bool, bool) {
	present := false
	malformed := false
	for _, field := range []struct {
		keys      []string
		target    *int
		available *bool
		quality   *int
	}{
		{keys: []string{"running"}, target: &counts.Running, available: &counts.RunningAvailable, quality: &counts.runningQuality},
		{keys: []string{"waiting"}, target: &counts.Waiting, available: &counts.WaitingAvailable, quality: &counts.waitingQuality},
		{keys: []string{"blocked"}, target: &counts.Blocked, available: &counts.BlockedAvailable, quality: &counts.blockedQuality},
		{keys: []string{"failed"}, target: &counts.Failed, available: &counts.FailedAvailable, quality: &counts.failedQuality},
		{keys: []string{"completed_recently", "completed", "recent"}, target: &counts.CompletedRecently, available: &counts.CompletedRecentlyAvailable, quality: &counts.completedRecentlyQuality},
	} {
		var value any
		found := false
		for _, key := range field.keys {
			if candidate, ok := values[key]; ok {
				value = candidate
				found = true
				break
			}
		}
		if !found {
			continue
		}
		parsed, ok := automationCountNumber(value)
		if !ok {
			malformed = true
			*field.target = 0
			*field.available = false
			*field.quality = -1
			continue
		}
		*field.target = parsed
		*field.available = true
		*field.quality = 2
		present = true
	}
	return present, malformed
}

func automationCountNumber(value any) (int, bool) {
	switch number := value.(type) {
	case json.Number:
		parsed, err := strconv.Atoi(string(number))
		return parsed, err == nil && parsed >= 0
	case float64:
		if number < 0 || number != float64(int(number)) {
			return 0, false
		}
		parsed := int(number)
		return parsed, parsed >= 0
	default:
		return 0, false
	}
}

func applyAutomationCountText(text string, counts *AutomationNodeCounts) (bool, bool) {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "no active work" {
		counts.Running, counts.Waiting, counts.Blocked, counts.Failed, counts.CompletedRecently = 0, 0, 0, 0, 0
		counts.RunningAvailable = true
		counts.WaitingAvailable = true
		counts.BlockedAvailable = true
		counts.FailedAvailable = true
		counts.CompletedRecentlyAvailable = true
		counts.runningQuality = 1
		counts.waitingQuality = 1
		counts.blockedQuality = 1
		counts.failedQuality = 1
		counts.completedRecentlyQuality = 1
		return true, false
	}
	present := false
	malformed := false
	for _, field := range []struct {
		labels    []string
		target    *int
		available *bool
		quality   *int
	}{
		{labels: []string{"running"}, target: &counts.Running, available: &counts.RunningAvailable, quality: &counts.runningQuality},
		{labels: []string{"waiting", "waiting human"}, target: &counts.Waiting, available: &counts.WaitingAvailable, quality: &counts.waitingQuality},
		{labels: []string{"blocked"}, target: &counts.Blocked, available: &counts.BlockedAvailable, quality: &counts.blockedQuality},
		{labels: []string{"failed"}, target: &counts.Failed, available: &counts.FailedAvailable, quality: &counts.failedQuality},
		{labels: []string{"completed recently", "recently completed", "recent"}, target: &counts.CompletedRecently, available: &counts.CompletedRecentlyAvailable, quality: &counts.completedRecentlyQuality},
	} {
		if value, found := namedAutomationCount(text, field.labels...); found {
			*field.target = value
			*field.available = true
			*field.quality = 1
			present = true
			continue
		}
		for _, label := range field.labels {
			if strings.Contains(text, label) {
				malformed = true
				break
			}
		}
	}
	return present, malformed
}

func automationCountBoundaryBefore(text string, start int) bool {
	if start <= 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(text[:start])
	return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
}

func automationCountBoundaryAfter(text string, end int) bool {
	if end >= len(text) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(text[end:])
	return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
}

var automationCountContinuationLabels = []string{
	"active invocations",
	"active invocation",
	"active work items",
	"active work item",
	"open work items",
	"open work item",
	"completed recently",
	"recently completed",
	"waiting human",
	"running",
	"waiting",
	"blocked",
	"failed",
	"recent",
}

func automationCountContinuationAfter(text string, end int) bool {
	for end < len(text) {
		r, size := utf8.DecodeRuneInString(text[end:])
		if !unicode.IsSpace(r) {
			break
		}
		end += size
	}
	if end >= len(text) {
		return true
	}
	r, size := utf8.DecodeRuneInString(text[end:])
	if r == ')' {
		end += size
		for end < len(text) {
			r, size = utf8.DecodeRuneInString(text[end:])
			if !unicode.IsSpace(r) {
				break
			}
			end += size
		}
		return end >= len(text)
	}
	if !strings.ContainsRune(",;·|", r) {
		return false
	}
	end += size
	for end < len(text) {
		r, size = utf8.DecodeRuneInString(text[end:])
		if !unicode.IsSpace(r) {
			break
		}
		end += size
	}
	if end >= len(text) {
		return false
	}
	segmentEnd, ok := automationCountSegmentEnd(text[end:])
	if !ok {
		return false
	}
	return automationCountContinuationAfter(text[end:], segmentEnd)
}

func automationCountSegmentEnd(source string) (int, bool) {
	text := strings.TrimLeftFunc(source, unicode.IsSpace)
	offset := len(source) - len(text)
	if text == "" {
		return 0, false
	}

	for _, label := range automationCountContinuationLabels {
		if !strings.HasPrefix(text, label) || !automationCountBoundaryAfter(text, len(label)) {
			continue
		}
		rawSuffix := text[len(label):]
		rest := strings.TrimLeftFunc(rawSuffix, isAutomationCountSeparator)
		if len(rawSuffix) == len(rest) || strings.HasPrefix(rest, "-") || strings.HasPrefix(rest, "−") {
			continue
		}
		numberEnd := 0
		for numberEnd < len(rest) && rest[numberEnd] >= '0' && rest[numberEnd] <= '9' {
			numberEnd++
		}
		if numberEnd == 0 || (numberEnd < len(rest) && rest[numberEnd] == '.') || !automationCountBoundaryAfter(rest, numberEnd) {
			continue
		}
		if _, err := strconv.Atoi(rest[:numberEnd]); err != nil {
			continue
		}
		return offset + len(text) - len(rest) + numberEnd, true
	}

	numberEnd := 0
	for numberEnd < len(text) && text[numberEnd] >= '0' && text[numberEnd] <= '9' {
		numberEnd++
	}
	if numberEnd == 0 || numberEnd >= len(text) {
		return 0, false
	}
	separator, _ := utf8.DecodeRuneInString(text[numberEnd:])
	if !unicode.IsSpace(separator) {
		return 0, false
	}
	if _, err := strconv.Atoi(text[:numberEnd]); err != nil {
		return 0, false
	}
	labelStart := numberEnd
	for labelStart < len(text) {
		r, size := utf8.DecodeRuneInString(text[labelStart:])
		if !unicode.IsSpace(r) {
			break
		}
		labelStart += size
	}
	for _, label := range automationCountContinuationLabels {
		labelEnd := labelStart + len(label)
		if strings.HasPrefix(text[labelStart:], label) && automationCountBoundaryAfter(text, labelEnd) {
			return offset + labelEnd, true
		}
	}
	return 0, false
}

func automationCountSequenceComplete(text string) bool {
	end, ok := automationCountSegmentEnd(text)
	return ok && automationCountContinuationAfter(text, end)
}

func automationCountLabelPrefixIsValid(prefix string) bool {
	if strings.TrimSpace(prefix) == "" {
		return true
	}
	withoutTrailingSeparators := strings.TrimRightFunc(prefix, isAutomationCountSeparator)
	if strings.TrimSpace(withoutTrailingSeparators) == "" {
		return false
	}
	return automationCountSequenceComplete(strings.TrimSpace(withoutTrailingSeparators))
}

func automationCountValueBoundaryAfter(text string, end int) bool {
	return automationCountContinuationAfter(text, end)
}

func isAutomationCountSeparator(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune(":=(),;·|", r)
}

func trimAutomationCountSeparators(value string) string {
	return strings.TrimFunc(value, isAutomationCountSeparator)
}

func automationCountPrefixIsValid(prefix string, numberStart int) bool {
	before := strings.TrimSpace(prefix[:numberStart])
	if before == "" {
		return true
	}
	last, _ := utf8.DecodeLastRuneInString(before)
	return strings.ContainsRune(":=,;·|(", last)
}

func namedAutomationCount(text string, labels ...string) (int, bool) {
	lower := strings.ToLower(text)
	for _, label := range labels {
		label = strings.ToLower(strings.TrimSpace(label))
		if label == "" {
			continue
		}
		searchFrom := 0
		for searchFrom < len(lower) {
			relative := strings.Index(lower[searchFrom:], label)
			if relative < 0 {
				break
			}
			idx := searchFrom + relative
			labelEnd := idx + len(label)
			if !automationCountBoundaryBefore(lower, idx) || !automationCountBoundaryAfter(lower, labelEnd) {
				searchFrom = labelEnd
				continue
			}

			// Accept a number before the label only when a separator appears
			// between it and the label. This rejects 2running and negative
			// values while preserving the compact backend form 2 running.
			rawPrefix := lower[:idx]
			prefix := trimAutomationCountSeparators(rawPrefix)
			separatorPresent := len(prefix) < len(rawPrefix)
			start := len(prefix)
			for start > 0 && prefix[start-1] >= '0' && prefix[start-1] <= '9' {
				start--
			}
			if separatorPresent && start < len(prefix) && automationCountBoundaryBefore(prefix, start) && automationCountPrefixIsValid(prefix, start) && automationCountContinuationAfter(lower, labelEnd) {
				negative := (start > 0 && prefix[start-1] == '-') || strings.HasSuffix(prefix[:start], "−")
				if !negative {
					if value, err := strconv.Atoi(prefix[start:]); err == nil {
						return value, true
					}
				}
			}

			// The label-before-number form also requires a separator and a
			// complete numeric token, so running2, running 2oops, and
			// decimal values remain unavailable.
			rawSuffix := lower[labelEnd:]
			rest := strings.TrimLeftFunc(rawSuffix, isAutomationCountSeparator)
			if len(rawSuffix) != len(rest) && automationCountLabelPrefixIsValid(rawPrefix) {
				if strings.HasPrefix(rest, "-") || strings.HasPrefix(rest, "−") {
					searchFrom = labelEnd
					continue
				}
				end := 0
				for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
					end++
				}
				if end > 0 && automationCountValueBoundaryAfter(rest, end) && (end == len(rest) || rest[end] != '.') {
					if value, err := strconv.Atoi(rest[:end]); err == nil {
						return value, true
					}
				}
			}
			searchFrom = labelEnd
		}
	}
	return 0, false
}

func splitAutomationEdgeEndpoints(text string) (string, string) {
	text = strings.TrimSpace(text)
	if idx := strings.Index(text, "→"); idx >= 0 {
		return strings.TrimSpace(text[:idx]), strings.TrimSpace(text[idx+len("→"):])
	}
	if idx := strings.Index(text, "->"); idx >= 0 {
		return strings.TrimSpace(text[:idx]), strings.TrimSpace(text[idx+2:])
	}
	return "", ""
}

func firstNonEmptyAutomation(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
