package terminal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/openvibely/openvibely-terminal/internal/client"
)

const cliProjects = `{"projects":[{"id":"p1","name":"demo"},{"id":"p2","name":"other"}]}`

// cliServer stubs the backend for headless runs.
func cliServer(t *testing.T, bodies map[string]string) (*client.Client, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.recordURL(r.Method, r.URL.RequestURI())
		_ = r.ParseForm()
		rec.mu.Lock()
		rec.forms = append(rec.forms, r.Method+" "+r.URL.Path+"?"+r.PostForm.Encode())
		rec.mu.Unlock()
		if body, ok := bodies[r.URL.Path]; ok {
			if strings.HasPrefix(strings.TrimSpace(body), "{") ||
				strings.HasPrefix(strings.TrimSpace(body), "[") {
				w.Header().Set("Content-Type", "application/json")
			} else {
				w.Header().Set("Content-Type", "text/html")
			}
			_, _ = w.Write([]byte(body))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/channels/") && strings.HasSuffix(r.URL.Path, "/test") {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<div class="text-success"><span>Connection successful!</span></div>`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c, rec
}

// cliVoteServer stubs project loading and one vote-record response for CLI
// success and error cases.
func cliVoteServer(t *testing.T, status int, body string) (*client.Client, *recorder) {
	t.Helper()
	const votePath = "/api/workflows/votes/step-1"
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.recordURL(r.Method, r.URL.RequestURI())
		switch r.URL.Path {
		case "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(cliProjects))
		case votePath:
			w.Header().Set("Content-Type", "application/json")
			if status == http.StatusFound {
				w.Header().Set("Location", "/login")
			}
			if status != http.StatusOK {
				w.WriteHeader(status)
			}
			_, _ = w.Write([]byte(body))
		default:
			t.Errorf("unexpected CLI vote request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c, rec
}

func TestCLIAgentsVotesPlainJSONAndEmptyOutput(t *testing.T) {
	const records = `[
		{"id":"vote-b","step_execution_id":"step-1","agent_config_id":"agent-b","vote":"reject","confidence":0.42,"reasoning":"needs more evidence"},
		{"id":"vote-a","step_execution_id":"step-1","agent_config_id":"agent-a","vote":"approve","confidence":0.91,"reasoning":"checks passed"}
	]`

	t.Run("plain", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{
			"/api/projects":               cliProjects,
			"/api/workflows/votes/step-1": records,
		})
		var out bytes.Buffer
		if err := RunCLI(c, &out, "demo", []string{"agents", "votes", "step-1"}, false, false); err != nil {
			t.Fatalf("plain vote inspection failed: %v", err)
		}
		text := stripANSI(out.String())
		for _, want := range []string{"Workflow votes", "step execution: step-1", "agent-a", "approve", "0.91", "agent-b", "reject", "0.42", "needs more evidence"} {
			if !strings.Contains(text, want) {
				t.Errorf("plain output missing %q:\n%s", want, out.String())
			}
		}
		if got := rec.count("GET", "/api/workflows/votes/step-1"); got != 1 {
			t.Fatalf("plain vote inspection made %d vote requests, want one:\n%s", got, rec.all())
		}
	})

	t.Run("json", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{
			"/api/projects":               cliProjects,
			"/api/workflows/votes/step-1": records,
		})
		var out bytes.Buffer
		if err := RunCLI(c, &out, "demo", []string{"agents", "votes", "step-1"}, false, true); err != nil {
			t.Fatalf("JSON vote inspection failed: %v", err)
		}
		var got []client.VoteRecord
		if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &got); err != nil {
			t.Fatalf("JSON vote output is invalid: %v\n%s", err, out.String())
		}
		if len(got) != 2 || got[0].AgentConfigID != "agent-b" || got[1].Reasoning != "checks passed" {
			t.Fatalf("JSON vote records = %+v", got)
		}
		if strings.Contains(out.String(), "Workflow votes") || strings.Contains(out.String(), "\x1b[") {
			t.Fatalf("JSON vote output contains human/styled text: %q", out.String())
		}
		if got := rec.count("GET", "/api/workflows/votes/step-1"); got != 1 {
			t.Fatalf("JSON vote inspection made %d vote requests, want one:\n%s", got, rec.all())
		}
	})

	t.Run("empty JSON", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{
			"/api/projects":               cliProjects,
			"/api/workflows/votes/step-1": `[]`,
		})
		var out bytes.Buffer
		if err := RunCLI(c, &out, "demo", []string{"agents", "votes", "step-1"}, false, true); err != nil {
			t.Fatalf("empty JSON vote inspection failed: %v", err)
		}
		if got := strings.TrimSpace(out.String()); got != "[]" {
			t.Fatalf("empty JSON vote output = %q, want []", got)
		}
		if got := rec.count("GET", "/api/workflows/votes/step-1"); got != 1 {
			t.Fatalf("empty JSON vote inspection made %d vote requests, want one:\n%s", got, rec.all())
		}
	})
}

func TestCLIAgentsVotesErrorsAndUnresolvedReferences(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		wantError string
	}{
		{name: "malformed", status: http.StatusOK, body: `not-json`, wantError: "decoding /api/workflows/votes/step-1 response"},
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{"error":"private body"}`, wantError: "requires sign-in"},
		{name: "server error", status: http.StatusServiceUnavailable, body: `{"error":"vote service unavailable"}`, wantError: "server error (503): vote service unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := cliVoteServer(t, tc.status, tc.body)
			var out bytes.Buffer
			err := RunCLI(c, &out, "demo", []string{"agents", "votes", "step-1"}, false, false)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %v, want substring %q", err, tc.wantError)
			}
			if out.Len() != 0 {
				t.Errorf("failed vote inspection wrote success output: %q", out.String())
			}
			if got := rec.count("GET", "/api/workflows/votes/step-1"); got != 1 {
				t.Fatalf("failed vote inspection made %d vote requests, want one:\n%s", got, rec.all())
			}
		})
	}

	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "missing reference", args: []string{"agents", "votes"}},
		{name: "extra reference", args: []string{"agents", "votes", "step-1", "other"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects})
			var out bytes.Buffer
			err := RunCLI(c, &out, "demo", tc.args, false, false)
			if err == nil || !strings.Contains(err.Error(), "usage: agents votes <step-execution-id>") {
				t.Fatalf("error = %v, want vote usage", err)
			}
			if got := rec.count("GET", "/api/workflows/votes/step-1"); got != 0 {
				t.Fatalf("unresolved vote reference made %d vote requests, want zero:\n%s", got, rec.all())
			}
			if out.Len() != 0 {
				t.Errorf("unresolved vote reference wrote output: %q", out.String())
			}
		})
	}

	t.Run("multiple projects require selection", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects})
		var out bytes.Buffer
		err := RunCLI(c, &out, "", []string{"agents", "votes", "step-1"}, false, false)
		if err == nil || !strings.Contains(err.Error(), "multiple projects") {
			t.Fatalf("error = %v, want multiple-project selection error", err)
		}
		if got := rec.count("GET", "/api/workflows/votes/step-1"); got != 0 {
			t.Fatalf("unselected multi-project vote inspection made %d requests, want zero:\n%s", got, rec.all())
		}
	})
}

func TestCLIWorkersShowJSONWithoutProject(t *testing.T) {
	c, rec := cliServer(t, map[string]string{
		"/api/capacity/global":   `{"total_running":1,"max_workers":4,"queue_size":2}`,
		"/api/capacity/projects": `[{"id":"p1","name":"Demo","running":1,"queue_size":2,"max_workers":2}]`,
		"/api/capacity/models":   `[{"name":"Sonnet","model":"claude-sonnet","running":1,"max_workers":3}]`,
	})
	var out bytes.Buffer
	if err := RunCLI(c, &out, "", []string{"works", "show"}, false, true); err != nil {
		t.Fatalf("workers show JSON failed: %v", err)
	}

	var got struct {
		Workers         []workerCapacityRow      `json:"workers"`
		Models          []modelWorkerCapacityRow `json:"models"`
		ModelsAvailable bool                     `json:"models_available"`
		Warnings        []string                 `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &got); err != nil {
		t.Fatalf("workers JSON is invalid: %v\n%s", err, out.String())
	}
	if len(got.Workers) != 2 || got.Workers[0].Scope != "global" || got.Workers[1].Name != "Demo" {
		t.Fatalf("workers JSON rows = %+v", got.Workers)
	}
	if len(got.Models) != 1 || got.Models[0].Model != "claude-sonnet" || !got.ModelsAvailable || got.Warnings == nil {
		t.Fatalf("workers JSON model availability/warnings = %+v/%t/%#v", got.Models, got.ModelsAvailable, got.Warnings)
	}
	for _, path := range []string{"/api/capacity/global", "/api/capacity/projects", "/api/capacity/models"} {
		if count := rec.count("GET", path); count != 1 {
			t.Errorf("%s request count = %d, want 1", path, count)
		}
	}
	if count := rec.count("GET", "/api/projects"); count != 0 {
		t.Errorf("project-independent workers show made %d project-list requests, want 0", count)
	}
	if strings.Contains(out.String(), "Worker capacity") || strings.Contains(out.String(), "available_slots") {
		t.Fatalf("workers JSON contains presentation or unrelated data: %s", out.String())
	}
}

func TestCLIWorkersLimitOperandValidation(t *testing.T) {
	cases := []struct {
		name     string
		operands []string
		wantPost bool
		wantForm string
	}{
		{name: "empty", operands: []string{""}},
		{name: "nonnumeric", operands: []string{"abc"}},
		{name: "negative", operands: []string{"-1"}},
		{name: "overflow 2^63", operands: []string{"9223372036854775808"}},
		{name: "overflow max uint64", operands: []string{"18446744073709551615"}},
		{name: "overflow 2^64", operands: []string{"18446744073709551616"}},
		{name: "positive", operands: []string{"6"}, wantPost: true, wantForm: "max_workers=6"},
		{name: "zero", operands: []string{"0"}, wantPost: true, wantForm: "max_workers=0"},
		{name: "surplus operand", operands: []string{"6", "extra"}},
	}

	for _, action := range []string{"limit", "project"} {
		action := action
		for _, tc := range cases {
			tc := tc
			t.Run(action+"/"+tc.name, func(t *testing.T) {
				c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects})
				args := append([]string{"workers", action}, tc.operands...)
				var out bytes.Buffer
				err := RunCLI(c, &out, "demo", args, false, false)

				path := "/workers"
				if action == "project" {
					path = "/workers/projects/p1/limit"
				}
				if tc.wantPost {
					if err != nil {
						t.Fatalf("valid worker limit failed: %v", err)
					}
					if got := rec.count("POST", path); got != 1 {
						t.Fatalf("valid worker limit should make exactly one POST, got %d:\n%s", got, rec.all())
					}
					if !rec.sawForm(tc.wantForm) {
						t.Fatalf("expected %s in form data:\n%v", tc.wantForm, rec.forms)
					}
					if tc.operands[0] == "0" {
						if !strings.Contains(out.String(), "unlimited") && !strings.Contains(out.String(), "no limit") {
							t.Errorf("zero worker limit should retain its unlimited message:\n%s", out.String())
						}
					} else if !strings.Contains(out.String(), "set to 6") {
						t.Errorf("positive worker limit should report its value:\n%s", out.String())
					}
					return
				}

				if err == nil {
					t.Fatalf("invalid worker limit should return a nonzero CLI result; output=%q", out.String())
				}
				if got := rec.count("POST", path); got != 0 {
					t.Fatalf("invalid worker limit must not make a POST, got %d:\n%s", got, rec.all())
				}
				if strings.Contains(out.String(), "unlimited") || strings.Contains(out.String(), "no limit") || strings.Contains(out.String(), "set to") {
					t.Errorf("invalid worker limit must not report success:\n%s", out.String())
				}
			})
		}
	}
}

func TestCLIPersonalityListShowAndJSON(t *testing.T) {
	const personalitiesHTML = `<div id="personality-section" data-selected-personality="release_coach">
		<div data-personality-key="" data-personality-name="Base" data-personality-description="Standard tone"
			data-personality-preview="" data-personality-is-preset="true" data-personality-has-custom="false"></div>
		<div data-personality-key="release_coach" data-personality-name="Release Coach" data-personality-description="safe releases"
			data-personality-preview="Keep releases safe..." data-personality-is-preset="false" data-personality-has-custom="true"></div>
	</div>`
	const fullPrompt = "This is the complete release coach system prompt for production work."
	c, rec := cliServer(t, map[string]string{
		"/api/projects":                     cliProjects,
		"/personality":                      personalitiesHTML,
		"/personality/custom/release_coach": `{"id":"cp1","key":"release_coach","name":"Release Coach","description":"safe releases","system_prompt":"` + fullPrompt + `"}`,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"personality", "list"}, false, false); err != nil {
		t.Fatalf("personality list failed: %v", err)
	}
	for _, want := range []string{"release_coach", "Release Coach", "safe releases", "Keep releases safe..."} {
		if !strings.Contains(stripANSI(out.String()), want) {
			t.Errorf("personality list missing %q:\n%s", want, out.String())
		}
	}
	if !rec.sawQuery("project_id=p1") {
		t.Fatalf("personality list lost project scope:\n%s", rec.all())
	}

	out.Reset()
	if err := RunCLI(c, &out, "demo", []string{"personality", "show", "release_coach"}, false, false); err != nil {
		t.Fatalf("personality show failed: %v", err)
	}
	if !strings.Contains(out.String(), fullPrompt) {
		t.Errorf("personality show omitted full prompt:\n%s", out.String())
	}

	out.Reset()
	if err := RunCLI(c, &out, "demo", []string{"personality", "list"}, false, true); err != nil {
		t.Fatalf("JSON personality list failed: %v", err)
	}
	var got []client.Personality
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &got); err != nil {
		t.Fatalf("JSON personality list = %q: %v", out.String(), err)
	}
	if len(got) != 2 || got[1].Key != "release_coach" || !got[1].Active {
		t.Fatalf("JSON personalities = %+v", got)
	}
	if !strings.Contains(out.String(), "system_prompt_preview") || strings.Contains(out.String(), "system_prompt\\\":\\\""+fullPrompt) {
		t.Errorf("list JSON should expose preview without the full prompt: %s", out.String())
	}
}

func TestCLIPersonalityCRUDAndForceDelete(t *testing.T) {
	const personalitiesHTML = `<div id="personality-section" data-selected-personality="release_coach">
		<div data-personality-key="release_coach" data-personality-name="Release Coach" data-personality-description="safe releases"
			data-personality-preview="Keep releases safe..." data-personality-is-preset="false" data-personality-has-custom="true"></div>
	</div>`
	var deleteRequests int
	var addBody, editBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(cliProjects))
		case r.Method == http.MethodGet && r.URL.Path == "/personality":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(personalitiesHTML))
		case r.Method == http.MethodPost && r.URL.Path == "/personality/custom":
			if err := json.NewDecoder(r.Body).Decode(&addBody); err != nil {
				t.Errorf("decode CLI add body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"id":"cp1","key":"release_coach","name":"Release Coach","description":"safe releases","system_prompt":"Keep releases safe in production deployments."}`)
		case r.Method == http.MethodPut && r.URL.Path == "/personality/custom/release_coach":
			if err := json.NewDecoder(r.Body).Decode(&editBody); err != nil {
				t.Errorf("decode CLI edit body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"id":"cp1","key":"release_coach","name":"Updated Coach","description":"updated","system_prompt":"Keep every release reversible and observable."}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/personality/custom/release_coach":
			deleteRequests++
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	addPrompt := "Keep releases safe | preserve this literal marker in production deployments."
	if err := RunCLI(c, &out, "demo", []string{"personality", "add", "Release Coach", "|", addPrompt}, false, false); err != nil {
		t.Fatalf("CLI personality add failed: %v", err)
	}
	if addBody["name"] != "Release Coach" || addBody["description"] != "" || addBody["system_prompt"] != addPrompt {
		t.Errorf("CLI add body = %#v, want prompt %q and empty description", addBody, addPrompt)
	}
	if !strings.Contains(out.String(), "key: release_coach") || !strings.Contains(out.String(), "ID: cp1") {
		t.Errorf("CLI add did not report key/ID:\n%s", out.String())
	}

	out.Reset()
	if err := RunCLI(c, &out, "demo", []string{"personality", "edit", "release_coach", "|", "Updated Coach", "|", "updated", "|", "Keep every release reversible and observable."}, false, false); err != nil {
		t.Fatalf("CLI personality edit failed: %v", err)
	}
	if editBody["name"] != "Updated Coach" || editBody["description"] != "updated" || editBody["system_prompt"] == "" {
		t.Errorf("CLI edit body = %#v", editBody)
	}

	if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"personality", "delete", "release_coach"}, false, false); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("delete without --force error = %v", err)
	}
	if deleteRequests != 0 {
		t.Fatal("CLI delete mutated without --force")
	}
	out.Reset()
	if err := RunCLI(c, &out, "demo", []string{"personality", "delete", "release_coach"}, true, true); err != nil {
		t.Fatalf("forced JSON personality delete failed: %v", err)
	}
	var deleted map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &deleted); err != nil {
		t.Fatalf("forced delete JSON = %q: %v", out.String(), err)
	}
	if deleted["action"] != "delete" || deleted["key"] != "release_coach" || deleteRequests != 1 {
		t.Fatalf("delete result = %#v, requests=%d", deleted, deleteRequests)
	}
}

func TestCLIPersonalityAddLiteralDescriptionPrefixUsesExactTwoFieldPayload(t *testing.T) {
	const personalitiesHTML = `<div id="personality-section" data-selected-personality="">
		<div data-personality-key="release_coach" data-personality-name="Release Coach" data-personality-description=""
			data-personality-preview="prompt" data-personality-is-preset="false" data-personality-has-custom="true"></div>
	</div>`
	const prompt = "description: Explain release risks clearly | preserve every literal | pipe."
	want := map[string]string{
		"name":          "Release Coach",
		"description":   "",
		"system_prompt": prompt,
	}
	var body map[string]string
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(cliProjects))
		case r.Method == http.MethodGet && r.URL.Path == "/personality":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(personalitiesHTML))
		case r.Method == http.MethodPost && r.URL.Path == "/personality/custom":
			posts++
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode CLI add body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"id":"cp1","key":"release_coach","name":"Release Coach","description":"","system_prompt":%q}`, prompt)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"personality", "add", "Release Coach", "|", prompt}, false, false); err != nil {
		t.Fatalf("CLI literal-prefix add failed: %v", err)
	}
	if posts != 1 {
		t.Fatalf("CLI add made %d POST requests, want exactly 1", posts)
	}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("CLI add body = %#v, want %#v", body, want)
	}
	if strings.Contains(out.String(), "error") || !strings.Contains(out.String(), "created personality") {
		t.Fatalf("CLI add output = %q", out.String())
	}
}

func TestCLIPersonalityAddOptionalDescriptionPreservesPromptPipes(t *testing.T) {
	const personalitiesHTML = `<div id="personality-section" data-selected-personality="">
		<div data-personality-key="existing" data-personality-name="Existing" data-personality-description="existing"
			data-personality-preview="prompt" data-personality-is-preset="false" data-personality-has-custom="true"></div>
	</div>`
	const description = "safe releases for production"
	const prompt = "Keep every release reversible | observable | and easy to explain."
	var body map[string]string
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(cliProjects))
		case r.Method == http.MethodGet && r.URL.Path == "/personality":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(personalitiesHTML))
		case r.Method == http.MethodPost && r.URL.Path == "/personality/custom":
			posts++
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode add body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"id":"cp-new","key":"release_coach","name":"Release Coach","description":"safe releases for production","system_prompt":"created prompt that is long enough"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{
		"personality", "add", "Release Coach", "|", "description=" + description, "|", prompt,
	}, false, false); err != nil {
		t.Fatalf("CLI optional-description add failed: %v", err)
	}
	if posts != 1 || body["name"] != "Release Coach" || body["description"] != description || body["system_prompt"] != prompt {
		t.Fatalf("CLI add requests/body = %d/%#v, want description %q and prompt %q", posts, body, description, prompt)
	}
}

func TestCLIPersonalityAddRejectsMalformedOptionalDescription(t *testing.T) {
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(cliProjects))
		case "/personality/custom":
			if r.Method == http.MethodPost {
				posts++
			}
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"personality", "add", "Release Coach"},
		{"personality", "add", "|", "Keep releases safe in production deployments."},
		{"personality", "add", "Release Coach", "|", "description=safe releases"},
		{"personality", "add", "Release Coach", "|", "description=", "|", "Keep releases safe in production deployments."},
		{"personality", "add", "Release Coach", "|", "description=safe releases", "|"},
	} {
		var out bytes.Buffer
		err := RunCLI(c, &out, "demo", args, false, false)
		if err == nil || !strings.Contains(err.Error(), "personality add") || !strings.Contains(err.Error(), "description") {
			t.Errorf("malformed CLI add %q error = %v", args, err)
		}
	}
	if posts != 0 {
		t.Fatalf("malformed optional-description forms made %d POST requests", posts)
	}
}

func TestCLIEditingActivePersonalityPreservesActiveJSON(t *testing.T) {
	const personalitiesHTML = `<div id="personality-section" data-selected-personality="release_coach">
		<div data-personality-key="release_coach" data-personality-name="Release Coach" data-personality-description="safe releases"
			data-personality-preview="Keep releases safe..." data-personality-is-preset="false" data-personality-has-custom="true"></div>
	</div>`
	var putRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(cliProjects))
		case r.Method == http.MethodGet && r.URL.Path == "/personality":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(personalitiesHTML))
		case r.Method == http.MethodPut && r.URL.Path == "/personality/custom/release_coach":
			putRequests++
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"id":"cp1","key":"release_coach","name":"Release Coach","description":"updated","system_prompt":"Updated prompt that is long enough."}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{
		"personality", "edit", "release_coach", "|", "Release Coach", "|", "updated", "|", "Updated prompt that is long enough.",
	}, false, true); err != nil {
		t.Fatalf("JSON personality edit failed: %v", err)
	}
	var got client.Personality
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &got); err != nil {
		t.Fatalf("JSON personality edit = %q: %v", out.String(), err)
	}
	if putRequests != 1 {
		t.Fatalf("PUT requests = %d, want 1", putRequests)
	}
	if got.Key != "release_coach" || !got.Active {
		t.Fatalf("edited active personality = %+v, want key release_coach and active=true", got)
	}
}

func TestCLIPersonalityFailuresAndReferenceSafety(t *testing.T) {
	const personalitiesHTML = `<div id="personality-section" data-selected-personality="">
		<div data-personality-key="known" data-personality-name="Known" data-personality-description="known"
			data-personality-preview="known" data-personality-is-preset="false" data-personality-has-custom="true"></div>
		<div data-personality-key="review_one" data-personality-name="Review One" data-personality-description="one"
			data-personality-preview="one" data-personality-is-preset="false" data-personality-has-custom="true"></div>
		<div data-personality-key="review_two" data-personality-name="Review Two" data-personality-description="two"
			data-personality-preview="two" data-personality-is-preset="false" data-personality-has-custom="true"></div>
	</div>`
	var posts, puts, saves, deletes int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(cliProjects))
		case r.Method == http.MethodGet && r.URL.Path == "/personality":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(personalitiesHTML))
		case r.Method == http.MethodPost && r.URL.Path == "/personality/custom":
			posts++
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = fmt.Fprint(w, `{"error":"System prompt must be at least 20 characters"}`)
		case r.Method == http.MethodPut && r.URL.Path == "/personality/custom/known":
			puts++
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"error":"personality update failed"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/personality/save":
			saves++
			w.WriteHeader(http.StatusBadGateway)
			_, _ = fmt.Fprint(w, `{"error":"personality activation failed"}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/personality/custom/known":
			deletes++
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"error":"personality deletion failed"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"personality", "add", "Known", "|", "short"}, false, false); err == nil || !strings.Contains(err.Error(), "System prompt must be at least 20 characters") {
		t.Fatalf("CLI validation error = %v", err)
	}
	if posts != 1 || out.Len() != 0 || strings.Contains(out.String(), "created personality") {
		t.Fatalf("validation failure output/mutation: posts=%d output=%q", posts, out.String())
	}

	if err := RunCLI(c, &out, "demo", []string{"personality", "set", "missing"}, false, false); err == nil || !strings.Contains(err.Error(), "nothing matches") {
		t.Fatalf("CLI unknown-reference error = %v", err)
	}
	if saves != 0 {
		t.Fatal("unknown set mutated the active personality")
	}

	if err := RunCLI(c, &out, "demo", []string{"personality", "set", "Review"}, false, false); err == nil || !strings.Contains(err.Error(), "ambiguous") || !strings.Contains(err.Error(), "Review One") || !strings.Contains(err.Error(), "Review Two") {
		t.Fatalf("CLI ambiguous-reference error = %v", err)
	}
	if saves != 0 {
		t.Fatal("ambiguous set mutated the active personality")
	}

	if err := RunCLI(c, &out, "demo", []string{"personality", "edit", "known", "|", "Updated", "|", "updated", "|", "A valid prompt that is long enough"}, false, false); err == nil || !strings.Contains(err.Error(), "personality update failed") {
		t.Fatalf("CLI backend edit error = %v", err)
	}
	if puts != 1 || strings.Contains(out.String(), "updated personality") {
		t.Fatalf("edit failure output/mutation: puts=%d output=%q", puts, out.String())
	}

	beforeDelete := deletes
	if err := RunCLI(c, &out, "demo", []string{"personality", "delete", "known"}, false, false); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("CLI delete safeguard error = %v", err)
	}
	if deletes != beforeDelete {
		t.Fatal("CLI delete mutated without --force")
	}

	if err := RunCLI(c, &out, "demo", []string{"personality", "delete", "known"}, true, false); err == nil || !strings.Contains(err.Error(), "personality deletion failed") {
		t.Fatalf("CLI backend delete error = %v", err)
	}
	if deletes != beforeDelete+1 || strings.Contains(out.String(), "deleted personality") {
		t.Fatalf("delete failure output/mutation: deletes=%d output=%q", deletes, out.String())
	}
}

func TestCLIUnknownPersonalityActionDoesNotReadOrMutate(t *testing.T) {
	var personalityGets, mutations int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(cliProjects))
		case "/personality":
			personalityGets++
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<div id="personality-section"></div>`))
		default:
			mutations++
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = RunCLI(c, &out, "demo", []string{"personality", "unsupported"}, false, false)
	if err == nil || !strings.Contains(err.Error(), "unknown personality action") {
		t.Fatalf("unknown personality action error = %v", err)
	}
	if personalityGets != 0 || mutations != 0 {
		t.Fatalf("unknown action made requests: personality GETs=%d mutations=%d", personalityGets, mutations)
	}
}

func TestCLIAnalyticsUsageMatchesInteractiveQuotaOutput(t *testing.T) {
	const usage = `{
		"totals":{"call_count":2,"total_tokens":700},
		"account_limits":[{
			"provider":"OpenAI","plan_type":"team","status_label":"healthy",
			"primary_limit":{"label":"tokens","used_percent":100,"resets_at":"tomorrow"},
			"limits":[{"label":"requests","used_percent":25,"resets_at":"next hour"}]
		}]
	}`
	c, rec := cliServer(t, map[string]string{
		"/api/projects":        cliProjects,
		"/api/analytics/usage": usage,
	})

	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.selectedID = "p1"
	interactive := stripANSI(transcript(runLine(t, m, "/analytics usage")))

	var cliOut bytes.Buffer
	if err := RunCLI(c, &cliOut, "demo", []string{"analytics", "usage"}, false, false); err != nil {
		t.Fatalf("CLI analytics usage failed: %v", err)
	}
	cli := stripANSI(cliOut.String())

	for _, want := range []string{"OpenAI", "team", "healthy", "tokens", "100.0%", "tomorrow", "requests", "25.0%", "next hour"} {
		if !strings.Contains(interactive, want) {
			t.Errorf("interactive usage output missing %q:\n%s", want, interactive)
		}
		if !strings.Contains(cli, want) {
			t.Errorf("CLI usage output missing %q:\n%s", want, cli)
		}
	}
	if !rec.sawQuery("project_id=p1") {
		t.Errorf("CLI analytics usage request lost selected project scope:\n%s", rec.all())
	}
}

func TestCLIHelpWorksOffline(t *testing.T) {
	c, err := client.New("http://127.0.0.1:1") // nothing listening
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunCLI(c, &out, "", []string{"help"}, false, false); err != nil {
		t.Fatalf("help failed: %v", err)
	}
	// CLI help lists bare subcommands, since that is how they are invoked
	// from a shell; the leading slash belongs to the chat window.
	got := out.String()
	if !strings.Contains(got, "tasks") {
		t.Errorf("help output missing commands:\n%s", got)
	}
	if strings.Contains(got, "/tasks") {
		t.Errorf("CLI help should not use slash form:\n%s", got)
	}
	for _, want := range []string{"-project <name|id>", "projects list/create", "Global commands"} {
		if !strings.Contains(got, want) {
			t.Errorf("help output missing project-scope guidance %q:\n%s", want, got)
		}
	}
	// Every registered command must be reachable from help.
	for _, c := range commands {
		if !strings.Contains(got, c.name) {
			t.Errorf("CLI help omits %q", c.name)
		}
	}
}

func TestCLIBackendRequiredFailureIncludesRecoveryGuidance(t *testing.T) {
	c, err := client.New("http://127.0.0.1:1") // nothing listening
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err = RunCLI(c, &out, "", []string{"tasks"}, false, false)
	if err == nil {
		t.Fatal("expected backend-required command to fail")
	}
	got := err.Error()
	for _, want := range []string{
		"Unable to reach the OpenVibely backend",
		"Start or check your local OpenVibely backend",
		"-server <url>",
		"OPENVIBELY_SERVER_URL",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("error missing %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "Details:") || !strings.Contains(got, "GET /api/projects") {
		t.Fatalf("error should keep concise transport details after guidance:\n%s", got)
	}
	detailsMarker := "\nDetails: "
	detailsAt := strings.Index(got, detailsMarker)
	if detailsAt < 0 {
		t.Fatalf("CLI project-load error missing details marker: %s", got)
	}
	details := got[detailsAt+len(detailsMarker):]
	if want := OfflineRecoveryMessage(c.BaseURL(), errors.New(details)); got != want {
		t.Fatalf("CLI project-load failure did not use the shared recovery formatter:\n got: %s\nwant: %s", got, want)
	}
}

func TestCLIProjectLoadFailuresUseReachableBackendPresentation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body       string
		diagnostic string
	}{
		{
			name:       "http 500",
			status:     http.StatusInternalServerError,
			body:       `{"error":"project store failed"}`,
			diagnostic: "project store failed",
		},
		{
			name:       "malformed json",
			status:     http.StatusOK,
			body:       `{"projects":`,
			diagnostic: "decoding /api/projects response",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/projects" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err = RunCLI(c, &out, "demo", []string{"tasks"}, false, false)
			if err == nil {
				t.Fatal("expected project preload failure")
			}
			got := strings.ToLower(err.Error())
			for _, want := range []string{"backend error", "unhealthy", strings.ToLower(tc.diagnostic)} {
				if !strings.Contains(got, want) {
					t.Errorf("reachable CLI project failure missing %q: %s", want, err)
				}
			}
			for _, unwanted := range []string{"unable to reach", "offline", "start or check your local backend"} {
				if strings.Contains(got, unwanted) {
					t.Errorf("reachable CLI project failure contains offline guidance %q: %s", unwanted, err)
				}
			}
			if out.Len() != 0 {
				t.Fatalf("failed project preload printed partial output: %q", out.String())
			}
		})
	}
}

func TestCLIReachableUnauthorizedBackendUsesSignInGuidance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/projects" {
			w.Header().Set("Location", "/login")
			w.WriteHeader(http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = RunCLI(c, &out, "", []string{"tasks"}, false, false)
	if err == nil {
		t.Fatal("expected protected CLI command to fail without a session")
	}
	got := err.Error()
	if strings.Contains(strings.ToLower(got), "offline") {
		t.Fatalf("reachable unauthorized backend was classified offline: %v", err)
	}
	for _, want := range []string{"requires sign-in", "/login", "OPENVIBELY_AUTH_USERNAME"} {
		if !strings.Contains(got, want) {
			t.Errorf("auth guidance missing %q: %v", want, err)
		}
	}
	if out.Len() != 0 {
		t.Fatalf("auth preflight should not print a partial result: %q", out.String())
	}
}

func TestCLIRunsCommandAndPrintsResult(t *testing.T) {
	const board = `<div data-task-id="t-1" data-task-status="running" data-task-category="active">
		<a href="/tasks/t-1?from=tasks" title="Refactor the API">Refactor the API</a>
	</div>`
	c, rec := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/tasks":        board,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"tasks"}, false, false); err != nil {
		t.Fatalf("tasks failed: %v", err)
	}
	if !rec.saw("GET", "/tasks") {
		t.Fatalf("no board fetch, calls:\n%s", rec.all())
	}
	if !strings.Contains(out.String(), "Refactor the API") {
		t.Errorf("output missing task title:\n%s", out.String())
	}
}

func TestCLIStatusProjectListFailureStillRendersGlobalStatus(t *testing.T) {
	for _, tc := range []struct {
		name        string
		projectList func(http.ResponseWriter)
		wantError   string
	}{
		{
			name: "service unavailable",
			projectList: func(w http.ResponseWriter) {
				http.Error(w, "projects unavailable", http.StatusServiceUnavailable)
			},
			wantError: "503",
		},
		{
			name: "connection dropped",
			projectList: func(w http.ResponseWriter) {
				hijacker, ok := w.(http.Hijacker)
				if !ok {
					panic("response writer cannot hijack connection")
				}
				conn, _, err := hijacker.Hijack()
				if err != nil {
					panic(err)
				}
				_ = conn.Close()
			},
			wantError: "loading projects",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				rec.recordURL(r.Method, r.URL.RequestURI())
				switch r.URL.Path {
				case "/api/projects":
					tc.projectList(w)
				case "/api/capacity/global":
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"total_running":2,"max_workers":5,"queue_size":1,"available_slots":3}`)
				case "/auth/me":
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"authenticated":true,"username":"operator"}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err = RunCLI(c, &out, "", []string{"status"}, false, false)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("status error = %v, want substring %q", err, tc.wantError)
			}
			got := stripANSI(out.String())
			for _, want := range []string{"Status", "connected", "signed in as operator", "2 running / 5 max, 1 queued, 3 free", "projects", "unavailable", "partial failure"} {
				if !strings.Contains(strings.ToLower(got), strings.ToLower(want)) {
					t.Errorf("partial status missing %q:\n%s", want, got)
				}
			}
			if rec.count("GET", "/api/capacity/global") != 1 || rec.count("GET", "/auth/me") != 1 {
				t.Errorf("global status checks did not run exactly once:\n%s", rec.all())
			}
			if rec.count("GET", "/alerts") != 0 || rec.count("GET", "/tasks") != 0 {
				t.Errorf("project-list failure made scoped requests:\n%s", rec.all())
			}
		})
	}
}

func TestCLIStatusMultipleProjectsIsGlobalAndUnambiguous(t *testing.T) {
	c, rec := cliServer(t, map[string]string{
		"/api/projects":        cliProjects,
		"/api/capacity/global": `{"total_running":1,"max_workers":4,"queue_size":0,"available_slots":3}`,
		"/auth/me":             `{"authenticated":false}`,
	})
	var out bytes.Buffer
	if err := RunCLI(c, &out, "", []string{"status"}, false, false); err != nil {
		t.Fatalf("status failed: %v", err)
	}
	got := stripANSI(out.String())
	for _, forbidden := range []string{"project         demo", "project         other", "alerts", "tasks"} {
		if strings.Contains(strings.ToLower(got), strings.ToLower(forbidden)) {
			t.Errorf("multi-project status used ambiguous scope %q:\n%s", forbidden, got)
		}
	}
	if rec.count("GET", "/alerts") != 0 || rec.count("GET", "/tasks") != 0 {
		t.Fatalf("multi-project status made scoped requests:\n%s", rec.all())
	}
	for _, path := range []string{"/api/projects", "/api/capacity/global", "/auth/me"} {
		if got := rec.count("GET", path); got != 1 {
			t.Errorf("multi-project status made %d GET requests to %s, want 1:\n%s", got, path, rec.all())
		}
	}
	if !strings.Contains(got, "1 running / 4 max") || !strings.Contains(got, "disabled or anonymous") {
		t.Fatalf("multi-project status omitted global rows:\n%s", got)
	}
}

func TestCLIStatusZeroProjectsSkipsScopedCounts(t *testing.T) {
	c, rec := cliServer(t, map[string]string{
		"/api/projects":        `{"projects":[]}`,
		"/api/capacity/global": `{"total_running":0,"max_workers":4,"queue_size":0,"available_slots":4}`,
		"/auth/me":             `{"authenticated":false}`,
	})
	var out bytes.Buffer
	if err := RunCLI(c, &out, "", []string{"status"}, false, false); err != nil {
		t.Fatalf("status failed: %v", err)
	}
	for _, path := range []string{"/api/projects", "/api/capacity/global", "/auth/me"} {
		if got := rec.count("GET", path); got != 1 {
			t.Errorf("zero-project status made %d GET requests to %s, want 1:\n%s", got, path, rec.all())
		}
	}
	if rec.count("GET", "/alerts") != 0 || rec.count("GET", "/tasks") != 0 {
		t.Fatalf("zero-project status made scoped requests:\n%s", rec.all())
	}
}

func TestCLIStatusCancellationStopsFirstWaveBeforeScopedCounts(t *testing.T) {
	started := make(chan string, 3)
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.recordURL(r.Method, r.URL.RequestURI())
		switch r.URL.Path {
		case "/api/projects", "/api/capacity/global", "/auth/me":
			started <- r.URL.Path
			<-r.Context().Done()
		case "/alerts", "/tasks":
			http.Error(w, "unexpected scoped request", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- RunCLIContext(ctx, c, &bytes.Buffer{}, "", []string{"status"}, false, false)
	}()
	seen := make(map[string]bool, 3)
	for len(seen) < 3 {
		select {
		case path := <-started:
			seen[path] = true
		case <-time.After(time.Second):
			t.Fatalf("first wave did not start before cancellation; saw %v", seen)
		}
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("canceled status returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled status did not return promptly")
	}
	for _, path := range []string{"/api/projects", "/api/capacity/global", "/auth/me"} {
		if got := rec.count("GET", path); got != 1 {
			t.Errorf("canceled status made %d GET requests to %s, want 1:\n%s", got, path, rec.all())
		}
	}
	if rec.count("GET", "/alerts") != 0 || rec.count("GET", "/tasks") != 0 {
		t.Fatalf("canceled status made scoped requests:\n%s", rec.all())
	}
}

func TestCLIStatusUsesInvalidServerURLRecovery(t *testing.T) {
	server := "https://cli-user:cli-password-must-not-appear@ops.example/%zz?token=cli-token-must-not-appear#cli-fragment-must-not-appear\x1b[7m"
	c, err := client.New(server)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = RunCLI(c, &out, "", []string{"status"}, false, false)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "invalid configured server url") {
		t.Fatalf("invalid URL status error = %v, want invalid configured server URL guidance", err)
	}
	output := stripANSI(out.String())
	lower := strings.ToLower(output)
	for _, want := range []string{"invalid configured server url", "-server <url>", "openvibely_server_url"} {
		if !strings.Contains(lower, strings.ToLower(want)) {
			t.Errorf("status output missing %q:\n%s", want, output)
		}
	}
	for _, unwanted := range []string{"offline", "backend responded", "start/check your local backend", "start or check your local openvibely backend"} {
		if strings.Contains(lower, unwanted) {
			t.Errorf("status output contains misleading text %q:\n%s", unwanted, output)
		}
	}
	for _, secret := range []string{"cli-user", "cli-password-must-not-appear", "cli-token-must-not-appear", "cli-fragment-must-not-appear"} {
		if strings.Contains(output, secret) || strings.Contains(err.Error(), secret) {
			t.Errorf("invalid URL status leaked %q:\noutput=%s\nerror=%v", secret, output, err)
		}
	}
}

func TestCLITaskCancellationPropagatesToBlockedBoardRequest(t *testing.T) {
	boardStarted := make(chan struct{})
	requestCanceled := make(chan struct{})
	var startOnce, cancelOnce sync.Once

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"projects":[{"id":"p1","name":"demo"}]}`)
		case "/tasks":
			startOnce.Do(func() { close(boardStarted) })
			<-r.Context().Done()
			cancelOnce.Do(func() { close(requestCanceled) })
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- RunCLIContext(ctx, c, io.Discard, "demo", []string{"tasks"}, false, false)
	}()

	select {
	case <-boardStarted:
	case <-time.After(time.Second):
		t.Fatal("tasks board request did not start")
	}
	cancel()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("canceled tasks command returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled tasks command did not return promptly")
	}
	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("tasks board request did not observe caller cancellation")
	}
}

func TestCLICanceledForcedTaskDeleteDoesNotMutateAfterLookupRelease(t *testing.T) {
	const taskBoard = `<div data-task-id="t-1" data-task-status="pending" data-task-category="backlog"><a href="/tasks/t-1" title="Refactor">Refactor</a></div>`
	lookupStarted := make(chan struct{})
	releaseLookup := make(chan struct{})
	lookupCanceled := make(chan struct{})
	lookupDone := make(chan struct{})
	var startOnce, cancelOnce sync.Once
	var deleteCalls int
	var deleteMu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"projects":[{"id":"p1","name":"demo"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/tasks":
			startOnce.Do(func() { close(lookupStarted) })
			go func() {
				<-r.Context().Done()
				cancelOnce.Do(func() { close(lookupCanceled) })
			}()
			<-releaseLookup
			defer close(lookupDone)
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, taskBoard)
		case r.Method == http.MethodDelete && r.URL.Path == "/tasks/t-1":
			deleteMu.Lock()
			deleteCalls++
			deleteMu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- RunCLIContext(ctx, c, io.Discard, "demo", []string{"tasks", "delete", "Refactor"}, true, false)
	}()

	select {
	case <-lookupStarted:
	case <-time.After(time.Second):
		t.Fatal("task lookup did not start")
	}
	cancel()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("canceled forced delete returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled forced delete did not return promptly")
	}
	select {
	case <-lookupCanceled:
	case <-time.After(time.Second):
		t.Fatal("task lookup did not observe caller cancellation")
	}
	close(releaseLookup)
	select {
	case <-lookupDone:
	case <-time.After(time.Second):
		t.Fatal("released task lookup did not finish")
	}
	deleteMu.Lock()
	gotDeletes := deleteCalls
	deleteMu.Unlock()
	if gotDeletes != 0 {
		t.Fatalf("canceled forced delete sent %d DELETE requests after lookup release", gotDeletes)
	}
}

func TestCLIStatusPreservesAuthRequiredAndOfflineOutput(t *testing.T) {
	t.Run("auth required", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/projects" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"projects":[]}`)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer srv.Close()
		c, err := client.New(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		err = RunCLI(c, &out, "", []string{"status"}, false, false)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "requires sign-in") {
			t.Fatalf("auth-required status error = %v, want sign-in requirement", err)
		}
		got := strings.ToLower(stripANSI(out.String()))
		if !strings.Contains(got, "sign-in required") || strings.Contains(got, "offline") {
			t.Fatalf("auth-required status output is wrong:\n%s", got)
		}
	})

	t.Run("fully offline", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		baseURL := srv.URL
		srv.Close()
		c, err := client.New(baseURL)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		err = RunCLI(c, &out, "", []string{"status"}, false, false)
		if err == nil {
			t.Fatal("offline status unexpectedly succeeded")
		}
		got := strings.ToLower(stripANSI(out.String()))
		for _, want := range []string{"status", "offline", "projects", "unavailable", "partial failure"} {
			if !strings.Contains(got, want) {
				t.Errorf("offline status missing %q:\n%s", want, got)
			}
		}
	})
}

func TestCLIStatusRendersPrefetchedCounts(t *testing.T) {
	const alertsHTML = `<div data-alert-id="a-1" data-alert-scroll-anchor="a-1">
		<p class="font-semibold">Needs approval</p>
		<span class="badge">pending</span>
	</div>`
	const tasksHTML = `<div>
		<div class="card" data-task-id="t-1" data-task-status="running" data-task-category="active" data-display-order="0">
			<div class="card-body"><a href="/tasks/t-1" title="Task A">Task A</a></div>
		</div>
		<div class="card" data-task-id="t-2" data-task-status="queued" data-task-category="active" data-display-order="1">
			<div class="card-body"><a href="/tasks/t-2" title="Task B">Task B</a></div>
		</div>
	</div>`
	c, rec := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/alerts":       alertsHTML,
		"/tasks":        tasksHTML,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"status"}, false, false); err != nil {
		t.Fatalf("status failed: %v", err)
	}
	got := out.String()
	if !rec.saw("GET", "/alerts") {
		t.Errorf("expected alerts fetch during CLI status:\n%s", rec.all())
	}
	if !rec.saw("GET", "/tasks") {
		t.Errorf("expected tasks fetch during CLI status:\n%s", rec.all())
	}
	if got := rec.count("GET", "/alerts"); got != 1 {
		t.Errorf("CLI status made %d /alerts requests, want exactly 1:\n%s", got, rec.all())
	}
	if got := rec.count("GET", "/tasks"); got != 1 {
		t.Errorf("CLI status made %d /tasks requests, want exactly 1:\n%s", got, rec.all())
	}
	if !strings.Contains(got, "1 pending approvals") {
		t.Errorf("status missing pending alert count:\n%s", got)
	}
	if !strings.Contains(got, "2 active, 1 queued") {
		t.Errorf("status missing task counts:\n%s", got)
	}
	if strings.Contains(got, "none pending") || strings.Contains(got, "none active") {
		t.Errorf("status rendered zero-count placeholders despite mocked counts:\n%s", got)
	}
}

func TestCLIStatusStartsGlobalChecksWithProjectDiscoveryBeforeScopedCounts(t *testing.T) {
	firstWaveStarted := make(chan string, 3)
	releaseFirstWave := make(chan struct{})
	var releaseOnce sync.Once
	var mu sync.Mutex
	projectCompleted := false
	scopedStartedEarly := false

	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.recordURL(r.Method, r.URL.RequestURI())
		switch r.URL.Path {
		case "/api/projects", "/api/capacity/global", "/auth/me":
			firstWaveStarted <- r.URL.Path
			<-releaseFirstWave
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/projects":
				_, _ = io.WriteString(w, `{"projects":[{"id":"p1","name":"demo"}]}`)
				mu.Lock()
				projectCompleted = true
				mu.Unlock()
			case "/api/capacity/global":
				_, _ = io.WriteString(w, `{"total_running":1,"max_workers":4,"available_slots":3}`)
			case "/auth/me":
				_, _ = io.WriteString(w, `{"authenticated":true,"username":"operator"}`)
			}
		case "/alerts", "/tasks":
			mu.Lock()
			if !projectCompleted {
				scopedStartedEarly = true
			}
			mu.Unlock()
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<div></div>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	allFirstWaveStarted := make(chan bool, 1)
	go func() {
		seen := make(map[string]bool, 3)
		timer := time.NewTimer(500 * time.Millisecond)
		defer timer.Stop()
		for len(seen) < 3 {
			select {
			case path := <-firstWaveStarted:
				seen[path] = true
			case <-timer.C:
				releaseOnce.Do(func() { close(releaseFirstWave) })
				allFirstWaveStarted <- false
				return
			}
		}
		releaseOnce.Do(func() { close(releaseFirstWave) })
		allFirstWaveStarted <- true
	}()

	if err := RunCLI(c, &bytes.Buffer{}, "", []string{"status"}, false, false); err != nil {
		t.Fatalf("status failed: %v", err)
	}
	if !<-allFirstWaveStarted {
		t.Fatal("projects, capacity, and auth did not begin in one wave")
	}
	mu.Lock()
	early := scopedStartedEarly
	mu.Unlock()
	if early {
		t.Fatal("project-scoped counts started before project selection completed")
	}
	for _, path := range []string{"/api/projects", "/api/capacity/global", "/auth/me", "/alerts", "/tasks"} {
		if got := rec.count("GET", path); got != 1 {
			t.Errorf("status made %d GET requests to %s, want 1:\n%s", got, path, rec.all())
		}
	}
}

func TestCLIStatusUsesTwoDelayedRequestWaves(t *testing.T) {
	const endpointDelay = 100 * time.Millisecond
	const alertsHTML = `<div data-alert-id="a-1" data-alert-scroll-anchor="a-1"><span class="badge">pending</span></div>`
	const tasksHTML = `<div><div data-task-id="t-1" data-task-status="running" data-task-category="active"><a href="/tasks/t-1" title="Task A">Task A</a></div></div>`

	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.recordURL(r.Method, r.URL.RequestURI())
		time.Sleep(endpointDelay)
		switch r.URL.Path {
		case "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"projects":[{"id":"p1","name":"demo"}]}`))
		case "/alerts":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(alertsHTML))
		case "/tasks":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(tasksHTML))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	const runs = 5
	durations := make([]time.Duration, 0, runs)
	for range runs {
		start := time.Now()
		if err := RunCLI(c, &bytes.Buffer{}, "", []string{"status"}, false, false); err != nil {
			t.Fatalf("status failed: %v", err)
		}
		durations = append(durations, time.Since(start))
	}
	slices.Sort(durations)
	median := durations[len(durations)/2]

	for _, path := range []string{"/api/projects", "/api/capacity/global", "/auth/me", "/alerts", "/tasks"} {
		if got := rec.count("GET", path); got != runs {
			t.Errorf("delayed status made %d GET requests to %s, want %d:\n%s", got, path, runs, rec.all())
		}
	}
	if median >= 225*time.Millisecond {
		t.Fatalf("median CLI status latency = %s, want under 225ms for two 100ms request waves (durations: %v)", median, durations)
	}
}

func TestCLIRequiresExplicitProjectWhenMultipleProjectsExist(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		force     bool
		json      bool
		forbidden []string
	}{
		{name: "tasks read", args: []string{"tasks"}, forbidden: []string{"/tasks"}},
		{name: "tasks JSON read", args: []string{"tasks"}, json: true, forbidden: []string{"/tasks"}},
		{name: "tasks mutation", args: []string{"tasks", "run", "task"}, forbidden: []string{"/tasks"}},
		{name: "alerts read", args: []string{"alerts"}, forbidden: []string{"/alerts"}},
		{name: "automations mutation", args: []string{"automations", "pause", "automation"}, forbidden: []string{"/automations"}},
		{name: "forced task deletion", args: []string{"tasks", "delete", "task"}, force: true, forbidden: []string{"/tasks"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := cliServer(t, map[string]string{
				"/api/projects": cliProjects,
			})
			var out bytes.Buffer
			err := RunCLI(c, &out, "", tc.args, tc.force, tc.json)
			if err == nil {
				t.Fatalf("%v succeeded without an explicit project", tc.args)
			}
			for _, want := range []string{"multiple projects", "-project <name|id>"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing %q", err, want)
				}
			}
			for _, path := range tc.forbidden {
				if strings.Contains(rec.all(), " "+path) {
					t.Errorf("unscoped command called %s; requests:\n%s", path, rec.all())
				}
			}
			if out.Len() != 0 {
				t.Errorf("failed preflight wrote output: %q", out.String())
			}
		})
	}
}

func TestCLIModelsFilterOutputDistinguishesMatchesFromNoMatches(t *testing.T) {
	const modelsHTML = `<div data-model-id="m-1" data-model-name="Sonnet"
		data-model-provider="Anthropic" data-model-model="claude-sonnet-4"></div>`
	cases := []struct {
		name      string
		filter    string
		want      string
		forbidden []string
	}{
		{name: "name match", filter: "sonNET", want: "Sonnet"},
		{name: "model match", filter: "CLAUDE-SONNET", want: "Sonnet"},
		{name: "provider match", filter: "anthROPIC", want: "Sonnet"},
		{name: "no match", filter: "Missing", want: `no models match "Missing"`, forbidden: []string{"no models configured", "web UI", "API"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := cliServer(t, map[string]string{
				"/api/projects": `{"projects":[]}`,
				"/models":       modelsHTML,
			})
			var out bytes.Buffer
			if err := RunCLI(c, &out, "", []string{"models", tc.filter}, false, false); err != nil {
				t.Fatalf("filtered model list failed: %v", err)
			}
			got := stripANSI(out.String())
			if !strings.Contains(got, tc.want) {
				t.Fatalf("output missing %q:\n%s", tc.want, got)
			}
			for _, forbidden := range tc.forbidden {
				if strings.Contains(got, forbidden) {
					t.Errorf("output unexpectedly contains %q:\n%s", forbidden, got)
				}
			}
			if !rec.saw("GET", "/models") || rec.sawQuery("GET /models?") {
				t.Fatalf("filtered model list was not requested globally:\n%s", rec.all())
			}
		})
	}
}

func TestCLIGlobalModelsListWorksAcrossProjectStates(t *testing.T) {
	const modelsHTML = `<div data-model-id="m-1" data-model-name="Sonnet"
		data-model-provider="anthropic" data-model-model="claude-sonnet-4"></div>`
	states := []struct {
		name     string
		projects string
	}{
		{name: "zero projects", projects: `{"projects":[]}`},
		{name: "single project", projects: `{"projects":[{"id":"p1","name":"solo"}]}`},
		{name: "multiple projects", projects: cliProjects},
	}
	commands := []struct {
		name string
		args []string
	}{
		{name: "bare", args: []string{"models"}},
		{name: "list", args: []string{"models", "list"}},
		{name: "filtered", args: []string{"models", "Sonnet"}},
	}
	for _, state := range states {
		state := state
		for _, command := range commands {
			command := command
			for _, jsonOutput := range []bool{false, true} {
				name := state.name + " " + command.name + " plain"
				if jsonOutput {
					name = state.name + " " + command.name + " JSON"
				}
				t.Run(name, func(t *testing.T) {
					c, rec := cliServer(t, map[string]string{
						"/api/projects": state.projects,
						"/models":       modelsHTML,
					})
					var out bytes.Buffer
					if err := RunCLI(c, &out, "", command.args, false, jsonOutput); err != nil {
						t.Fatalf("global models list failed: %v", err)
					}
					if !rec.saw("GET", "/models") || rec.sawQuery("GET /models?") {
						t.Fatalf("models list was not requested globally; request URLs:\n%s", strings.Join(rec.urlsSnapshot(), "\n"))
					}
					if jsonOutput {
						var models []client.LLMModel
						if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &models); err != nil {
							t.Fatalf("global JSON output is not a raw model array: %v\n%s", err, out.String())
						}
						if len(models) != 1 || models[0].ID != "m-1" {
							t.Fatalf("models = %+v", models)
						}
					} else if !strings.Contains(out.String(), "Sonnet") {
						t.Fatalf("plain global output missing model:\n%s", out.String())
					}
				})
			}
		}
	}
}

func TestCLIModelsHelpDocumentsSafeAddWorkflow(t *testing.T) {
	c, err := client.New("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunCLI(c, &out, "", []string{"help", "models"}, false, false); err != nil {
		t.Fatalf("models help failed without a backend: %v", err)
	}
	for _, want := range []string{"models add", "models edit", "--api-key", "--api-key-stdin", "--oauth", "ollama"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("CLI models help missing %q:\n%s", want, out.String())
		}
	}
}

func TestCLIModelsAddUsesStdinAndRefreshesWithoutLeakingAPIKey(t *testing.T) {
	secret := "cli-model-api-key"
	var postForm url.Values
	var postCount, listCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			switch r.Method {
			case http.MethodPost:
				postCount++
				if err := r.ParseForm(); err != nil {
					t.Fatal(err)
				}
				postForm = r.PostForm
				w.WriteHeader(http.StatusOK)
			case http.MethodGet:
				listCount++
				_, _ = io.WriteString(w, `<div data-model-id="m-openai" data-model-name="OpenAI" data-model-provider="openai" data-model-model="gpt-4o"></div>`)
			default:
				t.Fatalf("unexpected models method %s", r.Method)
			}
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = RunCLIWithInput(c, &out, strings.NewReader(secret+"\n"), "", []string{
		"models", "add", "openai", "OpenAI", "gpt-4o", "--api-key-stdin",
	}, false, true)
	if err != nil {
		t.Fatalf("models add failed: %v", err)
	}
	if postCount != 1 || listCount != 1 {
		t.Fatalf("POST/refresh counts = %d/%d, want 1/1", postCount, listCount)
	}
	for key, want := range map[string]string{
		"name": "OpenAI", "provider": "openai", "model": "gpt-4o", "openai_auth_type": "api_key", "api_key": secret,
	} {
		if got := postForm.Get(key); got != want {
			t.Errorf("form[%q] = %q, want %q", key, got, want)
		}
	}
	if strings.Contains(out.String(), secret) {
		t.Fatalf("API key leaked in JSON output: %s", out.String())
	}
	var result modelAddOutput
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &result); err != nil {
		t.Fatalf("invalid add JSON: %v\n%s", err, out.String())
	}
	if result.Status != "added OpenAI" || len(result.Models) != 1 || result.Models[0].ID != "m-openai" {
		t.Fatalf("add JSON = %+v", result)
	}
}

func TestCLIModelsAddOllamaAndOAuthHandoff(t *testing.T) {
	t.Run("ollama", func(t *testing.T) {
		var postForm url.Values
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method + " " + r.URL.Path {
			case "POST /models":
				if err := r.ParseForm(); err != nil {
					t.Fatal(err)
				}
				postForm = r.PostForm
				w.WriteHeader(http.StatusOK)
			case "GET /models":
				_, _ = io.WriteString(w, `<div data-model-id="m-ollama" data-model-name="Local Ollama" data-model-provider="ollama" data-model-model="llama3.1:8b"></div>`)
			default:
				t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
			}
		}))
		defer srv.Close()
		c, err := client.New(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := RunCLIWithInput(c, &out, nil, "", []string{"models", "add", "ollama", "Local Ollama", "llama3.1:8b", "--endpoint", "http://localhost:11434"}, false, false); err != nil {
			t.Fatalf("ollama add failed: %v", err)
		}
		if postForm.Get("provider") != "ollama" || postForm.Get("ollama_base_url") != "http://localhost:11434" || postForm.Get("api_key") != "" {
			t.Fatalf("ollama form = %v", postForm)
		}
		if !strings.Contains(stripANSI(out.String()), "Local Ollama") {
			t.Fatalf("refreshed Ollama list missing from output:\n%s", out.String())
		}
	})

	t.Run("oauth", func(t *testing.T) {
		var postForm url.Values
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method + " " + r.URL.Path {
			case "POST /models":
				if err := r.ParseForm(); err != nil {
					t.Fatal(err)
				}
				postForm = r.PostForm
				w.WriteHeader(http.StatusOK)
			case "GET /models":
				_, _ = io.WriteString(w, `<div data-model-id="m-oauth" data-model-name="Claude OAuth" data-model-provider="anthropic" data-model-model="claude-sonnet-4-6"></div>`)
			case "GET /models/m-oauth/oauth/status":
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"status":"not_connected"}`)
			default:
				t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
			}
		}))
		defer srv.Close()
		c, err := client.New(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := RunCLIWithInput(c, &out, nil, "", []string{"models", "add", "anthropic", "Claude OAuth", "claude-sonnet-4-6", "--oauth"}, false, true); err != nil {
			t.Fatalf("oauth add failed: %v", err)
		}
		if postForm.Get("anthropic_auth_type") != "oauth" || postForm.Get("auth_method") != "oauth" || postForm.Get("api_key") != "" {
			t.Fatalf("OAuth form = %v", postForm)
		}
		var result modelAddOutput
		if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &result); err != nil {
			t.Fatalf("invalid OAuth JSON: %v\n%s", err, out.String())
		}
		if result.OAuthStatus != "not_connected" || result.AuthorizationURL == "" || !strings.Contains(result.AuthorizationURL, "/models/m-oauth/oauth/initiate") {
			t.Fatalf("OAuth result = %+v", result)
		}
		if strings.Contains(result.Status, "connected") {
			t.Fatalf("OAuth result incorrectly claims connection: %+v", result)
		}
	})
}

func TestCLIModelsAddOAuthRefreshFailureStillProvidesHandoff(t *testing.T) {
	var postCount, listCount, oauthStatusCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /models":
			postCount++
			w.WriteHeader(http.StatusOK)
		case "GET /models":
			listCount++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":"models temporarily unavailable"}`)
		case "GET /models/m-oauth/oauth/status":
			oauthStatusCount++
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"status":"not_connected"}`)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	for _, jsonOutput := range []bool{false, true} {
		var out bytes.Buffer
		if err := RunCLIWithInput(c, &out, nil, "", []string{"models", "add", "anthropic", "Claude OAuth", "claude-sonnet-4-6", "--oauth"}, false, jsonOutput); err != nil {
			t.Fatalf("OAuth add with refresh failure failed: %v", err)
		}
		if !jsonOutput {
			plain := stripANSI(out.String())
			for _, want := range []string{"OAuth authorization is required", "status: unknown; model refresh unavailable", srv.URL + "/models"} {
				if !strings.Contains(plain, want) {
					t.Fatalf("OAuth refresh-failure output missing %q:\n%s", want, plain)
				}
			}
			if strings.Contains(plain, "OAuth connected") {
				t.Fatalf("OAuth refresh-failure output incorrectly claims connection:\n%s", plain)
			}
			continue
		}

		var result modelAddOutput
		if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &result); err != nil {
			t.Fatalf("invalid OAuth refresh-failure JSON: %v\n%s", err, out.String())
		}
		if result.Status != "added Claude OAuth" || result.OAuthStatus != "unknown" {
			t.Fatalf("OAuth refresh-failure result = %+v", result)
		}
		if want := srv.URL + "/models"; result.AuthorizationURL != want {
			t.Fatalf("authorization URL = %q, want %q", result.AuthorizationURL, want)
		}
	}
	if postCount != 2 || listCount != 2 || oauthStatusCount != 0 {
		t.Fatalf("POST/list/status counts = %d/%d/%d, want 2/2/0", postCount, listCount, oauthStatusCount)
	}
}

func TestCLIModelsAddOAuthPostCreateAuthenticationFailuresDoNotClaimSuccess(t *testing.T) {
	for _, tc := range []struct {
		name            string
		failurePath     string
		wantListCount   int
		wantStatusCount int
	}{
		{name: "list refresh", failurePath: "/models", wantListCount: 1},
		{name: "oauth status", failurePath: "/models/m-oauth/oauth/status", wantListCount: 1, wantStatusCount: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var postCount, listCount, oauthStatusCount int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "POST /models":
					postCount++
					w.WriteHeader(http.StatusOK)
				case "GET /models":
					listCount++
					if tc.failurePath == r.URL.Path {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusUnauthorized)
						_, _ = io.WriteString(w, `{"error":"session expired"}`)
						return
					}
					_, _ = io.WriteString(w, `<div data-model-id="m-oauth" data-model-name="Claude OAuth" data-model-provider="anthropic" data-model-model="claude-sonnet-4-6"></div>`)
				case "GET /models/m-oauth/oauth/status":
					oauthStatusCount++
					if tc.failurePath == r.URL.Path {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusUnauthorized)
						_, _ = io.WriteString(w, `{"error":"session expired"}`)
						return
					}
					t.Fatalf("unexpected OAuth status request")
				default:
					t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
				}
			}))
			defer srv.Close()
			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}

			var out bytes.Buffer
			err = RunCLIWithInput(c, &out, nil, "", []string{"models", "add", "anthropic", "Claude OAuth", "claude-sonnet-4-6", "--oauth"}, false, true)
			if err == nil || !strings.Contains(err.Error(), "requires sign-in") {
				t.Fatalf("post-create authentication error = %v", err)
			}
			if postCount != 1 || listCount != tc.wantListCount || oauthStatusCount != tc.wantStatusCount {
				t.Fatalf("POST/list/status counts = %d/%d/%d, want 1/%d/%d", postCount, listCount, oauthStatusCount, tc.wantListCount, tc.wantStatusCount)
			}
			for _, forbidden := range []string{"added Claude OAuth", "authorization_url", "oauth_status", "OAuth authorization"} {
				if strings.Contains(out.String(), forbidden) || strings.Contains(err.Error(), forbidden) {
					t.Fatalf("post-create authentication failure claimed success/handoff %q:\nerror: %v\noutput: %s", forbidden, err, out.String())
				}
			}
			if out.Len() != 0 {
				t.Fatalf("post-create authentication failure wrote JSON output: %s", out.String())
			}
		})
	}
}

func TestCLIModelsAddRejectsInvalidInputAndBackendFailuresWithoutRefresh(t *testing.T) {
	t.Run("local validation", func(t *testing.T) {
		for _, args := range [][]string{
			{"models", "add", "openai", "OpenAI", "gpt-4o"},
			{"models", "add", "ollama", "Local", "llama3", "--endpoint", "http:/missing-host"},
			{"models", "add", "ollama", "Local", "llama3", "--api-key-stdin"},
			{"models", "add", "openai", "OpenAI", "gpt-4o", "--api-key", "visible-secret"},
		} {
			c, rec := cliServer(t, nil)
			var out bytes.Buffer
			err := RunCLIWithInput(c, &out, strings.NewReader("unused"), "", args, false, false)
			if err == nil {
				t.Fatalf("%v unexpectedly succeeded", args)
			}
			if strings.Contains(err.Error(), "visible-secret") || strings.Contains(out.String(), "visible-secret") {
				t.Fatalf("%v echoed an unsupported secret: error=%v output=%s", args, err, out.String())
			}
			if strings.Contains(rec.all(), "POST /models") || strings.Contains(rec.all(), "GET /models") || out.Len() != 0 {
				t.Fatalf("%v made a mutation/refresh or wrote output:\n%s\n%s", args, rec.all(), out.String())
			}
		}
	})

	t.Run("authentication failure skips refresh and redacts secret", func(t *testing.T) {
		secret := "authentication-model-secret"
		var postCount, listCount int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method + " " + r.URL.Path {
			case "POST /models":
				postCount++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"error":"rejected `+secret+`"}`)
			case "GET /models":
				listCount++
				_, _ = io.WriteString(w, `<div></div>`)
			default:
				t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
			}
		}))
		defer srv.Close()
		c, err := client.New(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		err = RunCLIWithInput(c, &out, strings.NewReader(secret), "", []string{"models", "add", "openai", "OpenAI", "gpt-4o", "--api-key-stdin"}, false, true)
		if err == nil || !strings.Contains(err.Error(), "requires sign-in") {
			t.Fatalf("authentication failure error = %v", err)
		}
		if postCount != 1 || listCount != 0 {
			t.Fatalf("POST/refresh counts = %d/%d, want 1/0", postCount, listCount)
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(out.String(), secret) {
			t.Fatalf("secret leaked after authentication failure:\nerror: %v\noutput: %s", err, out.String())
		}
	})

	t.Run("backend validation redacts secret", func(t *testing.T) {
		secret := "backend-reflected-model-secret"
		var postCount, listCount int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method + " " + r.URL.Path {
			case "POST /models":
				postCount++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"rejected `+secret+`"}`)
			case "GET /models":
				listCount++
				_, _ = io.WriteString(w, `<div></div>`)
			default:
				t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
			}
		}))
		defer srv.Close()
		c, err := client.New(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		err = RunCLIWithInput(c, &out, strings.NewReader(secret), "", []string{"models", "add", "openai", "OpenAI", "gpt-4o", "--api-key-stdin"}, false, true)
		if err == nil {
			t.Fatal("backend rejection unexpectedly succeeded")
		}
		if postCount != 1 || listCount != 0 {
			t.Fatalf("POST/refresh counts = %d/%d, want 1/0", postCount, listCount)
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(out.String(), secret) {
			t.Fatalf("secret leaked after backend rejection:\nerror: %v\noutput: %s", err, out.String())
		}
	})
}

func TestCLIGlobalModelsEmptyJSONIsRawArray(t *testing.T) {
	c, rec := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/models":       `<div></div>`,
	})
	var out bytes.Buffer
	if err := RunCLI(c, &out, "", []string{"models", "list"}, false, true); err != nil {
		t.Fatalf("empty global models list failed: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "[]" {
		t.Fatalf("empty global models JSON = %q, want raw []", got)
	}
	if !rec.sawQuery("GET /models") || rec.sawQuery("GET /models?") {
		t.Fatalf("empty models list was not requested globally; request URLs:\n%s", strings.Join(rec.urlsSnapshot(), "\n"))
	}
}

func TestCLIProjectScopedModelsActionsRejectMissingOrAmbiguousProject(t *testing.T) {
	states := []struct {
		name     string
		projects string
		want     string
	}{
		{name: "zero projects", projects: `{"projects":[]}`, want: "-project <name|id>"},
		{name: "multiple projects", projects: cliProjects, want: "multiple projects"},
	}
	actions := []struct {
		name  string
		args  []string
		force bool
	}{
		{name: "capacity", args: []string{"models", "capacity"}},
		{name: "default", args: []string{"models", "default", "Sonnet"}},
		{name: "delete", args: []string{"models", "delete", "Sonnet"}, force: true},
	}
	for _, state := range states {
		state := state
		for _, action := range actions {
			action := action
			for _, jsonOutput := range []bool{false, true} {
				mode := "plain"
				if jsonOutput {
					mode = "JSON"
				}
				t.Run(state.name+" "+action.name+" "+mode, func(t *testing.T) {
					c, rec := cliServer(t, map[string]string{"/api/projects": state.projects})
					var out bytes.Buffer
					err := RunCLI(c, &out, "", action.args, action.force, jsonOutput)
					if err == nil || !strings.Contains(err.Error(), state.want) {
						t.Fatalf("err = %v, want %q", err, state.want)
					}
					if rec.saw("GET", "/models") || rec.saw("GET", "/api/capacity/models") || rec.saw("GET", "/api/analytics/usage") || rec.saw("POST", "/models/m-1/set-default") || rec.saw("DELETE", "/models/m-1") {
						t.Fatalf("guarded models action made an unscoped request:\n%s", rec.all())
					}
					if out.Len() != 0 {
						t.Fatalf("guarded models action wrote output before failing: %q", out.String())
					}
				})
			}
		}
	}
}

func TestCLISingleProjectModelsActionsRemainScoped(t *testing.T) {
	const modelsHTML = `<div data-model-id="m-1" data-model-name="Sonnet"
		data-model-provider="anthropic" data-model-model="claude-sonnet-4"></div>`
	cases := []struct {
		name       string
		args       []string
		force      bool
		wantMethod string
		wantPath   string
	}{
		{name: "capacity", args: []string{"models", "capacity"}, wantMethod: "GET", wantPath: "/api/analytics/usage?project_id=p1"},
		{name: "default", args: []string{"models", "default", "Sonnet"}, wantMethod: "POST", wantPath: "/models/m-1/set-default"},
		{name: "delete", args: []string{"models", "delete", "Sonnet"}, force: true, wantMethod: "DELETE", wantPath: "/models/m-1"},
	}
	for _, tc := range cases {
		tc := tc
		for _, jsonOutput := range []bool{false, true} {
			mode := "plain"
			if jsonOutput {
				mode = "JSON"
			}
			t.Run(tc.name+" "+mode, func(t *testing.T) {
				c, rec := cliServer(t, map[string]string{
					"/api/projects":        `{"projects":[{"id":"p1","name":"solo"}]}`,
					"/models":              modelsHTML,
					"/api/capacity/models": `[]`,
					"/api/analytics/usage": `{}`,
				})
				var out bytes.Buffer
				if err := RunCLI(c, &out, "", tc.args, tc.force, jsonOutput); err != nil {
					t.Fatalf("scoped models action failed: %v\n%s", err, rec.all())
				}
				if tc.wantPath == "/api/analytics/usage?project_id=p1" {
					if !rec.sawQuery("GET " + tc.wantPath) {
						t.Fatalf("capacity usage request lost implicit scope; request URLs:\n%s", strings.Join(rec.urlsSnapshot(), "\n"))
					}
				} else if !rec.saw(tc.wantMethod, tc.wantPath) {
					t.Fatalf("missing %s %s:\n%s", tc.wantMethod, tc.wantPath, rec.all())
				}
				if tc.name != "capacity" && !rec.sawQuery("GET /models?project_id=p1") {
					t.Fatalf("model mutation lookup was not scoped to the implicit project; request URLs:\n%s", strings.Join(rec.urlsSnapshot(), "\n"))
				}
				if jsonOutput {
					var scoped cliScopedJSONOutput
					if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &scoped); err != nil {
						t.Fatalf("scoped model JSON is not a valid scope envelope: %v\n%s", err, out.String())
					}
					if scoped.ProjectID != "p1" || scoped.ProjectName != "solo" || len(scoped.Data) == 0 {
						t.Fatalf("scoped model JSON = %+v", scoped)
					}
				} else if !strings.Contains(out.String(), "project: solo (project_id=p1)") {
					t.Fatalf("plain scoped output omitted implicit project identity:\n%s", out.String())
				}
			})
		}
	}
}

func TestCLIGlobalProjectListRemainsUsableWithoutProject(t *testing.T) {
	c, rec := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "", []string{"projects", "list"}, false, true); err != nil {
		t.Fatalf("projects list failed without a project: %v", err)
	}
	var projects []client.Project
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &projects); err != nil {
		t.Fatalf("projects list output is not the existing JSON shape: %v\n%s", err, out.String())
	}
	if len(projects) != 2 || projects[0].ID != "p1" || projects[1].ID != "p2" {
		t.Fatalf("projects list = %+v", projects)
	}
	if rec.saw("GET", "/tasks") || rec.saw("GET", "/alerts") || rec.saw("GET", "/automations") {
		t.Fatalf("projects list dispatched a project-scoped endpoint:\n%s", rec.all())
	}
}

func TestCLISingleProjectImplicitScopeIsVisibleInPlainAndJSONOutput(t *testing.T) {
	const board = `<div data-task-id="t-1" data-task-status="running" data-task-category="active">
		<a href="/tasks/t-1" title="Implicit project task">Implicit project task</a>
	</div>`
	c, rec := cliServer(t, map[string]string{
		"/api/projects": `{"projects":[{"id":"p1","name":"solo"}]}`,
		"/tasks":        board,
	})

	var plain bytes.Buffer
	if err := RunCLI(c, &plain, "", []string{"tasks"}, false, false); err != nil {
		t.Fatalf("implicit plain tasks failed: %v", err)
	}
	if !strings.Contains(plain.String(), "project: solo (project_id=p1)") || !strings.Contains(plain.String(), "Implicit project task") {
		t.Fatalf("plain output did not identify its implicit scope:\n%s", plain.String())
	}
	if !rec.sawQuery("GET /tasks?project_id=p1") {
		t.Fatalf("implicit plain task request lost project scope:\n%s", rec.all())
	}

	var machine bytes.Buffer
	if err := RunCLI(c, &machine, "", []string{"tasks"}, false, true); err != nil {
		t.Fatalf("implicit JSON tasks failed: %v", err)
	}
	var scoped struct {
		ProjectID   string          `json:"project_id"`
		ProjectName string          `json:"project_name"`
		Data        json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(machine.String())), &scoped); err != nil {
		t.Fatalf("implicit JSON output is not scoped JSON: %v\n%s", err, machine.String())
	}
	if scoped.ProjectID != "p1" || scoped.ProjectName != "solo" {
		t.Fatalf("implicit JSON scope = %q/%q, want p1/solo", scoped.ProjectID, scoped.ProjectName)
	}
	var tasks []client.Task
	if err := json.Unmarshal(scoped.Data, &tasks); err != nil {
		t.Fatalf("scoped JSON data is not the task list: %v\n%s", err, scoped.Data)
	}
	if len(tasks) != 1 || tasks[0].ID != "t-1" {
		t.Fatalf("scoped JSON tasks = %+v", tasks)
	}
}

func TestCLIExplicitProjectNameIDAndPrefixRemainScoped(t *testing.T) {
	const board = `<div data-task-id="t-1" data-task-status="running" data-task-category="active">
		<a href="/tasks/t-1" title="Scoped task">Scoped task</a>
	</div>`
	c, rec := cliServer(t, map[string]string{
		"/api/projects": `{"projects":[{"id":"project-alpha","name":"Demo Project"},{"id":"project-beta","name":"Other Project"}]}`,
		"/tasks":        board,
	})

	for _, tc := range []struct {
		name string
		ref  string
		want string
	}{
		{name: "name", ref: "Demo Project", want: "project-alpha"},
		{name: "full ID", ref: "project-beta", want: "project-beta"},
		{name: "unique prefix", ref: "project-al", want: "project-alpha"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := RunCLI(c, &bytes.Buffer{}, tc.ref, []string{"tasks"}, false, false); err != nil {
				t.Fatalf("explicit project %q failed: %v", tc.ref, err)
			}
			if !rec.sawQuery("GET /tasks?project_id=" + tc.want) {
				t.Fatalf("project %q was not preserved on the task request:\n%s", tc.ref, rec.all())
			}
		})
	}
}

// A CLI command must run against the requested project, not the first one.
func TestCLISelectsRequestedProject(t *testing.T) {
	c, rec := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/tasks":        `<div></div>`,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "other", []string{"tasks"}, false, false); err != nil {
		t.Fatalf("tasks failed: %v", err)
	}
	if !rec.sawQuery("project_id=p2") {
		t.Fatalf("command was not scoped to the requested project:\n%s",
			strings.Join(rec.urls, "\n"))
	}
	if got := rec.count("GET", "/api/capacity/projects"); got != 0 {
		t.Fatalf("CLI project setup made %d per-project capacity requests, want 0:\n%s", got, rec.all())
	}
}

func TestCLIUnknownProjectFails(t *testing.T) {
	c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects})

	var out bytes.Buffer
	err := RunCLI(c, &out, "nope", []string{"tasks"}, false, false)
	if err == nil {
		t.Fatal("expected an error for an unknown project")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("err = %v", err)
	}
	if got := rec.count("GET", "/api/capacity/projects"); got != 0 {
		t.Fatalf("unknown project lookup made %d per-project capacity requests, want 0:\n%s", got, rec.all())
	}
}

func TestCLINoProjectDoesNotFetchCapacityOrRunCommand(t *testing.T) {
	c, rec := cliServer(t, map[string]string{
		"/api/projects": `{"projects":[]}`,
	})

	var out bytes.Buffer
	err := RunCLI(c, &out, "", []string{"tasks"}, false, false)
	if err == nil || !strings.Contains(err.Error(), "no project selected") {
		t.Fatalf("err = %v, want no-project error", err)
	}
	if got := rec.count("GET", "/api/capacity/projects"); got != 0 {
		t.Fatalf("no-project startup made %d per-project capacity requests, want 0:\n%s", got, rec.all())
	}
	if got := rec.count("GET", "/tasks"); got != 0 {
		t.Fatalf("no-project command made %d task requests, want 0:\n%s", got, rec.all())
	}
}

func TestCLIBriefingCommandsRequireProjectWhenProjectListIsEmpty(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		fetchPath   string
		triggerPath string
	}{
		{name: "pulse", args: []string{"pulse"}, fetchPath: "/upcoming"},
		{name: "pulse show", args: []string{"pulse", "show"}, fetchPath: "/upcoming"},
		{name: "pulse summary", args: []string{"pulse", "summary"}, fetchPath: "/upcoming", triggerPath: "/upcoming/summary"},
		{name: "reflection", args: []string{"reflection"}, fetchPath: "/history"},
		{name: "reflection show", args: []string{"reflection", "show"}, fetchPath: "/history"},
		{name: "reflection summary", args: []string{"reflection", "summary"}, fetchPath: "/history", triggerPath: "/history/summary"},
		{name: "grades", args: []string{"grades"}, fetchPath: "/history"},
		{name: "grades show", args: []string{"grades", "show"}, fetchPath: "/history"},
		{name: "grades run", args: []string{"grades", "run"}, fetchPath: "/history", triggerPath: "/history/grade-ideas"},
		{name: "insights", args: []string{"insights"}, fetchPath: "/insights"},
		{name: "insights show", args: []string{"insights", "show"}, fetchPath: "/insights"},
		{name: "insights analyze", args: []string{"insights", "analyze"}, fetchPath: "/insights", triggerPath: "/insights/analyze"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			c, rec := cliServer(t, map[string]string{
				"/api/projects": `{"projects":[]}`,
			})
			err := RunCLI(c, &bytes.Buffer{}, "", tc.args, false, false)
			if err == nil || !strings.Contains(err.Error(), "no project selected — use /project <name>") {
				t.Fatalf("%v: err = %v, want actionable no-project guidance", tc.args, err)
			}
			if rec.saw("GET", tc.fetchPath) || (tc.triggerPath != "" && rec.saw("POST", tc.triggerPath)) {
				t.Fatalf("%v made an unscoped briefing/analysis request:\n%s", tc.args, rec.all())
			}
		})
	}
}

func TestCLIBriefingCommandsPreserveSupportedActions(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		fetchPath   string
		triggerPath string
		body        string
	}{
		{name: "pulse bare", args: []string{"pulse"}, fetchPath: "/upcoming", body: "<div>upcoming briefing</div>"},
		{name: "pulse show", args: []string{"pulse", "show"}, fetchPath: "/upcoming", body: "<div>upcoming briefing</div>"},
		{name: "pulse summary", args: []string{"pulse", "summary"}, fetchPath: "/upcoming", triggerPath: "/upcoming/summary", body: "<div>upcoming briefing</div>"},
		{name: "reflection bare", args: []string{"reflection"}, fetchPath: "/history", body: "<div>history debrief</div>"},
		{name: "reflection show", args: []string{"reflection", "show"}, fetchPath: "/history", body: "<div>history debrief</div>"},
		{name: "reflection summary", args: []string{"reflection", "summary"}, fetchPath: "/history", triggerPath: "/history/summary", body: "<div>history debrief</div>"},
		{name: "grades bare", args: []string{"grades"}, fetchPath: "/history", body: `<div id="idea-grade-content">grades</div>`},
		{name: "grades show", args: []string{"grades", "show"}, fetchPath: "/history", body: `<div id="idea-grade-content">grades</div>`},
		{name: "grades run", args: []string{"grades", "run"}, fetchPath: "/history", triggerPath: "/history/grade-ideas", body: `<div id="idea-grade-content">grades</div>`},
		{name: "insights bare", args: []string{"insights"}, fetchPath: "/insights", body: "<div>insights</div>"},
		{name: "insights show", args: []string{"insights", "show"}, fetchPath: "/insights", body: "<div>insights</div>"},
		{name: "insights analyze", args: []string{"insights", "analyze"}, fetchPath: "/insights", triggerPath: "/insights/analyze", body: "<div>insights</div>"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bodies := map[string]string{
				"/api/projects": cliProjects,
				tc.fetchPath:    tc.body,
			}
			if tc.triggerPath != "" {
				bodies[tc.triggerPath] = "ok"
			}
			c, rec := cliServer(t, bodies)

			if err := RunCLI(c, &bytes.Buffer{}, "demo", tc.args, false, false); err != nil {
				t.Fatalf("RunCLI(%v): %v", tc.args, err)
			}
			if got := rec.count("GET", tc.fetchPath); got != 1 {
				t.Fatalf("RunCLI(%v) made %d GET requests to %s, want 1:\n%s", tc.args, got, tc.fetchPath, rec.all())
			}

			requests := rec.urlsSnapshot()
			fetchIndex := -1
			for i, request := range requests {
				if strings.HasPrefix(request, "GET "+tc.fetchPath+"?") {
					fetchIndex = i
					if !strings.Contains(request, "project_id=p1") {
						t.Fatalf("RunCLI(%v) sent an unscoped fetch request: %s", tc.args, request)
					}
				}
			}
			if fetchIndex == -1 {
				t.Fatalf("RunCLI(%v) did not make the expected fetch request:\n%s", tc.args, strings.Join(requests, "\n"))
			}

			if tc.triggerPath == "" {
				if calls := rec.all(); strings.Contains(calls, "POST ") {
					t.Fatalf("RunCLI(%v) unexpectedly made a POST request:\n%s", tc.args, calls)
				}
				return
			}

			if got := rec.count("POST", tc.triggerPath); got != 1 {
				t.Fatalf("RunCLI(%v) made %d POST requests to %s, want 1:\n%s", tc.args, got, tc.triggerPath, rec.all())
			}
			postIndex := -1
			for i, request := range requests {
				if strings.HasPrefix(request, "POST "+tc.triggerPath+"?") {
					postIndex = i
					if !strings.Contains(request, "project_id=p1") {
						t.Fatalf("RunCLI(%v) sent an unscoped trigger request: %s", tc.args, request)
					}
				}
			}
			if postIndex == -1 || postIndex >= fetchIndex {
				t.Fatalf("RunCLI(%v) requests = %s, want POST %s before one GET %s", tc.args, strings.Join(requests, "\n"), tc.triggerPath, tc.fetchPath)
			}
		})
	}
}

func TestCLIBriefingCommandsRejectInvalidOperands(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		usage string
	}{
		{name: "pulse unknown action", args: []string{"pulse", "sumary"}, usage: "usage: pulse [show|summary]"},
		{name: "pulse surplus operand", args: []string{"pulse", "summary", "now"}, usage: "usage: pulse [show|summary]"},
		{name: "reflection unknown action", args: []string{"reflection", "refresh"}, usage: "usage: reflection [show|summary]"},
		{name: "reflection surplus operand", args: []string{"reflection", "show", "extra"}, usage: "usage: reflection [show|summary]"},
		{name: "grades unknown action", args: []string{"grades", "rerun"}, usage: "usage: grades [show|run]"},
		{name: "grades surplus operand", args: []string{"grades", "run", "again"}, usage: "usage: grades [show|run]"},
		{name: "insights unknown action", args: []string{"insights", "analyse"}, usage: "usage: insights [show|analyze]"},
		{name: "insights surplus operand", args: []string{"insights", "analyze", "tomorrow"}, usage: "usage: insights [show|analyze]"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := cliServer(t, nil)
			err := RunCLI(c, &bytes.Buffer{}, "", tc.args, false, false)
			if err == nil || !strings.Contains(err.Error(), tc.usage) {
				t.Fatalf("RunCLI(%v) error = %v, want %q", tc.args, err, tc.usage)
			}
			if calls := rec.all(); calls != "" {
				t.Fatalf("RunCLI(%v) made backend requests:\n%s", tc.args, calls)
			}
		})
	}
}

func TestCLIUnknownCommandFails(t *testing.T) {
	for _, name := range []string{"frobnicate", "build", "/build"} {
		t.Run(name, func(t *testing.T) {
			c, rec := cliServer(t, nil)

			var out bytes.Buffer
			err := RunCLI(c, &out, "", []string{name}, false, false)
			if err == nil || !strings.Contains(err.Error(), "unknown command") {
				t.Fatalf("err = %v, want unknown command", err)
			}
			if calls := rec.all(); calls != "" {
				t.Fatalf("unsupported command made a backend request:\n%s", calls)
			}
		})
	}
}

func TestCLINoArgsFails(t *testing.T) {
	c, _ := cliServer(t, nil)
	if err := RunCLI(c, &bytes.Buffer{}, "", nil, false, false); err == nil {
		t.Fatal("expected an error with no command")
	}
}

// Leading slashes are accepted so TUI lines can be pasted into the shell.
func TestCLIAcceptsLeadingSlash(t *testing.T) {
	c, _ := client.New("http://127.0.0.1:1")
	var out bytes.Buffer
	if err := RunCLI(c, &out, "", []string{"/help"}, false, false); err != nil {
		t.Fatalf("/help failed: %v", err)
	}
	if !strings.Contains(out.String(), "tasks") {
		t.Errorf("output:\n%s", out.String())
	}
}

// A command that fails on the backend must exit non-zero.
func TestCLIReportsBackendErrors(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r.Method, r.URL.Path)
		if r.URL.Path == "/api/projects" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(cliProjects))
			return
		}
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c, _ := client.New(srv.URL)
	if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"tasks"}, false, false); err == nil {
		t.Fatal("expected a backend error")
	}
}

// Chat in CLI mode sends the message and prints the agent's reply.
func TestCLIChatSendsAndPrintsReply(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r.Method, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/projects":
			_, _ = w.Write([]byte(cliProjects))
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/message":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"message_id":"m1","status":"processing"}`))
		case strings.HasPrefix(r.URL.Path, "/api/chat/message/"):
			_, _ = w.Write([]byte(`{"status":"completed","response":"docs shipped"}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	c, _ := client.New(srv.URL)
	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"chat", "ship the docs"}, false, false); err != nil {
		t.Fatalf("chat failed: %v", err)
	}
	if !rec.saw("POST", "/api/chat/message") {
		t.Fatalf("no chat post, calls:\n%s", rec.all())
	}
	if !strings.Contains(out.String(), "docs shipped") {
		t.Errorf("output missing reply:\n%s", out.String())
	}
}

func TestCLIChatFlushesIncrementalOutputBeforeCompletion(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, cliProjects)
		case "/api/chat/message":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"message_id":"exec-1","status":"processing"}`)
		case "/events/chat/exec-1":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: hello\n\n")
			w.(http.Flusher).Flush()
			<-release
			fmt.Fprint(w, "data:  world\n\nevent: done\ndata: completed\n\n")
		case "/api/chat/message/exec-1":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"message_id":"exec-1","status":"completed","response":"hello world"}`)
		default:
			t.Fatalf("unexpected request %s", r.URL.String())
		}
	}))
	defer srv.Close()
	c, _ := client.New(srv.URL)
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := RunCLIContext(context.Background(), c, writer, "demo", []string{"chat", "go"}, false, false)
		_ = writer.Close()
		done <- err
	}()

	first := make([]byte, 5)
	if _, err := io.ReadFull(reader, first); err != nil || string(first) != "hello" {
		t.Fatalf("first output = %q, err=%v", first, err)
	}
	close(release)
	rest, _ := io.ReadAll(reader)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	writer.Close()
	if got := string(first) + string(rest); got != "hello world\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestCLIChatReconnectsByOffsetWithoutDuplicateOutput(t *testing.T) {
	var streams, statusCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, cliProjects)
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/message":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"message_id":"exec-1","status":"processing"}`)
		case r.URL.Path == "/api/chat/message/exec-1":
			statusCalls++
			w.Header().Set("Content-Type", "application/json")
			if statusCalls == 1 {
				fmt.Fprint(w, `{"message_id":"exec-1","status":"processing"}`)
			} else {
				fmt.Fprint(w, `{"message_id":"exec-1","status":"completed","response":"hello"}`)
			}
		case r.URL.Path == "/events/chat/exec-1":
			streams++
			w.Header().Set("Content-Type", "text/event-stream")
			if streams == 1 {
				if r.URL.Query().Get("offset") != "0" {
					t.Fatalf("first offset = %s", r.URL.Query().Get("offset"))
				}
				fmt.Fprint(w, "data: hel\n\n")
				return
			}
			if r.URL.Query().Get("offset") != "3" {
				t.Fatalf("resume offset = %s", r.URL.Query().Get("offset"))
			}
			fmt.Fprint(w, "data: lo\n\nevent: done\ndata: completed\n\n")
		default:
			t.Fatalf("unexpected request %s", r.URL.String())
		}
	}))
	defer srv.Close()
	c, _ := client.New(srv.URL)
	var out bytes.Buffer
	if err := RunCLIContext(context.Background(), c, &out, "demo", []string{"chat", "go"}, false, false); err != nil {
		t.Fatal(err)
	}
	if out.String() != "hello\n" || streams != 2 {
		t.Fatalf("output=%q streams=%d", out.String(), streams)
	}
}

func TestCLIChatStreamingJSONAndFailure(t *testing.T) {
	for _, tc := range []struct {
		name, terminal string
		wantErr        bool
	}{
		{name: "completed", terminal: "event: done\ndata: completed\n\n"},
		{name: "failed", terminal: "event: error\ndata: model failed\n\n", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/projects":
					fmt.Fprint(w, cliProjects)
				case "/api/chat/message":
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusCreated)
					fmt.Fprint(w, `{"message_id":"exec","status":"processing"}`)
				case "/events/chat/exec":
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: chunk\n\n"+tc.terminal)
				case "/api/chat/message/exec":
					w.Header().Set("Content-Type", "application/json")
					if tc.wantErr {
						fmt.Fprint(w, `{"message_id":"exec","status":"failed","error":"model failed"}`)
					} else {
						fmt.Fprint(w, `{"message_id":"exec","status":"completed","response":"chunk"}`)
					}
				}
			}))
			defer srv.Close()
			c, _ := client.New(srv.URL)
			var out bytes.Buffer
			err := RunCLIContext(context.Background(), c, &out, "demo", []string{"chat", "go"}, false, true)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v", err)
			}
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			if len(lines) != 2 {
				t.Fatalf("JSON lines = %q", lines)
			}
			for _, line := range lines {
				var record cliExecutionRecord
				if err := json.Unmarshal([]byte(line), &record); err != nil {
					t.Fatalf("invalid JSON %q: %v", line, err)
				}
				if record.ProjectID != "p1" || record.ExecID != "exec" {
					t.Fatalf("record = %#v", record)
				}
			}
		})
	}
}

func TestCLIExecutionTerminalSignalUsesAuthoritativeFailure(t *testing.T) {
	var statusCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/events/chat/exec":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: partial\n\nevent: done\ndata: completed\n\n")
		case "/api/chat/message/exec":
			statusCalls++
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"message_id":"exec","status":"failed","error":"authoritative failure"}`)
		default:
			t.Fatalf("unexpected request %s", r.URL.String())
		}
	}))
	defer srv.Close()
	c, _ := client.New(srv.URL)
	var out bytes.Buffer
	status := func() (*client.ChatStatus, error) { return c.GetChatStatus(context.Background(), "exec") }
	err := streamCLIExecution(context.Background(), c, &out, "p1", "", "exec", false, status)
	if err == nil || !strings.Contains(err.Error(), "authoritative failure") {
		t.Fatalf("error = %v", err)
	}
	if statusCalls != 1 || out.String() != "partial\n" {
		t.Fatalf("status calls=%d output=%q", statusCalls, out.String())
	}
}

func TestCLIExecutionErrorSignalCanReconcileToCompletion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/events/chat/exec":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: par\n\nevent: error\ndata: transient signal\n\n")
		case "/api/chat/message/exec":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"message_id":"exec","status":"completed","response":"partial"}`)
		default:
			t.Fatalf("unexpected request %s", r.URL.String())
		}
	}))
	defer srv.Close()
	c, _ := client.New(srv.URL)
	var out bytes.Buffer
	status := func() (*client.ChatStatus, error) { return c.GetChatStatus(context.Background(), "exec") }
	if err := streamCLIExecution(context.Background(), c, &out, "p1", "", "exec", false, status); err != nil {
		t.Fatal(err)
	}
	if out.String() != "partial\n" {
		t.Fatalf("output = %q", out.String())
	}
}

func TestCLIExecutionDeadlineIsNotSuccess(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()
	c, _ := client.New(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- streamCLIExecution(ctx, c, io.Discard, "p1", "", "exec", false, nil) }()
	<-started
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
}

func TestCLIQueuedPromotionDeadlineIsNotSuccess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	status := func() (*client.ChatStatus, error) {
		return &client.ChatStatus{MessageID: "input-1", Status: "queued"}, nil
	}
	if _, err := waitForCLIChatExecution(ctx, status, "input-1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
}

func TestCLIExecutionDisconnectExhaustionIsNotSuccess(t *testing.T) {
	var streams int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		streams++
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c, _ := client.New(srv.URL)
	status := func() (*client.ChatStatus, error) {
		return &client.ChatStatus{MessageID: "exec", Status: "processing"}, nil
	}
	err := streamCLIExecution(context.Background(), c, io.Discard, "p1", "", "exec", false, status)
	if !errors.Is(err, client.ErrEventStreamClosed) {
		t.Fatalf("error = %v, want event stream closed", err)
	}
	if streams != cliStreamReconnectLimit+1 {
		t.Fatalf("stream attempts = %d, want %d", streams, cliStreamReconnectLimit+1)
	}
}

func TestCLITaskReplyRecoversMissedPromotionThroughStatus(t *testing.T) {
	const board = `<div data-task-id="task-1" data-task-status="running" data-task-category="active"><a href="/tasks/task-1">Fix stream</a></div>`
	var statusCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/projects":
			fmt.Fprint(w, cliProjects)
		case "/tasks":
			fmt.Fprint(w, board)
		case "/events/live":
			w.Header().Set("Content-Type", "text/event-stream")
			return
		case "/tasks/task-1/thread":
			fmt.Fprint(w, `<div data-thread-input-id="input-1" data-task-id="task-1" data-input-mode="queued"></div>`)
		case "/api/chat/message/input-1":
			statusCalls++
			w.Header().Set("Content-Type", "application/json")
			if statusCalls == 1 {
				fmt.Fprint(w, `{"message_id":"input-1","status":"queued"}`)
			} else if statusCalls == 2 {
				fmt.Fprint(w, `{"message_id":"exec-2","status":"processing"}`)
			} else {
				fmt.Fprint(w, `{"message_id":"exec-2","status":"completed","response":"recovered"}`)
			}
		case "/events/chat/exec-2":
			fmt.Fprint(w, "data: recovered\n\nevent: done\ndata: completed\n\n")
		case "/api/chat/message/exec-2":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"message_id":"exec-2","status":"completed","response":"recovered"}`)
		default:
			t.Fatalf("unexpected request %s", r.URL.String())
		}
	}))
	defer srv.Close()
	c, _ := client.New(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var out bytes.Buffer
	if err := RunCLIContext(ctx, c, &out, "demo", []string{"tasks", "reply", "Fix stream", "|", "continue"}, false, false); err != nil {
		t.Fatal(err)
	}
	if statusCalls < 3 || out.String() != "recovered\n" {
		t.Fatalf("status calls=%d output=%q", statusCalls, out.String())
	}
}

func TestCLIChatCancellationClosesExecutionStream(t *testing.T) {
	streamClosed := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/projects":
			fmt.Fprint(w, cliProjects)
		case "/api/chat/message":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"message_id":"exec","status":"processing"}`)
		case "/events/chat/exec":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			close(streamClosed)
		}
	}))
	defer srv.Close()
	c, _ := client.New(srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunCLIContext(ctx, c, io.Discard, "demo", []string{"chat", "go"}, false, false) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case <-streamClosed:
	case <-time.After(time.Second):
		t.Fatal("stream request was not cancelled")
	}
}

func TestCLITaskReplySwarmParentAcceptedWithoutExecutionIdentity(t *testing.T) {
	const board = `<div data-task-id="swarm-1" data-task-status="running" data-task-category="active"><a href="/tasks/swarm-1">Coordinate release</a></div>`
	for _, jsonOutput := range []bool{false, true} {
		name := "plain"
		if jsonOutput {
			name = "json"
		}
		t.Run(name, func(t *testing.T) {
			var posts, statusCalls, streamCalls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/projects":
					fmt.Fprint(w, cliProjects)
				case "/tasks":
					fmt.Fprint(w, board)
				case "/tasks/swarm-1/thread":
					posts++
					if r.URL.Query().Get("project_id") != "p1" || r.FormValue("message") != "continue coordination" {
						t.Fatalf("thread request = %s form=%q", r.URL.String(), r.FormValue("message"))
					}
					fmt.Fprint(w, `<div><div>User: continue coordination</div><div>Assistant: Queued. This message will be sent to the model after the active response finishes.</div></div>`)
				case "/api/chat/message/swarm-1":
					statusCalls++
				case "/events/chat/swarm-1":
					streamCalls++
				default:
					t.Fatalf("unexpected request %s", r.URL.String())
				}
			}))
			defer srv.Close()
			c, _ := client.New(srv.URL)
			var out bytes.Buffer
			if err := RunCLIContext(context.Background(), c, &out, "demo", []string{"tasks", "reply", "Coordinate release", "|", "continue coordination"}, false, jsonOutput); err != nil {
				t.Fatalf("swarm parent reply failed after acceptance: %v", err)
			}
			if posts != 1 || statusCalls != 0 || streamCalls != 0 {
				t.Fatalf("posts=%d status=%d streams=%d", posts, statusCalls, streamCalls)
			}
			if !jsonOutput {
				if got := out.String(); got != "sent to thread of Coordinate release\n" {
					t.Fatalf("plain output = %q", got)
				}
				return
			}
			var record cliExecutionRecord
			if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &record); err != nil {
				t.Fatalf("JSON output = %q: %v", out.String(), err)
			}
			if record.Type != "accepted" || record.ProjectID != "p1" || record.TaskID != "swarm-1" || record.ExecID != "" || record.Status != "accepted" {
				t.Fatalf("record = %#v", record)
			}
		})
	}
}

func TestCLITaskReplyStreamsScopedExecution(t *testing.T) {
	const board = `<div data-task-id="task-1" data-task-status="running" data-task-category="active"><a href="/tasks/task-1">Fix stream</a></div>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/projects":
			fmt.Fprint(w, cliProjects)
		case "/tasks":
			fmt.Fprint(w, board)
		case "/events/live":
			if r.URL.Query().Get("project_id") != "p1" {
				t.Fatalf("live project = %q", r.URL.Query().Get("project_id"))
			}
			<-r.Context().Done()
		case "/tasks/task-1/thread":
			if r.URL.Query().Get("project_id") != "p1" {
				t.Fatalf("thread project = %q", r.URL.Query().Get("project_id"))
			}
			fmt.Fprint(w, `<div data-execution-pair="true" data-exec-id="follow-1" data-exec-status="running"></div>`)
		case "/events/chat/follow-1":
			fmt.Fprint(w, "data: task output\n\nevent: done\ndata: completed\n\n")
		case "/api/chat/message/follow-1":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"message_id":"follow-1","status":"completed","response":"task output"}`)
		default:
			t.Fatalf("unexpected request %s", r.URL.String())
		}
	}))
	defer srv.Close()
	c, _ := client.New(srv.URL)
	var out bytes.Buffer
	if err := RunCLIContext(context.Background(), c, &out, "demo", []string{"tasks", "reply", "Fix stream", "|", "continue"}, false, false); err != nil {
		t.Fatal(err)
	}
	if out.String() != "task output\n" {
		t.Fatalf("output = %q", out.String())
	}
}

func TestCLIChatQueuedPromotionStreamsPromotedExecution(t *testing.T) {
	var statusCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/projects":
			fmt.Fprint(w, cliProjects)
		case "/api/chat/message":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"message_id":"input-1","status":"queued","queued":true}`)
		case "/api/chat/message/input-1":
			statusCalls++
			w.Header().Set("Content-Type", "application/json")
			if statusCalls == 1 {
				fmt.Fprint(w, `{"message_id":"input-1","status":"queued"}`)
			} else if statusCalls == 2 {
				fmt.Fprint(w, `{"message_id":"exec-2","status":"processing"}`)
			} else {
				fmt.Fprint(w, `{"message_id":"exec-2","status":"completed","response":"promoted"}`)
			}
		case "/events/chat/exec-2":
			fmt.Fprint(w, "data: promoted\n\nevent: done\ndata: completed\n\n")
		default:
			t.Fatalf("unexpected request %s", r.URL.String())
		}
	}))
	defer srv.Close()
	c, _ := client.New(srv.URL)
	var out bytes.Buffer
	if err := RunCLIContext(context.Background(), c, &out, "demo", []string{"chat", "go"}, false, false); err != nil {
		t.Fatal(err)
	}
	if out.String() != "promoted\n" || statusCalls != 3 {
		t.Fatalf("output=%q status calls=%d", out.String(), statusCalls)
	}
}

func TestCLITaskReplyQueuedPromotionUsesAuthoritativeStatus(t *testing.T) {
	const board = `<div data-task-id="task-1" data-task-status="running" data-task-category="active"><a href="/tasks/task-1">Fix stream</a></div>`
	var statusCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/projects":
			fmt.Fprint(w, cliProjects)
		case "/tasks":
			fmt.Fprint(w, board)
		case "/tasks/task-1/thread":
			if r.URL.Query().Get("project_id") != "p1" {
				t.Fatalf("thread project = %q", r.URL.Query().Get("project_id"))
			}
			fmt.Fprint(w, `<div data-thread-input-id="input-1" data-task-id="task-1" data-input-mode="queued"></div>`)
		case "/api/chat/message/input-1":
			statusCalls++
			w.Header().Set("Content-Type", "application/json")
			if statusCalls == 1 {
				fmt.Fprint(w, `{"message_id":"input-1","status":"queued"}`)
			} else if statusCalls == 2 {
				fmt.Fprint(w, `{"message_id":"exec-2","status":"processing"}`)
			} else {
				fmt.Fprint(w, `{"message_id":"exec-2","status":"completed","response":"queued output"}`)
			}
		case "/events/chat/exec-2":
			fmt.Fprint(w, "data: queued output\n\nevent: done\ndata: completed\n\n")
		default:
			t.Fatalf("unexpected request %s", r.URL.String())
		}
	}))
	defer srv.Close()
	c, _ := client.New(srv.URL)
	var out bytes.Buffer
	if err := RunCLIContext(context.Background(), c, &out, "demo", []string{"tasks", "reply", "Fix stream", "|", "continue"}, false, false); err != nil {
		t.Fatal(err)
	}
	if out.String() != "queued output\n" || statusCalls != 3 {
		t.Fatalf("output=%q status calls=%d", out.String(), statusCalls)
	}
}

// Mutating commands work headlessly too.
func TestCLIRunsTaskMutation(t *testing.T) {
	const board = `<div data-task-id="t-1" data-task-status="pending" data-task-category="backlog">
		<a href="/tasks/t-1?from=tasks" title="Refactor the API">Refactor the API</a>
	</div>`
	c, rec := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/tasks":        board,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"tasks", "run", "Refactor"}, false, false); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if !rec.saw("POST", "/tasks/t-1/run") {
		t.Fatalf("no run call, calls:\n%s", rec.all())
	}
}

func TestCLIAutomationEditIsDeterministicAndSecretSafe(t *testing.T) {
	const current = "schema_version: 1\nname: Original\n"
	const edited = "schema_version: 1\nname: Edited\ndescription: TOP-SECRET\n"
	builder := `<div id="automation-builder"><form id="automation-design-form" action="/automations/au-1/builder?project_id=p1"></form><textarea name="automation_yaml">` + current + `</textarea></div>`
	c, rec := cliServer(t, map[string]string{
		"/api/projects":             cliProjects,
		"/automations":              `<div>` + automationCardHTML("au-1", "Original", "active") + `</div>`,
		"/automations/au-1/builder": builder,
	})
	path := filepath.Join(t.TempDir(), "automation.yaml")
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"automations", "edit", "Original", "--file", path}, false, false); err != nil {
		t.Fatal(err)
	}
	if rec.count("POST", "/automations/au-1/builder") != 2 {
		t.Fatalf("requests = %s", rec.all())
	}
	if !strings.Contains(out.String(), "updated automation Original") || strings.Contains(out.String(), "TOP-SECRET") {
		t.Fatalf("output = %s", out.String())
	}
}

// One-shot CLI mode works headlessly for the new automations actions,
// exiting cleanly on success and nonzero on a backend failure.
func TestCLIRunsAutomationsPause(t *testing.T) {
	const automationsHTML = `<div class="card" data-automation-url="/automations/au-1?project_id=p1">
		<div class="card-body relative">
			<span class="badge badge-outline badge-sm">active</span>
			<button type="button" data-automation-card-delete="au-1" data-automation-name="Native SDLC"></button>
		</div>
	</div>`
	c, rec := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/automations":  automationsHTML,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"automations", "pause", "Native"}, false, false); err != nil {
		t.Fatalf("pause failed: %v", err)
	}
	if !rec.saw("POST", "/automations/au-1/pause") {
		t.Fatalf("no pause call, calls:\n%s", rec.all())
	}
}

func TestCLIRunsAutomationsRunAndCompatibilityAlias(t *testing.T) {
	const automationsHTML = `<div class="card" data-automation-url="/automations/au-1?project_id=p1">
		<div class="card-body relative">
			<span class="badge badge-outline badge-sm">active</span>
			<button type="button" data-automation-card-delete="au-1" data-automation-name="Native SDLC"></button>
		</div>
	</div>`
	for _, action := range []string{"run", "run-now"} {
		t.Run(action, func(t *testing.T) {
			c, rec := cliServer(t, map[string]string{
				"/api/projects": cliProjects,
				"/automations":  automationsHTML,
			})
			var out bytes.Buffer
			if err := RunCLI(c, &out, "demo", []string{"automations", action, "Native"}, false, false); err != nil {
				t.Fatalf("%s failed: %v", action, err)
			}
			if !rec.sawQuery("POST /automations/au-1/run-now?project_id=p1") {
				t.Fatalf("%s lost backend route or project scope, calls:\n%s", action, rec.all())
			}
			if !strings.Contains(stripANSI(out.String()), "run: Native SDLC") {
				t.Fatalf("%s output did not use canonical action:\n%s", action, out.String())
			}
		})
	}
}

// One-shot CLI mode works headlessly for automation detail and JSON output.
func TestCLIRunsAutomationsShowAndJSON(t *testing.T) {
	const automationsHTML = `<div class="card" data-automation-url="/automations/au-1?project_id=p2">
		<div class="card-body relative">
			<span class="badge badge-outline badge-sm">active</span>
			<button type="button" data-automation-card-delete="au-1" data-automation-name="Native SDLC"></button>
		</div>
	</div>`
	c, rec := cliServer(t, map[string]string{
		"/api/projects":     cliProjects,
		"/automations":      automationsHTML,
		"/automations/au-1": automationDetailHTML("au-1", "p2", "Native SDLC"),
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "other", []string{"automations", "show", "Native"}, false, false); err != nil {
		t.Fatalf("plain automation show failed: %v", err)
	}
	plain := stripANSI(out.String())
	for _, want := range []string{"Automation: Native SDLC", "Graph", "Nodes", "Runtime", "Resources", "External state"} {
		if !strings.Contains(plain, want) {
			t.Errorf("plain detail missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "\x1b[") {
		t.Fatalf("plain CLI detail contains ANSI styling: %q", out.String())
	}
	if !rec.sawQuery("GET /automations?project_id=p2") || !rec.sawQuery("GET /automations/au-1?project_id=p2") {
		t.Fatalf("automation show lost selected project:\n%s", rec.all())
	}

	out.Reset()
	if err := RunCLI(c, &out, "other", []string{"automations", "show", "Native"}, false, true); err != nil {
		t.Fatalf("JSON automation show failed: %v", err)
	}
	var detail client.AutomationDetail
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &detail); err != nil {
		t.Fatalf("automation detail JSON = %q: %v", out.String(), err)
	}
	if detail.Automation.ID != "au-1" || detail.Automation.ProjectID != "p2" || detail.Automation.Name != "Native SDLC" {
		t.Fatalf("automation detail JSON = %+v", detail.Automation)
	}
	if len(detail.Nodes) != 1 || !detail.Nodes[0].Counts.RunningAvailable || detail.Nodes[0].Counts.Running != 1 {
		t.Fatalf("automation count availability was not preserved in JSON: %+v", detail.Nodes)
	}
	if strings.Contains(out.String(), "\x1b[") || strings.Contains(out.String(), "OpenVibely") {
		t.Fatalf("JSON detail contains styling or a banner: %q", out.String())
	}
}

func TestCLIScheduleShowAliasScopingAndMissingReference(t *testing.T) {
	const scheduleHTML = `<div id="schedule-content"><div data-task-id="t-2" data-schedule-id="schedule-full-id">Weekly report</div></div>`
	const taskHTML = `<div data-task-id="t-2" data-project-id="p1"><h2 class="font-bold">Ship the docs</h2><div data-task-status="running"></div></div>`
	for _, action := range []string{"show", "open"} {
		t.Run(action, func(t *testing.T) {
			c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects, "/schedule": scheduleHTML, "/tasks/t-2": taskHTML})
			var out bytes.Buffer
			if err := RunCLI(c, &out, "demo", []string{"schedule", action, "schedule-full-id"}, false, false); err != nil {
				t.Fatalf("schedule %s: %v", action, err)
			}
			for _, want := range []string{"Weekly report", "Schedule ID: schedule-full-id", "Task ID: t-2", "/tasks open t-2"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("output missing %q: %q", want, out.String())
				}
			}
			for _, path := range []string{"/schedule", "/tasks/t-2"} {
				if !rec.sawQuery(http.MethodGet + " " + path + "?project_id=p1") {
					t.Errorf("missing scoped %s request; calls:\n%s", path, rec.all())
				}
			}
		})
	}

	for _, action := range []string{"show", "open"} {
		c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects})
		err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"schedule", action}, false, false)
		if err == nil || !strings.Contains(err.Error(), "usage: schedule "+action+" <id|name>") {
			t.Errorf("missing %s ref error = %v", action, err)
		}
		if calls := rec.all(); calls != "" {
			t.Errorf("missing %s ref prompted or requested backend:\n%s", action, calls)
		}
	}
}

func TestCLIScheduleRejectsUnknownActionAndListSurplusBeforeRequests(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{name: "unknown action", args: []string{"schedule", "unknown"}},
		{name: "list surplus", args: []string{"schedule", "list", "extra"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects})
			err := RunCLI(c, &bytes.Buffer{}, "demo", tc.args, false, false)
			if err == nil || !strings.Contains(err.Error(), "schedule [list|show|open|add|edit|delete|toggle]") {
				t.Fatalf("error = %v, want canonical schedule usage", err)
			}
			if calls := rec.all(); calls != "" {
				t.Fatalf("invalid schedule command dispatched requests:\n%s", calls)
			}
		})
	}
}

func TestCLIScheduleEditParityAndMissingReferenceValidation(t *testing.T) {
	c, rec := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/schedule":     selScheduleHTML,
		"/tasks/t-1":    scheduleEditDetail("p1"),
	})
	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"schedule", "edit", "s-2", "repeat", "monthly", "interval", "2", "clear-context", "false"}, false, false); err != nil {
		t.Fatalf("headless schedule edit: %v", err)
	}
	if !rec.saw(http.MethodPut, "/schedules/s-2") || !rec.sawForm("repeat_type=monthly") || !rec.sawForm("repeat_interval=2") || !rec.sawForm("clear_context_on_start=false") {
		t.Fatalf("headless edit requests/forms:\n%s\n%v", rec.all(), rec.forms)
	}
	if !strings.Contains(out.String(), "updated schedule s-2") {
		t.Fatalf("headless output = %q", out.String())
	}

	c, rec = cliServer(t, map[string]string{"/api/projects": cliProjects})
	err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"schedule", "edit"}, false, false)
	if err == nil || !strings.Contains(err.Error(), "usage: schedule edit") {
		t.Fatalf("missing ref error = %v", err)
	}
	if rec.saw(http.MethodGet, "/schedule") || rec.saw(http.MethodPut, "/schedules/") {
		t.Fatalf("missing ref dispatched schedule work:\n%s", rec.all())
	}
}

func TestCLIScheduleEditReservedWordReference(t *testing.T) {
	const scheduleHTML = `<div id="schedule-content"><div data-task-id="t-1" data-schedule-id="s-1">repeat</div></div>`
	c, rec := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/schedule":     scheduleHTML,
		"/tasks/t-1":    scheduleEditDetail("p1"),
	})
	if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"schedule", "edit", "repeat", "interval", "4"}, false, false); err != nil {
		t.Fatalf("headless reserved-word schedule edit: %v", err)
	}
	if !rec.saw(http.MethodPut, "/schedules/s-1") || !rec.sawForm("repeat_interval=4") {
		t.Fatalf("headless reserved-word edit requests/forms:\n%s\n%v", rec.all(), rec.forms)
	}
}

func TestCLIScheduleEditQuotedValidSettingPairReference(t *testing.T) {
	const scheduleHTML = `<div id="schedule-content"><div data-task-id="t-1" data-schedule-id="s-1">Run repeat daily report</div></div>`
	c, rec := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/schedule":     scheduleHTML,
		"/tasks/t-1":    scheduleEditDetailForIDs("p1", "s-1"),
	})
	if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"schedule", "edit", "Run repeat daily report", "interval", "4"}, false, false); err != nil {
		t.Fatalf("headless quoted valid-setting-pair schedule edit: %v", err)
	}
	if !rec.saw(http.MethodPut, "/schedules/s-1") || !rec.sawForm("repeat_interval=4") {
		t.Fatalf("headless valid-setting-pair edit requests/forms:\n%s\n%v", rec.all(), rec.forms)
	}
}

func TestCLIScheduleEditHourlyAliasUsesBackendHours(t *testing.T) {
	c, rec := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/schedule":     selScheduleHTML,
		"/tasks/t-1":    scheduleEditDetail("p1"),
	})
	if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"schedule", "edit", "s-2", "repeat", "hourly"}, false, false); err != nil {
		t.Fatalf("headless hourly schedule edit: %v", err)
	}
	if !rec.saw(http.MethodPut, "/schedules/s-2") || !rec.sawForm("repeat_type=hours") || !rec.sawForm("repeat_interval=3") {
		t.Fatalf("headless hourly edit requests/forms:\n%s\n%v", rec.all(), rec.forms)
	}
}

func TestCLIScheduleEditMalformedEarlierOptionFailsBeforeRequests(t *testing.T) {
	c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects})
	err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"schedule", "edit", "s-1", "repeat", "yearly", "interval", "5"}, false, false)
	if err == nil || !strings.Contains(err.Error(), `unknown repeat type "yearly"`) {
		t.Fatalf("malformed earlier option error = %v", err)
	}
	if rec.saw(http.MethodGet, "/schedule") || rec.saw(http.MethodGet, "/tasks/") || rec.saw(http.MethodPut, "/schedules/") {
		t.Fatalf("malformed headless edit dispatched schedule work:\n%s", rec.all())
	}
}

func TestCLIScheduleEditMalformedTitleOptionsFailBeforeRequests(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "timestamp", args: []string{"schedule", "edit", "Nightly", "run-at", "bad", "interval", "5"}, want: "run time must use"},
		{name: "repeat", args: []string{"schedule", "edit", "Nightly", "repeat", "yearly", "interval", "5"}, want: `unknown repeat type "yearly"`},
		{name: "interval", args: []string{"schedule", "edit", "Nightly", "interval", "0", "repeat", "daily"}, want: "repeat interval must be between"},
		{name: "boolean", args: []string{"schedule", "edit", "Nightly", "clear-context", "maybe", "interval", "5"}, want: "clear-context must be true or false"},
		{name: "duplicate", args: []string{"schedule", "edit", "Nightly", "repeat", "daily", "repeat", "weekly", "interval", "5"}, want: "usage"},
		{name: "surplus", args: []string{"schedule", "edit", "Nightly", "repeat", "daily", "surplus", "interval", "5"}, want: "usage"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects})
			err := RunCLI(c, &bytes.Buffer{}, "demo", tc.args, false, false)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if calls := rec.all(); calls != "" {
				t.Fatalf("malformed title edit dispatched requests:\n%s", calls)
			}
		})
	}
}

func TestCLIScheduleEditExactIDWinsAcrossCandidateBoundaries(t *testing.T) {
	const scheduleHTML = `<div id="schedule-content">
		<div data-task-id="t-1" data-schedule-id="s-1">Canonical ID target</div>
		<div data-task-id="t-2" data-schedule-id="s-2">s-1 repeat daily</div>
	</div>`
	c, rec := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/schedule":     scheduleHTML,
		"/tasks/t-1":    scheduleEditDetailForIDs("p1", "s-1"),
	})
	if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"schedule", "edit", "s-1", "repeat", "daily", "interval", "5"}, false, false); err != nil {
		t.Fatalf("headless exact-ID edit: %v", err)
	}
	if !rec.saw(http.MethodGet, "/tasks/t-1") || !rec.saw(http.MethodPut, "/schedules/s-1") || rec.saw(http.MethodGet, "/tasks/t-2") {
		t.Fatalf("headless exact-ID precedence requests:\n%s", rec.all())
	}
}

func TestCLIScheduleEditAmbiguousReferenceDoesNotMutate(t *testing.T) {
	const ambiguous = `<div id="schedule-content"><div data-task-id="t1" data-schedule-id="s1">Weekly report</div><div data-task-id="t2" data-schedule-id="s2">Weekly report</div></div>`
	c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects, "/schedule": ambiguous})
	err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"schedule", "edit", "Weekly report", "repeat", "daily"}, false, false)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "ambiguous") {
		t.Fatalf("ambiguous ref error = %v", err)
	}
	if rec.saw(http.MethodPut, "/schedules/") {
		t.Fatalf("ambiguous ref mutated schedule:\n%s", rec.all())
	}
}

func TestCLIAutomationsShowMissingReferenceReturnsUsageWithoutSelectorOrList(t *testing.T) {
	c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects, "/automations": `<div></div>`})
	err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"automations", "show"}, false, false)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "usage") {
		t.Fatalf("missing CLI reference error = %v, want usage", err)
	}
	if rec.saw("GET", "/automations") {
		t.Fatalf("missing CLI reference listed automations or opened a selector:\n%s", rec.all())
	}
}

func TestCLIAutomationsRunMissingReferenceUsesCanonicalUsageWithoutList(t *testing.T) {
	c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects, "/automations": `<div></div>`})
	err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"automations", "run"}, false, false)
	if err == nil || !strings.Contains(err.Error(), "usage: automations run <automation>") {
		t.Fatalf("missing CLI run reference error = %v, want canonical usage", err)
	}
	if strings.Contains(err.Error(), "run-now") {
		t.Fatalf("missing CLI run reference advertised compatibility alias: %v", err)
	}
	if rec.saw("GET", "/automations") {
		t.Fatalf("missing CLI run reference listed automations or opened a selector:\n%s", rec.all())
	}
}

func TestCLIChannelAccessAllProvidersJSONSafetyAndRemovalGuards(t *testing.T) {
	providers := []struct {
		provider string
		identity string
	}{
		{"telegram", "telegram_user"},
		{"slack", "U12345678"},
		{"discord", "123456789012345678"},
		{"email", "person@example.com"},
	}
	for _, tc := range providers {
		t.Run("json list "+tc.provider, func(t *testing.T) {
			route, _ := channelAccessTestRoute(tc.provider)
			c, rec := cliServer(t, map[string]string{
				"/api/projects": cliProjects,
				route:           channelAccessTestPage(tc.provider, channelAccessTestRow{id: "row-1", name: "Visible User", identity: tc.identity}),
			})
			var out bytes.Buffer
			if err := RunCLI(c, &out, "demo", []string{"channels", "access", tc.provider, "list"}, false, true); err != nil {
				t.Fatalf("access list: %v", err)
			}
			var users []channelAccessOutputUser
			if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &users); err != nil {
				t.Fatalf("access JSON = %q: %v", out.String(), err)
			}
			if len(users) != 1 || users[0].Provider != tc.provider || users[0].ProjectID != "p1" || strings.Contains(out.String(), "channel-access-backend-secret") {
				t.Fatalf("unsafe access JSON = %q", out.String())
			}
			if !rec.saw(http.MethodGet, route) || !rec.sawQuery("project_id=p1") {
				t.Fatalf("unscoped access list calls:\n%s", rec.all())
			}
		})
	}

	route, _ := channelAccessTestRoute("slack")
	rows := []channelAccessTestRow{{id: "row-1", name: "Shared User", identity: "U12345678"}, {id: "row-2", name: "Shared User", identity: "U87654321"}}
	for _, tc := range []struct {
		name       string
		refs       []string
		force      bool
		wantError  string
		wantDelete bool
	}{
		{name: "unforced", refs: []string{"row-1"}, wantError: "--force to confirm removal", wantDelete: false},
		{name: "unknown foreign", refs: []string{"foreign-row"}, wantError: "nothing matches", wantDelete: false},
		{name: "ambiguous", refs: []string{"U"}, wantError: "ambiguous", wantDelete: false},
		{name: "duplicate", refs: []string{"row-1", "row-1"}, wantError: "more than once", wantDelete: false},
		{name: "surplus", refs: []string{"row-1", "row-2"}, wantError: "exactly one", wantDelete: false},
		{name: "forced captured row", refs: []string{"row-1"}, force: true, wantDelete: true},
	} {
		t.Run("remove "+tc.name, func(t *testing.T) {
			c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects, route: channelAccessTestPage("slack", rows...)})
			args := append([]string{"channels", "access", "slack", "remove"}, tc.refs...)
			var out bytes.Buffer
			err := RunCLI(c, &out, "demo", args, tc.force, false)
			if tc.wantError != "" && (err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.wantError))) {
				t.Fatalf("remove error = %v, want %q", err, tc.wantError)
			}
			if tc.wantError == "" && err != nil {
				t.Fatalf("forced removal: %v", err)
			}
			if got := rec.saw(http.MethodDelete, route+"/row-1"); got != tc.wantDelete || rec.saw(http.MethodDelete, route+"/row-2") {
				t.Fatalf("delete guard/captured target failure: calls:\n%s", rec.all())
			}
		})
	}

	t.Run("foreign rows cannot be forced to delete", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{
			"/api/projects": cliProjects,
			route:           channelAccessTestPage("slack", channelAccessTestRow{id: "foreign-row", projectID: "other-project", name: "Foreign User", identity: "U12345678"}),
		})
		err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"channels", "access", "slack", "remove", "U12345678"}, true, false)
		if err == nil || !strings.Contains(err.Error(), "authorized channel access list unavailable") || rec.saw(http.MethodDelete, route+"/foreign-row") {
			t.Fatalf("forced foreign removal was not rejected safely: err=%v calls=%s", err, rec.all())
		}
	})

	t.Run("duplicate add is rejected before mutation", func(t *testing.T) {
		emailRoute, _ := channelAccessTestRoute("email")
		c, rec := cliServer(t, map[string]string{
			"/api/projects": cliProjects,
			emailRoute:      channelAccessTestPage("email", channelAccessTestRow{id: "row-1", name: "support@example.com Team", identity: "real.sender@example.com"}),
		})
		err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"channels", "access", "email", "add", "Real.Sender@Example.COM"}, false, false)
		if err == nil || !strings.Contains(err.Error(), "already exists") || rec.saw(http.MethodPost, emailRoute) {
			t.Fatalf("duplicate add was not stopped before mutation: err=%v calls=%s", err, rec.all())
		}
	})

	t.Run("mutation JSON is secret-free", func(t *testing.T) {
		emailRoute, _ := channelAccessTestRoute("email")
		c, _ := cliServer(t, map[string]string{
			"/api/projects": cliProjects,
			emailRoute:      channelAccessTestPage("email", channelAccessTestRow{id: "row-1", name: "Visible User", identity: "person@example.com"}),
		})
		var out bytes.Buffer
		if err := RunCLI(c, &out, "demo", []string{"channels", "access", "email", "add", "new@example.com"}, false, true); err != nil {
			t.Fatalf("JSON add: %v", err)
		}
		var action channelAccessActionJSON
		if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &action); err != nil || action.Action != "add" || action.User.Identity != "new@example.com" || strings.Contains(out.String(), "channel-access-backend-secret") {
			t.Fatalf("unsafe mutation JSON = %q, err=%v", out.String(), err)
		}
	})

	for _, args := range [][]string{
		{"channels", "access", "telegram", "add", "invalid!"},
		{"channels", "access", "telegram", "add", "0"},
		{"channels", "access", "telegram", "add", "9223372036854775808"},
		{"channels", "access", "slack", "add", "alice"},
		{"channels", "access", "discord", "add", "alice"},
		{"channels", "access", "email", "add", "not-an-email"},
		{"channels", "access", "github", "list"},
	} {
		c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects})
		if err := RunCLI(c, &bytes.Buffer{}, "demo", args, false, false); err == nil || rec.all() != "" {
			t.Fatalf("invalid access command %v made requests or succeeded: %s", args, rec.all())
		}
	}
}

func TestCLIChannelAccessMutationFailureAndRefreshFailureRemainSafe(t *testing.T) {
	const secret = "channel-access-backend-secret"
	route, _ := channelAccessTestRoute("email")
	page := channelAccessTestPage("email", channelAccessTestRow{id: "row-1", name: "Visible User", identity: "person@example.com"})

	t.Run("mutation failure", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/projects":
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, cliProjects)
			case route:
				if r.Method == http.MethodPost {
					http.Error(w, secret, http.StatusBadGateway)
					return
				}
				_, _ = io.WriteString(w, page)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()
		c, _ := client.New(srv.URL)
		var out bytes.Buffer
		err := RunCLI(c, &out, "demo", []string{"channels", "access", "email", "add", "New@Example.COM"}, false, false)
		if err == nil || !strings.Contains(err.Error(), "channel request failed") || strings.Contains(err.Error(), secret) || strings.Contains(out.String(), secret) || strings.Contains(out.String(), "authorized Email access") {
			t.Fatalf("unsafe mutation failure: err=%v output=%q", err, out.String())
		}
	})

	t.Run("refresh failure retains success", func(t *testing.T) {
		lists := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/projects":
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, cliProjects)
			case route:
				switch r.Method {
				case http.MethodGet:
					lists++
					if lists > 1 {
						http.Error(w, secret, http.StatusBadGateway)
						return
					}
					_, _ = io.WriteString(w, page)
				case http.MethodPost:
					w.WriteHeader(http.StatusNoContent)
				}
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()
		c, _ := client.New(srv.URL)
		var out bytes.Buffer
		if err := RunCLI(c, &out, "demo", []string{"channels", "access", "email", "add", "New@Example.COM"}, false, false); err != nil {
			t.Fatalf("successful mutation with failed refresh: %v", err)
		}
		if !strings.Contains(out.String(), "authorized Email access") || strings.Contains(out.String(), secret) {
			t.Fatalf("refresh failure did not preserve safe success: %q", out.String())
		}
	})
}

func TestCLIChannelsConfigureEverySupportedTypeWithoutEchoingSecrets(t *testing.T) {
	tests := []struct {
		name string
		args []string
		path string
	}{
		{"telegram", []string{"channels", "add", "telegram", "--token", "secret-telegram"}, "/channels/telegram"},
		{"github", []string{"channels", "add", "github", "--auth-mode", "pat", "--pat", "secret-github"}, "/channels/github/configure"},
		{"slack", []string{"channels", "add", "slack", "--client-id", "client", "--client-secret", "secret-slack", "--app-token", "secret-app"}, "/channels/slack/configure"},
		{"discord", []string{"channels", "add", "discord", "--bot-token", "secret-discord"}, "/channels/discord/configure"},
		{"x", []string{"channels", "add", "x", "--consumer-key", "secret-x-key", "--consumer-secret", "secret-x-consumer", "--access-token", "secret-x-token", "--access-token-secret", "secret-x-access"}, "/channels/x/configure"},
		{"email", []string{"channels", "add", "email", "--provider", "gmail", "--address", "bot@example.com", "--password", "secret-email"}, "/channels/email/configure"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects, "/channels": structuredChannelsPage, tt.path: ""})
			var out bytes.Buffer
			if err := RunCLI(c, &out, "demo", tt.args, false, false); err != nil {
				t.Fatal(err)
			}
			if !rec.saw("POST", tt.path) || !rec.sawQuery("project_id=p1") {
				t.Fatalf("missing scoped configure request: %s", rec.all())
			}
			if strings.Contains(strings.ToLower(out.String()), "secret-") {
				t.Fatalf("secret appeared in output: %s", out.String())
			}
		})
	}
}

func TestCLIChannelsEditXPreservesSafeSettingsAndNeverEchoesCredentials(t *testing.T) {
	const storedSecret = "stored-x-secret"
	const rotatedSecret = "rotated-x-secret"
	var posted url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, cliProjects)
		case r.Method == http.MethodGet && r.URL.Path == "/channels":
			_, _ = io.WriteString(w, `<div data-channel-type="x" data-search-text="X formerly Twitter mentions posts"><span class="badge badge-success">Connected</span></div><form><input name="x_consumer_key" value="`+storedSecret+`"><input name="x_poll_interval_seconds" value="30"><input type="checkbox" name="x_send_responses" checked></form>`)
		case r.Method == http.MethodPost && r.URL.Path == "/channels/x/configure":
			if r.URL.Query().Get("project_id") != "p1" {
				t.Errorf("project_id = %q", r.URL.Query().Get("project_id"))
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			posted = r.PostForm
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, _ := client.New(srv.URL)
	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"channels", "edit", "x", "--access-token", rotatedSecret, "--poll-interval", "45"}, false, false); err != nil {
		t.Fatal(err)
	}
	if posted.Get("x_access_token") != rotatedSecret || posted.Get("x_poll_interval_seconds") != "45" || posted.Get("x_send_responses") != "true" {
		t.Fatalf("posted X edit = %v", posted)
	}
	if posted.Get("x_consumer_key") != "" {
		t.Fatalf("scraped X credential was reposted: %v", posted)
	}
	if strings.Contains(out.String(), storedSecret) || strings.Contains(out.String(), rotatedSecret) {
		t.Fatalf("X edit exposed credentials: %s", out.String())
	}
}

func TestCLIChannelsHeadlessEditUsesAuthoritativeTransitionState(t *testing.T) {
	const githubPATPage = `<div data-channel-type="github" data-search-text="GitHub Connected"></div><form><select name="github_auth_mode"><option value="pat" selected>PAT</option><option value="app">App</option></select><input name="github_pat" value="stored-github-pat"><input name="github_app_id" value="old-app-id"><input name="github_app_slug" value="old-app-slug"><textarea name="github_app_private_key">stored-private-key</textarea><input name="github_api_endpoint" value="https://api.github.com"></form>`
	const githubAppPage = `<div data-channel-type="github" data-search-text="GitHub Connected"></div><form><select name="github_auth_mode"><option value="pat">PAT</option><option value="app" selected>App</option></select><input name="github_pat" value="stored-github-pat"><input name="github_app_id" value="app-id"><input name="github_app_slug" value="app-slug"><textarea name="github_app_private_key">stored-private-key</textarea><input name="github_api_endpoint" value="https://api.github.com"></form>`
	const slackOAuthPage = `<div data-channel-type="slack" data-search-text="Slack Configured"></div><form><input name="slack_client_id" value="client-id"><input name="slack_client_secret" value="stored-client-secret"><input name="slack_app_token" value="stored-app-token"><select name="slack_bot_token_mode"><option value="oauth" selected>OAuth</option><option value="manual">Manual</option></select><input name="slack_bot_token" value="stored-bot-token"><input type="checkbox" name="slack_send_responses" checked></form>`
	const slackManualPage = `<div data-channel-type="slack" data-search-text="Slack Configured"></div><form><input name="slack_client_id" value="client-id"><input name="slack_client_secret" value="stored-client-secret"><input name="slack_app_token" value="stored-app-token"><select name="slack_bot_token_mode"><option value="oauth">OAuth</option><option value="manual" selected>Manual</option></select><input name="slack_bot_token" value="stored-bot-token"><input type="checkbox" name="slack_send_responses" checked></form>`
	const emailGmailPage = `<div data-channel-type="email" data-search-text="Email bot@example.com"><span class="badge badge-success">Connected</span></div><form><select name="email_provider"><option value="gmail" selected>Gmail</option><option value="custom">Custom</option></select><input name="email_address" value="bot@example.com"><input name="email_password" value="stored-email-password"><input name="email_imap_host" value="imap.gmail.com"><input name="email_imap_port" value="993"><input name="email_smtp_host" value="smtp.gmail.com"><input name="email_smtp_port" value="587"><input name="email_poll_interval_seconds" value="15"></form>`
	const emailCustomPage = `<div data-channel-type="email" data-search-text="Email bot@example.com"><span class="badge badge-success">Connected</span></div><form><select name="email_provider"><option value="gmail">Gmail</option><option value="custom" selected>Custom</option></select><input name="email_address" value="bot@example.com"><input name="email_password" value="stored-email-password"><input name="email_imap_host" value="imap.example.com"><input name="email_imap_port" value="993"><input name="email_smtp_host" value="smtp.example.com"><input name="email_smtp_port" value="587"><input name="email_poll_interval_seconds" value="15"></form>`

	tests := []struct {
		name     string
		page     string
		args     []string
		wantErr  string
		wantPath string
	}{
		{name: "github unchanged app", page: githubAppPage, args: []string{"channels", "edit", "github", "--auth-mode", "app", "--api-endpoint", "https://github.example/api/v3"}, wantPath: "/channels/github/configure"},
		{name: "github valid pat to app", page: githubPATPage, args: []string{"channels", "edit", "github", "--auth-mode", "app", "--app-id", "new-id", "--app-slug", "new-slug", "--private-key", "-----BEGIN PRIVATE KEY-----\nYWJj\n-----END PRIVATE KEY-----"}, wantPath: "/channels/github/configure"},
		{name: "github invalid pat to app", page: githubPATPage, args: []string{"channels", "edit", "github", "--auth-mode", "app", "--app-id", "new-id", "--app-slug", "new-slug"}, wantErr: "editing GitHub into app mode requires --app-id, --app-slug, and --private-key"},
		{name: "slack unchanged manual", page: slackManualPage, args: []string{"channels", "edit", "slack", "--bot-token-mode", "manual", "--send-responses", "false"}, wantPath: "/channels/slack/configure"},
		{name: "slack valid oauth to manual", page: slackOAuthPage, args: []string{"channels", "edit", "slack", "--bot-token-mode", "manual", "--bot-token", "new-bot-token"}, wantPath: "/channels/slack/configure"},
		{name: "slack invalid oauth to manual", page: slackOAuthPage, args: []string{"channels", "edit", "slack", "--bot-token-mode", "manual"}, wantErr: "editing Slack into manual mode requires --bot-token"},
		{name: "email unchanged custom", page: emailCustomPage, args: []string{"channels", "edit", "email", "--provider", "custom", "--poll-interval", "30"}, wantPath: "/channels/email/configure"},
		{name: "email valid gmail to custom", page: emailGmailPage, args: []string{"channels", "edit", "email", "--provider", "custom", "--imap-host", "imap.example.com", "--smtp-host", "smtp.example.com"}, wantPath: "/channels/email/configure"},
		{name: "email invalid gmail to custom", page: emailGmailPage, args: []string{"channels", "edit", "email", "--provider", "custom"}, wantErr: "editing Email to custom requires --imap-host and --smtp-host"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recorder{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				rec.recordURL(r.Method, r.URL.RequestURI())
				switch {
				case r.URL.Path == "/api/projects":
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, cliProjects)
				case r.Method == http.MethodGet && r.URL.Path == "/channels":
					w.Header().Set("Content-Type", "text/html")
					_, _ = io.WriteString(w, tt.page)
				case r.Method == http.MethodPost && r.URL.Path == tt.wantPath:
					w.WriteHeader(http.StatusNoContent)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err = RunCLI(c, &out, "demo", tt.args, false, false)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				if rec.count("GET", "/channels") != 1 || rec.saw("POST", "/channels/github/configure") || rec.saw("POST", "/channels/slack/configure") || rec.saw("POST", "/channels/email/configure") {
					t.Fatalf("invalid transition requests:\n%s", rec.all())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !rec.saw("POST", tt.wantPath) {
				t.Fatalf("valid authoritative edit did not POST:\n%s", rec.all())
			}
			for _, secret := range []string{"stored-github-pat", "stored-private-key", "stored-client-secret", "stored-app-token", "stored-bot-token", "stored-email-password", "new-bot-token"} {
				if strings.Contains(out.String(), secret) {
					t.Fatalf("edit output exposed %q: %s", secret, out.String())
				}
			}
		})
	}
}

func TestCLIChannelsValidationPrecedesRequests(t *testing.T) {
	cases := [][]string{
		{"channels", "add", "telegram", "--token", ""},
		{"channels", "edit", "discord", "--send-responses", "maybe"},
		{"channels", "edit", "email", "--imap-port", "70000"},
		{"channels", "edit", "email", "--address", "not-an-email"},
		{"channels", "edit", "github", "--api-endpoint", "not-a-url"},
		{"channels", "edit", "github", "--auth-mode", "oauth"},
		{"channels", "add", "github", "--auth-mode", "pat"},
		{"channels", "add", "slack", "--client-id", "id", "--client-secret", "secret", "--app-token", "app", "--bot-token-mode", "manual"},
		{"channels", "add", "x", "--consumer-key", "key"},
		{"channels", "edit", "x", "--poll-interval", "301"},
		{"channels", "add", "email", "--provider", "unknown", "--address", "a@example.com", "--password", "secret"},
		{"channels", "add", "email", "--provider", "custom", "--address", "a@example.com", "--password", "secret"},
		{"channels", "test", "github"},
		{"channels", "connect", "telegram"},
		{"channels", "disconnect", "email"},
	}
	for _, args := range cases {
		c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects})
		if err := RunCLI(c, &bytes.Buffer{}, "demo", args, false, false); err == nil {
			t.Fatalf("%v unexpectedly succeeded", args)
		}
		if calls := rec.all(); calls != "" {
			t.Fatalf("%v made requests before validation:\n%s", args, calls)
		}
	}
}

func TestCLIChannelsRejectMalformedArgumentsBeforeRequests(t *testing.T) {
	cases := []struct {
		args      []string
		wantUsage string
	}{
		{args: []string{"channels", "nonsense"}, wantUsage: "usage: channels [list|show|add|connect|edit|test|remove|disconnect|access|webhooks]"},
		{args: []string{"channels", "list", "extra"}, wantUsage: "usage: channels [list|show|add|connect|edit|test|remove|disconnect|access|webhooks]"},
		{args: []string{"channels", "test", "telegram", "extra"}, wantUsage: "nothing matches"},
		{args: []string{"channels", "remove", "slack", "extra"}, wantUsage: "nothing matches"},
	}

	for _, tc := range cases {
		t.Run(strings.Join(tc.args[1:], "_"), func(t *testing.T) {
			c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects})
			err := RunCLI(c, &bytes.Buffer{}, "demo", tc.args, true, false)
			if err == nil {
				t.Fatal("malformed channels command returned success")
			}
			if !strings.Contains(err.Error(), tc.wantUsage) {
				t.Fatalf("malformed command error = %v, want canonical usage", err)
			}
			if calls := rec.all(); calls != "" {
				t.Fatalf("malformed CLI command made backend requests:\n%s", calls)
			}
		})
	}
}

func TestCLIChannelsBareAndListRemainValid(t *testing.T) {
	for _, args := range [][]string{{"channels"}, {"channels", "list"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			c, rec := cliServer(t, map[string]string{
				"/api/projects": cliProjects,
				"/channels":     `<html><body>channels</body></html>`,
			})
			if err := RunCLI(c, &bytes.Buffer{}, "demo", args, false, false); err != nil {
				t.Fatalf("valid channels command failed: %v", err)
			}
			if !rec.saw("GET", "/channels") {
				t.Fatalf("valid channels command did not list channels:\n%s", rec.all())
			}
		})
	}
}

func TestCLIChannelsRemoveResolvesReferenceBeforeForce(t *testing.T) {
	t.Run("invalid references report matching errors", func(t *testing.T) {
		cases := []struct {
			ref  string
			want string
		}{
			{ref: "a", want: `"a" is ambiguous`},
			{ref: "irc", want: `nothing matches "irc"`},
		}
		for _, tc := range cases {
			t.Run(tc.ref, func(t *testing.T) {
				c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects})
				err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"channels", "remove", tc.ref}, false, false)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("remove %q error = %v, want %q", tc.ref, err, tc.want)
				}
				if strings.Contains(err.Error(), "--force") {
					t.Fatalf("remove %q checked force before reference: %v", tc.ref, err)
				}
				if calls := rec.all(); calls != "" {
					t.Fatalf("invalid removal made backend requests:\n%s", calls)
				}
			})
		}
	})

	t.Run("valid partial names canonical target", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects})
		err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"channels", "remove", "tele"}, false, false)
		if err == nil || !strings.Contains(err.Error(), `--force to confirm removal of channel "Telegram Bot"`) {
			t.Fatalf("partial removal error = %v, want canonical force guidance", err)
		}
		if rec.saw("POST", "/channels/telegram/remove") {
			t.Fatalf("unforced partial removal mutated the backend:\n%s", rec.all())
		}
	})
}

// One-shot CLI mode works headlessly for the new channels actions,
// exiting cleanly on success and nonzero on a backend failure.
func TestCLIChannelsDisconnectRequiresForceAndUsesSupportedRoute(t *testing.T) {
	for _, channelType := range []string{"github", "slack"} {
		t.Run(channelType, func(t *testing.T) {
			c, rec := cliServer(t, map[string]string{
				"/api/projects": cliProjects,
				"/channels":     structuredChannelsPage,
			})
			var out bytes.Buffer
			err := RunCLI(c, &out, "demo", []string{"channels", "disconnect", channelType}, false, false)
			if err == nil || !strings.Contains(err.Error(), `--force to confirm disconnect of channel`) {
				t.Fatalf("unforced %s disconnect error = %v", channelType, err)
			}
			if rec.saw("POST", "/channels/"+channelType+"/disconnect") {
				t.Fatalf("unforced disconnect mutated backend: %s", rec.all())
			}

			c, rec = cliServer(t, map[string]string{
				"/api/projects": cliProjects,
				"/channels":     structuredChannelsPage,
			})
			if err := RunCLI(c, &out, "demo", []string{"channels", "disconnect", channelType}, true, false); err != nil {
				t.Fatalf("forced %s disconnect failed: %v", channelType, err)
			}
			if !rec.saw("POST", "/channels/"+channelType+"/disconnect") || rec.saw("POST", "/channels/"+channelType+"/remove") {
				t.Fatalf("forced disconnect used wrong route: %s", rec.all())
			}
		})
	}

	c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects})
	if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"channels", "disconnect", "discord"}, false, true); err == nil || !strings.Contains(err.Error(), "does not support disconnect") {
		t.Fatalf("unsupported disconnect error = %v", err)
	}
	if rec.saw("POST", "/channels/discord/remove") || rec.saw("POST", "/channels/discord/disconnect") {
		t.Fatalf("unsupported disconnect mutated backend: %s", rec.all())
	}
}

func TestCLIRunsChannelsTest(t *testing.T) {
	c, rec := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"channels", "test", "email"}, false, false); err != nil {
		t.Fatalf("channels test failed: %v", err)
	}
	if !rec.saw("POST", "/channels/email/test") {
		t.Fatalf("no channel test call, calls:\n%s", rec.all())
	}
	if !rec.sawQuery("project_id=p1") {
		t.Fatalf("channel test request was not scoped to the -project selection:\n%s", rec.all())
	}
}

func TestCLIChannelsTestFailsOnHTTP200FeedbackWithoutLeakingBody(t *testing.T) {
	const secret = "cli-channel-test-secret"
	c, _ := cliServer(t, map[string]string{
		"/api/projects":        cliProjects,
		"/channels/email/test": `<div class="text-error"><span>Connection failed: ` + secret + `</span></div>`,
	})
	var out bytes.Buffer
	err := RunCLI(c, &out, "demo", []string{"channels", "test", "email"}, false, false)
	if err == nil || !strings.Contains(err.Error(), "channel test failed") {
		t.Fatalf("HTTP-200 feedback error = %v", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(out.String(), secret) || strings.Contains(out.String(), "test: Email") {
		t.Fatalf("HTTP-200 feedback leaked or reported success: err=%v out=%q", err, out.String())
	}
}

func TestCLIChannelsTestFailsOnBackendError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(cliProjects))
		case r.URL.Path == "/channels/telegram/test":
			http.Error(w, "boom", http.StatusInternalServerError)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"channels", "test", "telegram"}, false, false); err == nil {
		t.Fatal("expected a nonzero exit on backend failure")
	}
}

func TestCLIAutomationsPauseFailsOnBackendError(t *testing.T) {
	const automationsHTML = `<div class="card" data-automation-url="/automations/au-1?project_id=p1">
		<div class="card-body relative">
			<span class="badge badge-outline badge-sm">active</span>
			<button type="button" data-automation-card-delete="au-1" data-automation-name="Native SDLC"></button>
		</div>
	</div>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(cliProjects))
		case r.URL.Path == "/automations/au-1/pause":
			http.Error(w, "boom", http.StatusInternalServerError)
		case r.URL.Path == "/automations":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(automationsHTML))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"automations", "pause", "Native"}, false, false); err == nil {
		t.Fatal("expected a nonzero exit on backend failure")
	}
}

// TestCLIDestructiveCommandsRequireForce verifies the --force gate on every
// destructive one-shot CLI command: exit nonzero without the flag, exit zero
// with it, and only call the backend when --force is present.
func TestCLIDestructiveCommandsRequireForce(t *testing.T) {
	const taskBoard = `<div data-task-id="t-1" data-task-status="pending" data-task-category="backlog">
		<a href="/tasks/t-1?from=tasks" title="Refactor the API">Refactor the API</a>
	</div>`

	t.Run("tasks_delete/without_force_exits_nonzero", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{
			"/api/projects": cliProjects,
			"/tasks":        taskBoard,
		})
		err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"tasks", "delete", "Refactor"}, false, false)
		if err == nil {
			t.Fatal("expected nonzero exit without --force")
		}
		if !strings.Contains(err.Error(), "--force") {
			t.Errorf("error should mention --force, got: %v", err)
		}
		if rec.saw("DELETE", "/tasks/t-1") {
			t.Error("must not call backend without --force")
		}
	})

	t.Run("tasks_delete/with_force_calls_backend", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{
			"/api/projects": cliProjects,
			"/tasks":        taskBoard,
		})
		if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"tasks", "delete", "Refactor"}, true, false); err != nil {
			t.Fatalf("expected zero exit with --force: %v", err)
		}
		if !rec.saw("DELETE", "/tasks/t-1") {
			t.Errorf("expected backend call with --force:\n%s", rec.all())
		}
	})

	t.Run("tasks_clear/without_force_exits_nonzero", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects})
		err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"tasks", "clear", "completed"}, false, false)
		if err == nil {
			t.Fatal("expected nonzero exit without --force")
		}
		if !strings.Contains(err.Error(), "--force") {
			t.Errorf("error should mention --force, got: %v", err)
		}
		if rec.saw("DELETE", "/tasks/completed") {
			t.Error("must not call backend without --force")
		}
	})

	t.Run("tasks_clear/with_force_calls_backend", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects})
		if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"tasks", "clear", "completed"}, true, false); err != nil {
			t.Fatalf("expected zero exit with --force: %v", err)
		}
		if !rec.saw("DELETE", "/tasks/completed") {
			t.Errorf("expected backend call with --force:\n%s", rec.all())
		}
	})

	t.Run("alerts_clear/without_force_exits_nonzero", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects})
		err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"alerts", "clear"}, false, false)
		if err == nil {
			t.Fatal("expected nonzero exit without --force")
		}
		if !strings.Contains(err.Error(), "--force") {
			t.Errorf("error should mention --force, got: %v", err)
		}
		if rec.saw("DELETE", "/alerts") {
			t.Error("must not call backend without --force")
		}
	})

	t.Run("alerts_clear/with_force_calls_backend", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects})
		if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"alerts", "clear"}, true, false); err != nil {
			t.Fatalf("expected zero exit with --force: %v", err)
		}
		if !rec.saw("DELETE", "/alerts") {
			t.Errorf("expected backend call with --force:\n%s", rec.all())
		}
	})

	t.Run("agents_delete/without_force_exits_nonzero", func(t *testing.T) {
		const agentsHTML = `<div data-agent-id="ag-1" data-agent-key="reviewer"
			data-agent-name="Reviewer" data-agent-description="reviews code"
			data-agent-model="claude" data-agent-scope="project"></div>`
		c, rec := cliServer(t, map[string]string{
			"/api/projects": cliProjects,
			"/agents":       agentsHTML,
		})
		err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"agents", "delete", "Reviewer"}, false, false)
		if err == nil {
			t.Fatal("expected nonzero exit without --force")
		}
		if !strings.Contains(err.Error(), "--force") {
			t.Errorf("error should mention --force, got: %v", err)
		}
		if rec.saw("DELETE", "/agents/ag-1") {
			t.Error("must not call backend without --force")
		}
	})

	t.Run("agents_delete/with_force_calls_backend", func(t *testing.T) {
		const agentsHTML = `<div data-agent-id="ag-1" data-agent-key="reviewer"
			data-agent-name="Reviewer" data-agent-description="reviews code"
			data-agent-model="claude" data-agent-scope="project"></div>`
		c, rec := cliServer(t, map[string]string{
			"/api/projects": cliProjects,
			"/agents":       agentsHTML,
		})
		if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"agents", "delete", "Reviewer"}, true, false); err != nil {
			t.Fatalf("expected zero exit with --force: %v", err)
		}
		if !rec.saw("DELETE", "/agents/ag-1") {
			t.Errorf("expected backend call with --force:\n%s", rec.all())
		}
	})

	t.Run("channels_remove/without_force_exits_nonzero", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects})
		err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"channels", "remove", "email"}, false, false)
		if err == nil {
			t.Fatal("expected nonzero exit without --force")
		}
		if !strings.Contains(err.Error(), "--force") {
			t.Errorf("error should mention --force, got: %v", err)
		}
		if rec.saw("POST", "/channels/email/remove") {
			t.Error("must not call backend without --force")
		}
	})

	t.Run("channels_remove/with_force_calls_backend", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{
			"/api/projects": cliProjects,
			"/channels":     `<html><body>refreshed channels page</body></html>`,
		})
		if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"channels", "remove", "email"}, true, false); err != nil {
			t.Fatalf("expected zero exit with --force: %v", err)
		}
		if !rec.saw("POST", "/channels/email/remove") {
			t.Errorf("expected backend call with --force:\n%s", rec.all())
		}
		if !rec.sawQuery("project_id=p1") {
			t.Errorf("forced channel removal was not scoped to the -project selection:\n%s", rec.all())
		}
	})
}

func TestCLIAgentsDeleteValidatesBeforeForceGate(t *testing.T) {
	const agentsHTML = `<div data-agent-id="ag-reviewer" data-agent-key="reviewer"
		data-agent-name="Code Reviewer" data-agent-description="reviews code"
		data-agent-model="claude" data-agent-scope="project"></div>
	<div data-agent-id="ag-alpha" data-agent-key="alpha"
		data-agent-name="Review Alpha" data-agent-description="reviews releases"
		data-agent-model="claude" data-agent-scope="project"></div>
	<div data-agent-id="ag-beta" data-agent-key="beta"
		data-agent-name="Review Beta" data-agent-description="reviews releases"
		data-agent-model="claude" data-agent-scope="project"></div>`

	for _, tc := range []struct {
		name    string
		ref     string
		wantErr string
	}{
		{name: "ambiguous", ref: "review", wantErr: "is ambiguous"},
		{name: "unknown", ref: "missing", wantErr: "nothing matches"},
		{name: "partial canonical force message", ref: "code", wantErr: `use --force to confirm deletion of agent "Code Reviewer"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := cliServer(t, map[string]string{
				"/api/projects": cliProjects,
				"/agents":       agentsHTML,
			})
			err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"agents", "delete", tc.ref}, false, false)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
			if calls := rec.all(); strings.Contains(calls, "DELETE /agents/") {
				t.Fatalf("unforced delete mutated backend:\n%s", calls)
			}
		})
	}
}

// --- JSON output mode tests ---

func TestCLICreatesProjectAndSupportsJSON(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r.Method, r.URL.Path)
		if r.Method != http.MethodPost || r.URL.Path != "/projects" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if r.FormValue("repo_source") != "local" {
			t.Errorf("repo_source = %q", r.FormValue("repo_source"))
		}
		if r.FormValue("name") == "My Project" && r.FormValue("repo_path") != `C:\Users\me\repo` {
			t.Errorf("unexpected platform path: %q", r.FormValue("repo_path"))
		}
		id := "created-project"
		if r.FormValue("name") == "JSON Project" {
			id = "json-project"
		}
		w.Header().Set("Location", "/tasks?project_id="+id)
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunCLI(c, &out, "", []string{"projects", "create", "My", "Project", "|", `C:\Users\me\repo`}, false, false); err != nil {
		t.Fatalf("projects create failed: %v", err)
	}
	plain := out.String()
	for _, want := range []string{"My Project", "project ID: created-project", "-project created-project", "openvibely-terminal -project created-project tasks"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("plain creation output missing %q:\n%s", want, plain)
		}
	}
	if rec.saw("GET", "/api/projects") {
		t.Fatalf("creation should not require a project-list preflight:\n%s", rec.all())
	}

	out.Reset()
	if err := RunCLI(c, &out, "", []string{"projects", "create", "JSON", "Project", "/tmp/json-project"}, false, true); err != nil {
		t.Fatalf("projects create --json failed: %v", err)
	}
	var project client.Project
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &project); err != nil {
		t.Fatalf("JSON output is invalid: %v\noutput: %s", err, out.String())
	}
	if project.ID != "json-project" || project.Name != "JSON Project" || project.Path != "/tmp/json-project" {
		t.Fatalf("unexpected JSON project: %+v", project)
	}
}

func TestCLICreateProjectValidationAndBackendFailure(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r.Method, r.URL.Path)
		if r.Method == http.MethodPost && r.URL.Path == "/projects" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"backend rejected project"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := RunCLI(c, &bytes.Buffer{}, "", []string{"projects", "create"}, false, false); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("missing-name validation error = %v", err)
	}
	if err := RunCLI(c, &bytes.Buffer{}, "", []string{"projects", "create", "demo"}, false, false); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("missing-path validation error = %v", err)
	}
	if rec.saw("POST", "/projects") {
		t.Fatal("validation should not call the backend")
	}

	if err := RunCLI(c, &bytes.Buffer{}, "", []string{"projects", "create", "demo", "/tmp/demo"}, false, false); err == nil || !strings.Contains(err.Error(), "backend rejected project") {
		t.Fatalf("backend failure error = %v", err)
	}
}

func TestCLIJSONEntryWritersShareNormalization(t *testing.T) {
	entries := []entry{
		{role: "error", text: "hidden error\n"},
		{role: "system", text: "hidden system\n"},
		{role: "result", text: "\n\n"},
		{role: "agent", text: "{\"result\":\"ok\"}\n\n"},
		{role: "you", text: "plain user entry\n"},
		{role: "event", text: "event entry\n\n"},
	}

	var raw bytes.Buffer
	writeJSONEntries(&raw, entries)
	if got, want := raw.String(), "{\"result\":\"ok\"}\nplain user entry\nevent entry\n"; got != want {
		t.Fatalf("raw JSON entries = %q, want %q", got, want)
	}

	var scoped bytes.Buffer
	writeScopedJSONEntries(&scoped, entries, client.Project{ID: "p1", Name: "demo"})
	var output struct {
		ProjectID   string          `json:"project_id"`
		ProjectName string          `json:"project_name"`
		Data        json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(scoped.Bytes(), &output); err != nil {
		t.Fatalf("scoped JSON output is invalid: %v\n%s", err, scoped.String())
	}
	if output.ProjectID != "p1" || output.ProjectName != "demo" {
		t.Fatalf("scoped project = %q/%q, want p1/demo", output.ProjectID, output.ProjectName)
	}
	var values []json.RawMessage
	if err := json.Unmarshal(output.Data, &values); err != nil {
		t.Fatalf("scoped data is not an array: %v\n%s", err, output.Data)
	}
	if got, want := len(values), 3; got != want {
		t.Fatalf("scoped data contains %d values, want %d: %s", got, want, output.Data)
	}
	if got, want := string(values[0]), `{"result":"ok"}`; got != want {
		t.Errorf("scoped raw JSON value = %s, want %s", got, want)
	}
	if got, want := string(values[1]), `"plain user entry"`; got != want {
		t.Errorf("scoped non-JSON value = %s, want %s", got, want)
	}
	if got, want := string(values[2]), `"event entry"`; got != want {
		t.Errorf("scoped event value = %s, want %s", got, want)
	}
}

func TestCLIJSONEntryWritersOmitEntriesWithoutText(t *testing.T) {
	entries := []entry{
		{role: "error", text: "error"},
		{role: "system", text: "system"},
		{role: "result", text: "\n\n"},
	}

	var raw bytes.Buffer
	writeJSONEntries(&raw, entries)
	if raw.Len() != 0 {
		t.Fatalf("raw JSON output = %q, want empty output", raw.String())
	}

	var scoped bytes.Buffer
	writeScopedJSONEntries(&scoped, entries, client.Project{ID: "p1", Name: "demo"})
	if scoped.Len() != 0 {
		t.Fatalf("scoped JSON output = %q, want empty output", scoped.String())
	}
}

func TestCLIJSONScopedEntryPayloadsPreserveJSONAndQuoteText(t *testing.T) {
	entries := []entry{
		{role: "result", text: "  {\"answer\":true}  \n"},
		{role: "agent", text: "not JSON\n"},
	}

	var out bytes.Buffer
	writeScopedJSONEntries(&out, entries, client.Project{ID: "p1", Name: "demo"})
	var output struct {
		ProjectID   string          `json:"project_id"`
		ProjectName string          `json:"project_name"`
		Data        json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &output); err != nil {
		t.Fatalf("scoped JSON output is invalid: %v\n%s", err, out.String())
	}
	var values []json.RawMessage
	if err := json.Unmarshal(output.Data, &values); err != nil {
		t.Fatalf("scoped data is not an array: %v\n%s", err, output.Data)
	}
	if got, want := string(values[0]), `{"answer":true}`; got != want {
		t.Errorf("scoped JSON payload = %s, want %s", got, want)
	}
	if got, want := string(values[1]), `"not JSON"`; got != want {
		t.Errorf("scoped text payload = %s, want %s", got, want)
	}
}

func TestCLIJSONScopedSingleEntryKeepsDataShape(t *testing.T) {
	var out bytes.Buffer
	writeScopedJSONEntries(&out, []entry{{role: "result", text: "{\"ok\":true}\n"}}, client.Project{ID: "p1", Name: "demo"})

	var output struct {
		ProjectID   string          `json:"project_id"`
		ProjectName string          `json:"project_name"`
		Data        json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &output); err != nil {
		t.Fatalf("scoped JSON output is invalid: %v\n%s", err, out.String())
	}
	if got, want := string(output.Data), `{"ok":true}`; got != want {
		t.Fatalf("single scoped data = %s, want raw JSON object %s", got, want)
	}
	if output.ProjectID != "p1" || output.ProjectName != "demo" {
		t.Fatalf("scoped project = %q/%q, want p1/demo", output.ProjectID, output.ProjectName)
	}
}

func TestCLIJSONTasksList(t *testing.T) {
	const board = `<div data-task-id="t-1" data-task-status="running" data-task-category="active">
		<a href="/tasks/t-1?from=tasks" title="Refactor the API">Refactor the API</a>
	</div>`
	c, _ := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/tasks":        board,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"tasks"}, false, true); err != nil {
		t.Fatalf("tasks --json failed: %v", err)
	}
	got := out.String()
	var tasks []client.Task
	if err := json.Unmarshal([]byte(strings.TrimSpace(got)), &tasks); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, got)
	}
	if len(tasks) != 1 {
		t.Errorf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].ID != "t-1" {
		t.Errorf("unexpected task ID: %s", tasks[0].ID)
	}
}

func TestCLILaterPageAlertCommands(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		jsonMode   bool
		force      bool
		wantMethod string
		wantPath   string
	}{
		{name: "json list", args: []string{"alerts"}, jsonMode: true},
		{name: "json show", args: []string{"alerts", "show", "a-later"}, jsonMode: true, wantMethod: http.MethodGet, wantPath: "/alerts/a-later/details"},
		{name: "approve", args: []string{"alerts", "approve", "a-later"}, wantMethod: http.MethodPost, wantPath: "/alerts/a-later/approve"},
		{name: "reject", args: []string{"alerts", "reject", "a-later"}, wantMethod: http.MethodPost, wantPath: "/alerts/a-later/reject"},
		{name: "dismiss", args: []string{"alerts", "dismiss", "a-later"}, wantMethod: http.MethodPost, wantPath: "/alerts/a-later/dismiss"},
		{name: "read", args: []string{"alerts", "read", "a-later"}, wantMethod: http.MethodPost, wantPath: "/alerts/a-later/read"},
		{name: "delete", args: []string{"alerts", "delete", "a-later"}, force: true, wantMethod: http.MethodDelete, wantPath: "/alerts/a-later"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mutation string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/projects" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, cliProjects)
					return
				}
				if r.URL.Query().Get("project_id") != "p1" {
					t.Errorf("request lost project scope: %s", r.URL.RequestURI())
				}
				w.Header().Set("Content-Type", "text/html")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/alerts" && r.URL.Query().Get("card_page") == "1":
					q := r.URL.Query()
					if q.Get("page") != "1" || q.Get("page_size") != "50" || q.Get("offset") != "1" {
						t.Errorf("bad continuation query: %s", r.URL.RawQuery)
					}
					w.Header().Set("X-OpenVibely-Card-Page-Has-More", "false")
					_, _ = io.WriteString(w, `<div data-card-pagination-root data-card-pagination-card-selector="[data-alert-id]" data-card-pagination-key="data-alert-id" data-card-pagination-has-more="false"><div data-alert-id="a-later" data-alert-scroll-anchor="a-later"><p class="font-semibold">Later CLI alert</p></div></div>`)
				case r.Method == http.MethodGet && r.URL.Path == "/alerts":
					_, _ = io.WriteString(w, `<div data-card-pagination-root data-card-pagination-card-selector="[data-alert-id]" data-card-pagination-key="data-alert-id" data-card-pagination-has-more="true"><div data-alert-id="a-first" data-alert-scroll-anchor="a-first"><p class="font-semibold">First alert</p></div></div>`)
				case r.Method == http.MethodGet && r.URL.Path == "/alerts/a-later/details":
					mutation = r.Method + " " + r.URL.Path
					_, _ = io.WriteString(w, `<div data-alert-detail-loaded><div data-alert-markdown data-raw-content="Later CLI detail"></div></div>`)
				case r.Method == http.MethodDelete && r.URL.Path == "/alerts/a-later":
					mutation = r.Method + " " + r.URL.Path
					_, _ = io.WriteString(w, `<div data-alert-id="a-first" data-alert-scroll-anchor="a-first"><p class="font-semibold">First alert</p></div>`)
				case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/alerts/a-later/"):
					mutation = r.Method + " " + r.URL.Path
					w.WriteHeader(http.StatusNoContent)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := RunCLI(c, &out, "demo", tt.args, tt.force, tt.jsonMode); err != nil {
				t.Fatalf("RunCLI: %v", err)
			}
			if tt.wantPath != "" && mutation != tt.wantMethod+" "+tt.wantPath {
				t.Fatalf("mutation/detail request = %q, want %s %s", mutation, tt.wantMethod, tt.wantPath)
			}
			if !strings.Contains(out.String(), "Later CLI alert") && !strings.Contains(out.String(), "Later CLI detail") {
				t.Fatalf("CLI output omitted later-page alert:\n%s", out.String())
			}
		})
	}
}

func TestCLIAlertActionsExactTitleBeatsLongerTitlePrefix(t *testing.T) {
	const alertsHTML = `<div data-alert-id="a-deploy" data-alert-scroll-anchor="a-deploy" data-search-text="deploy exact body"><p class="font-semibold">Deploy</p></div>
		<div data-alert-id="a-deploy-service" data-alert-scroll-anchor="a-deploy-service" data-search-text="deploy service body"><p class="font-semibold">Deploy service</p></div>`

	actions := []struct {
		action string
		method string
		path   string
		force  bool
	}{
		{action: "read", method: http.MethodPost, path: "/alerts/a-deploy/read"},
		{action: "approve", method: http.MethodPost, path: "/alerts/a-deploy/approve"},
		{action: "reject", method: http.MethodPost, path: "/alerts/a-deploy/reject"},
		{action: "dismiss", method: http.MethodPost, path: "/alerts/a-deploy/dismiss"},
		{action: "delete", method: http.MethodDelete, path: "/alerts/a-deploy", force: true},
	}
	for _, tc := range actions {
		t.Run(tc.action, func(t *testing.T) {
			c, rec := cliServer(t, map[string]string{
				"/api/projects":    cliProjects,
				"/alerts":          alertsHTML,
				"/alerts/a-deploy": alertsHTML,
			})
			var out bytes.Buffer
			if err := RunCLI(c, &out, "demo", []string{"alerts", tc.action, "dEpLoY"}, tc.force, false); err != nil {
				t.Fatalf("RunCLI(%s): %v\n%s", tc.action, err, out.String())
			}
			if !rec.saw(tc.method, tc.path) {
				t.Fatalf("%s did not act on the exact title:\n%s", tc.action, rec.all())
			}
			if rec.saw(http.MethodPost, "/alerts/a-deploy-service/"+tc.action) || rec.saw(http.MethodDelete, "/alerts/a-deploy-service") {
				t.Fatalf("%s acted on the longer prefix candidate:\n%s", tc.action, rec.all())
			}
		})
	}
}

func TestCLIAlertsDeleteForceGuidanceSanitizesReference(t *testing.T) {
	const hostileRef = "Deploy\x1b[31m\nproduction\r\x00"
	c, rec := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
	})

	var out bytes.Buffer
	err := RunCLI(c, &out, "demo", []string{"alerts", "delete", hostileRef}, false, false)
	if err == nil {
		t.Fatal("unforced delete unexpectedly succeeded")
	}
	got := err.Error()
	for _, unsafe := range []string{"\x1b", "\n", "\r", "\x00"} {
		if strings.Contains(got, unsafe) {
			t.Fatalf("unsafe force guidance contains %q: %q", unsafe, got)
		}
	}
	if !strings.Contains(got, "Deploy production") || !strings.Contains(got, "--force") {
		t.Fatalf("force guidance omitted sanitized reference or flag: %q", got)
	}
	if rec.count(http.MethodGet, "/alerts") != 0 || rec.count(http.MethodDelete, "/alerts/a-1") != 0 {
		t.Fatalf("unforced delete made an alert request:\n%s", rec.all())
	}
}

func TestCLIJSONAlertsDeleteUsesForceAndRefreshedResponse(t *testing.T) {
	const initialAlerts = `<div data-alert-id="a-1" data-alert-scroll-anchor="a-1" data-search-text="build"><p class="font-semibold">Build failed</p></div>`
	const refreshedAlerts = `<div data-alert-id="a-2" data-alert-scroll-anchor="a-2" data-search-text="remaining"><p class="font-semibold">Remaining</p></div>`
	var gets, deletes int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, cliProjects)
		case r.Method == http.MethodGet && r.URL.Path == "/alerts":
			gets++
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, initialAlerts)
		case r.Method == http.MethodDelete && r.URL.Path == "/alerts/a-1":
			deletes++
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, refreshedAlerts)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"/alerts", "delete", "a-1"}, false, true); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("unforced delete error = %v", err)
	}
	if gets != 0 || deletes != 0 {
		t.Fatalf("unforced delete performed work: gets=%d deletes=%d", gets, deletes)
	}

	out.Reset()
	if err := RunCLI(c, &out, "demo", []string{"/alerts", "delete", "a-1"}, true, true); err != nil {
		t.Fatalf("forced JSON delete: %v", err)
	}
	var got []client.Alert
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &got); err != nil {
		t.Fatalf("delete JSON is invalid: %v\n%s", err, out.String())
	}
	if len(got) != 1 || got[0].ID != "a-2" || got[0].Title != "Remaining" {
		t.Fatalf("delete JSON = %#v", got)
	}
	if gets != 1 || deletes != 1 {
		t.Fatalf("forced delete requests: gets=%d deletes=%d, want 1 each", gets, deletes)
	}
}

func TestCLIAlertBulkCommandsForceJSONAndResolution(t *testing.T) {
	const alertsHTML = `<div data-alert-id="a-one" data-alert-scroll-anchor="a-one"><p class="font-semibold">One</p></div>
		<div data-alert-id="a-two" data-alert-scroll-anchor="a-two"><p class="font-semibold">Two alert</p></div>`
	var reads, deletes int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, cliProjects)
		case r.Method == http.MethodGet && r.URL.Path == "/alerts":
			if r.URL.Query().Get("project_id") != "p1" {
				t.Errorf("alert resolution lost project scope: %s", r.URL.RequestURI())
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, alertsHTML)
		case r.Method == http.MethodPost && r.URL.Path == "/alerts/read-bulk":
			reads++
			assertCLIAlertBulkRequest(t, r, []string{"a-one", "a-two"})
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"updated":2}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/alerts/bulk":
			deletes++
			assertCLIAlertBulkRequest(t, r, []string{"a-one", "a-two"})
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"deleted":2}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"alerts", "delete-bulk", "a-one", "a-two"}, false, false); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("unforced bulk delete error = %v", err)
	}
	if deletes != 0 {
		t.Fatalf("unforced bulk delete mutated backend: %d", deletes)
	}

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"alerts", "delete-bulk", "a-one", "Two alert"}, true, true); err != nil {
		t.Fatalf("forced JSON bulk delete: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != `{"deleted":2}` {
		t.Fatalf("bulk delete JSON = %q, want stable count object", got)
	}
	if deletes != 1 {
		t.Fatalf("forced bulk delete requests = %d, want 1", deletes)
	}

	out.Reset()
	if err := RunCLI(c, &out, "demo", []string{"alerts", "read-bulk", "a-one", "Two alert"}, false, true); err != nil {
		t.Fatalf("JSON bulk read: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != `{"updated":2}` {
		t.Fatalf("bulk read JSON = %q, want stable count object", got)
	}
	if reads != 1 {
		t.Fatalf("bulk read requests = %d, want 1", reads)
	}

	if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"alerts", "read-bulk", "a-one", "One"}, false, false); err == nil || !strings.Contains(err.Error(), "selected more than once") {
		t.Fatalf("duplicate bulk reference error = %v", err)
	}
	if reads != 1 || deletes != 1 {
		t.Fatalf("duplicate bulk reference mutated backend: reads=%d deletes=%d", reads, deletes)
	}
	if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"alerts", "delete-bulk", "foreign-alert"}, true, false); err == nil || !strings.Contains(err.Error(), "nothing matches") {
		t.Fatalf("foreign bulk reference error = %v", err)
	}
	if deletes != 1 {
		t.Fatalf("foreign bulk reference mutated backend: deletes=%d", deletes)
	}
}

func TestCLIAlertBulkJSONRejectsMalformedCounts(t *testing.T) {
	const alertsHTML = `<div data-alert-id="a-one" data-alert-scroll-anchor="a-one"><p class="font-semibold">One</p></div>`
	endpoints := []struct {
		name      string
		action    string
		method    string
		path      string
		countName string
	}{
		{name: "read", action: "read-bulk", method: http.MethodPost, path: "/alerts/read-bulk", countName: "updated"},
		{name: "delete", action: "delete-bulk", method: http.MethodDelete, path: "/alerts/bulk", countName: "deleted"},
	}
	responses := []struct {
		name    string
		body    func(string) string
		wantErr bool
	}{
		{name: "missing count", body: func(string) string { return `{}` }, wantErr: true},
		{name: "null count", body: func(field string) string { return fmt.Sprintf(`{%q:null}`, field) }, wantErr: true},
		{name: "negative count", body: func(field string) string { return fmt.Sprintf(`{%q:-1}`, field) }, wantErr: true},
		{name: "error object with count", body: func(field string) string { return fmt.Sprintf(`{"error":"bulk mutation rejected",%q:0}`, field) }, wantErr: true},
		{name: "explicit zero", body: func(field string) string { return fmt.Sprintf(`{%q:0}`, field) }},
	}

	for _, endpoint := range endpoints {
		t.Run(endpoint.name, func(t *testing.T) {
			for _, response := range responses {
				t.Run(response.name, func(t *testing.T) {
					var lists, mutations int
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						switch {
						case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
							w.Header().Set("Content-Type", "application/json")
							_, _ = io.WriteString(w, cliProjects)
						case r.Method == http.MethodGet && r.URL.Path == "/alerts":
							lists++
							w.Header().Set("Content-Type", "text/html")
							_, _ = io.WriteString(w, alertsHTML)
						case r.Method == endpoint.method && r.URL.Path == endpoint.path:
							mutations++
							assertCLIAlertBulkRequest(t, r, []string{"a-one"})
							w.Header().Set("Content-Type", "application/json")
							_, _ = io.WriteString(w, response.body(endpoint.countName))
						default:
							http.NotFound(w, r)
						}
					}))
					t.Cleanup(srv.Close)
					c, err := client.New(srv.URL)
					if err != nil {
						t.Fatal(err)
					}
					var out bytes.Buffer
					err = RunCLI(c, &out, "demo", []string{"alerts", endpoint.action, "a-one"}, endpoint.action == "delete-bulk", true)
					if lists != 1 || mutations != 1 {
						t.Fatalf("requests = lists %d mutations %d, want 1 each", lists, mutations)
					}
					if response.wantErr {
						if err == nil {
							t.Fatal("malformed count response succeeded")
						}
						if got := strings.TrimSpace(out.String()); got != "" {
							t.Fatalf("malformed JSON output = %q, want no success count", got)
						}
						return
					}
					if err != nil {
						t.Fatalf("explicit zero failed: %v", err)
					}
					want := fmt.Sprintf(`{%q:0}`, endpoint.countName)
					if got := strings.TrimSpace(out.String()); got != want {
						t.Fatalf("explicit zero JSON = %q, want %q", got, want)
					}
				})
			}
		})
	}
}

func assertCLIAlertBulkRequest(t *testing.T, r *http.Request, want []string) {
	t.Helper()
	if r.URL.Query().Get("project_id") != "p1" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		t.Errorf("bulk request = %s content-type=%q", r.URL.RequestURI(), r.Header.Get("Content-Type"))
	}
	var payload struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		t.Fatalf("decode bulk request: %v", err)
	}
	if !reflect.DeepEqual(payload.IDs, want) {
		t.Errorf("bulk IDs = %#v, want %#v", payload.IDs, want)
	}
}

func TestCLIAlertsDeleteResolutionAndBackendErrors(t *testing.T) {
	const duplicateAlerts = `<div data-alert-id="a-1" data-alert-scroll-anchor="a-1" data-search-text="first"><p class="font-semibold">Duplicate</p></div>
		<div data-alert-id="a-2" data-alert-scroll-anchor="a-2" data-search-text="second"><p class="font-semibold">Duplicate</p></div>`
	const ambiguousSearchText = `<div data-alert-id="a-1" data-alert-scroll-anchor="a-1" data-search-text="shared release note"><p class="font-semibold">Deploy</p></div>
		<div data-alert-id="a-2" data-alert-scroll-anchor="a-2" data-search-text="shared release note"><p class="font-semibold">Deploy service</p></div>`

	for _, tc := range []struct {
		name   string
		ref    string
		alerts string
		want   string
	}{
		{name: "missing", ref: "missing", alerts: duplicateAlerts, want: `nothing matches "missing"`},
		{name: "duplicate title", ref: "Duplicate", alerts: duplicateAlerts, want: `"Duplicate" is ambiguous:`},
		{name: "ambiguous search text", ref: "shared release note", alerts: ambiguousSearchText, want: `"shared release note" is ambiguous:`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := cliServer(t, map[string]string{
				"/api/projects": cliProjects,
				"/alerts":       tc.alerts,
			})
			var out bytes.Buffer
			err := RunCLI(c, &out, "demo", []string{"alerts", "delete", tc.ref}, true, false)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("delete error = %v, want %q", err, tc.want)
			}
			if rec.count(http.MethodDelete, "/alerts/a-1") != 0 || rec.count(http.MethodDelete, "/alerts/a-2") != 0 {
				t.Fatalf("unresolved reference performed deletion:\n%s", rec.all())
			}
		})
	}

	t.Run("backend failure", func(t *testing.T) {
		const alertsHTML = `<div data-alert-id="a-1" data-alert-scroll-anchor="a-1" data-search-text="build"><p class="font-semibold">Build failed</p></div>`
		var deletes int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, cliProjects)
			case r.Method == http.MethodGet && r.URL.Path == "/alerts":
				w.Header().Set("Content-Type", "text/html")
				_, _ = io.WriteString(w, alertsHTML)
			case r.Method == http.MethodDelete && r.URL.Path == "/alerts/a-1":
				deletes++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadGateway)
				_, _ = io.WriteString(w, `{"error":"alert deletion unavailable"}`)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()
		c, err := client.New(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		err = RunCLI(c, &out, "demo", []string{"alerts", "delete", "a-1"}, true, false)
		if err == nil || !strings.Contains(err.Error(), "alert deletion unavailable") {
			t.Fatalf("backend error = %v", err)
		}
		if deletes != 1 || strings.Contains(stripANSI(out.String()), "delete: Build failed") {
			t.Fatalf("backend failure reported success or wrong delete count: deletes=%d output=%q", deletes, out.String())
		}
	})
}

func TestCLIAlertsWorkflowStatusFilterOutput(t *testing.T) {
	t.Run("plain output includes later filtered page", func(t *testing.T) {
		requests := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/projects":
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, cliProjects)
			case "/alerts":
				requests++
				q := r.URL.Query()
				if q.Get("project_id") != "p1" || q.Get("decision_state") != "pending" || q.Get("processing_state") != "unclaimed" {
					t.Errorf("filtered request = %s", r.URL.RequestURI())
				}
				w.Header().Set("Content-Type", "text/html")
				if requests == 1 {
					_, _ = io.WriteString(w, `<div data-card-pagination-root data-card-pagination-card-selector="[data-alert-id]" data-card-pagination-key="data-alert-id" data-card-pagination-has-more="true"><div data-alert-id="first" data-alert-scroll-anchor="first"><p class="font-semibold">First queued alert</p></div></div>`)
					return
				}
				if q.Get("card_page") != "1" || q.Get("offset") != "1" {
					t.Errorf("continuation query = %s", r.URL.RawQuery)
				}
				w.Header().Set("X-OpenVibely-Card-Page-Has-More", "false")
				_, _ = io.WriteString(w, `<div data-card-pagination-root data-card-pagination-card-selector="[data-alert-id]" data-card-pagination-key="data-alert-id" data-card-pagination-has-more="false"><div data-alert-id="later" data-alert-scroll-anchor="later"><p class="font-semibold">Later queued alert</p></div></div>`)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()
		c, err := client.New(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := RunCLI(c, &out, "demo", []string{"alerts", "list", "--decision-state", "pending", "--processing-state", "unclaimed"}, false, false); err != nil {
			t.Fatalf("filtered alerts: %v", err)
		}
		plain := stripANSI(out.String())
		if !strings.Contains(plain, "First queued alert") || !strings.Contains(plain, "Later queued alert") {
			t.Fatalf("plain output = %s", plain)
		}
		if requests != 2 {
			t.Fatalf("requests = %d, want 2", requests)
		}
	})

	t.Run("empty JSON remains an array", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/projects":
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, cliProjects)
			case "/alerts":
				q := r.URL.Query()
				if q.Get("project_id") != "p1" || q.Get("decision_state") != "dismissed" || q.Get("processing_state") != "completed" {
					t.Errorf("empty filtered request = %s", r.URL.RequestURI())
				}
				w.Header().Set("Content-Type", "text/html")
				_, _ = io.WriteString(w, `<div data-card-pagination-root data-card-pagination-card-selector="[data-alert-id]" data-card-pagination-key="data-alert-id" data-card-pagination-has-more="false"></div>`)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()
		c, err := client.New(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := RunCLI(c, &out, "demo", []string{"alerts", "list", "--decision-state=dismissed", "--processing-state", "completed"}, false, true); err != nil {
			t.Fatalf("filtered JSON alerts: %v", err)
		}
		if got := strings.TrimSpace(out.String()); got != "[]" {
			t.Fatalf("empty JSON output = %q, want []", got)
		}
		var alerts []client.Alert
		if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &alerts); err != nil || alerts == nil || len(alerts) != 0 {
			t.Fatalf("empty JSON decode = %#v, %v", alerts, err)
		}
	})
}

func TestCLIJSONAlertsList(t *testing.T) {
	const alertsHTML = `<div data-alert-id="a-1" data-alert-scroll-anchor="1" data-search-text="bug pending">
		<p class="font-semibold">Login broken</p>
		<p class="text-sm opacity-60">OAuth redirect failure</p>
	</div>`
	c, _ := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/alerts":       alertsHTML,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"alerts"}, false, true); err != nil {
		t.Fatalf("alerts --json failed: %v", err)
	}
	got := out.String()
	var alerts []client.Alert
	if err := json.Unmarshal([]byte(strings.TrimSpace(got)), &alerts); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, got)
	}
	if len(alerts) != 1 {
		t.Errorf("expected 1 alert, got %d", len(alerts))
	}
	if alerts[0].Title != "Login broken" {
		t.Errorf("unexpected alert title: %s", alerts[0].Title)
	}
}

func TestCLIAlertsShowCanonicalIDStopsEarlyWithEquivalentOutput(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	runShow := func(t *testing.T, ref string, jsonOutput bool) (string, int, int) {
		t.Helper()
		listRequests, detailRequests := 0, 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			switch r.URL.Path {
			case "/api/projects":
				_, _ = io.WriteString(w, cliProjects)
			case "/alerts":
				listRequests++
				if r.URL.Query().Get("project_id") != "p1" {
					t.Errorf("unscoped alert request: %s", r.URL.RequestURI())
				}
				if r.URL.Query().Get("card_page") == "" {
					_, _ = io.WriteString(w, alertPageForCLI([]string{id}, true))
					return
				}
				w.Header().Set("X-OpenVibely-Card-Page-Has-More", "false")
				_, _ = io.WriteString(w, alertPageForCLI([]string{"fedcba9876543210fedcba9876543210"}, false))
			case "/alerts/" + id + "/details":
				detailRequests++
				_, _ = io.WriteString(w, `<div data-alert-detail-loaded><div data-alert-markdown data-raw-content="same detail"></div><pre>{"key":"value"}</pre></div>`)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()
		c, err := client.New(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := RunCLI(c, &out, "demo", []string{"alerts", "show", ref}, false, jsonOutput); err != nil {
			t.Fatalf("alerts show %q: %v", ref, err)
		}
		return out.String(), listRequests, detailRequests
	}

	for _, jsonOutput := range []bool{false, true} {
		canonical, canonicalLists, canonicalDetails := runShow(t, id, jsonOutput)
		noncanonical, noncanonicalLists, noncanonicalDetails := runShow(t, strings.ToUpper(id), jsonOutput)
		if canonical != noncanonical {
			t.Fatalf("json=%t output differs\ncanonical:\n%s\nnoncanonical:\n%s", jsonOutput, canonical, noncanonical)
		}
		if canonicalLists != 1 || noncanonicalLists != 2 {
			t.Fatalf("json=%t list requests canonical=%d noncanonical=%d, want 1 and 2", jsonOutput, canonicalLists, noncanonicalLists)
		}
		if canonicalDetails != 1 || noncanonicalDetails != 1 {
			t.Fatalf("json=%t detail requests canonical=%d noncanonical=%d", jsonOutput, canonicalDetails, noncanonicalDetails)
		}
	}
}

func alertPageForCLI(ids []string, hasMore bool) string {
	var body strings.Builder
	fmt.Fprintf(&body, `<div data-card-pagination-root data-card-pagination-card-selector="[data-alert-id]" data-card-pagination-key="data-alert-id" data-card-pagination-has-more="%t">`, hasMore)
	for _, id := range ids {
		fmt.Fprintf(&body, `<div data-alert-id="%s" data-alert-scroll-anchor="%s" data-alert-type="custom" data-alert-severity="warning" data-alert-decision-state="pending" data-alert-processing-state="unclaimed"><p class="font-semibold">Target alert</p><p class="text-sm opacity-60">same message</p></div>`, id, id)
	}
	body.WriteString(`</div>`)
	return body.String()
}

func TestCLIAlertsShowCanonicalUnknownAndMalformedReferencesStaySafe(t *testing.T) {
	const listedID = "0123456789abcdef0123456789abcdef"
	tests := []struct {
		name string
		ref  string
	}{
		{name: "unknown canonical", ref: "ffffffffffffffffffffffffffffffff"},
		{name: "malformed id like", ref: "fffffffffffffffffffffffffffffff"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			listRequests, detailRequests := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				switch r.URL.Path {
				case "/api/projects":
					_, _ = io.WriteString(w, cliProjects)
				case "/alerts":
					listRequests++
					if r.URL.Query().Get("card_page") == "" {
						_, _ = io.WriteString(w, alertPageForCLI([]string{listedID}, true))
						return
					}
					w.Header().Set("X-OpenVibely-Card-Page-Has-More", "false")
					_, _ = io.WriteString(w, alertPageForCLI([]string{"11111111111111111111111111111111"}, false))
				default:
					if strings.HasSuffix(r.URL.Path, "/details") {
						detailRequests++
					}
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			c, _ := client.New(srv.URL)
			var out bytes.Buffer
			err := RunCLI(c, &out, "demo", []string{"alerts", "show", tt.ref}, false, false)
			if err == nil || !strings.Contains(err.Error(), "nothing matches") {
				t.Fatalf("error = %v", err)
			}
			if listRequests != 2 || detailRequests != 0 {
				t.Fatalf("list requests = %d, detail requests = %d", listRequests, detailRequests)
			}
		})
	}
}

func TestCLIAlertsShowCanonicalShapedExactTitleFallsBackToFullMatching(t *testing.T) {
	const ref = "ffffffffffffffffffffffffffffffff"
	const alertID = "0123456789abcdef0123456789abcdef"
	listRequests, detailRequests := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/api/projects":
			_, _ = io.WriteString(w, cliProjects)
		case "/alerts":
			listRequests++
			if r.URL.Query().Get("card_page") == "" {
				_, _ = io.WriteString(w, alertPageForCLI([]string{"11111111111111111111111111111111"}, true))
				return
			}
			w.Header().Set("X-OpenVibely-Card-Page-Has-More", "false")
			_, _ = io.WriteString(w, `<div data-card-pagination-root data-card-pagination-card-selector="[data-alert-id]" data-card-pagination-key="data-alert-id" data-card-pagination-has-more="false"><div data-alert-id="`+alertID+`" data-alert-scroll-anchor="`+alertID+`"><p class="font-semibold">`+ref+`</p></div></div>`)
		case "/alerts/" + alertID + "/details":
			detailRequests++
			_, _ = io.WriteString(w, `<div data-alert-detail-loaded><div data-alert-markdown data-raw-content="title fallback detail"></div></div>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, _ := client.New(srv.URL)

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"alerts", "show", ref}, false, false); err != nil {
		t.Fatalf("alerts show canonical-shaped title: %v", err)
	}
	if !strings.Contains(out.String(), "title fallback detail") {
		t.Fatalf("output missing detail: %s", out.String())
	}
	if listRequests != 2 || detailRequests != 1 {
		t.Fatalf("list requests = %d, detail requests = %d; want 2 and 1", listRequests, detailRequests)
	}
}

func TestCLIJSONAlertsShowIncludesSummaryAndFullDetail(t *testing.T) {
	const alertsHTML = `<div class="card" data-alert-id="a-1" data-alert-scroll-anchor="a-1"
		data-search-text="review pending unclaimed" data-alert-type="custom"
		data-alert-severity="warning" data-alert-decision-state="pending"
		data-alert-processing-state="unclaimed">
		<svg class="h-5 w-5 text-warning"></svg>
		<p class="font-semibold">Review request</p>
		<p class="text-sm opacity-60">Approval is needed</p>
	</div>`
	const detailHTML = `<div data-alert-detail-loaded>
		<div data-alert-markdown data-raw-content="# Review request&#10;&#10;First line&#10;Second line"></div>
		<pre>{
  "owner": "release team",
  "attempt": 2
}</pre>
	</div>`
	c, rec := cliServer(t, map[string]string{
		"/api/projects":       cliProjects,
		"/alerts":             alertsHTML,
		"/alerts/a-1/details": detailHTML,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"alerts", "show", "a-1"}, false, true); err != nil {
		t.Fatalf("alerts show --json failed: %v", err)
	}
	if strings.Contains(out.String(), "\x1b") || strings.Contains(out.String(), "Alert\n") || strings.Contains(out.String(), "project:") {
		t.Fatalf("JSON output contains ANSI or banner text: %q", out.String())
	}
	var inspection client.AlertInspection
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &inspection); err != nil {
		t.Fatalf("alerts show JSON is invalid: %v\noutput: %s", err, out.String())
	}
	if inspection.Summary.ID != "a-1" || inspection.Summary.Title != "Review request" {
		t.Fatalf("summary identity = %+v", inspection.Summary)
	}
	if inspection.Summary.Type != "custom" || inspection.Summary.Severity != "warning" || inspection.Summary.DecisionState != "pending" || inspection.Summary.ProcessingState != "unclaimed" {
		t.Fatalf("summary state = %+v", inspection.Summary)
	}
	if inspection.Detail.Body != "# Review request\n\nFirst line\nSecond line" {
		t.Fatalf("detail body = %q", inspection.Detail.Body)
	}
	if inspection.Detail.Metadata == nil || inspection.Detail.Metadata["owner"] != "release team" {
		t.Fatalf("detail metadata = %#v", inspection.Detail.Metadata)
	}
	if got := rec.count("GET", "/alerts/a-1/details"); got != 1 {
		t.Fatalf("detail requests = %d, want one:\n%s", got, rec.all())
	}
	if rec.count("POST", "/alerts/a-1/approve") != 0 || rec.count("DELETE", "/alerts/a-1") != 0 {
		t.Fatalf("show made a mutation:\n%s", rec.all())
	}
}

func TestCLIJSONAlertsShowEmptyDetailUsesExplicitValues(t *testing.T) {
	c, _ := cliServer(t, map[string]string{
		"/api/projects":       cliProjects,
		"/alerts":             `<div data-alert-id="a-1" data-alert-scroll-anchor="a-1"><p class="font-semibold">No detail</p></div>`,
		"/alerts/a-1/details": `<p class="text-sm opacity-60">No additional detail.</p>`,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"alerts", "show", "a-1"}, false, true); err != nil {
		t.Fatalf("empty alerts show --json failed: %v", err)
	}
	var inspection client.AlertInspection
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &inspection); err != nil {
		t.Fatalf("empty alerts show JSON is invalid: %v\noutput: %s", err, out.String())
	}
	if inspection.Detail.Body != "" {
		t.Fatalf("empty detail body = %q, want empty string", inspection.Detail.Body)
	}
	if inspection.Detail.Metadata == nil || len(inspection.Detail.Metadata) != 0 {
		t.Fatalf("empty detail metadata = %#v, want {}", inspection.Detail.Metadata)
	}
	if !strings.Contains(strings.TrimSpace(out.String()), `"body":""`) || !strings.Contains(strings.TrimSpace(out.String()), `"metadata":{}`) {
		t.Fatalf("empty detail JSON does not represent empty values explicitly: %s", out.String())
	}
}

func TestCLIAlertsShowReferenceAndBackendErrors(t *testing.T) {
	const oneAlert = `<div class="card" data-alert-id="a-1" data-alert-scroll-anchor="a-1" data-search-text="review"><p class="font-semibold">Review request</p></div>`
	const twoAlerts = `<div>
		<div class="card" data-alert-id="a-1" data-alert-scroll-anchor="a-1" data-search-text="same one"><p class="font-semibold">Same title</p></div>
		<div class="card" data-alert-id="a-2" data-alert-scroll-anchor="a-2" data-search-text="same two"><p class="font-semibold">Same title</p></div>
	</div>`

	t.Run("unknown reference", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{
			"/api/projects": cliProjects,
			"/alerts":       oneAlert,
		})
		var out bytes.Buffer
		err := RunCLI(c, &out, "demo", []string{"alerts", "show", "missing"}, false, false)
		if err == nil || !strings.Contains(err.Error(), `nothing matches "missing"`) {
			t.Fatalf("unknown reference error = %v", err)
		}
		if rec.count("GET", "/alerts/a-1/details") != 0 {
			t.Fatal("unknown reference requested detail")
		}
	})

	t.Run("ambiguous reference", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{
			"/api/projects": cliProjects,
			"/alerts":       twoAlerts,
		})
		var out bytes.Buffer
		err := RunCLI(c, &out, "demo", []string{"alerts", "show", "same"}, false, false)
		if err == nil || !strings.Contains(err.Error(), "ambiguous") {
			t.Fatalf("ambiguous reference error = %v", err)
		}
		if rec.count("GET", "/alerts/a-1/details") != 0 || rec.count("GET", "/alerts/a-2/details") != 0 {
			t.Fatal("ambiguous reference requested detail")
		}
	})
}

func TestCLIAlertsShowPropagatesUnauthorizedAndBackendErrors(t *testing.T) {
	const alertsHTML = `<div data-alert-id="a-1" data-alert-scroll-anchor="a-1"><p class="font-semibold">Review request</p></div>`
	for _, tc := range []struct {
		name       string
		status     int
		body       string
		location   string
		wantError  string
		wantSecret string
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{"error":"private detail"}`, wantError: "requires sign-in", wantSecret: "private detail"},
		{name: "backend error", status: http.StatusServiceUnavailable, body: `{"error":"detail service unavailable"}`, wantError: "server error (503): detail service unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				rec.recordURL(r.Method, r.URL.RequestURI())
				switch r.URL.Path {
				case "/api/projects":
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(cliProjects))
				case "/alerts":
					w.Header().Set("Content-Type", "text/html")
					_, _ = w.Write([]byte(alertsHTML))
				case "/alerts/a-1/details":
					if tc.location != "" {
						w.Header().Set("Location", tc.location)
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(srv.Close)
			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}

			var out bytes.Buffer
			err = RunCLI(c, &out, "demo", []string{"alerts", "show", "a-1"}, false, false)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %v, want %q", err, tc.wantError)
			}
			if tc.wantSecret != "" && (strings.Contains(err.Error(), tc.wantSecret) || strings.Contains(out.String(), tc.wantSecret)) {
				t.Fatalf("error/output leaked response detail %q: error=%v output=%q", tc.wantSecret, err, out.String())
			}
		})
	}
}

func TestCLIJSONAutomationsList(t *testing.T) {
	const automationsHTML = `<div>
		<div class="card" data-automation-url="/automations/au-1?project_id=p1">
			<span class="badge badge-outline">active</span>
			<button data-automation-card-delete="au-1" data-automation-name="Native SDLC"></button>
		</div>
		<div class="card" data-automation-url="/automations/au-2?project_id=p1">
			<span class="badge badge-outline">paused</span>
			<button data-automation-card-delete="au-2" data-automation-name="GitHub SDLC"></button>
		</div>
	</div>`
	c, _ := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/automations":  automationsHTML,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"automations"}, false, true); err != nil {
		t.Fatalf("automations --json failed: %v", err)
	}
	var automations []client.Automation
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &automations); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out.String())
	}
	if len(automations) != 2 || automations[0].ID != "au-1" || automations[0].Name != "Native SDLC" || automations[0].State != "active" {
		t.Fatalf("automations = %+v", automations)
	}
}

func TestCLIJSONAutomationsEmptyListIsArray(t *testing.T) {
	c, _ := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/automations":  `<div></div>`,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"automations"}, false, true); err != nil {
		t.Fatalf("empty automations --json failed: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "[]" {
		t.Fatalf("empty automations JSON = %q, want []", got)
	}
}

func TestCLIJSONProjectsList(t *testing.T) {
	c, _ := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"projects"}, false, true); err != nil {
		t.Fatalf("projects --json failed: %v", err)
	}
	got := out.String()
	var projects []client.Project
	if err := json.Unmarshal([]byte(strings.TrimSpace(got)), &projects); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, got)
	}
	if len(projects) != 2 {
		t.Errorf("expected 2 projects, got %d", len(projects))
	}
}

func TestCLIJSONEmptyProjectsListStaysAnEmptyArray(t *testing.T) {
	c, _ := cliServer(t, map[string]string{
		"/api/projects": `{"projects":[]}`,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "", []string{"projects", "list"}, false, true); err != nil {
		t.Fatalf("empty projects list --json failed: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "[]" {
		t.Fatalf("empty projects list JSON = %q, want [] without guidance text", got)
	}
	if strings.Contains(out.String(), "/projects create <name> <path>") {
		t.Fatalf("JSON output mixed in interactive creation guidance: %q", out.String())
	}
}

func TestCLIJSONTasksShow(t *testing.T) {
	const board = `<div data-task-id="t-1" data-task-status="running" data-task-category="active">
		<a href="/tasks/t-1?from=tasks" title="Refactor the API">Refactor the API</a>
	</div>`
	c, _ := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/tasks":        board,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"tasks", "show", "t-1"}, false, true); err != nil {
		t.Fatalf("tasks show --json failed: %v", err)
	}
	got := out.String()
	var task client.Task
	if err := json.Unmarshal([]byte(strings.TrimSpace(got)), &task); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, got)
	}
	if task.ID != "t-1" {
		t.Errorf("unexpected task ID: %s", task.ID)
	}
}

func TestCLILifecycleJSONEmptyExecutionsPreservesPageEnvelope(t *testing.T) {
	c, rec := cliServer(t, map[string]string{
		"/api/projects":                       cliProjects,
		"/tasks":                              `<div data-task-id="t-1" data-task-status="completed" data-task-category="completed"><a href="/tasks/t-1" title="Refactor the API">Refactor the API</a></div>`,
		"/api/tasks/t-1/lifecycle-executions": `{"items":[],"has_more":false}`,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"tasks", "lifecycle", "t-1"}, false, true); err != nil {
		t.Fatalf("empty lifecycle executions --json failed: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != `{"items":[],"has_more":false}` {
		t.Fatalf("empty lifecycle executions JSON = %q, want page envelope", got)
	}
	if rec.saw("GET", "/api/lifecycle-executions/exec-1/events") {
		t.Error("empty lifecycle executions must not fetch event traces")
	}
}

func TestCLILifecycleJSONOneExecutionPreservesPageEnvelopeWithoutImplicitEvents(t *testing.T) {
	const executions = `{"items":[{"id":"exec-1","skill_key":"router","status":"completed","summary":"routing complete"}],"has_more":true,"next_cursor":"older-cursor","future_metadata":{"retained":true}}`
	c, rec := cliServer(t, map[string]string{
		"/api/projects":                       cliProjects,
		"/tasks":                              `<div data-task-id="t-1" data-task-status="completed" data-task-category="completed"><a href="/tasks/t-1" title="Refactor the API">Refactor the API</a></div>`,
		"/api/tasks/t-1/lifecycle-executions": executions,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"tasks", "lifecycle", "t-1"}, false, true); err != nil {
		t.Fatalf("one-item lifecycle executions --json failed: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != executions {
		t.Fatalf("one-item lifecycle JSON did not preserve page envelope\ngot:  %s\nwant: %s", got, executions)
	}
	if rec.saw("GET", "/api/lifecycle-executions/exec-1/events") {
		t.Fatalf("task-only --json request implicitly fetched event traces:\n%s", rec.all())
	}
}

func TestCLILifecycleOneExecutionRendersPageWithoutImplicitEvents(t *testing.T) {
	c, rec := cliServer(t, map[string]string{
		"/api/projects":                       cliProjects,
		"/tasks":                              `<div data-task-id="t-1" data-task-status="completed" data-task-category="completed"><a href="/tasks/t-1" title="Refactor the API">Refactor the API</a></div>`,
		"/api/tasks/t-1/lifecycle-executions": `{"items":[{"id":"exec-1","skill_key":"router","status":"completed","summary":"routing complete"}],"has_more":true,"next_cursor":"older-cursor"}`,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"tasks", "lifecycle", "t-1"}, false, false); err != nil {
		t.Fatalf("one-item lifecycle executions failed: %v", err)
	}
	for _, want := range []string{"exec-1", "router", "completed", "routing complete", "has more: true", "older-cursor"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("one-item lifecycle page missing %q:\n%s", want, out.String())
		}
	}
	if rec.saw("GET", "/api/lifecycle-executions/exec-1/events") {
		t.Fatalf("task-only request implicitly fetched event traces:\n%s", rec.all())
	}
}

func TestCLILifecycleJSONExecutionsPreserveFieldsMetadataAndOrder(t *testing.T) {
	const executions = `{
		"items":[
			{"id":"exec-1","skill_key":"router","when":"post_task","status":"completed","agent_id":"agent-1","output_contract":"selected_skills","started_at":"2026-01-20T10:00:00Z","completed_at":"2026-01-20T10:00:01Z","summary":"first","error":"","selected_skills":["lint"],"selected_memories":[{"file":"routing.md","topic":"Routing"}]},
			{"id":"exec-2","skill_key":"reviewer","when":"post_task","status":"failed","agent_id":"agent-2","started_at":"2026-01-20T11:00:00Z","completed_at":"2026-01-20T11:00:02Z","summary":"second","error":"review failed","selected_skills":[]}
		],
		"has_more":true,"next_cursor":"cursor-2","future_metadata":{"retained":true}
	}`
	c, _ := cliServer(t, map[string]string{
		"/api/projects":                       cliProjects,
		"/tasks":                              `<div data-task-id="t-1" data-task-status="completed" data-task-category="completed"><a href="/tasks/t-1" title="Refactor the API">Refactor the API</a></div>`,
		"/api/tasks/t-1/lifecycle-executions": executions,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"tasks", "lifecycle", "t-1"}, false, true); err != nil {
		t.Fatalf("lifecycle executions --json failed: %v", err)
	}
	got := strings.TrimSpace(out.String())
	var decoded client.LifecycleExecutionPage
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("output is not lifecycle execution page JSON: %v\noutput: %s", err, got)
	}
	if len(decoded.Items) != 2 || decoded.Items[0].ID != "exec-1" || decoded.Items[1].ID != "exec-2" || !decoded.HasMore || decoded.NextCursor != "cursor-2" {
		t.Fatalf("decoded lifecycle page = %+v", decoded)
	}
	if decoded.Items[0].SelectedSkills[0] != "lint" || decoded.Items[1].Error != "review failed" || decoded.Items[0].SelectedMemories[0].Topic != "Routing" {
		t.Fatalf("decoded execution fields = %+v", decoded.Items)
	}
	for _, want := range []string{`"items"`, `"has_more"`, `"next_cursor"`, `"future_metadata"`, `"skill_key"`, `"agent_id"`, `"output_contract"`, `"started_at"`, `"completed_at"`, `"selected_skills"`, `"selected_memories"`} {
		if !strings.Contains(got, want) {
			t.Errorf("JSON output missing %s: %s", want, got)
		}
	}
	if strings.Index(got, `"exec-1"`) > strings.Index(got, `"exec-2"`) {
		t.Errorf("execution order changed: %s", got)
	}
}

func TestCLILifecycleJSONLargePayloadIsComplete(t *testing.T) {
	message := strings.Repeat("日本語<&", 1<<15)
	events, err := json.Marshal([]client.LifecycleEvent{{
		ID:        "event-1",
		Seq:       1,
		EventType: "completed",
		Payload:   map[string]any{"message": message, "nested": map[string]any{"ok": true}},
	}})
	if err != nil {
		t.Fatalf("marshal lifecycle fixture: %v", err)
	}
	c, _ := cliServer(t, map[string]string{
		"/api/projects":                           cliProjects,
		"/tasks":                                  `<div data-task-id="t-1" data-task-status="completed" data-task-category="completed"><a href="/tasks/t-1" title="Refactor the API">Refactor the API</a></div>`,
		"/api/tasks/t-1/lifecycle-executions":     `[{"id":"exec-1","status":"completed"}]`,
		"/api/lifecycle-executions/exec-1/events": string(events),
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"tasks", "lifecycle", "t-1", "exec-1"}, false, true); err != nil {
		t.Fatalf("large lifecycle --json failed: %v", err)
	}
	var decoded []client.LifecycleEvent
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &decoded); err != nil {
		t.Fatalf("decode lifecycle JSON: %v", err)
	}
	if len(decoded) != 1 {
		t.Fatalf("raw lifecycle JSON event count = %d, want 1", len(decoded))
	}
	if got := fmt.Sprint(decoded[0].Payload["message"]); got != message {
		t.Fatalf("raw lifecycle JSON payload was truncated: message length=%d, want %d", len(got), len(message))
	}
}

func TestCLILifecycleJSONEmptyEventsIsArray(t *testing.T) {
	c, _ := cliServer(t, map[string]string{
		"/api/projects":                           cliProjects,
		"/tasks":                                  `<div data-task-id="t-1" data-task-status="completed" data-task-category="completed"><a href="/tasks/t-1" title="Refactor the API">Refactor the API</a></div>`,
		"/api/tasks/t-1/lifecycle-executions":     `[{"id":"exec-1","skill_key":"router","status":"completed"}]`,
		"/api/lifecycle-executions/exec-1/events": "null",
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"tasks", "lifecycle", "t-1", "exec-1"}, false, true); err != nil {
		t.Fatalf("empty lifecycle events --json failed: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "[]" {
		t.Fatalf("empty lifecycle events JSON = %q, want []", got)
	}
}

func TestCLILifecycleListsMultipleExecutionsAndMetadataPlainText(t *testing.T) {
	const executions = `{
		"items":[
			{"id":"exec-1","skill_key":"router","when":"post_task","status":"completed","started_at":"2026-01-20T10:00:00Z","summary":"selected routing skill"},
			{"id":"exec-2","skill_key":"reviewer","when":"post_task","status":"failed","started_at":"2026-01-20T11:00:00Z","error":"review failed"}
		],
		"has_more":true,"next_cursor":"older-cursor"
	}`
	c, rec := cliServer(t, map[string]string{
		"/api/projects":                       cliProjects,
		"/tasks":                              `<div data-task-id="t-1" data-task-status="completed" data-task-category="completed"><a href="/tasks/t-1" title="Refactor the API">Refactor the API</a></div>`,
		"/api/tasks/t-1/lifecycle-executions": executions,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"tasks", "lifecycle", "t-1"}, false, false); err != nil {
		t.Fatalf("tasks lifecycle failed: %v", err)
	}
	got := out.String()
	for _, want := range []string{"exec-1", "exec-2", "router", "reviewer", "completed", "failed", "selected routing skill", "review failed", "has more: true", "older-cursor"} {
		if !strings.Contains(got, want) {
			t.Errorf("plain lifecycle output missing %q:\n%s", want, got)
		}
	}
	if rec.saw("GET", "/api/lifecycle-executions/exec-1/events") || rec.saw("GET", "/api/lifecycle-executions/exec-2/events") {
		t.Error("CLI execution listing must not fetch event traces for multiple executions")
	}
}

func TestCLILifecycleUsesRequestedProjectScope(t *testing.T) {
	const executions = `[{"id":"exec-1","skill_key":"router","status":"completed"}]`
	const events = `[{"id":"event-1","seq":1,"event_type":"completed","payload":{"ok":true},"created_at":"2026-01-20T10:00:00Z"}]`
	c, rec := cliServer(t, map[string]string{
		"/api/projects":                           cliProjects,
		"/tasks":                                  `<div data-task-id="t-1" data-task-status="completed" data-task-category="completed"><a href="/tasks/t-1" title="Refactor the API">Refactor the API</a></div>`,
		"/api/tasks/t-1/lifecycle-executions":     executions,
		"/api/lifecycle-executions/exec-1/events": events,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "other", []string{"tasks", "lifecycle", "t-1", "exec-1"}, false, true); err != nil {
		t.Fatalf("tasks lifecycle for selected project failed: %v", err)
	}
	for _, want := range []string{
		"GET /tasks?project_id=p2",
		"GET /api/tasks/t-1/lifecycle-executions?project_id=p2",
		"GET /api/lifecycle-executions/exec-1/events?project_id=p2",
	} {
		if !rec.sawQuery(want) {
			t.Errorf("missing selected-project request %q; calls:\n%s", want, rec.all())
		}
	}

	var decoded []client.LifecycleEvent
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &decoded); err != nil {
		t.Fatalf("output is not lifecycle event JSON: %v\noutput: %s", err, out.String())
	}
	if len(decoded) != 1 || decoded[0].EventType != "completed" {
		t.Fatalf("decoded events = %+v", decoded)
	}
}

func TestCLILifecycleJSONEventsUseSnakeCase(t *testing.T) {
	const executions = `[{"id":"exec-1","skill_key":"router","status":"completed"}]`
	const events = `[{"id":"event-1","seq":1,"event_type":"started","payload":{"message":"ok"},"created_at":"2026-01-20T10:00:00Z"}]`
	c, _ := cliServer(t, map[string]string{
		"/api/projects":                           cliProjects,
		"/tasks":                                  `<div data-task-id="t-1" data-task-status="completed" data-task-category="completed"><a href="/tasks/t-1" title="Refactor the API">Refactor the API</a></div>`,
		"/api/tasks/t-1/lifecycle-executions":     executions,
		"/api/lifecycle-executions/exec-1/events": events,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"tasks", "lifecycle", "t-1", "exec-1"}, false, true); err != nil {
		t.Fatalf("tasks lifecycle --json failed: %v", err)
	}
	got := strings.TrimSpace(out.String())
	var decoded []client.LifecycleEvent
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("output is not lifecycle event JSON: %v\noutput: %s", err, got)
	}
	if len(decoded) != 1 || decoded[0].Seq != 1 || decoded[0].EventType != "started" {
		t.Fatalf("decoded events = %+v", decoded)
	}
	for _, want := range []string{`"event_type"`, `"created_at"`, `"payload"`} {
		if !strings.Contains(got, want) {
			t.Errorf("JSON output missing %s: %s", want, got)
		}
	}
	if strings.Contains(got, `"EventType"`) || strings.Contains(got, `"CreatedAt"`) {
		t.Errorf("JSON output used Go field names: %s", got)
	}
}
func TestCLIJSONNonJSONModeUnchanged(t *testing.T) {
	const board = `<div data-task-id="t-1" data-task-status="running" data-task-category="active">
		<a href="/tasks/t-1?from=tasks" title="Refactor the API">Refactor the API</a>
	</div>`
	c, _ := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/tasks":        board,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"tasks"}, false, false); err != nil {
		t.Fatalf("tasks without --json failed: %v", err)
	}
	got := out.String()
	// Without --json the output should contain the task title as styled text,
	// not a JSON array.
	if !strings.Contains(got, "Refactor the API") {
		t.Errorf("expected task title in non-JSON output:\n%s", got)
	}
	// Non-JSON output should not be a JSON array (starts with "[")
	if strings.HasPrefix(strings.TrimSpace(got), "[") {
		t.Errorf("non-JSON mode should not produce JSON array:\n%s", got)
	}
}

func TestCLIJSONTaskReviewsList(t *testing.T) {
	const board = `<div data-task-id="t-1" data-task-status="running" data-task-category="active">
		<a href="/tasks/t-1?from=tasks" title="Refactor the API">Refactor the API</a>
	</div>`
	c, _ := cliServer(t, map[string]string{
		"/api/projects":      cliProjects,
		"/tasks":             board,
		"/tasks/t-1/reviews": taskReviewHTML,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"tasks", "reviews", "t-1"}, false, true); err != nil {
		t.Fatalf("tasks reviews --json failed: %v", err)
	}
	var reviews []client.ReviewComment
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &reviews); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out.String())
	}
	if len(reviews) != 1 || reviews[0].FilePath != "internal/client/tasks.go" || reviews[0].LineNumber != 42 {
		t.Fatalf("reviews = %+v", reviews)
	}
}

func TestCLITaskReviewsPreserveMultilineOutput(t *testing.T) {
	const unsafeTitle = "Refactor \x1b[31mred\x1b[0m\a\ninjected API"
	const unsafeFilePath = "internal/\x1b[31mred\x1b[0m\a\ninjected.go"
	const board = `<div data-task-id="t-1" data-task-status="running" data-task-category="active">
		<a href="/tasks/t-1?from=tasks" title="` + unsafeTitle + `">` + unsafeTitle + `</a>
	</div>`
	const reviews = `<div id="review-comments-list" data-task-id="t-1" data-comment-count="1">
		<div class="review-comment-item" data-comment-id="rc-1" data-file-path="internal/client/tasks.go" data-line-number="42" data-line-type="new" data-state="open">
			<div><span>alice</span><p>
First &amp; second

Third<br>Fourth &#27;[31mred&#27;[0m
			</p></div>
		</div>
	</div>`
	wantComment := "First & second\n\nThird\nFourth \x1b[31mred\x1b[0m"

	for _, tc := range []struct {
		name     string
		args     []string
		jsonMode bool
		want     string
	}{
		{
			name:     "list JSON",
			args:     []string{"tasks", "reviews", "t-1"},
			jsonMode: true,
			want:     `[{"id":"rc-1","task_id":"t-1","file_path":"internal/client/tasks.go","line_number":42,"line_type":"new","comment_text":"First \u0026 second\n\nThird\nFourth \u001b[31mred\u001b[0m","reviewed_by":"alice","state":"open"}]`,
		},
		{
			name:     "add JSON",
			args:     []string{"tasks", "reviews", "add", "t-1", "internal/client/tasks.go:42", wantComment},
			jsonMode: true,
			want:     `{"id":"rc-1","task_id":"t-1","file_path":"internal/client/tasks.go","line_number":42,"line_type":"new","comment_text":"First \u0026 second\n\nThird\nFourth \u001b[31mred\u001b[0m","reviewed_by":"alice","state":"open"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := cliServer(t, map[string]string{
				"/api/projects":      cliProjects,
				"/tasks":             board,
				"/tasks/t-1/reviews": reviews,
			})
			var out bytes.Buffer
			if err := RunCLI(c, &out, "demo", tc.args, false, tc.jsonMode); err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(out.String()); got != tc.want {
				t.Errorf("output = %q\nwant   = %q", got, tc.want)
			}
		})
	}

	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "list plain", args: []string{"tasks", "reviews", "t-1"}},
		{name: "add plain", args: []string{"tasks", "reviews", "add", "t-1", unsafeFilePath + ":42", wantComment}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := cliServer(t, map[string]string{
				"/api/projects":      cliProjects,
				"/tasks":             board,
				"/tasks/t-1/reviews": reviews,
			})
			var out bytes.Buffer
			if err := RunCLI(c, &out, "demo", tc.args, false, false); err != nil {
				t.Fatal(err)
			}
			raw := out.String()
			for _, unsafe := range []string{"\x1b[31m", "\a", "\ninjected"} {
				if strings.Contains(raw, unsafe) {
					t.Errorf("plain output retained injected terminal control %q: %q", unsafe, raw)
				}
			}
			plain := stripANSI(raw)
			for _, want := range []string{"alice: First & second", "\n\n", "Third", "Fourth red"} {
				if !strings.Contains(plain, want) {
					t.Errorf("plain output missing %q:\n%s", want, plain)
				}
			}
			if tc.name == "add plain" {
				for _, want := range []string{"added review comment on internal/red injected.go:42", "for Refactor red injected API"} {
					if !strings.Contains(plain, want) {
						t.Errorf("plain add output missing sanitized confirmation %q:\n%s", want, plain)
					}
				}
			}
		})
	}
}

func TestCLIJSONTaskReviewReadPathsHaveEquivalentOutputAndSingleFetch(t *testing.T) {
	paths := []struct {
		name string
		args []string
	}{
		{name: "show review tab", args: []string{"tasks", "show", "t-1", "review"}},
		{name: "reviews default list", args: []string{"tasks", "reviews", "t-1"}},
		{name: "reviews list subcommand", args: []string{"tasks", "reviews", "list", "t-1"}},
	}
	cases := []struct {
		name      string
		reviews   string
		wantCount int
	}{
		{name: "populated", reviews: taskReviewHTML, wantCount: 1},
		{name: "empty", reviews: `<div id="review-comments-list" data-task-id="t-1" data-comment-count="0"></div>`},
	}
	const board = `<div data-task-id="t-1" data-task-status="running" data-task-category="active">
		<a href="/tasks/t-1?from=tasks" title="Refactor the API">Refactor the API</a>
	</div>`

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var want string
			for _, path := range paths {
				path := path
				t.Run(path.name, func(t *testing.T) {
					c, rec := cliServer(t, map[string]string{
						"/api/projects":      cliProjects,
						"/tasks":             board,
						"/tasks/t-1/reviews": tc.reviews,
					})

					var out bytes.Buffer
					if err := RunCLI(c, &out, "demo", path.args, false, true); err != nil {
						t.Fatalf("%v --json failed: %v", path.name, err)
					}
					got := strings.TrimSpace(out.String())
					var reviews []client.ReviewComment
					if err := json.Unmarshal([]byte(got), &reviews); err != nil {
						t.Fatalf("%v output is not valid JSON: %v\noutput: %s", path.name, err, got)
					}
					if len(reviews) != tc.wantCount {
						t.Fatalf("%v returned %d reviews, want %d: %s", path.name, len(reviews), tc.wantCount, got)
					}
					if tc.wantCount == 1 && (reviews[0].ID != "rc-1" || reviews[0].FilePath != "internal/client/tasks.go" || reviews[0].LineNumber != 42) {
						t.Fatalf("%v returned unexpected review: %+v", path.name, reviews[0])
					}
					if got := rec.count("GET", "/tasks"); got != 1 {
						t.Fatalf("%v should make exactly one board request, got %d:\n%s", path.name, got, rec.all())
					}
					if got := rec.count("GET", "/tasks/t-1/reviews"); got != 1 {
						t.Fatalf("%v should make exactly one review request, got %d:\n%s", path.name, got, rec.all())
					}
					if want == "" {
						want = got
					} else if got != want {
						t.Errorf("%v JSON differs from the first read path: %s\nwant: %s", path.name, got, want)
					}
				})
			}
		})
	}
}

func cliReviewErrorServer(t *testing.T) (*client.Client, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.recordURL(r.Method, r.URL.RequestURI())
		switch r.URL.Path {
		case "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(cliProjects))
		case "/tasks":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(taskBoardHTML))
		case "/tasks/t-1/reviews":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"review fetch failed"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c, rec
}

func TestCLIJSONTaskReviewReadPathsPropagateFetchErrorsIdentically(t *testing.T) {
	paths := []struct {
		name string
		args []string
	}{
		{name: "show review tab", args: []string{"tasks", "show", "t-1", "review"}},
		{name: "reviews default list", args: []string{"tasks", "reviews", "t-1"}},
		{name: "reviews list subcommand", args: []string{"tasks", "reviews", "list", "t-1"}},
	}
	const wantError = "server error (502): review fetch failed"

	for _, path := range paths {
		path := path
		t.Run(path.name, func(t *testing.T) {
			c, rec := cliReviewErrorServer(t)
			var out bytes.Buffer
			err := RunCLI(c, &out, "demo", path.args, false, true)
			if err == nil || err.Error() != wantError {
				t.Fatalf("%v error = %v, want %q; output: %s", path.name, err, wantError, out.String())
			}
			if got := rec.count("GET", "/tasks"); got != 1 {
				t.Fatalf("%v should make exactly one board request, got %d:\n%s", path.name, got, rec.all())
			}
			if got := rec.count("GET", "/tasks/t-1/reviews"); got != 1 {
				t.Fatalf("%v should make exactly one review request, got %d:\n%s", path.name, got, rec.all())
			}
		})
	}
}

func TestCLIJSONTaskReviewsAdd(t *testing.T) {
	const board = `<div data-task-id="t-1" data-task-status="running" data-task-category="active">
		<a href="/tasks/t-1?from=tasks" title="Refactor the API">Refactor the API</a>
	</div>`
	c, rec := cliServer(t, map[string]string{
		"/api/projects":      cliProjects,
		"/tasks":             board,
		"/tasks/t-1/reviews": taskReviewHTML,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"tasks", "reviews", "add", "Refactor", "internal/client/tasks.go:42", "Needs", "error", "handling"}, false, true); err != nil {
		t.Fatalf("tasks reviews add --json failed: %v", err)
	}
	if !rec.saw("POST", "/tasks/t-1/reviews") {
		t.Fatalf("expected add review call, calls:\n%s", rec.all())
	}
	if !rec.sawForm("comment_text=Needs+error+handling") {
		t.Fatalf("posted form missing comment text: %v", rec.forms)
	}
	var review client.ReviewComment
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &review); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out.String())
	}
	if review.ID != "rc-1" || review.CommentText != "Needs error handling" {
		t.Fatalf("review = %+v", review)
	}
}

func TestCLIJSONTaskReviewsAddUnquotedTitleContainingLocationToken(t *testing.T) {
	const board = `<div data-task-id="t-1" data-task-status="running" data-task-category="active">
		<a href="/tasks/t-1?from=tasks" title="Add 1:1 customer support">Add 1:1 customer support</a>
	</div>`
	addedReview := strings.Replace(taskReviewHTML, "internal/client/tasks.go", "internal/auth.go", 1)
	c, rec := cliServer(t, map[string]string{
		"/api/projects":      cliProjects,
		"/tasks":             board,
		"/tasks/t-1/reviews": addedReview,
	})

	var out bytes.Buffer
	args := []string{"tasks", "reviews", "add", "Add", "1:1", "customer", "support", "internal/auth.go:42", "Needs", "error", "handling"}
	if err := RunCLI(c, &out, "demo", args, false, true); err != nil {
		t.Fatalf("tasks reviews add --json failed: %v\noutput: %s", err, out.String())
	}
	wantForm := "POST /tasks/t-1/reviews?comment_text=Needs+error+handling&file_path=internal%2Fauth.go&line_number=42&line_type=new"
	if !rec.sawForm(wantForm) {
		t.Fatalf("unquoted location-shaped title posted unexpected form, want %q; forms: %v", wantForm, rec.forms)
	}
	var review client.ReviewComment
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &review); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out.String())
	}
	if review.FilePath != "internal/auth.go" || review.LineNumber != 42 || review.CommentText != "Needs error handling" || review.LineType != "new" {
		t.Fatalf("review = %+v, want exact submitted fields", review)
	}
}

func TestCLITaskReviewsAddAmbiguousPrefixDoesNotMutate(t *testing.T) {
	const board = `<div>
	  <div data-task-id="t-1" data-task-status="pending" data-task-category="backlog">
	    <a href="/tasks/t-1?from=tasks" title="Add 1:1 customer support">Add 1:1 customer support</a>
	  </div>
	  <div data-task-id="t-2" data-task-status="pending" data-task-category="backlog">
	    <a href="/tasks/t-2?from=tasks" title="Add 1:1 customer success">Add 1:1 customer success</a>
	  </div>
	</div>`
	c, rec := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/tasks":        board,
	})

	var out bytes.Buffer
	args := []string{"tasks", "reviews", "add", "Add", "1:1", "customer", "internal/auth.go:42", "Needs", "error", "handling"}
	err := RunCLI(c, &out, "demo", args, false, false)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "ambiguous") {
		t.Fatalf("expected ambiguous task-reference error, got %v; output: %s", err, out.String())
	}
	if got := rec.count("POST", "/tasks/t-1/reviews") + rec.count("POST", "/tasks/t-2/reviews"); got != 0 {
		t.Fatalf("ambiguous task/location boundary must not mutate, got %d POSTs; calls:\n%s", got, rec.all())
	}
}

func TestCLITaskReviewsAddWithoutReferenceReturnsUsageWithoutMutation(t *testing.T) {
	c, rec := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/tasks":        taskBoardHTML,
	})

	var out bytes.Buffer
	err := RunCLI(c, &out, "demo", []string{"tasks", "reviews", "add"}, false, false)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "usage") {
		t.Fatalf("expected non-zero usage error, got %v; output: %s", err, out.String())
	}
	if got := rec.count("GET", "/tasks"); got != 0 {
		t.Fatalf("CLI missing-reference path must not fetch selector tasks, got %d requests", got)
	}
	if got := rec.count("POST", "/tasks/t-1/reviews"); got != 0 {
		t.Fatalf("CLI missing-reference path must not mutate reviews, got %d POSTs", got)
	}
}

// cliEventWriter makes it possible to assert that event lines are written while
// a foreground stream is still running, rather than only after it terminates.
type cliEventWriter struct {
	lines chan string
}

func (w *cliEventWriter) Write(p []byte) (int, error) {
	line := string(p)
	w.lines <- line
	return len(p), nil
}

func TestFormatCLIEventPreservesOutputAndFiltering(t *testing.T) {
	tests := []struct {
		name       string
		event      client.Event
		projectID  string
		jsonOutput bool
		want       string
		wantOK     bool
	}{
		{
			name: "recognized plain event",
			event: client.Event{
				Name: " task_status_changed ",
				Data: json.RawMessage(` { "type": "task_status_changed", "project_id": "p1", "task_id": "t1", "task_name": "Deploy API", "status": "running", "queued": true, "ignored": [ 1, 2 ] } `),
			},
			projectID: "p1",
			want:      `event="task_status_changed" type="task_status_changed" project_id="p1" task_id="t1" task_name="Deploy API" status="running" queued=true`,
			wantOK:    true,
		},
		{
			name: "JSON event retains compact data",
			event: client.Event{
				Name: "task_status_changed",
				Data: json.RawMessage(` { "type": "task_status_changed", "project_id": "p1", "task_id": "t1", "ignored": [ 1, 2 ] } `),
			},
			projectID:  "p1",
			jsonOutput: true,
			want:       `{"event":"task_status_changed","type":"task_status_changed","project_id":"p1","task_id":"t1","task_name":"","status":"","category":"","message":"","exec_id":"","source":"","agent_name":"","completed_output":"","queued":false,"data":{"type":"task_status_changed","project_id":"p1","task_id":"t1","ignored":[1,2]}}`,
			wantOK:     true,
		},
		{
			name:      "valid raw plain fallback is compact",
			event:     client.Event{Name: "future_event", Data: json.RawMessage(` { "ignored": [ 1, 2 ] } `)},
			projectID: "",
			want:      `event="future_event" type="future_event" data="{\"ignored\":[1,2]}"`,
			wantOK:    true,
		},
		{
			name:      "invalid raw plain fallback is byte identical",
			event:     client.Event{Name: "future_event", Data: json.RawMessage("  not-json \n")},
			projectID: "",
			want:      "event=\"future_event\" type=\"future_event\" data=\"  not-json \\n\"",
			wantOK:    true,
		},
		{
			name:      "foreign project is filtered",
			event:     client.Event{Name: "task_status_changed", Data: json.RawMessage(`{"type":"task_status_changed","project_id":"other","task_id":"t1"}`)},
			projectID: "p1",
			wantOK:    false,
		},
		{
			name:      "omitted project retains selected ownership",
			event:     client.Event{Name: "chat_new_message", Data: json.RawMessage(`{"type":"chat_new_message","message":"hello"}`)},
			projectID: "p1",
			want:      `event="chat_new_message" type="chat_new_message" project_id="p1" message="hello"`,
			wantOK:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := formatCLIEvent(tc.event, tc.projectID, tc.jsonOutput)
			if err != nil {
				t.Fatalf("formatCLIEvent() error = %v", err)
			}
			if ok != tc.wantOK {
				t.Fatalf("formatCLIEvent() include = %t, want %t", ok, tc.wantOK)
			}
			if got != tc.want {
				t.Fatalf("formatCLIEvent() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFormatCLIEventJSONNormalizesWhitespaceEscapesAndOmitsMalformedData(t *testing.T) {
	validInputs := []json.RawMessage{
		json.RawMessage(`{"type":"chat_new_message","project_id":"","message":"<>&  ","extra":"<>&  "}`),
		json.RawMessage(` {
			"type": "chat_new_message",
			"project_id": "",
			"message": "<>&  ",
			"extra": "<>&  "
		} `),
	}
	want := `{"event":"chat_new_message","type":"chat_new_message","project_id":"p1","task_id":"","task_name":"","status":"","category":"","message":"\u003c\u003e\u0026\u2028\u2029","exec_id":"","source":"","agent_name":"","completed_output":"","queued":false,"data":{"type":"chat_new_message","project_id":"","message":"\u003c\u003e\u0026\u2028\u2029","extra":"\u003c\u003e\u0026\u2028\u2029"}}`

	for _, raw := range validInputs {
		got, include, err := formatCLIEvent(client.Event{Name: " chat_new_message ", Data: raw}, "p1", true)
		if err != nil {
			t.Fatalf("formatCLIEvent() error = %v", err)
		}
		if !include {
			t.Fatal("formatCLIEvent() unexpectedly filtered a compatible taskless chat event")
		}
		if got != want {
			t.Fatalf("formatCLIEvent() = %q, want %q", got, want)
		}
	}

	got, include, err := formatCLIEvent(client.Event{Name: "task_status_changed", Data: json.RawMessage(`{"type":"task_status_changed"`)}, "p1", true)
	if err != nil {
		t.Fatalf("formatCLIEvent() malformed error = %v", err)
	}
	if !include {
		t.Fatal("formatCLIEvent() unexpectedly filtered malformed metadata")
	}
	wantMalformed := `{"event":"task_status_changed","type":"task_status_changed","project_id":"p1","task_id":"","task_name":"","status":"","category":"","message":"","exec_id":"","source":"","agent_name":"","completed_output":"","queued":false}`
	if got != wantMalformed {
		t.Fatalf("formatCLIEvent() malformed = %q, want %q", got, wantMalformed)
	}
	if strings.Contains(got, `"data"`) {
		t.Fatalf("malformed event unexpectedly included data: %s", got)
	}
}

func TestFormatCLIEventJSONRetainsValidDataAfterPayloadDecodeError(t *testing.T) {
	tests := []struct {
		name     string
		raw      json.RawMessage
		wantData string
	}{
		{
			name:     "array",
			raw:      json.RawMessage(` [ 1, 2 ] `),
			wantData: `[1,2]`,
		},
		{
			name:     "string",
			raw:      json.RawMessage(` "value" `),
			wantData: `"value"`,
		},
		{
			name:     "number",
			raw:      json.RawMessage(` 42 `),
			wantData: `42`,
		},
		{
			name:     "null",
			raw:      json.RawMessage(` null `),
			wantData: `null`,
		},
		{
			name:     "type-mismatched known field",
			raw:      json.RawMessage(` { "type": 123, "project_id": "p1", "task_id": "t1", "extra": [ 1, 2 ] } `),
			wantData: `{"type":123,"project_id":"p1","task_id":"t1","extra":[1,2]}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, include, err := formatCLIEvent(client.Event{Name: "task_status_changed", Data: tc.raw}, "p1", true)
			if err != nil {
				t.Fatalf("formatCLIEvent() error = %v", err)
			}
			if !include {
				t.Fatal("formatCLIEvent() unexpectedly filtered valid JSON data")
			}
			if !strings.Contains(got, `"data":`+tc.wantData) {
				t.Fatalf("formatCLIEvent() = %q, missing compact data %s", got, tc.wantData)
			}
		})
	}
}

func BenchmarkFormatCLIEventRecognizedJSON(b *testing.B) {
	for _, size := range []int{1 << 10, 64 << 10, 1 << 20} {
		b.Run(fmt.Sprintf("%dKiB", size>>10), func(b *testing.B) {
			prefix := []byte(`{"type":"task_status_changed","project_id":"p1","task_id":"t1","status":"running","padding":"`)
			suffix := []byte(`"}`)
			raw := make([]byte, 0, size)
			raw = append(raw, prefix...)
			raw = append(raw, bytes.Repeat([]byte{'x'}, size-len(prefix)-len(suffix))...)
			raw = append(raw, suffix...)
			event := client.Event{Name: "task_status_changed", Data: raw}

			b.ReportAllocs()
			b.SetBytes(int64(len(raw)))
			b.ResetTimer()
			for b.Loop() {
				if _, ok, err := formatCLIEvent(event, "p1", true); err != nil || !ok {
					b.Fatalf("formatCLIEvent() include = %t, error = %v", ok, err)
				}
			}
		})
	}
}

func BenchmarkFormatCLIEventRecognizedPlain(b *testing.B) {
	for _, size := range []int{1 << 10, 64 << 10, 1 << 20} {
		b.Run(fmt.Sprintf("%dKiB", size>>10), func(b *testing.B) {
			prefix := []byte(`{"type":"task_status_changed","project_id":"p1","task_id":"t1","status":"running","padding":"`)
			suffix := []byte(`"}`)
			raw := make([]byte, 0, size)
			raw = append(raw, prefix...)
			raw = append(raw, bytes.Repeat([]byte{'x'}, size-len(prefix)-len(suffix))...)
			raw = append(raw, suffix...)
			event := client.Event{Name: "task_status_changed", Data: raw}

			b.ReportAllocs()
			b.SetBytes(int64(len(raw)))
			b.ResetTimer()
			for b.Loop() {
				if _, ok, err := formatCLIEvent(event, "p1", false); err != nil || !ok {
					b.Fatalf("formatCLIEvent() include = %t, error = %v", ok, err)
				}
			}
		})
	}
}

func TestCLIEventsOnStreamsSelectedProjectAndWritesLiveLines(t *testing.T) {
	var mu sync.Mutex
	var eventRequests []string
	writer := &cliEventWriter{lines: make(chan string, 2)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"projects":[{"id":"p1","name":"other"},{"id":"p2","name":"Demo Project"}]}`)
		case "/events/live":
			mu.Lock()
			eventRequests = append(eventRequests, r.URL.RequestURI())
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Fatal("SSE response does not support flushing")
			}
			_, _ = fmt.Fprint(w, "event: task_status_changed\n")
			_, _ = fmt.Fprint(w, `data: {"type":"task_status_changed","project_id":"p2","task_id":"t1","task_name":"Deploy API","status":"running"}`+"\n\n")
			flusher.Flush()
			_, _ = fmt.Fprint(w, "event: chat_new_message\n")
			_, _ = fmt.Fprint(w, `data: {"type":"chat_new_message","project_id":"p2","exec_id":"e1","message":"hello from chat"}`+"\n\n")
			flusher.Flush()
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := RunCLI(c, writer, "Demo Project", []string{"events", "on"}, false, false); err != nil {
		t.Fatalf("events on failed: %v", err)
	}

	mu.Lock()
	requests := append([]string(nil), eventRequests...)
	mu.Unlock()
	if len(requests) != 1 || requests[0] != "/events/live?project_id=p2" {
		t.Fatalf("event requests = %v, want one scoped request", requests)
	}
	var lines []string
	for i := 0; i < 2; i++ {
		lines = append(lines, <-writer.lines)
	}
	joined := strings.Join(lines, "")
	for _, want := range []string{"task_status_changed", "Deploy API", "chat_new_message", "hello from chat"} {
		if !strings.Contains(joined, want) {
			t.Errorf("plain event output missing %q:\n%s", want, joined)
		}
	}
	if strings.Count(strings.TrimSpace(joined), "\n") != 1 {
		t.Fatalf("event output was not exactly two line-oriented records: %q", joined)
	}
}

func TestCLIEventsRequireExactTaskProjectOwnershipInAllOutputModes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		jsonOutput bool
	}{
		{name: "plain"},
		{name: "JSON", jsonOutput: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var eventRequest string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/projects":
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"projects":[{"id":"p1","name":"demo"}]}`)
				case "/events/live":
					eventRequest = r.URL.RequestURI()
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprint(w, `data: {"type":"task_status_changed","task_id":"unscoped-task","status":"running"}`+"\n\n")
					_, _ = fmt.Fprint(w, `data: {"type":"task_status_changed","project_id":"p2","task_id":"foreign-task","status":"running"}`+"\n\n")
					_, _ = fmt.Fprint(w, `data: {"type":"task_status_changed","project_id":"p1","task_id":"valid-task","status":"completed"}`+"\n\n")
					_, _ = fmt.Fprint(w, `data: {"type":"chat_new_message","exec_id":"legacy-chat","message":"compatible chat"}`+"\n\n")
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(srv.Close)

			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := RunCLI(c, &out, "demo", []string{"events", "on"}, false, tc.jsonOutput); err != nil {
				t.Fatalf("events on failed: %v", err)
			}

			if eventRequest != "/events/live?project_id=p1" {
				t.Fatalf("event request = %q, want selected-project query", eventRequest)
			}
			output := out.String()
			for _, excluded := range []string{"unscoped-task", "foreign-task"} {
				if strings.Contains(output, excluded) {
					t.Errorf("output includes unowned task %q:\n%s", excluded, output)
				}
			}
			for _, included := range []string{"valid-task", "legacy-chat", "compatible chat"} {
				if !strings.Contains(output, included) {
					t.Errorf("output missing compatible event field %q:\n%s", included, output)
				}
			}

			lines := strings.Split(strings.TrimSpace(output), "\n")
			if len(lines) != 2 {
				t.Fatalf("output lines = %d, want valid task and unscoped chat only: %q", len(lines), output)
			}
			if tc.jsonOutput {
				for _, line := range lines {
					var record cliEventRecord
					if err := json.Unmarshal([]byte(line), &record); err != nil {
						t.Fatalf("invalid JSON event line: %v\n%s", err, line)
					}
					if record.ProjectID != "p1" {
						t.Errorf("project_id = %q, want synthesized selected project", record.ProjectID)
					}
				}
			}
		})
	}
}

func TestCLIEventsJSONEmitsValidStableLines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"projects":[{"id":"p1","name":"demo"}]}`)
		case "/events/live":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "event: task_status_changed\n")
			_, _ = fmt.Fprint(w, `data: {"type":"task_status_changed","project_id":"p1","task_id":"t1","status":"completed","message":"line 1\\nline 2"}`+"\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"events", "on"}, false, true); err != nil {
		t.Fatalf("JSON events on failed: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("JSON event output lines = %d, want one: %q", len(lines), out.String())
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("JSON event line is invalid: %v\n%s", err, lines[0])
	}
	for key, want := range map[string]string{
		"event":      "task_status_changed",
		"type":       "task_status_changed",
		"project_id": "p1",
		"task_id":    "t1",
		"status":     "completed",
	} {
		if got, _ := record[key].(string); got != want {
			t.Errorf("JSON %s = %#v, want %q", key, record[key], want)
		}
	}
	if strings.Contains(lines[0], "\n") || strings.Contains(lines[0], "\x1b") {
		t.Errorf("JSON event line contains an unescaped line/control character: %q", lines[0])
	}
}

func TestCLIEventsOnEmptyProjectListDoesNotOpenUnscopedStream(t *testing.T) {
	var eventRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"projects":[]}`)
		case "/events/live":
			eventRequests++
			http.Error(w, "unexpected unscoped stream", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = RunCLI(c, &out, "", []string{"events", "on"}, false, false)
	if err == nil || !strings.Contains(err.Error(), "no project selected") {
		t.Fatalf("empty project list error = %v, want no-project error", err)
	}
	if eventRequests != 0 {
		t.Fatalf("empty project list opened %d event streams", eventRequests)
	}
	if out.Len() != 0 {
		t.Fatalf("empty project list wrote success output: %q", out.String())
	}
}

func TestCLIEventsCancellationClosesStream(t *testing.T) {
	requestStarted := make(chan struct{})
	requestClosed := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"projects":[{"id":"p1","name":"demo"}]}`)
		case "/events/live":
			close(requestStarted)
			w.Header().Set("Content-Type", "text/event-stream")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			<-r.Context().Done()
			close(requestClosed)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- RunCLIContext(ctx, c, &bytes.Buffer{}, "demo", []string{"events", "on"}, false, false)
	}()
	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for foreground event stream")
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("cancelled events stream returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled events stream did not return")
	}
	select {
	case <-requestClosed:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled events stream did not close the HTTP request")
	}
}

func TestCLIEventsOffIsExplicitlyUnsupportedForAnotherProcess(t *testing.T) {
	var eventRequests int
	var projectRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/projects":
			projectRequests++
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"projects":[{"id":"p1","name":"demo"}]}`)
		case "/events/live":
			eventRequests++
			http.Error(w, "unexpected stream", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = RunCLI(c, &out, "demo", []string{"events", "off"}, false, false)
	if err == nil || !strings.Contains(err.Error(), "cannot disable") || !strings.Contains(err.Error(), "Ctrl-C") {
		t.Fatalf("events off error = %v, want actionable nonzero error", err)
	}
	if eventRequests != 0 || projectRequests != 0 || strings.Contains(out.String(), "live events off") {
		t.Fatalf("events off changed/claimed stream state: projects=%d events=%d output=%q", projectRequests, eventRequests, out.String())
	}
}

func TestCLIEventsInvalidArgumentsRejectBeforeProjectLoad(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{name: "unknown mode", args: []string{"events", "maybe"}},
		{name: "extra argument", args: []string{"events", "on", "extra"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := cliServer(t, map[string]string{
				"/api/projects": cliProjects,
			})
			var out bytes.Buffer

			err := RunCLI(c, &out, "demo", tc.args, false, false)
			if err == nil || !strings.Contains(err.Error(), "usage: events [on|off]") {
				t.Fatalf("error = %v, want events usage error; output: %q", err, out.String())
			}
			if got := rec.count("GET", "/api/projects"); got != 0 {
				t.Fatalf("invalid events arguments fetched projects %d times", got)
			}
			if got := rec.count("GET", "/events/live"); got != 0 {
				t.Fatalf("invalid events arguments opened %d streams", got)
			}
		})
	}
}

func TestCLIEventsUsesAuthenticatedCookieSession(t *testing.T) {
	const session = "session-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, cookieErr := r.Cookie("ov_session")
		authed := cookieErr == nil && cookie.Value == session
		switch r.URL.Path {
		case "/login":
			http.SetCookie(w, &http.Cookie{Name: "ov_session", Value: session})
			w.Header().Set("Location", "/")
			w.WriteHeader(http.StatusFound)
		case "/api/projects":
			if !authed {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"projects":[{"id":"p1","name":"private"}]}`)
		case "/events/live":
			if !authed {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "event: chat_new_message\n")
			_, _ = fmt.Fprint(w, `data: {"type":"chat_new_message","project_id":"p1","exec_id":"e1","message":"authenticated"}`+"\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Login(context.Background(), "admin", "secret"); err != nil {
		t.Fatalf("login: %v", err)
	}
	var out bytes.Buffer
	if err := RunCLI(c, &out, "private", []string{"events", "on"}, false, false); err != nil {
		t.Fatalf("authenticated events stream failed: %v", err)
	}
	if !strings.Contains(out.String(), "authenticated") {
		t.Fatalf("authenticated event output missing message: %q", out.String())
	}
}

func TestCLIBareEventsResolvesExplicitProjectID(t *testing.T) {
	var eventRequest string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"projects":[{"id":"p1","name":"other"},{"id":"p2","name":"demo"}]}`)
		case "/events/live":
			eventRequest = r.URL.RequestURI()
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, `data: {"type":"task_status_changed","project_id":"p2","task_id":"t2","status":"queued"}`+"\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunCLI(c, &out, "p2", []string{"events"}, false, false); err != nil {
		t.Fatalf("bare events failed: %v", err)
	}
	if eventRequest != "/events/live?project_id=p2" {
		t.Fatalf("bare events request = %q, want explicit project scope", eventRequest)
	}
	if !strings.Contains(out.String(), `task_id="t2"`) {
		t.Fatalf("bare events output missing task ID: %q", out.String())
	}
}

func TestEventsHelpDistinguishesInteractiveAndCLI(t *testing.T) {
	cmd := lookupCommand("events")
	if cmd == nil {
		t.Fatal("events command missing")
	}
	help := renderCommandHelp(*cmd)
	for _, want := range []string{
		"interactive: show events from the TUI stream",
		"interactive: hide events",
		"one-shot CLI: monitor the selected project until Ctrl-C or EOF",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("events help missing %q:\n%s", want, help)
		}
	}
}

func TestCLIWebhookCanonicalIDAliasesUseScopedDetailWithoutCatalog(t *testing.T) {
	for _, root := range [][]string{{"channels", "webhooks"}, {"webhooks"}, {"inbound-webhooks"}} {
		t.Run(strings.Join(root, " "), func(t *testing.T) {
			c, rec := cliServer(t, map[string]string{
				"/api/projects": cliProjects,
				"/channels/webhooks/" + canonicalWebhookID:           canonicalWebhookDetailJSON(canonicalWebhookID, "p1", "Canonical Hook"),
				"/channels/webhooks/" + canonicalWebhookID + "/test": `{"task_id":"canonical-alias-test"}`,
			})
			var out bytes.Buffer
			args := append(append([]string(nil), root...), "test", canonicalWebhookID)
			if err := RunCLI(c, &out, "demo", args, false, false); err != nil {
				t.Fatal(err)
			}
			if got := rec.count(http.MethodGet, "/channels"); got != 0 {
				t.Fatalf("catalog requests = %d, want 0; calls: %s", got, rec.all())
			}
			if got := rec.count(http.MethodGet, "/channels/webhooks/"+canonicalWebhookID); got != 1 {
				t.Fatalf("detail requests = %d, want 1; calls: %s", got, rec.all())
			}
			if got := rec.count(http.MethodPost, "/channels/webhooks/"+canonicalWebhookID+"/test"); got != 1 {
				t.Fatalf("test requests = %d, want 1; calls: %s", got, rec.all())
			}
		})
	}
}

func TestCLIWebhookCanonicalShowPreservesPlainAndJSONOutput(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%t", jsonOutput), func(t *testing.T) {
			c, rec := cliServer(t, map[string]string{
				"/api/projects": cliProjects,
				"/channels":     `<div data-webhook-id="` + canonicalWebhookID + `" data-webhook-name="Canonical Hook" data-webhook-token="safe-token"></div>`,
				"/channels/webhooks/" + canonicalWebhookID: canonicalWebhookDetailJSON(canonicalWebhookID, "p1", "Canonical Hook"),
			})
			var direct, named bytes.Buffer
			if err := RunCLI(c, &direct, "demo", []string{"webhooks", "show", canonicalWebhookID}, false, jsonOutput); err != nil {
				t.Fatal(err)
			}
			if got := rec.count(http.MethodGet, "/channels"); got != 0 {
				t.Fatalf("direct catalog requests = %d, want 0; calls: %s", got, rec.all())
			}
			if err := RunCLI(c, &named, "demo", []string{"webhooks", "show", "Canonical Hook"}, false, jsonOutput); err != nil {
				t.Fatal(err)
			}
			if got := rec.count(http.MethodGet, "/channels"); got != 1 {
				t.Fatalf("named catalog requests = %d, want 1; calls: %s", got, rec.all())
			}
			if direct.String() != named.String() {
				t.Fatalf("show output changed for direct ID\ndirect: %q\nnamed:  %q", direct.String(), named.String())
			}
			if jsonOutput && !json.Valid(direct.Bytes()) {
				t.Fatalf("direct JSON output is invalid: %q", direct.String())
			}
		})
	}
}

func TestCLIChannelsWebhooksCanonicalAndAliasParity(t *testing.T) {
	roots := [][]string{{"channels", "webhooks"}, {"webhooks"}, {"inbound-webhooks"}}
	var wantOutput string
	for i, root := range roots {
		t.Run(strings.Join(root, " "), func(t *testing.T) {
			c, rec := cliServer(t, map[string]string{
				"/api/projects":              cliProjects,
				"/channels":                  webhookCardsHTML,
				"/channels/webhooks/w1/test": `{"task_id":"task-parity"}`,
			})
			var out bytes.Buffer
			args := append(append([]string(nil), root...), "test", "duty")
			if err := RunCLI(c, &out, "demo", args, false, false); err != nil {
				t.Fatal(err)
			}
			if got := rec.count("GET", "/channels"); got != 1 {
				t.Fatalf("webhook resolution requests = %d, want 1; calls: %s", got, rec.all())
			}
			if got := rec.count("POST", "/channels/webhooks/w1/test"); got != 1 {
				t.Fatalf("webhook test requests = %d, want 1; calls: %s", got, rec.all())
			}
			if strings.Contains(strings.ToLower(out.String()), "secret") {
				t.Fatalf("output disclosed secret material: %q", out.String())
			}
			if i == 0 {
				wantOutput = out.String()
			} else if out.String() != wantOutput {
				t.Fatalf("alias output = %q, canonical output = %q", out.String(), wantOutput)
			}
		})
	}
}

func TestCLIChannelsWebhooksPreservesOptionLikeExactName(t *testing.T) {
	const cards = `<div data-webhook-id="w-opt" data-webhook-name="Hook --enabled maybe" data-webhook-token="opt-token"></div><div data-webhook-id="w-short" data-webhook-name="Hook" data-webhook-token="short-token"></div>`
	const detail = `{"id":"w-opt","project_id":"p1","name":"Hook --enabled maybe","enabled":true,"path_token":"opt-token","system_instructions":"","title_template":"","prompt_template":"","default_priority":2,"agent_ids":[]}`
	c, rec := cliServer(t, map[string]string{
		"/api/projects":            cliProjects,
		"/channels":                cards,
		"/channels/webhooks/w-opt": detail,
	})
	if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"channels", "webhooks", "show", "Hook", "--enabled", "maybe"}, false, false); err != nil {
		t.Fatal(err)
	}
	if !rec.saw("GET", "/channels/webhooks/w-opt") || rec.saw("GET", "/channels/webhooks/w-short") {
		t.Fatalf("canonical hierarchy shortened exact option-like name: %s", rec.all())
	}
}

func TestCLIChannelsWebhooksValidatesBeforeAnyRequest(t *testing.T) {
	c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects, "/channels": webhookCardsHTML})
	err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"channels", "webhooks", "create", "Hook", "--enabled", "maybe"}, false, false)
	if err == nil || !strings.Contains(err.Error(), "true or false") {
		t.Fatalf("error = %v, want enabled validation", err)
	}
	if calls := rec.all(); calls != "" {
		t.Fatalf("invalid canonical invocation made requests: %s", calls)
	}
}

func TestCLIWebhooksTrailingOptionTokensRemainInReference(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "unknown option", args: []string{"webhooks", "show", "w1", "--bogus", "value"}},
		{name: "malformed known option", args: []string{"webhooks", "test", "w1", "--enabled", "maybe"}},
		{name: "missing option value", args: []string{"webhooks", "rotate", "w1", "--enabled"}},
		{name: "surplus option pair", args: []string{"webhooks", "delete", "w1", "--priority", "3"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects, "/channels": webhookCardsHTML})
			err := RunCLI(c, &bytes.Buffer{}, "demo", tc.args, true, false)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), "nothing matches") {
				t.Fatalf("error = %v, want full-reference rejection", err)
			}
			for _, call := range []struct{ method, path string }{
				{"GET", "/channels/webhooks/w1"},
				{"POST", "/channels/webhooks/w1/test"},
				{"POST", "/channels/webhooks/w1/rotate-secret"},
				{"DELETE", "/channels/webhooks/w1"},
			} {
				if rec.saw(call.method, call.path) {
					t.Fatalf("surplus operands dispatched %s %s; calls: %s", call.method, call.path, rec.all())
				}
			}
		})
	}

	const cards = `<div data-webhook-id="w-opt" data-webhook-name="Hook --enabled maybe" data-webhook-token="opt-token"></div><div data-webhook-id="w-short" data-webhook-name="Hook" data-webhook-token="short-token"></div>`
	const detail = `{"id":"w-opt","project_id":"p1","name":"Hook --enabled maybe","enabled":true,"path_token":"opt-token","system_instructions":"","title_template":"","prompt_template":"","default_priority":2,"agent_ids":[]}`
	for _, action := range []string{"show", "test", "rotate", "delete"} {
		t.Run("exact option-token name "+action, func(t *testing.T) {
			bodies := map[string]string{
				"/api/projects":                          cliProjects,
				"/channels":                              cards,
				"/channels/webhooks/w-opt":               detail,
				"/channels/webhooks/w-opt/test":          `{"task_id":"task-option-name"}`,
				"/channels/webhooks/w-opt/rotate-secret": `{"secret":"replacement"}`,
			}
			c, rec := cliServer(t, bodies)
			err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"webhooks", action, "Hook", "--enabled", "maybe"}, true, false)
			if err != nil {
				t.Fatalf("exact option-token name failed: %v", err)
			}
			wantMethod, wantPath := "GET", "/channels/webhooks/w-opt"
			switch action {
			case "test":
				wantMethod, wantPath = "POST", "/channels/webhooks/w-opt/test"
			case "rotate":
				wantMethod, wantPath = "POST", "/channels/webhooks/w-opt/rotate-secret"
			case "delete":
				wantMethod, wantPath = "DELETE", "/channels/webhooks/w-opt"
			}
			if !rec.saw(wantMethod, wantPath) {
				t.Fatalf("full exact name did not dispatch %s %s; calls: %s", wantMethod, wantPath, rec.all())
			}
		})
	}
}

func TestCLITaskGoalLifecycleUsesProjectScopedRoutes(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantURL string
		wantOut string
	}{
		{name: "set", args: []string{"tasks", "goal", "Refactor", "|", "all tests pass"}, wantURL: "POST /tasks/t-1/goal?project_id=p1", wantOut: "goal set on Refactor the API: all tests pass"},
		{name: "clear", args: []string{"tasks", "goal", "Refactor", "|", "clear"}, wantURL: "POST /tasks/t-1/goal/clear?project_id=p1", wantOut: "cleared goal on Refactor the API"},
		{name: "pause", args: []string{"tasks", "goal", "pause", "Refactor"}, wantURL: "POST /tasks/t-1/goal/pause?project_id=p1", wantOut: "paused goal on Refactor the API"},
		{name: "resume", args: []string{"tasks", "goal", "resume", "Refactor"}, wantURL: "POST /tasks/t-1/goal/resume?project_id=p1", wantOut: "resumed goal on Refactor the API"},
		{name: "pause is an objective", args: []string{"tasks", "goal", "Refactor", "|", "pause"}, wantURL: "POST /tasks/t-1/goal?project_id=p1", wantOut: "goal set on Refactor the API: pause"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := cliServer(t, map[string]string{
				"/api/projects": cliProjects,
				"/tasks":        taskBoardHTML,
			})
			var out bytes.Buffer
			if err := RunCLI(c, &out, "demo", tc.args, false, false); err != nil {
				t.Fatal(err)
			}
			if !rec.sawQuery(tc.wantURL) {
				t.Fatalf("requests = %q, want %q", rec.urlsSnapshot(), tc.wantURL)
			}
			if got := stripANSI(out.String()); !strings.Contains(got, tc.wantOut) {
				t.Fatalf("output missing %q:\n%s", tc.wantOut, got)
			}
		})
	}
}

func TestCLITaskGoalPauseFailureHasNoSuccessOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(cliProjects))
		case "/tasks":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(taskBoardHTML))
		case "/tasks/t-1/goal/pause":
			if got := r.URL.Query().Get("project_id"); got != "p1" {
				t.Errorf("project_id = %q, want p1", got)
			}
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"pause denied"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = RunCLI(c, &out, "demo", []string{"tasks", "goal", "pause", "Refactor"}, false, false)
	if err == nil || !strings.Contains(err.Error(), "pause denied") {
		t.Fatalf("error = %v, want pause denial", err)
	}
	if strings.Contains(stripANSI(out.String()), "paused goal on") {
		t.Fatalf("backend failure claimed success:\n%s", out.String())
	}
}

func TestCLIWebhooksJSONAndForceGates(t *testing.T) {
	t.Run("list JSON is secret-free", func(t *testing.T) {
		c, _ := cliServer(t, map[string]string{"/api/projects": cliProjects, "/channels": webhookCardsHTML})
		var out bytes.Buffer
		if err := RunCLI(c, &out, "demo", []string{"webhooks", "list"}, false, true); err != nil {
			t.Fatal(err)
		}
		if !json.Valid(out.Bytes()) || !strings.Contains(out.String(), `"path":"/webhooks/inbound/safe-token"`) {
			t.Fatalf("unexpected JSON: %s", out.String())
		}
		if strings.Contains(strings.ToLower(out.String()), "secret") {
			t.Fatalf("list JSON disclosed a secret field: %s", out.String())
		}
	})

	for _, tc := range []struct {
		name   string
		action string
		ref    string
		want   string
	}{
		{name: "unknown before force gate", action: "rotate", ref: "foreign-id", want: "nothing matches"},
		{name: "ambiguous before force gate", action: "delete", ref: "pager", want: "ambiguous"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects, "/channels": webhookCardsHTML})
			err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"webhooks", tc.action, tc.ref}, false, false)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.want) || strings.Contains(err.Error(), "--force") {
				t.Fatalf("error = %v, want matching error %q before force gate", err, tc.want)
			}
			if rec.saw("POST", "/channels/webhooks/w1/rotate-secret") || rec.saw("DELETE", "/channels/webhooks/w1") || rec.saw("DELETE", "/channels/webhooks/w2") {
				t.Fatalf("invalid reference mutated: %s", rec.all())
			}
		})
	}

	t.Run("unique partial force guidance is canonical", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects, "/channels": webhookCardsHTML})
		err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"webhooks", "rotate", "duty"}, false, false)
		if err == nil || !strings.Contains(err.Error(), "--force") || !strings.Contains(err.Error(), `"Pager Duty"`) || strings.Contains(err.Error(), `"duty"`) {
			t.Fatalf("error = %v, want canonical force guidance", err)
		}
		if got := rec.count("GET", "/channels"); got != 1 {
			t.Fatalf("resolution requests = %d, want 1; calls: %s", got, rec.all())
		}
	})

	t.Run("forced unique partial mutates captured canonical target", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{
			"/api/projects":                       cliProjects,
			"/channels":                           webhookCardsHTML,
			"/channels/webhooks/w1/rotate-secret": `{"secret":"replacement"}`,
		})
		if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"webhooks", "rotate", "duty"}, true, false); err != nil {
			t.Fatal(err)
		}
		if !rec.saw("POST", "/channels/webhooks/w1/rotate-secret") || rec.count("GET", "/channels") != 1 {
			t.Fatalf("partial reference rebound or mutated wrong target: %s", rec.all())
		}
	})

	for _, action := range []string{"rotate", "delete"} {
		t.Run(action+" requires force", func(t *testing.T) {
			c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects, "/channels": webhookCardsHTML})
			err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"webhooks", action, "w1"}, false, false)
			if err == nil || !strings.Contains(err.Error(), "--force") {
				t.Fatalf("error = %v", err)
			}
			if rec.saw("POST", "/channels/webhooks/w1/rotate-secret") || rec.saw("DELETE", "/channels/webhooks/w1") {
				t.Fatal("unforced destructive action mutated")
			}
		})
	}

	t.Run("forced delete resolves then mutates", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects, "/channels": webhookCardsHTML, "/channels/webhooks/w1": ""})
		if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"webhooks", "delete", "w1"}, true, false); err != nil {
			t.Fatal(err)
		}
		if !rec.saw("DELETE", "/channels/webhooks/w1") || !rec.sawQuery("project_id=p1") {
			t.Fatalf("calls: %s %#v", rec.all(), rec.urlsSnapshot())
		}
	})

	t.Run("test JSON reports task ID", func(t *testing.T) {
		c, _ := cliServer(t, map[string]string{"/api/projects": cliProjects, "/channels": webhookCardsHTML, "/channels/webhooks/w1/test": `{"task_id":"task-cli-1"}`})
		var out bytes.Buffer
		if err := RunCLI(c, &out, "demo", []string{"webhooks", "test", "w1"}, false, true); err != nil {
			t.Fatal(err)
		}
		if !json.Valid(out.Bytes()) || !strings.Contains(out.String(), `"task_id":"task-cli-1"`) {
			t.Fatalf("output = %s", out.String())
		}
	})
}
