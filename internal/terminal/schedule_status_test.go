package terminal

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/openvibely/openvibely-terminal/internal/client"
)

const scheduleStatusHTML = `<div id="schedule-content">
<div data-task-id="t-on" data-schedule-id="s-on" data-schedule-enabled="true"><div class="font-semibold">Enabled report</div></div>
<div data-task-id="t-off" data-schedule-id="s-off" data-schedule-enabled="false"><div class="font-semibold">Paused report</div><span>Paused</span></div>
<div data-task-id="t-unk" data-schedule-id="s-unk"><div class="font-semibold">Mystery report</div></div>
</div>`

func scheduleStatusTaskHTML(id string) string {
	return `<div data-task-id="` + id + `" data-project-id="p1"><h2 class="font-bold">Report</h2><div data-task-status="pending"></div></div>`
}

func scheduleStatusRow(t *testing.T, out, scheduleID string) string {
	t.Helper()
	for _, line := range strings.Split(stripANSI(out), "\n") {
		if strings.Contains(line, scheduleID) {
			return line
		}
	}
	t.Fatalf("no row for %s in:\n%s", scheduleID, out)
	return ""
}

func assertRowStatus(t *testing.T, row, want string) {
	t.Helper()
	if !regexp.MustCompile(`\b` + want + `\b`).MatchString(row) {
		t.Fatalf("row %q missing status %q", row, want)
	}
}

func TestCLIScheduleListShowsEnabledPausedUnknownStatus(t *testing.T) {
	for _, args := range [][]string{{"schedule"}, {"schedule", "list"}, {"schedule", "list", "--all"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			c, _ := cliServer(t, map[string]string{"/api/projects": cliProjects, "/schedule": scheduleStatusHTML})
			var out bytes.Buffer
			if err := RunCLI(c, &out, "demo", args, false, false); err != nil {
				t.Fatal(err)
			}
			plain := stripANSI(out.String())
			if !strings.Contains(plain, "STATUS") {
				t.Fatalf("list missing STATUS header:\n%s", plain)
			}
			assertRowStatus(t, scheduleStatusRow(t, plain, "s-on"), "enabled")
			assertRowStatus(t, scheduleStatusRow(t, plain, "s-off"), "paused")
			assertRowStatus(t, scheduleStatusRow(t, plain, "s-unk"), "unknown")
			if strings.Contains(scheduleStatusRow(t, plain, "s-unk"), "enabled") {
				t.Fatalf("unknown schedule rendered as enabled:\n%s", plain)
			}
			on, off, unk := strings.Index(plain, "s-on"), strings.Index(plain, "s-off"), strings.Index(plain, "s-unk")
			if on > off || off > unk {
				t.Fatalf("list ordering changed:\n%s", plain)
			}
		})
	}
}

func TestCLIScheduleJSONExposesStableStatus(t *testing.T) {
	c, _ := cliServer(t, map[string]string{"/api/projects": cliProjects, "/schedule": scheduleStatusHTML})
	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"schedule"}, false, true); err != nil {
		t.Fatal(err)
	}
	var raw []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &raw); err != nil {
		t.Fatalf("invalid JSON %q: %v", out.String(), err)
	}
	want := map[string]struct {
		status  string
		enabled any
	}{
		"s-on":  {"enabled", true},
		"s-off": {"paused", false},
		"s-unk": {"unknown", nil},
	}
	if len(raw) != len(want) {
		t.Fatalf("JSON entries = %v", raw)
	}
	for _, entry := range raw {
		id, _ := entry["schedule_id"].(string)
		w, ok := want[id]
		if !ok {
			t.Fatalf("unexpected entry %v", entry)
		}
		if entry["status"] != w.status {
			t.Errorf("%s status = %v, want %s", id, entry["status"], w.status)
		}
		if enabled, present := entry["enabled"]; !present || enabled != w.enabled {
			t.Errorf("%s enabled = %v (present=%v), want %v", id, enabled, present, w.enabled)
		}
	}
}

func TestCLIScheduleShowMatchesListStatus(t *testing.T) {
	bodies := func() map[string]string {
		return map[string]string{"/api/projects": cliProjects, "/schedule": scheduleStatusHTML, "/tasks/t-on": scheduleStatusTaskHTML("t-on"), "/tasks/t-off": scheduleStatusTaskHTML("t-off"), "/tasks/t-unk": scheduleStatusTaskHTML("t-unk")}
	}
	cases := map[string]string{"s-on": "enabled", "s-off": "paused", "s-unk": "unknown"}
	for _, action := range []string{"show", "open"} {
		for id, status := range cases {
			t.Run(action+" "+id, func(t *testing.T) {
				c, _ := cliServer(t, bodies())
				var out bytes.Buffer
				if err := RunCLI(c, &out, "demo", []string{"schedule", action, id}, false, false); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(stripANSI(out.String()), "Status: "+status) {
					t.Fatalf("%s %s missing Status: %s:\n%s", action, id, status, out.String())
				}

				c, _ = cliServer(t, bodies())
				out.Reset()
				if err := RunCLI(c, &out, "demo", []string{"schedule", action, id}, false, true); err != nil {
					t.Fatal(err)
				}
				var inspection struct {
					Schedule map[string]any `json:"schedule"`
				}
				if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &inspection); err != nil {
					t.Fatalf("invalid JSON %q: %v", out.String(), err)
				}
				if inspection.Schedule["status"] != status {
					t.Fatalf("inspection JSON status = %v, want %s", inspection.Schedule["status"], status)
				}
			})
		}
	}
}

func TestCLIScheduleToggleRefreshShowsUpdatedStatus(t *testing.T) {
	var mu sync.Mutex
	enabled := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, cliProjects)
		case r.URL.Path == "/api/tasks/reference-catalog":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"tasks":[]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/schedule":
			state := "true"
			if !enabled {
				state = "false"
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<div id="schedule-content"><div data-task-id="t-1" data-schedule-id="s-1" data-schedule-enabled="`+state+`"><div class="font-semibold">Nightly report</div></div></div>`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/schedules/s-1/toggle":
			enabled = !enabled
			w.Header().Set("Content-Type", "application/json")
			body, _ := json.Marshal(map[string]any{"id": "s-1", "task_id": "t-1", "enabled": enabled, "repeat_type": "daily", "run_at": "2026-01-20T09:00:00Z"})
			_, _ = w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	steps := []struct {
		json bool
		want string
	}{{false, "paused"}, {false, "enabled"}, {true, "paused"}, {true, "enabled"}}
	for _, step := range steps {
		var out bytes.Buffer
		if err := RunCLI(c, &out, "demo", []string{"schedule", "toggle", "s-1"}, false, step.json); err != nil {
			t.Fatal(err)
		}
		if !step.json {
			assertRowStatus(t, scheduleStatusRow(t, out.String(), "s-1"), step.want)
			continue
		}
		var got struct {
			Schedules []map[string]any `json:"schedules"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &got); err != nil {
			t.Fatalf("invalid JSON %q: %v", out.String(), err)
		}
		if len(got.Schedules) != 1 || got.Schedules[0]["status"] != step.want {
			t.Fatalf("toggle JSON schedules = %v, want status %s", got.Schedules, step.want)
		}
	}
}
