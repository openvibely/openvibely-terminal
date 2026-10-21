package client

import (
	"fmt"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func parseAttachmentMarkup(t *testing.T, markup, taskID, projectID string) ([]Attachment, error) {
	t.Helper()
	root, err := html.Parse(strings.NewReader(markup))
	if err != nil {
		t.Fatal(err)
	}
	return parseTaskAttachmentsForProject(root, taskID, projectID)
}

func TestParseTaskAttachmentsProjectAwareEmptyAndManyRows(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		got, err := parseAttachmentMarkup(t, `<div data-task-project-id="p1"><div id="attachment-list" data-project-id="p1"></div></div>`, "task-1", "p1")
		if err != nil {
			t.Fatal(err)
		}
		if got == nil || len(got) != 0 {
			t.Fatalf("attachments = %#v, want a non-nil empty slice", got)
		}
	})

	t.Run("many preserve fields and order", func(t *testing.T) {
		const count = 500
		var markup strings.Builder
		fmt.Fprint(&markup, `<div data-task-project-id="p1"><div id="attachment-list" data-project-id="p1">`)
		for i := 0; i < count; i++ {
			fmt.Fprintf(&markup, `<div class="attachment-row" data-file-path="/f/%d" data-media-type="text/plain"><p class="font-medium">file-%03d.txt</p><p class="text-xs">%d B</p><button hx-delete="/attachments/id-%03d?project_id=p1"></button></div>`, i, i, i+1, i)
		}
		fmt.Fprint(&markup, `</div></div>`)
		got, err := parseAttachmentMarkup(t, markup.String(), "task-1", "p1")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != count {
			t.Fatalf("attachment count = %d, want %d", len(got), count)
		}
		for i, attachment := range got {
			want := Attachment{ID: fmt.Sprintf("id-%03d", i), TaskID: "task-1", FileName: fmt.Sprintf("file-%03d.txt", i), FilePath: fmt.Sprintf("/f/%d", i), MediaType: "text/plain", FileSize: int64(i + 1)}
			if attachment != want {
				t.Errorf("attachments[%d] = %+v, want %+v", i, attachment, want)
				break
			}
		}
	})
}

func TestParseTaskAttachmentsProjectAwareNestedDeleteControlUsesOuterRow(t *testing.T) {
	markup := `<div data-task-project-id="p1"><div id="attachment-list">
		<div class="attachment-row" data-file-path="/wrapped" data-media-type="text/plain">
			<p class="font-medium">wrapped.txt</p><p class="text-xs">7 B</p>
			<div class="control-wrapper"><span><button hx-delete="/attachments/wrapped?project_id=p1"></button></span></div>
		</div>
	</div></div>`
	got, err := parseAttachmentMarkup(t, markup, "task-1", "p1")
	if err != nil {
		t.Fatal(err)
	}
	want := []Attachment{{ID: "wrapped", TaskID: "task-1", FileName: "wrapped.txt", FilePath: "/wrapped", MediaType: "text/plain", FileSize: 7}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("attachments = %+v, want %+v", got, want)
	}
}

func TestParseTaskAttachmentsProjectAwareDuplicateIDsAndFallbackMarkup(t *testing.T) {
	markup := `<div data-task-project-id="p1"><div id="attachment-list" data-project-id="p1">
		<div class="attachment-row" data-file-path="/first" data-content-type="text/plain">
			<div><p class="font-medium">first.txt</p><p class="text-xs">5 B</p></div>
			<button hx-delete="/attachments/dup?project_id=p1"></button><button hx-delete="/attachments/dup?project_id=p1"></button>
		</div>
		<div class="attachment-row"><p class="font-medium">second-duplicate.txt</p><p class="text-xs">9 B</p><button hx-delete="/attachments/dup?project_id=p1"></button></div>
		<div class="attachment-row"><p class="text-xs">2 B</p><button hx-delete="/attachments/later-valid?project_id=p1"></button></div>
		<div class="attachment-row"><p class="font-medium">later-valid.txt</p><button hx-delete="/attachments/later-valid?project_id=p1"></button></div>
		<div data-attachment-id="fallback-1" data-attachment-filename=" report.csv " data-attachment-size="1.5 KB" data-file-path="/fallback" data-media-type="text/csv"></div>
		<div data-attachment-id="fallback-1" data-filename="ignored.csv"></div>
		<div data-attachment-id="fallback-2"><p>plain name.bin</p><p class="text-xs">2 MB</p></div>
	</div></div>`
	got, err := parseAttachmentMarkup(t, markup, "task-1", "p1")
	if err != nil {
		t.Fatal(err)
	}
	want := []Attachment{
		{ID: "dup", TaskID: "task-1", FileName: "first.txt", FilePath: "/first", MediaType: "text/plain", FileSize: 5},
		{ID: "later-valid", TaskID: "task-1", FileName: "later-valid.txt"},
		{ID: "fallback-1", TaskID: "task-1", FileName: " report.csv ", FilePath: "/fallback", MediaType: "text/csv", FileSize: 1536},
		{ID: "fallback-2", TaskID: "task-1", FileName: "plain name.bin", FileSize: 2 << 20},
	}
	if len(got) != len(want) {
		t.Fatalf("attachments = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("attachments[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseTaskAttachmentsProjectAwareRejectsTaskAndDeleteURLProjectMismatches(t *testing.T) {
	for _, tc := range []struct {
		name   string
		markup string
		want   string
	}{
		{
			name:   "task marker",
			markup: `<div data-project-id="p1"><div data-task-project-id="p2"><div id="attachment-list" data-project-id="p1"></div></div></div>`,
			want:   `task attachment response belongs to project "p2", not selected project "p1"`,
		},
		{
			name:   "delete URL marker",
			markup: `<div data-task-project-id="p1"><div id="attachment-list" data-project-id="p1"><div class="attachment-row"><p class="font-medium">foreign.txt</p><button hx-delete="/attachments/foreign?project_id=p2"></button></div></div></div>`,
			want:   `attachment response belongs to project "p2", not selected project "p1"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseAttachmentMarkup(t, tc.markup, "task-1", "p1")
			if err == nil || err.Error() != tc.want {
				t.Fatalf("parse error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestParseAttachmentMutationResponseChecksMissingListBeforeProjectMismatch(t *testing.T) {
	root, err := html.Parse(strings.NewReader(`<div data-task-project-id="p2"></div>`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = parseAttachmentMutationResponse(root, "delete attachment", "task-1", "p1")
	if err == nil || err.Error() != `delete attachment response did not include the attachment list` {
		t.Fatalf("mutation error = %v, want missing-list error before project mismatch", err)
	}
}

func TestLegacyAndSinglePassAttachmentParsersAgree(t *testing.T) {
	fixtures := []string{
		`<div data-task-project-id="p1"><div id="attachment-list" data-project-id="p1"></div></div>`,
		`<main data-project-id="p1"><div id="attachment-list"><div class="attachment-row"><p class="font-medium">a.txt</p><p class="text-xs">1 KB</p><button hx-delete="/attachments/a?project_id=p1"></button></div></div></main>`,
		`<div data-task-project-id="p1"><div id="attachment-list"><div data-attachment-id="a" data-file-name="a.txt" data-file-size="8"></div><div data-attachment-id="b"><p>b.txt</p><p class="text-xs">2 B</p></div></div></div>`,
		`<div data-project-id="p1"><div id="attachment-list"><section><div><p class="font-medium">nested.txt</p><button hx-delete="/attachments/nested?project_id=p1"></button></div></section></div></div>`,
		`<div data-project-id="p1"><div id="attachment-list"><section data-file-name="standalone.txt" data-file-path="/standalone"><button hx-delete="/attachments/standalone?project_id=p1"></button></section></div></div>`,
	}
	for i, markup := range fixtures {
		root, err := html.Parse(strings.NewReader(markup))
		if err != nil {
			t.Fatal(err)
		}
		got, gotErr := parseTaskAttachmentsForProject(root, "task-1", "p1")
		want, wantErr := legacyParseTaskAttachmentsForProject(root, "task-1", "p1")
		if (gotErr == nil) != (wantErr == nil) || gotErr != nil && gotErr.Error() != wantErr.Error() {
			t.Fatalf("fixture %d errors: changed=%v baseline=%v", i, gotErr, wantErr)
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("fixture %d: changed attachments = %+v, baseline = %+v", i, got, want)
		}
	}
}
