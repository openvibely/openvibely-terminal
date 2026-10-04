package client

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

func parseWorkerSettingsCapacity(root *html.Node) (*CapacitySnapshot, error) {
	globalRow := findByID(root, "global-row")
	projectBody := findByID(root, "project-stats-tbody")
	if globalRow == nil || projectBody == nil {
		return nil, ErrCapacitySnapshotUnsupported
	}

	globalCells := capacityHTMLCells(globalRow)
	if len(globalCells) < 4 {
		return nil, fmt.Errorf("worker capacity page has an incomplete global row")
	}
	maxWorkers, err := capacityInputValue(globalRow, "limit-input-global")
	if err != nil {
		return nil, fmt.Errorf("reading global worker limit: %w", err)
	}
	running, err := capacityLeadingInteger(globalCells[2])
	if err != nil {
		return nil, fmt.Errorf("reading global running workers: %w", err)
	}
	queueSize, err := capacityInteger(globalCells[3])
	if err != nil {
		return nil, fmt.Errorf("reading global worker queue: %w", err)
	}

	snapshot := &CapacitySnapshot{
		Global:            &GlobalCapacity{MaxWorkers: maxWorkers, TotalRunning: running, QueueSize: queueSize},
		Projects:          make([]ProjectCapacity, 0),
		Models:            make([]ModelCapacity, 0),
		ProjectsAvailable: attr(projectBody, "data-capacity-available") != "false",
		ModelsAvailable:   true,
	}
	if !snapshot.ProjectsAvailable {
		snapshot.Projects = nil
	} else {
		for _, row := range capacityHTMLRows(projectBody) {
			id := strings.TrimPrefix(attr(row, "id"), "project-row-")
			if id == attr(row, "id") || id == "" {
				continue
			}
			cells := capacityHTMLCells(row)
			if len(cells) < 4 {
				return nil, fmt.Errorf("worker capacity page has an incomplete project row")
			}
			projectRunning, err := capacityLeadingInteger(cells[2])
			if err != nil {
				return nil, fmt.Errorf("reading project worker count: %w", err)
			}
			projectQueue, err := capacityInteger(cells[3])
			if err != nil {
				return nil, fmt.Errorf("reading project worker queue: %w", err)
			}
			projectLimit, err := capacityInputValue(row, "")
			if err != nil {
				return nil, fmt.Errorf("reading project worker limit: %w", err)
			}
			snapshot.Projects = append(snapshot.Projects, ProjectCapacity{
				ID: id, Name: NodeText(cells[1]), Running: projectRunning,
				QueueSize: projectQueue, MaxWorkers: &projectLimit,
			})
		}
	}

	modelBody := findByID(root, "model-stats-tbody")
	if modelBody != nil {
		snapshot.ModelsAvailable = attr(modelBody, "data-capacity-available") != "false"
		if snapshot.ModelsAvailable {
			for _, row := range capacityHTMLRows(modelBody) {
				cells := capacityHTMLCells(row)
				if len(cells) < 3 {
					return nil, fmt.Errorf("worker capacity page has an incomplete model row")
				}
				name, model := capacityModelIdentity(cells[0])
				modelRunning, err := capacityLeadingInteger(cells[1])
				if err != nil {
					return nil, fmt.Errorf("reading model worker count: %w", err)
				}
				modelLimit, err := capacityInteger(cells[2])
				if err != nil {
					return nil, fmt.Errorf("reading model worker limit: %w", err)
				}
				snapshot.Models = append(snapshot.Models, ModelCapacity{
					Name: name, Model: model, Running: modelRunning, MaxWorkers: modelLimit,
				})
			}
		} else {
			snapshot.Models = nil
		}
	}
	return snapshot, nil
}

func capacityHTMLRows(body *html.Node) []*html.Node {
	rows := make([]*html.Node, 0)
	for node := body.FirstChild; node != nil; node = node.NextSibling {
		if node.Type == html.ElementNode && node.Data == "tr" {
			rows = append(rows, node)
		}
	}
	return rows
}

func capacityHTMLCells(row *html.Node) []*html.Node {
	cells := make([]*html.Node, 0)
	for node := row.FirstChild; node != nil; node = node.NextSibling {
		if node.Type == html.ElementNode && node.Data == "td" {
			cells = append(cells, node)
		}
	}
	return cells
}

func capacityInputValue(root *html.Node, id string) (int, error) {
	input := findNode(root, func(node *html.Node) bool {
		return node.Data == "input" && (id == "" || attr(node, "id") == id) && attr(node, "name") == "max_workers"
	})
	if input == nil {
		return 0, fmt.Errorf("worker limit input is missing")
	}
	value, err := strconv.Atoi(strings.TrimSpace(attr(input, "value")))
	if err != nil || value < 0 {
		return 0, fmt.Errorf("worker limit is invalid")
	}
	return value, nil
}

func capacityInteger(node *html.Node) (int, error) {
	value, err := strconv.Atoi(strings.TrimSpace(NodeText(node)))
	if err != nil || value < 0 {
		return 0, fmt.Errorf("capacity value is invalid")
	}
	return value, nil
}

func capacityLeadingInteger(node *html.Node) (int, error) {
	value := strings.TrimSpace(NodeText(node))
	if leading, _, found := strings.Cut(value, "/"); found {
		value = strings.TrimSpace(leading)
	}
	number, err := strconv.Atoi(value)
	if err != nil || number < 0 {
		return 0, fmt.Errorf("capacity count is invalid")
	}
	return number, nil
}

func capacityModelIdentity(cell *html.Node) (string, string) {
	var values []string
	for node := cell.FirstChild; node != nil; node = node.NextSibling {
		if node.Type != html.ElementNode || node.Data != "div" {
			continue
		}
		text := strings.TrimSpace(NodeText(node))
		if text != "" {
			values = append(values, text)
		}
	}
	if len(values) >= 2 {
		return values[0], values[1]
	}
	parts := strings.Fields(NodeText(cell))
	if len(parts) == 0 {
		return "", ""
	}
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], strings.Join(parts[1:], " ")
}

// GetCapacitySnapshot returns the current server-rendered worker capacity as a
// single request. It reports unsupported route/markup once so callers can use
// the legacy capacity APIs without probing the page on every refresh.
func (c *Client) GetCapacitySnapshot(ctx context.Context) (*CapacitySnapshot, error) {
	c.capacitySnapshotMu.RLock()
	unsupported := c.capacitySnapshotUnsupported
	c.capacitySnapshotMu.RUnlock()
	if unsupported {
		return nil, ErrCapacitySnapshotUnsupported
	}

	root, _, err := c.getHTMLDocument(ctx, "/workers", nil, "worker capacity")
	if err != nil {
		if IsNotFoundError(err) {
			c.rememberCapacitySnapshotUnsupported()
			return nil, ErrCapacitySnapshotUnsupported
		}
		return nil, err
	}

	snapshot, err := parseWorkerSettingsCapacity(root)
	if err != nil {
		c.rememberCapacitySnapshotUnsupported()
		if errors.Is(err, ErrCapacitySnapshotUnsupported) {
			return nil, ErrCapacitySnapshotUnsupported
		}
		return nil, fmt.Errorf("%w: Workers page markup is incompatible", ErrCapacitySnapshotUnsupported)
	}
	return snapshot, nil
}

func (c *Client) rememberCapacitySnapshotUnsupported() {
	c.capacitySnapshotMu.Lock()
	c.capacitySnapshotUnsupported = true
	c.capacitySnapshotMu.Unlock()
}
