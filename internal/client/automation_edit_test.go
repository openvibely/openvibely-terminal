package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestLoadAutomationDefinitionUsesScopedBuilderContract(t *testing.T) {
	var gotQuery url.Values
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/automations/au-1/builder" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Accept"); got != "text/html" {
			t.Errorf("Accept = %q, want text/html", got)
		}
		if got := r.Header.Get("HX-Request"); got != "true" {
			t.Errorf("HX-Request = %q, want true", got)
		}
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`<div id="automation-builder"><form id="automation-design-form" action="/automations/au-1/builder?project_id=p2"></form><textarea name="automation_yaml">schema_version: 1
name: Keep &amp; edit
</textarea></div>`))
	}))
	defer s.Close()
	c, _ := New(s.URL)
	definition, err := c.LoadAutomationDefinition(context.Background(), "p2", "au-1")
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery.Get("project_id") != "p2" || definition.AutomationID != "au-1" || definition.ProjectID != "p2" {
		t.Fatalf("definition/scope = %+v, query=%v", definition, gotQuery)
	}
	if definition.YAML != "schema_version: 1\nname: Keep & edit\n" {
		t.Fatalf("YAML = %q", definition.YAML)
	}
}

func TestLoadAutomationDefinitionSharedGETLifecycleErrorsAndCleanup(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		location    string
		body        string
		wantErr     string
		wantAuthErr bool
	}{
		{name: "bare redirect is authentication", status: http.StatusFound, wantErr: "unauthorized", wantAuthErr: true},
		{name: "unauthorized", status: http.StatusUnauthorized, wantErr: "unauthorized", wantAuthErr: true},
		{name: "backend error", status: http.StatusBadGateway, body: `{"error":"upstream unavailable"}`, wantErr: "server error (502): upstream unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &trackedResponseBody{Reader: strings.NewReader(tc.body)}
			transport := htmlTestRoundTripper(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet || r.URL.Path != "/automations/au-1/builder" {
					t.Errorf("request = %s %s", r.Method, r.URL)
				}
				if got := r.Header.Get("Accept"); got != "text/html" {
					t.Errorf("Accept = %q, want text/html", got)
				}
				if got := r.Header.Get("HX-Request"); got != "true" {
					t.Errorf("HX-Request = %q, want true", got)
				}
				header := make(http.Header)
				if tc.location != "" {
					header.Set("Location", tc.location)
				}
				return &http.Response{StatusCode: tc.status, Header: header, Body: body, Request: r}, nil
			})
			c := &Client{baseURL: "http://backend.test", http: &http.Client{Transport: transport}}
			_, err := c.LoadAutomationDefinition(context.Background(), "p1", "au-1")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
			}
			if got := IsAuthRequired(err); got != tc.wantAuthErr {
				t.Errorf("IsAuthRequired = %t, want %t", got, tc.wantAuthErr)
			}
			if !body.closed {
				t.Error("response body was not closed")
			}
		})
	}
}

func TestLoadAutomationDefinitionTransportFailureContext(t *testing.T) {
	transportErr := errors.New("transport unavailable")
	c := &Client{baseURL: "http://backend.test", http: &http.Client{Transport: htmlTestRoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, transportErr
	})}}
	_, err := c.LoadAutomationDefinition(context.Background(), "p1", "au-1")
	if !errors.Is(err, transportErr) || !strings.Contains(err.Error(), "GET /automations/au-1/builder?project_id=p1") {
		t.Fatalf("error = %v, want GET path and wrapped transport failure", err)
	}
}

func TestLoadAutomationDefinitionParsingContextAndBodyLimit(t *testing.T) {
	t.Run("parse error context and cleanup", func(t *testing.T) {
		parseErr := errors.New("body read failed")
		body := &failingHTMLBody{err: parseErr}
		c := &Client{baseURL: "http://backend.test", http: &http.Client{Transport: htmlTestRoundTripper(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body, Request: r}, nil
		})}}
		_, err := c.LoadAutomationDefinition(context.Background(), "p1", "au-1")
		if !errors.Is(err, parseErr) || !strings.Contains(err.Error(), "parsing automation builder") {
			t.Fatalf("error = %v, want builder parse context wrapping read error", err)
		}
		if !body.closed {
			t.Error("response body was not closed after parse error")
		}
	})

	t.Run("oversized HTML is bounded", func(t *testing.T) {
		page := `<div id="automation-builder"><form id="automation-design-form" action="/automations/au-1/builder?project_id=p1"></form><textarea name="automation_yaml">valid</textarea></div>` + strings.Repeat("x", 9<<20)
		body := &countingTrackedHTMLBody{Reader: strings.NewReader(page)}
		c := &Client{baseURL: "http://backend.test", http: &http.Client{Transport: htmlTestRoundTripper(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body, Request: r}, nil
		})}}
		definition, err := c.LoadAutomationDefinition(context.Background(), "p1", "au-1")
		if err != nil {
			t.Fatal(err)
		}
		if definition.YAML != "valid" {
			t.Fatalf("YAML = %q, want valid", definition.YAML)
		}
		if body.read > (8<<20)+(64<<10) || body.read >= len(page) {
			t.Fatalf("read %d of %d bytes; parser limit was not retained", body.read, len(page))
		}
		if !body.closed {
			t.Error("response body was not closed")
		}
	})
}

type failingHTMLBody struct {
	err    error
	closed bool
}

func (b *failingHTMLBody) Read([]byte) (int, error) { return 0, b.err }
func (b *failingHTMLBody) Close() error             { b.closed = true; return nil }

type countingTrackedHTMLBody struct {
	*strings.Reader
	read   int
	closed bool
}

func (b *countingTrackedHTMLBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}
func (b *countingTrackedHTMLBody) Close() error { b.closed = true; return nil }

func TestLoadAutomationDefinitionFailsClosedWithoutMatchingFormIdentity(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "missing form", body: `<div id="automation-builder" data-automation-id="au-1" data-project-id="p1"><textarea name="automation_yaml">valid</textarea></div>`},
		{name: "missing action", body: `<div id="automation-builder"><form id="automation-design-form"></form><textarea name="automation_yaml">valid</textarea></div>`},
		{name: "foreign automation", body: `<div id="automation-builder"><form id="automation-design-form" action="/automations/au-2/builder?project_id=p1"></form><textarea name="automation_yaml">valid</textarea></div>`},
		{name: "foreign project", body: `<div id="automation-builder"><form id="automation-design-form" action="/automations/au-1/builder?project_id=p2"></form><textarea name="automation_yaml">valid</textarea></div>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.body)) }))
			defer s.Close()
			c, _ := New(s.URL)
			if _, err := c.LoadAutomationDefinition(context.Background(), "p1", "au-1"); err == nil {
				t.Fatal("expected identity error")
			}
		})
	}
}

func TestCreateAutomationFromDefinitionUsesBuilderContract(t *testing.T) {
	var gotForm url.Values
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/automations/builder" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		gotForm = r.PostForm
		_, _ = w.Write([]byte(`<div id="automation-builder"><form id="automation-design-form" action="/automations/au-new/builder?project_id=p1"></form><textarea name="automation_yaml">schema_version: 1
name: Created
</textarea></div>`))
	}))
	defer s.Close()
	c, _ := New(s.URL)
	created, err := c.CreateAutomationFromDefinition(context.Background(), "p1", "schema_version: 1\nname: Created\n")
	if err != nil {
		t.Fatal(err)
	}
	if gotForm.Get("project_id") != "p1" || gotForm.Get("automation_yaml") != "schema_version: 1\nname: Created\n" {
		t.Fatalf("form = %#v", gotForm)
	}
	if gotForm.Get("save_changes") != "true" {
		t.Fatalf("save_changes = %q, want true", gotForm.Get("save_changes"))
	}
	if created.AutomationID != "au-new" || created.ProjectID != "p1" || created.YAML != "schema_version: 1\nname: Created\n" {
		t.Fatalf("created = %+v", created)
	}
}

func TestCreateAutomationFromDefinitionValidationFailureDoesNotRetry(t *testing.T) {
	requests := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte(`<div id="automation-builder"><form id="automation-design-form" action="/automations/builder?project_id=p1"></form><div data-automation-validation-summary><ul><li>unsupported trigger type</li></ul></div><textarea name="automation_yaml">invalid</textarea></div>`))
	}))
	defer s.Close()
	c, _ := New(s.URL)
	_, err := c.CreateAutomationFromDefinition(context.Background(), "p1", "invalid")
	if err == nil || !strings.Contains(err.Error(), "unsupported trigger type") {
		t.Fatalf("error = %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want one create attempt", requests)
	}
}

func TestCreateAutomationFromDefinitionRejectsEmptyBeforeRequest(t *testing.T) {
	requests := 0
	s := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer s.Close()
	c, _ := New(s.URL)
	if _, err := c.CreateAutomationFromDefinition(context.Background(), "p1", " \n\t"); err == nil {
		t.Fatal("expected empty definition error")
	}
	if requests != 0 {
		t.Fatalf("requests = %d", requests)
	}
}

func TestUpdateAutomationDefinitionPreviewsBeforeSaving(t *testing.T) {
	var forms []url.Values
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		forms = append(forms, r.PostForm)
		if len(forms) == 1 {
			_, _ = w.Write([]byte(`<div id="automation-builder"><form id="automation-design-form" action="/automations/au-1/builder?project_id=p1"></form><textarea name="automation_yaml">ok</textarea></div>`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer s.Close()
	c, _ := New(s.URL)
	if err := c.UpdateAutomationDefinition(context.Background(), "p1", "au-1", "schema_version: 1\nname: Edited\n"); err != nil {
		t.Fatal(err)
	}
	if len(forms) != 2 || forms[0].Get("save_changes") != "" || forms[1].Get("save_changes") != "true" {
		t.Fatalf("forms = %#v", forms)
	}
	for _, form := range forms {
		if form.Get("automation_yaml") != "schema_version: 1\nname: Edited\n" {
			t.Fatalf("definition was not round-tripped: %#v", form)
		}
	}
}

func TestUpdateAutomationDefinitionValidationFailureDoesNotSave(t *testing.T) {
	requests := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte(`<div id="automation-builder"><form id="automation-design-form" action="/automations/au-1/builder?project_id=p1"></form><div data-automation-validation-summary><ul><li>node trigger is required</li></ul></div><textarea name="automation_yaml">invalid</textarea></div>`))
	}))
	defer s.Close()
	c, _ := New(s.URL)
	err := c.UpdateAutomationDefinition(context.Background(), "p1", "au-1", "invalid")
	if err == nil || !strings.Contains(err.Error(), "node trigger is required") {
		t.Fatalf("error = %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want preview only", requests)
	}
}

func TestUpdateAutomationDefinitionParseFailureDoesNotSave(t *testing.T) {
	requests := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte(`<div id="automation-builder"><form id="automation-design-form" action="/automations/au-1/builder?project_id=p1"></form><div role="alert">YAML did not parse: invalid mapping</div><textarea name="automation_yaml">invalid</textarea></div>`))
	}))
	defer s.Close()
	c, _ := New(s.URL)
	err := c.UpdateAutomationDefinition(context.Background(), "p1", "au-1", "invalid")
	if err == nil || !strings.Contains(err.Error(), "YAML did not parse") {
		t.Fatalf("error = %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want preview only", requests)
	}
}

func TestUpdateAutomationDefinitionRejectsOversizeBeforeRequest(t *testing.T) {
	requests := 0
	s := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer s.Close()
	c, _ := New(s.URL)
	if err := c.UpdateAutomationDefinition(context.Background(), "p1", "au-1", strings.Repeat("x", maxAutomationDefinitionBytes+1)); err == nil {
		t.Fatal("expected size error")
	}
	if requests != 0 {
		t.Fatalf("requests = %d", requests)
	}
}
