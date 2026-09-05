package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/openvibely/openvibely-tui/internal/client"
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

func TestCLIStatusUsesOneDelayedCountWave(t *testing.T) {
	const countDelay = 100 * time.Millisecond
	const alertsHTML = `<div data-alert-id="a-1" data-alert-scroll-anchor="a-1"><span class="badge">pending</span></div>`
	const tasksHTML = `<div><div data-task-id="t-1" data-task-status="running" data-task-category="active"><a href="/tasks/t-1" title="Task A">Task A</a></div></div>`

	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.recordURL(r.Method, r.URL.RequestURI())
		switch r.URL.Path {
		case "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(cliProjects))
		case "/alerts":
			time.Sleep(countDelay)
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(alertsHTML))
		case "/tasks":
			time.Sleep(countDelay)
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
	start := time.Now()
	if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"status"}, false, false); err != nil {
		t.Fatalf("status failed: %v", err)
	}
	elapsed := time.Since(start)

	if got := rec.count("GET", "/alerts"); got != 1 {
		t.Fatalf("delayed CLI status made %d /alerts requests, want 1:\n%s", got, rec.all())
	}
	if got := rec.count("GET", "/tasks"); got != 1 {
		t.Fatalf("delayed CLI status made %d /tasks requests, want 1:\n%s", got, rec.all())
	}
	if elapsed >= 2*countDelay {
		t.Fatalf("CLI status took %s; expected one delayed count-refresh wave, not two", elapsed)
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
		{name: "pulse summary", args: []string{"pulse", "summary"}, fetchPath: "/upcoming", triggerPath: "/upcoming/summary"},
		{name: "reflection", args: []string{"reflection"}, fetchPath: "/history"},
		{name: "reflection summary", args: []string{"reflection", "summary"}, fetchPath: "/history", triggerPath: "/history/summary"},
		{name: "grades", args: []string{"grades"}, fetchPath: "/history"},
		{name: "grades run", args: []string{"grades", "run"}, fetchPath: "/history", triggerPath: "/history/grade-ideas"},
		{name: "insights", args: []string{"insights"}, fetchPath: "/insights"},
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

func TestCLIUnknownCommandFails(t *testing.T) {
	for _, name := range []string{"frobnicate", "build", "/build"} {
		t.Run(name, func(t *testing.T) {
			c, rec := cliServer(t, nil)

			var out bytes.Buffer
			err := RunCLI(c, &out, "", []string{name}, false, false)
			if err == nil || !strings.Contains(err.Error(), "unknown command") {
				t.Fatalf("err = %v, want unknown command", err)
			}
			if rec.saw("POST", "/api/autonomous/trigger") {
				t.Fatalf("unsupported command made a backend request:\n%s", rec.all())
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

// One-shot CLI mode works headlessly for the new automations actions,
// exiting cleanly on success and nonzero on a backend failure.
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

// One-shot CLI mode works headlessly for the new channels actions,
// exiting cleanly on success and nonzero on a backend failure.
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
	for _, want := range []string{"My Project", "project ID: created-project", "-project created-project", "openvibely-tui -project created-project tasks"} {
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

func TestCLIAlertsDeleteResolutionAndBackendErrors(t *testing.T) {
	const duplicateAlerts = `<div data-alert-id="a-1" data-alert-scroll-anchor="a-1" data-search-text="first"><p class="font-semibold">Duplicate</p></div>
		<div data-alert-id="a-2" data-alert-scroll-anchor="a-2" data-search-text="second"><p class="font-semibold">Duplicate</p></div>`

	for _, tc := range []struct {
		name string
		ref  string
		want string
	}{
		{name: "missing", ref: "missing", want: `nothing matches "missing"`},
		{name: "ambiguous", ref: "Duplicate", want: `"Duplicate" is ambiguous:`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := cliServer(t, map[string]string{
				"/api/projects": cliProjects,
				"/alerts":       duplicateAlerts,
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

func TestCLILifecycleJSONEmptyExecutionsIsArray(t *testing.T) {
	c, rec := cliServer(t, map[string]string{
		"/api/projects":                       cliProjects,
		"/tasks":                              `<div data-task-id="t-1" data-task-status="completed" data-task-category="completed"><a href="/tasks/t-1" title="Refactor the API">Refactor the API</a></div>`,
		"/api/tasks/t-1/lifecycle-executions": "null",
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"tasks", "lifecycle", "t-1"}, false, true); err != nil {
		t.Fatalf("empty lifecycle executions --json failed: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "[]" {
		t.Fatalf("empty lifecycle executions JSON = %q, want []", got)
	}
	if rec.saw("GET", "/api/lifecycle-executions/exec-1/events") {
		t.Error("empty lifecycle executions must not fetch event traces")
	}
}

func TestCLILifecycleJSONExecutionsPreserveFieldsAndOrder(t *testing.T) {
	const executions = `[
		{"id":"exec-1","skill_key":"router","when":"post_task","status":"completed","agent_id":"agent-1","started_at":"2026-01-20T10:00:00Z","completed_at":"2026-01-20T10:00:01Z","summary":"first","error":"","selected_skills":["lint"]},
		{"id":"exec-2","skill_key":"reviewer","when":"post_task","status":"failed","agent_id":"agent-2","started_at":"2026-01-20T11:00:00Z","completed_at":"2026-01-20T11:00:02Z","summary":"second","error":"review failed","selected_skills":[]}
	]`
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
	var decoded []client.LifecycleExecution
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("output is not lifecycle execution JSON: %v\noutput: %s", err, got)
	}
	if len(decoded) != 2 || decoded[0].ID != "exec-1" || decoded[1].ID != "exec-2" {
		t.Fatalf("decoded executions = %+v", decoded)
	}
	if decoded[0].SelectedSkills[0] != "lint" || decoded[1].Error != "review failed" {
		t.Fatalf("decoded execution fields = %+v", decoded)
	}
	for _, want := range []string{`"skill_key"`, `"agent_id"`, `"started_at"`, `"completed_at"`, `"selected_skills"`} {
		if !strings.Contains(got, want) {
			t.Errorf("JSON output missing %s: %s", want, got)
		}
	}
	if strings.Index(got, `"exec-1"`) > strings.Index(got, `"exec-2"`) {
		t.Errorf("execution order changed: %s", got)
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

func TestCLILifecycleListsMultipleExecutionsPlainText(t *testing.T) {
	const executions = `[
		{"id":"exec-1","skill_key":"router","when":"post_task","status":"completed","started_at":"2026-01-20T10:00:00Z"},
		{"id":"exec-2","skill_key":"reviewer","when":"post_task","status":"failed","started_at":"2026-01-20T11:00:00Z"}
	]`
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
	for _, want := range []string{"exec-1", "exec-2", "router", "reviewer", "completed", "failed"} {
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
