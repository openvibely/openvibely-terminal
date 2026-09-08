package client

import (
	"context"
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
