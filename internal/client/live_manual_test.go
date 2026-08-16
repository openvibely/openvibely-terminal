package client

import (
	"context"
	"os"
	"testing"
)

// TestLiveBackend exercises the scrapers against a running OpenVibely server.
// Skipped unless OPENVIBELY_LIVE is set, so normal test runs stay hermetic.
func TestLiveBackend(t *testing.T) {
	base := os.Getenv("OPENVIBELY_LIVE")
	if base == "" {
		t.Skip("set OPENVIBELY_LIVE=http://localhost:3001 to run")
	}
	c, err := New(base)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	projects, err := c.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var pid string
	for _, p := range projects {
		if p.Path != "" {
			pid = p.ID
			break
		}
	}
	t.Logf("project %s", pid)

	tasks, err := c.ListTasks(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("scraped %d tasks", len(tasks))
	for i, task := range tasks {
		if task.Title == "" {
			t.Errorf("task %s has an empty title", task.ID)
		}
		switch task.Title {
		case "Run", "Cancel", "Edit", "X", "Tasks":
			t.Errorf("task %s title is chrome text %q", task.ID, task.Title)
		}
		if i < 5 {
			t.Logf("  %s %-9s %-9s %q %v", shortIDForLog(task.ID), task.Category, task.Status, task.Title, task.Badges)
		}
	}

	if len(tasks) > 0 {
		d, err := c.GetTask(ctx, tasks[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		if d.Task.Title == "" {
			t.Error("task detail has no title")
		}
		t.Logf("detail %q status=%q", d.Task.Title, d.Task.Status)
		for _, tab := range []string{"details", "thread", "changes", "schedules", "chaining", "attachments", "lifecycle"} {
			t.Logf("  tab %-12s %d chars", tab, len(d.TabText(tab)))
		}
	}

	alerts, err := c.ListAlerts(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("scraped %d alerts", len(alerts))
	for i, a := range alerts {
		if a.Title == "" {
			t.Errorf("alert %s has an empty title", a.ID)
		}
		if i < 3 {
			t.Logf("  %q read=%v badges=%v", a.Title, a.Read, a.Badges)
		}
	}
}

func shortIDForLog(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
