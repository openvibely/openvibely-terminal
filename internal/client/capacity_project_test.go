package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetProjectCapacityUsesScopedEndpoint(t *testing.T) {
	const projectID = "project-123"
	var requestedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		if r.URL.Path != "/api/capacity/projects/"+projectID {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"project-123","name":"demo","running":3,"queue_size":2,"max_workers":5}`))
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.GetProjectCapacity(context.Background(), projectID)
	if err != nil {
		t.Fatalf("GetProjectCapacity: %v", err)
	}
	if requestedPath != "/api/capacity/projects/"+projectID {
		t.Fatalf("request path = %q", requestedPath)
	}
	if got.ID != projectID || got.Running != 3 || got.QueueSize != 2 || got.MaxWorkers == nil || *got.MaxWorkers != 5 {
		t.Fatalf("capacity = %+v", got)
	}
}

func TestGetProjectCapacityReturnsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetProjectCapacity(context.Background(), "missing"); err == nil {
		t.Fatal("GetProjectCapacity unexpectedly succeeded")
	}
}

func TestGetProjectCapacityRequiresProjectID(t *testing.T) {
	c, err := New("http://example.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetProjectCapacity(context.Background(), " "); err == nil {
		t.Fatal("GetProjectCapacity accepted an empty project ID")
	}
}
