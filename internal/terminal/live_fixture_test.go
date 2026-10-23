package terminal

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/openvibely/openvibely-terminal/internal/testutil"
)

var acceptLiveTest = testutil.AcceptLive

func acceptExecutionTest(t *testing.T, w http.ResponseWriter, r *http.Request, projectID, execID string) *testutil.LiveSocket {
	t.Helper()
	live := acceptLiveTest(t, w, r)
	if live != nil && (live.Command.Op != "execution" || live.Command.ProjectID != projectID || live.Command.ExecID != execID) {
		t.Errorf("execution subscription = %+v, want project %q execution %q", live.Command, projectID, execID)
	}
	return live
}

func liveTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}
