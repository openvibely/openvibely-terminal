package tui

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

func scheduleDeletePage(entries ...string) string {
	return `<div id="schedule-content">` + strings.Join(entries, "\n") + `</div>`
}

func scheduleDeleteEntry(id, name string) string {
	return `<div data-task-id="task-1" data-schedule-id="` + id + `"><span class="font-semibold">` + name + `</span></div>`
}

func scheduleDeleteError(m Model) string {
	for i := len(m.log) - 1; i >= 0; i-- {
		if m.log[i].role == "error" {
			return m.log[i].text
		}
	}
	return ""
}

func assertScheduleDeleteTerminalSafe(t *testing.T, value string) {
	t.Helper()
	for _, control := range []string{"\x1b", "\a", "\r", "\n", "\t"} {
		if strings.Contains(value, control) {
			t.Fatalf("terminal output retained control %q: %q", control, value)
		}
	}
}

func TestScheduleDeleteInteractiveTypedReferenceResolution(t *testing.T) {
	catalog := scheduleDeletePage(scheduleDeleteEntry("sched-42", "Nightly backup"))
	for _, tc := range []struct {
		name string
		ref  string
	}{
		{name: "exact ID", ref: "sched-42"},
		{name: "exact title", ref: "Nightly backup"},
		{name: "unique partial title", ref: "backup"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{"/schedule": catalog})
			m = runLine(t, m, "/schedule delete "+tc.ref)

			if m.pendingConfirmation == nil {
				t.Fatalf("typed delete did not resolve before confirmation:\n%s", transcript(m))
			}
			const wantPrompt = `Delete schedule "sched-42"? Type 'yes' to confirm or Esc to cancel.`
			if got := m.pendingConfirmation.message; got != wantPrompt {
				t.Fatalf("confirmation = %q, want %q", got, wantPrompt)
			}
			if got := rec.count(http.MethodGet, "/schedule"); got != 1 {
				t.Fatalf("typed target resolution GETs = %d, want 1:\n%s", got, rec.all())
			}
			if rec.saw(http.MethodDelete, "/schedules/sched-42") {
				t.Fatal("delete ran before TUI confirmation")
			}

			m = runLine(t, m, "yes")
			if !rec.sawQuery("DELETE /schedules/sched-42?project_id=p1") {
				t.Fatalf("confirmed delete did not use the captured canonical ID and project scope:\n%s", strings.Join(rec.urls, "\n"))
			}
			if got := rec.count(http.MethodGet, "/schedule"); got != 2 {
				t.Fatalf("successful delete schedule GETs = %d, want lookup plus refresh:\n%s", got, rec.all())
			}
		})
	}
}

func TestScheduleDeleteInteractiveFailureBeforeConfirmation(t *testing.T) {
	cases := []struct {
		name    string
		catalog string
		ref     string
		want    string
	}{
		{
			name:    "unknown",
			catalog: scheduleDeletePage(scheduleDeleteEntry("sched-42", "Nightly backup")),
			ref:     "missing",
			want:    "nothing matches",
		},
		{
			name: "ambiguous",
			catalog: scheduleDeletePage(
				scheduleDeleteEntry("sched-42", "Nightly backup"),
				scheduleDeleteEntry("sched-43", "Nightly cleanup")),
			ref:  "Nightly",
			want: "ambiguous",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{"/schedule": tc.catalog})
			m = runLine(t, m, "/schedule delete "+tc.ref)
			if m.pendingConfirmation != nil {
				t.Fatalf("%s typed reference opened a confirmation", tc.name)
			}
			if err := scheduleDeleteError(m); !strings.Contains(strings.ToLower(err), tc.want) {
				t.Fatalf("%s error = %q, want %q", tc.name, err, tc.want)
			}
			if rec.saw(http.MethodDelete, "/schedules/sched-42") || rec.saw(http.MethodDelete, "/schedules/sched-43") {
				t.Fatalf("%s typed reference mutated a schedule:\n%s", tc.name, rec.all())
			}
		})
	}
}

func TestScheduleDeleteInteractiveCancellationIsScopedAndMutationFree(t *testing.T) {
	const projectID = "project B&mode=terminal"
	catalog := scheduleDeletePage(scheduleDeleteEntry("sched-42", "Nightly backup"))
	var scheduleGets, deletes int
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/schedule":
			scheduleGets++
			if got := r.URL.Query().Get("project_id"); got != projectID {
				t.Errorf("lookup project_id = %q, want %q", got, projectID)
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(catalog))
		case r.Method == http.MethodDelete && r.URL.Path == "/schedules/sched-42":
			deletes++
			if got := r.URL.Query().Get("project_id"); got != projectID {
				t.Errorf("delete project_id = %q, want %q", got, projectID)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
		}
	})
	m.selectedID = projectID
	m = runLine(t, m, "/schedule delete backup")
	if m.pendingConfirmation == nil {
		t.Fatalf("delete did not enter confirmation:\n%s", transcript(m))
	}
	m = runLine(t, m, "esc")
	if m.pendingConfirmation != nil {
		t.Fatal("Esc did not clear schedule deletion confirmation")
	}
	if deletes != 0 || scheduleGets != 1 {
		t.Fatalf("cancellation requests: DELETE=%d GET /schedule=%d, want 0 and 1", deletes, scheduleGets)
	}
	if m.selectedID != projectID {
		t.Fatalf("cancellation changed selected project to %q, want %q", m.selectedID, projectID)
	}
}

func TestScheduleDeleteInteractiveConfirmationCapturesPreConfirmationTarget(t *testing.T) {
	const projectID = "project B&mode=terminal"
	before := scheduleDeletePage(scheduleDeleteEntry("sched-before", "Nightly backup"))
	after := scheduleDeletePage(scheduleDeleteEntry("sched-after", "Nightly backup"))
	var scheduleGets, deletes int
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/schedule":
			scheduleGets++
			if got := r.URL.Query().Get("project_id"); got != projectID {
				t.Errorf("schedule GET project_id = %q, want %q", got, projectID)
			}
			w.Header().Set("Content-Type", "text/html")
			if scheduleGets == 1 {
				_, _ = w.Write([]byte(before))
				return
			}
			_, _ = w.Write([]byte(after))
		case r.Method == http.MethodDelete:
			deletes++
			if got, want := r.URL.Path, "/schedules/sched-before"; got != want {
				t.Fatalf("delete path = %q, want captured target %q", got, want)
			}
			if got := r.URL.Query().Get("project_id"); got != projectID {
				t.Errorf("delete project_id = %q, want %q", got, projectID)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
		}
	})
	m.selectedID = projectID
	m = runLine(t, m, "/schedule delete Nightly backup")
	if m.pendingConfirmation == nil {
		t.Fatalf("delete did not resolve target before confirmation:\n%s", transcript(m))
	}
	if got, want := m.pendingConfirmation.message, `Delete schedule "sched-before"? Type 'yes' to confirm or Esc to cancel.`; got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
	m = runLine(t, m, "yes")
	if deletes != 1 || scheduleGets != 2 {
		t.Fatalf("requests after confirmation: DELETE=%d GET /schedule=%d, want 1 and 2", deletes, scheduleGets)
	}
	if m.selectedID != projectID {
		t.Fatalf("confirmed delete changed selected project to %q, want %q", m.selectedID, projectID)
	}
}

func TestScheduleDeleteInteractiveTerminalSafety(t *testing.T) {
	t.Run("prompt uses safe canonical ID", func(t *testing.T) {
		catalog := scheduleDeletePage(scheduleDeleteEntry("sched-\x1b[31mred\r\n\a", "Nightly backup"))
		m, rec := dispatchModel(t, map[string]string{"/schedule": catalog})
		m = runLine(t, m, "/schedule delete backup")
		if m.pendingConfirmation == nil {
			t.Fatalf("delete did not reach confirmation:\n%s", transcript(m))
		}
		assertScheduleDeleteTerminalSafe(t, m.pendingConfirmation.message)
		if !strings.Contains(m.pendingConfirmation.message, `sched-red`) {
			t.Fatalf("prompt lost canonical identity after sanitization: %q", m.pendingConfirmation.message)
		}
		if rec.saw(http.MethodDelete, "/schedules/sched-") {
			t.Fatal("delete ran before terminal-safe confirmation")
		}
	})

	t.Run("errors sanitize typed references and schedule data", func(t *testing.T) {
		catalog := scheduleDeletePage(
			scheduleDeleteEntry("sched-42", "Daily \x1b[31mred\r\n\a"),
			scheduleDeleteEntry("sched-43", "Daily \x1b]8;;https://example.invalid\x07blue\r\n\a"))
		m, rec := dispatchModel(t, map[string]string{"/schedule": catalog})
		m = runLine(t, m, "/schedule delete Daily")
		if m.pendingConfirmation != nil {
			t.Fatal("ambiguous schedule data opened a confirmation")
		}
		if err := scheduleDeleteError(m); !strings.Contains(err, "ambiguous") {
			t.Fatalf("ambiguous error = %q", err)
		} else {
			assertScheduleDeleteTerminalSafe(t, err)
		}
		m = runLine(t, m, "/schedule delete missing\x1b[31m\a")
		if err := scheduleDeleteError(m); !strings.Contains(err, "nothing matches") {
			t.Fatalf("unknown error = %q", err)
		} else {
			assertScheduleDeleteTerminalSafe(t, err)
		}
		if rec.saw(http.MethodDelete, "/schedules/sched-42") || rec.saw(http.MethodDelete, "/schedules/sched-43") {
			t.Fatalf("terminal-unsafe failure mutated a schedule:\n%s", rec.all())
		}
	})
}

func TestCLIScheduleDeleteResolvesBeforeForceGate(t *testing.T) {
	catalog := scheduleDeletePage(scheduleDeleteEntry("sched-42", "Nightly backup"))
	t.Run("exact ID title and partial resolve with force", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			ref  string
		}{
			{name: "exact ID", ref: "sched-42"},
			{name: "exact title", ref: "Nightly backup"},
			{name: "unique partial title", ref: "backup"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				c, rec := cliServer(t, map[string]string{
					"/api/projects": cliProjects,
					"/schedule":     catalog,
				})
				var out bytes.Buffer
				if err := RunCLI(c, &out, "demo", []string{"schedule", "delete", tc.ref}, true, false); err != nil {
					t.Fatalf("forced delete: %v", err)
				}
				if !rec.sawQuery("DELETE /schedules/sched-42?project_id=p1") {
					t.Fatalf("forced delete did not use canonical ID and selected project:\n%s", strings.Join(rec.urls, "\n"))
				}
				if !strings.Contains(out.String(), "deleted schedule") {
					t.Fatalf("forced delete output missing success:\n%s", out.String())
				}
			})
		}
	})

	t.Run("unforced partial reports canonical identity", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{
			"/api/projects": cliProjects,
			"/schedule":     catalog,
		})
		err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"schedule", "delete", "backup"}, false, false)
		if err == nil || !strings.Contains(err.Error(), `use --force to confirm deletion of schedule "sched-42"`) {
			t.Fatalf("unforced partial error = %v", err)
		}
		if strings.Contains(err.Error(), `"backup"`) || rec.saw(http.MethodDelete, "/schedules/sched-42") {
			t.Fatalf("unforced partial used its raw reference or mutated:\n%s\n%v", rec.all(), err)
		}
	})

	for _, tc := range []struct {
		name    string
		catalog string
		ref     string
		want    string
	}{
		{
			name:    "unknown",
			catalog: catalog,
			ref:     "missing",
			want:    "nothing matches",
		},
		{
			name: "ambiguous",
			catalog: scheduleDeletePage(
				scheduleDeleteEntry("sched-42", "Nightly backup"),
				scheduleDeleteEntry("sched-43", "Nightly cleanup")),
			ref:  "Nightly",
			want: "ambiguous",
		},
	} {
		t.Run(tc.name+" force does not bypass matching", func(t *testing.T) {
			c, rec := cliServer(t, map[string]string{
				"/api/projects": cliProjects,
				"/schedule":     tc.catalog,
			})
			err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"schedule", "delete", tc.ref}, true, false)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.want) {
				t.Fatalf("forced %s error = %v, want %q", tc.name, err, tc.want)
			}
			if strings.Contains(err.Error(), "use --force") {
				t.Fatalf("forced %s was rejected by confirmation instead of matching: %v", tc.name, err)
			}
			if rec.saw(http.MethodDelete, "/schedules/sched-42") || rec.saw(http.MethodDelete, "/schedules/sched-43") {
				t.Fatalf("forced %s mutated a schedule:\n%s", tc.name, rec.all())
			}
		})
	}
}

func TestCLIScheduleDeleteTerminalSafety(t *testing.T) {
	for _, tc := range []struct {
		name    string
		catalog string
		ref     string
		want    string
	}{
		{
			name:    "unsafe typed unknown reference",
			catalog: scheduleDeletePage(scheduleDeleteEntry("sched-42", "Nightly backup")),
			ref:     "missing\x1b[31m\r\n\a",
			want:    "nothing matches",
		},
		{
			name: "unsafe ambiguous schedule names",
			catalog: scheduleDeletePage(
				scheduleDeleteEntry("sched-42", "Daily \x1b[31mred\r\n\a"),
				scheduleDeleteEntry("sched-43", "Daily \x1b]8;;https://example.invalid\x07blue\r\n\a")),
			ref:  "Daily",
			want: "ambiguous",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := cliServer(t, map[string]string{
				"/api/projects": cliProjects,
				"/schedule":     tc.catalog,
			})
			err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"schedule", "delete", tc.ref}, true, false)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			assertScheduleDeleteTerminalSafe(t, err.Error())
			if rec.saw(http.MethodDelete, "/schedules/sched-42") || rec.saw(http.MethodDelete, "/schedules/sched-43") {
				t.Fatalf("terminal-unsafe CLI failure mutated a schedule:\n%s", rec.all())
			}
		})
	}
}
