package terminal

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/openvibely/openvibely-terminal/internal/client"
)

const modelEditCard = `<div data-model-id="model-1" data-model-name="OpenAI" data-model-provider="openai" data-model-model="gpt-4o"></div>`

func TestModelsEditAppliesOnlyExplicitFieldsAndRefreshes(t *testing.T) {
	var lists, details, updates int
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /models":
			lists++
			if got := r.URL.Query().Get("project_id"); got != "p1" {
				t.Errorf("models project_id = %q, want p1", got)
			}
			_, _ = io.WriteString(w, modelEditCard)
		case "GET /models/model-1/edit-details":
			details++
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"model-1","name":"OpenAI","provider":"openai","model":"gpt-4o","auth_method":"api_key","is_default":true,"max_workers":2,"worker_timeout":60,"auto_start_tasks":true}`)
		case "PUT /models/model-1":
			updates++
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			form = r.PostForm
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
	m := New(c)
	m.selectedID, m.selectedName = "p1", "demo"
	m = runLine(t, m, `/models edit OpenAI --name "Updated OpenAI" --model gpt-4.1 --default false --max-workers 4 --worker-timeout 25`)
	if lists != 2 || details != 1 || updates != 1 {
		t.Fatalf("list/detail/update requests = %d/%d/%d, want 2/1/1", lists, details, updates)
	}
	for key, want := range map[string]string{
		"name": "Updated OpenAI", "model": "gpt-4.1", "provider": "openai",
		"openai_auth_type": "api_key", "model_max_workers": "4", "worker_timeout": "25", "auto_start_tasks": "on",
	} {
		if got := form.Get(key); got != want {
			t.Errorf("form[%q] = %q, want %q", key, got, want)
		}
	}
	if form.Get("is_default") != "" || form.Get("api_key") != "" {
		t.Fatalf("form changed implicit default/key: %v", form)
	}
	if out := stripANSI(transcript(m)); !strings.Contains(out, "updated Updated OpenAI") {
		t.Fatalf("edit output missing success:\n%s", out)
	}
}

func TestModelsEditInteractiveAPIKeyIsMaskedAndValidationFailureDoesNotRefresh(t *testing.T) {
	const secret = "interactive-replacement-secret"
	var lists, details, updates int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /models":
			lists++
			_, _ = io.WriteString(w, modelEditCard)
		case "GET /models/model-1/edit-details":
			details++
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"model-1","name":"OpenAI","provider":"openai","model":"gpt-4o","auth_method":"api_key","api_key":"saved-key"}`)
		case "PUT /models/model-1":
			updates++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"rejected `+secret+`"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	m.selectedID, m.selectedName = "p1", "demo"
	m = runLine(t, m, "/models edit OpenAI --api-key")
	if m.modelEditWizard == nil || m.input.EchoMode != textinput.EchoPassword {
		t.Fatal("API-key edit did not open masked input")
	}
	m = runLine(t, m, secret)
	if lists != 1 || details != 1 || updates != 1 || m.modelEditWizard != nil || m.input.EchoMode == textinput.EchoPassword {
		t.Fatalf("validation failure edit state/requests = lists=%d details=%d updates=%d wizard=%v", lists, details, updates, m.modelEditWizard != nil)
	}
	for _, output := range []string{m.View(), transcript(m)} {
		if strings.Contains(output, secret) || strings.Contains(output, "saved-key") {
			t.Fatalf("model credential leaked in output:\n%s", output)
		}
	}
	for _, item := range m.history {
		if strings.Contains(item, secret) {
			t.Fatalf("model credential leaked in history: %q", item)
		}
	}
	if out := transcript(m); strings.Contains(out, "updated OpenAI") || !strings.Contains(out, "[redacted]") {
		t.Fatalf("validation failure reported success or missed redaction:\n%s", out)
	}
}

func TestModelsEditRejectsAmbiguousReferenceBeforeDetailsOrMutation(t *testing.T) {
	var lists, details, updates int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /models":
			lists++
			_, _ = io.WriteString(w, `<div data-model-id="model-1" data-model-name="Duplicate" data-model-provider="openai" data-model-model="gpt-4o"></div><div data-model-id="model-2" data-model-name="Duplicate" data-model-provider="openai" data-model-model="gpt-4.1"></div>`)
		case "GET /models/model-1/edit-details", "GET /models/model-2/edit-details":
			details++
		case "PUT /models/model-1", "PUT /models/model-2":
			updates++
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	m.selectedID, m.selectedName = "p1", "demo"
	m = runLine(t, m, "/models edit Duplicate --model gpt-4.2")
	if lists != 1 || details != 0 || updates != 0 || !strings.Contains(strings.ToLower(transcript(m)), "ambiguous") {
		t.Fatalf("ambiguous edit requests/output = %d/%d/%d:\n%s", lists, details, updates, transcript(m))
	}
}

func TestModelsEditOAuthReportsBackendStatusWithoutPrematureSuccess(t *testing.T) {
	var lists, statusReads int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /models":
			lists++
			_, _ = io.WriteString(w, `<div data-model-id="model-1" data-model-name="OAuth OpenAI" data-model-provider="openai" data-model-model="gpt-4o"></div>`)
		case "GET /models/model-1/edit-details":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"model-1","name":"OAuth OpenAI","provider":"openai","model":"gpt-4o","auth_method":"oauth"}`)
		case "PUT /models/model-1":
			w.WriteHeader(http.StatusNoContent)
		case "GET /models/model-1/oauth/status":
			statusReads++
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"status":"authorization_required"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	m.selectedID, m.selectedName = "p1", "demo"
	m = runLine(t, m, "/models edit OAuth --max-workers 2")
	out := stripANSI(transcript(m))
	if lists != 2 || statusReads != 1 || !strings.Contains(out, "authorization is required") || strings.Contains(out, "OAuth connected") {
		t.Fatalf("OAuth edit output/status requests = lists=%d status=%d:\n%s", lists, statusReads, out)
	}
}

func TestModelsEditUpdatesCompatibleEndpoint(t *testing.T) {
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /models":
			_, _ = io.WriteString(w, `<div data-model-id="compatible-1" data-model-name="Compatible" data-model-provider="openai_compatible" data-model-model="custom-model"></div>`)
		case "GET /models/compatible-1/edit-details":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"compatible-1","name":"Compatible","provider":"openai_compatible","model":"custom-model","auth_method":"api_key","base_url":"https://old.example/v1","transport":"chat_completions","preset_slug":"custom"}`)
		case "PUT /models/compatible-1":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			form = r.PostForm
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
	m := New(c)
	m.selectedID, m.selectedName = "p1", "demo"
	m = runLine(t, m, "/models edit Compatible --endpoint https://new.example/v1")
	if form.Get("base_url") != "https://new.example/v1" || form.Get("ollama_base_url") != "" {
		t.Fatalf("compatible endpoint form = %v", form)
	}
}

func TestModelsEditMalformedSensitiveOptionsAreRedacted(t *testing.T) {
	for _, line := range []string{
		`/models edit OpenAI --api-key edit-inline-secret`,
		`/models edit OpenAI --endpoint "http://user:edit-endpoint-secret@localhost:11434"`,
		`/models edit OpenAI --api-key "edit-unmatched-secret`,
	} {
		m, rec := dispatchModel(t, nil)
		m.selectedID, m.selectedName = "p1", "demo"
		m = runLine(t, m, line)
		for _, secret := range []string{"edit-inline-secret", "edit-endpoint-secret", "edit-unmatched-secret"} {
			if strings.Contains(m.View(), secret) || strings.Contains(transcript(m), secret) {
				t.Fatalf("malformed edit command leaked %q:\n%s", secret, transcript(m))
			}
			for _, item := range m.history {
				if strings.Contains(item, secret) {
					t.Fatalf("malformed edit command leaked %q in history: %q", secret, item)
				}
			}
		}
		if calls := rec.all(); calls != "" {
			t.Fatalf("malformed sensitive edit made requests:\n%s", calls)
		}
	}
}

func TestCLIModelsEditUsesStdinAndProducesSecretFreeJSON(t *testing.T) {
	const secret = "cli-replacement-secret"
	var updateForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"projects":[{"id":"p1","name":"demo"}]}`)
		case "GET /models":
			_, _ = io.WriteString(w, modelEditCard)
		case "GET /models/model-1/edit-details":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"model-1","name":"OpenAI","provider":"openai","model":"gpt-4o","auth_method":"api_key","api_key":"saved-key"}`)
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
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = RunCLIWithInput(c, &out, strings.NewReader(secret+"\n"), "demo", []string{"models", "edit", "OpenAI", "--api-key-stdin"}, false, true)
	if err != nil {
		t.Fatalf("models edit CLI: %v", err)
	}
	if updateForm.Get("api_key") != secret {
		t.Errorf("replacement API key = %q, want supplied stdin value", updateForm.Get("api_key"))
	}
	if strings.Contains(out.String(), secret) || strings.Contains(out.String(), "saved-key") || !strings.Contains(out.String(), `"status":"updated OpenAI"`) {
		t.Fatalf("CLI JSON edit result leaked/missing status: %s", out.String())
	}
}

func TestModelsEditKeepsSaveSuccessWhenRefreshFails(t *testing.T) {
	var lists, updates int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /models":
			lists++
			if lists == 2 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `{"error":"refresh unavailable"}`)
				return
			}
			_, _ = io.WriteString(w, modelEditCard)
		case "GET /models/model-1/edit-details":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"model-1","name":"OpenAI","provider":"openai","model":"gpt-4o","auth_method":"api_key"}`)
		case "PUT /models/model-1":
			updates++
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
	m := New(c)
	m.selectedID, m.selectedName = "p1", "demo"
	m = runLine(t, m, "/models edit OpenAI --max-workers 3")
	out := transcript(m)
	if lists != 2 || updates != 1 || !strings.Contains(out, "updated OpenAI") || strings.Contains(out, "refresh unavailable") {
		t.Fatalf("save/refresh result = lists=%d updates=%d:\n%s", lists, updates, out)
	}
}
