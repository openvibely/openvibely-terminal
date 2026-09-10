package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestUpdateModelReadsAuthoritativeDetailsAndPreservesSecrets(t *testing.T) {
	const apiKey = "saved-api-key"
	const signingSecret = "saved-signing-secret"
	const staticToken = "saved-static-token"
	var detailReads, updates int
	var updateForm url.Values

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /models/model-1/edit-details":
			detailReads++
			if got := r.URL.Query().Get("project_id"); got != "project-1" {
				t.Errorf("edit details project_id = %q, want project-1", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"model-1","name":"Saved compatible","provider":"openai_compatible","model":"saved-model","reasoning_effort":"high","temperature":0.4,"is_default":true,"api_key":"`+apiKey+`","auth_method":"oauth","max_workers":3,"worker_timeout":42,"base_url":"https://provider.example/v1","transport":"responses","preset_slug":"custom","models_url":"https://provider.example/models","auth_header_name":"Authorization","auth_header_value_prefix":"Bearer ","oauth_client_id":"client-id","oauth_client_secret":"client-secret","oauth_authorize_url":"https://provider.example/authorize","oauth_token_url":"https://provider.example/token","oauth_scopes":"profile","extra_headers_json":"{\"X-Secret\":\"`+staticToken+`\"}","custom_auth_config_json":"{\"refresh_url\":\"https://provider.example/refresh\",\"pkce\":true,\"signing_secret\":\"`+signingSecret+`\",\"static_headers\":{\"X-Secret\":\"`+staticToken+`\"},\"token_headers\":{\"X-Token\":\"token-value\"}}","auto_start_tasks":true,"default_max_tokens":8192}`)
		case "PUT /models/model-1":
			updates++
			if got := r.URL.Query().Get("project_id"); got != "project-1" {
				t.Errorf("update project_id = %q, want project-1", got)
			}
			if r.Header.Get("HX-Request") != "true" {
				t.Error("update did not use HTMX contract")
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			updateForm = r.PostForm
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	details, err := c.GetModelEditDetails(context.Background(), "project-1", "model-1")
	if err != nil {
		t.Fatalf("GetModelEditDetails: %v", err)
	}
	if details.Name != "Saved compatible" || details.BaseURL != "https://provider.example/v1" || details.MaxWorkers != 3 || details.DefaultMaxTokens != 8192 {
		t.Fatalf("safe authoritative details = %#v", details)
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		t.Fatalf("marshal safe edit details: %v", err)
	}
	for _, secret := range []string{apiKey, signingSecret, staticToken, "client-secret"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("ModelEditDetails JSON exposed %q: %s", secret, encoded)
		}
	}

	name, workers := "Updated compatible", 9
	if err := c.UpdateModel(context.Background(), "project-1", details, ModelEditRequest{Name: &name, MaxWorkers: &workers}, ""); err != nil {
		t.Fatalf("UpdateModel: %v", err)
	}
	if detailReads != 1 || updates != 1 {
		t.Fatalf("detail/update requests = %d/%d, want 1/1", detailReads, updates)
	}
	for key, want := range map[string]string{
		"name":                       "Updated compatible",
		"provider":                   "openai_compatible",
		"model":                      "saved-model",
		"is_default":                 "on",
		"auto_start_tasks":           "on",
		"model_max_workers":          "9",
		"worker_timeout":             "42",
		"base_url":                   "https://provider.example/v1",
		"transport":                  "responses",
		"preset_slug":                "custom",
		"models_url":                 "https://provider.example/models",
		"default_max_tokens":         "8192",
		"custom_auth_method":         "oauth",
		"auth_method":                "oauth",
		"custom_refresh_url":         "https://provider.example/refresh",
		"custom_signing_secret":      signingSecret,
		"custom_static_headers_json": `{"X-Secret":"` + staticToken + `"}`,
		"custom_token_headers_json":  `{"X-Token":"token-value"}`,
		"custom_oauth_pkce":          "on",
	} {
		if got := updateForm.Get(key); got != want {
			t.Errorf("form[%q] = %q, want %q", key, got, want)
		}
	}
	if got := updateForm.Get("api_key"); got != "" {
		t.Errorf("unchanged API key was resubmitted as %q", got)
	}
}

func TestUpdateModelPreservesRedactedCustomAPIKeyConfiguration(t *testing.T) {
	var updateForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /models/model-1/edit-details":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"model-1","name":"Custom API key","provider":"openai_compatible","model":"custom-model","auth_method":"api_key","base_url":"https://provider.example/v1","transport":"chat_completions","preset_slug":"custom","models_url":"https://provider.example/models","auth_header_name":"Authorization","auth_header_value_prefix":"Bearer ","default_max_tokens":4096,"custom_auth_config_json":"{\"models_array_path\":\"inventory.items\",\"model_id_field\":\"slug\",\"allow_private_endpoints\":false}"}`)
		case "PUT /models/model-1":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			updateForm = r.PostForm
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	details, err := c.GetModelEditDetails(context.Background(), "p1", "model-1")
	if err != nil {
		t.Fatal(err)
	}
	workers := 3
	if err := c.UpdateModel(context.Background(), "p1", details, ModelEditRequest{MaxWorkers: &workers}, ""); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"custom_models_array_path": "inventory.items",
		"custom_model_id_field":    "slug",
		"default_max_tokens":       "4096",
		"model_max_workers":        "3",
	} {
		if got := updateForm.Get(key); got != want {
			t.Errorf("form[%q] = %q, want %q", key, got, want)
		}
	}
	for _, absent := range []string{"custom_signing_secret", "custom_static_headers_json", "custom_authorization_parameters_json", "custom_token_headers_json", "custom_refresh_parameters_json", "custom_refresh_headers_json"} {
		if got := updateForm.Get(absent); got != "" {
			t.Errorf("form unexpectedly supplied protected custom setting %q = %q", absent, got)
		}
	}
}

func TestUpdateModelValidationFailureRedactsReplacementSecret(t *testing.T) {
	const replacement = "replace-api-key"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /models/model-1/edit-details":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"model-1","name":"OpenAI","provider":"openai","model":"gpt-5.6-sol","auth_method":"api_key"}`)
		case "PUT /models/model-1":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"invalid key `+replacement+`"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	details, err := c.GetModelEditDetails(context.Background(), "p1", "model-1")
	if err != nil {
		t.Fatal(err)
	}
	err = c.UpdateModel(context.Background(), "p1", details, ModelEditRequest{}, replacement)
	if err == nil || strings.Contains(err.Error(), replacement) || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("replacement validation error = %v, want redacted error", err)
	}
}

func TestGetModelEditDetailsRejectsMismatchedIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"other-model","name":"Other"}`)
	}))
	defer srv.Close()
	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetModelEditDetails(context.Background(), "p1", "model-1"); err == nil || !strings.Contains(err.Error(), "did not match") {
		t.Fatalf("mismatched edit details error = %v", err)
	}
}

func TestUpdateModelRejectsUnsupportedEndpointWithoutMutation(t *testing.T) {
	c, err := New("http://example.test")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "https://example.test/v1"
	err = c.UpdateModel(context.Background(), "p1", ModelEditDetails{ID: "model-1", Provider: "openai", Model: "gpt-5.6-sol"}, ModelEditRequest{Endpoint: &endpoint}, "")
	if err == nil || !strings.Contains(err.Error(), "supported only") {
		t.Fatalf("unsupported endpoint error = %v", err)
	}
}

func TestUpdateModelRejectsUnsupportedOpenAIModelWithoutMutation(t *testing.T) {
	var updates int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			updates++
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	unsupported := "gpt-4o"
	err = c.UpdateModel(context.Background(), "p1", ModelEditDetails{
		ID: "model-1", Name: "OpenAI", Provider: "openai", Model: "gpt-5.6-sol",
	}, ModelEditRequest{Model: &unsupported}, "")
	if err == nil || !strings.Contains(err.Error(), "unsupported OpenAI model") {
		t.Fatalf("unsupported model error = %v", err)
	}
	if updates != 0 {
		t.Fatalf("unsupported OpenAI model made %d mutations, want zero", updates)
	}
}

func TestUpdateModelRejectsCompatibleDetailsWithoutDefaultMaxTokens(t *testing.T) {
	var updates int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /models/model-1/edit-details":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"model-1","name":"Compatible","provider":"openai_compatible","model":"provider/model","auth_method":"api_key","base_url":"https://provider.example/v1","transport":"chat_completions","preset_slug":"custom"}`)
		case "PUT /models/model-1":
			updates++
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	details, err := c.GetModelEditDetails(context.Background(), "p1", "model-1")
	if err != nil {
		t.Fatal(err)
	}
	workers := 2
	err = c.UpdateModel(context.Background(), "p1", details, ModelEditRequest{MaxWorkers: &workers}, "")
	if err == nil || !strings.Contains(err.Error(), "did not return default max tokens") {
		t.Fatalf("missing default max tokens error = %v", err)
	}
	if updates != 0 {
		t.Fatalf("missing default max tokens made %d mutations, want zero", updates)
	}
}

func TestUpdateModelRejectsUnsafeNameWithoutMutation(t *testing.T) {
	var updates int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			updates++
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	unsafe := "Unsafe\x1b[31m\nName"
	err = c.UpdateModel(context.Background(), "p1", ModelEditDetails{
		ID: "model-1", Name: "OpenAI", Provider: "openai", Model: "gpt-5.6-sol",
	}, ModelEditRequest{Name: &unsafe}, "")
	if err == nil || !strings.Contains(err.Error(), "terminal control") {
		t.Fatalf("unsafe name error = %v", err)
	}
	if updates != 0 {
		t.Fatalf("unsafe name made %d mutations, want zero", updates)
	}
}
