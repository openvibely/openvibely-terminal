package terminal

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openvibely/openvibely-terminal/internal/client"
)

// BenchmarkOutboundTargetsJSONEdit compares the canonical one-row save reply
// with the legacy reply that requires a second full-list refresh. Fixtures keep
// the initial list and save workload constant while varying list size and
// server delay.
func BenchmarkOutboundTargetsJSONEdit(b *testing.B) {
	for _, rowCount := range []int{100, 1000, 5000} {
		for _, delay := range []time.Duration{0, 50 * time.Millisecond} {
			b.Run(fmt.Sprintf("rows=%d/delay=%dms", rowCount, delay.Milliseconds()), func(b *testing.B) {
				original := client.OutboundTarget{
					ID: "target-edit", Platform: "email", TargetKind: "email",
					Name: "old", Destination: "old@example.com",
				}
				initial := terminalTargetsWithCount(rowCount, original)
				saved := append([]client.OutboundTarget(nil), initial...)
				saved[0].Name = "canonical"
				saved[0].Destination = "person@example.com"
				initialBody := terminalOutboundTargetPage("p1", initial, false)
				savedBody := terminalOutboundTargetPage("p1", saved, false)
				targetBody := terminalSavedOutboundTarget(saved[0])

				for _, mode := range []string{"canonical", "legacy-refresh"} {
					b.Run(mode, func(b *testing.B) {
						var targetGets, saves, responseBytes int
						getsPerOperation := 1
						if mode == "legacy-refresh" {
							getsPerOperation = 2
						}
						server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							switch {
							case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
								w.Header().Set("Content-Type", "application/json")
								n, _ := io.WriteString(w, cliProjects)
								responseBytes += n
							case r.Method == http.MethodGet && r.URL.Path == "/channels/outbound-targets":
								if delay > 0 {
									time.Sleep(delay)
								}
								which := targetGets % getsPerOperation
								targetGets++
								body := initialBody
								if which == 1 {
									body = savedBody
								}
								w.Header().Set("Content-Type", "text/html")
								n, _ := io.WriteString(w, body)
								responseBytes += n
							case r.Method == http.MethodPost && r.URL.Path == "/channels/send-message-explicit-targets":
								if delay > 0 {
									time.Sleep(delay)
								}
								saves++
								body := "<div>saved</div>"
								if mode == "canonical" {
									body = targetBody
								}
								w.Header().Set("Content-Type", "text/html")
								n, _ := io.WriteString(w, body)
								responseBytes += n
							default:
								http.NotFound(w, r)
							}
						}))
						defer server.Close()
						c, err := client.New(server.URL)
						if err != nil {
							b.Fatal(err)
						}
						args := []string{"channels", "targets", "edit", original.ID, "--destination", "PERSON@EXAMPLE.COM"}
						var out bytes.Buffer
						b.ReportAllocs()
						b.ResetTimer()
						for i := 0; i < b.N; i++ {
							out.Reset()
							if err := RunCLI(c, &out, "demo", args, false, true); err != nil {
								b.Fatal(err)
							}
						}
						b.StopTimer()
						b.ReportMetric(float64(targetGets+saves)/float64(b.N), "target-req/op")
						b.ReportMetric(float64(responseBytes)/float64(b.N), "response-B/op")
						if targetGets != b.N*getsPerOperation || saves != b.N {
							b.Fatalf("request counts GET/POST=%d/%d for %d operations", targetGets, saves, b.N)
						}
					})
				}
			})
		}
	}
}
