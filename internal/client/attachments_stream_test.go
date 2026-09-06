package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type attachmentTestFile struct {
	read      func([]byte) (int, error)
	close     func() error
	size      int64
	closeCall atomic.Int32
}

func (f *attachmentTestFile) Read(p []byte) (int, error) { return f.read(p) }
func (f *attachmentTestFile) Close() error {
	f.closeCall.Add(1)
	if f.close != nil {
		return f.close()
	}
	return nil
}
func (f *attachmentTestFile) Stat() (os.FileInfo, error) {
	return attachmentTestFileInfo{size: f.size}, nil
}

type attachmentTestFileInfo struct{ size int64 }

func stubAttachmentStat(t testing.TB, size int64) {
	t.Helper()
	originalStat := statAttachmentFile
	t.Cleanup(func() { statAttachmentFile = originalStat })
	statAttachmentFile = func(string) (os.FileInfo, error) {
		return attachmentTestFileInfo{size: size}, nil
	}
}

func (i attachmentTestFileInfo) Name() string       { return "fixture.bin" }
func (i attachmentTestFileInfo) Size() int64        { return i.size }
func (i attachmentTestFileInfo) Mode() os.FileMode  { return 0o600 }
func (i attachmentTestFileInfo) ModTime() time.Time { return time.Time{} }
func (i attachmentTestFileInfo) IsDir() bool        { return false }
func (i attachmentTestFileInfo) Sys() any           { return nil }

func attachmentHTMLResponse(status int) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(attachmentListMarkup("p1", attachmentRowMarkup("att-1", "p1", "fixture.bin", "64 KB")))),
	}
}

func TestAddTaskAttachmentsLooksUpBeforeOpeningFiles(t *testing.T) {
	originalOpen := openAttachmentFile
	defer func() { openAttachmentFile = originalOpen }()

	var opens atomic.Int32
	openAttachmentFile = func(path string) (attachmentFile, error) {
		opens.Add(1)
		return os.Open(path)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.bin")
	if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := newAttachmentUploadServer(t, func(r *http.Request) {
		if opens.Load() != 0 {
			t.Errorf("attachment opened before preliminary lookup completed")
		}
	}, nil)
	defer srv.Close()

	c, _ := New(srv.URL)
	if _, err := c.AddTaskAttachments(context.Background(), "t-1", "p1", []string{path}); err != nil {
		t.Fatalf("AddTaskAttachments: %v", err)
	}
	if opens.Load() != 1 {
		t.Fatalf("open calls = %d, want 1", opens.Load())
	}
}

func TestAddTaskAttachmentsStartsBodyBeforeReadingFullPayload(t *testing.T) {
	const payloadSize = 64 << 10
	stubAttachmentStat(t, payloadSize)
	originalOpen := openAttachmentFile
	defer func() { openAttachmentFile = originalOpen }()

	allowRest := make(chan struct{})
	var readBytes atomic.Int64
	file := &attachmentTestFile{size: payloadSize}
	file.read = func(p []byte) (int, error) {
		read := readBytes.Load()
		if read >= payloadSize {
			return 0, io.EOF
		}
		if read > 0 {
			<-allowRest
		}
		n := len(p)
		if remaining := payloadSize - int(read); n > remaining {
			n = remaining
		}
		for i := 0; i < n; i++ {
			p[i] = 'x'
		}
		readBytes.Add(int64(n))
		return n, nil
	}
	openAttachmentFile = func(string) (attachmentFile, error) { return file, nil }

	firstBodyByte := make(chan struct{})
	c, _ := New("http://attachments.test")
	c.http.Transport = htmlTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet {
			resp := attachmentHTMLResponse(http.StatusOK)
			resp.Body = io.NopCloser(strings.NewReader(attachmentListMarkup("p1", "")))
			return resp, nil
		}
		if r.ContentLength <= payloadSize {
			t.Errorf("Content-Length = %d, want multipart body larger than payload", r.ContentLength)
		}
		if len(r.TransferEncoding) != 0 {
			t.Errorf("Transfer-Encoding = %v, want fixed-length upload", r.TransferEncoding)
		}
		var one [1]byte
		if _, err := io.ReadFull(r.Body, one[:]); err != nil {
			return nil, err
		}
		close(firstBodyByte)
		close(allowRest)
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			return nil, err
		}
		return attachmentHTMLResponse(http.StatusOK), nil
	})

	if _, err := c.AddTaskAttachments(context.Background(), "t-1", "p1", []string{"fixture.bin"}); err != nil {
		t.Fatalf("AddTaskAttachments: %v", err)
	}
	select {
	case <-firstBodyByte:
	default:
		t.Fatal("request body did not start")
	}
	if readBytes.Load() != payloadSize {
		t.Fatalf("payload bytes read = %d, want %d after completion", readBytes.Load(), payloadSize)
	}
}

func TestAddTaskAttachmentsPreservesDuplicateEmptyPartsAndOrder(t *testing.T) {
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "same.txt"), filepath.Join(dir, "empty.txt")}
	if err := os.WriteFile(paths[0], []byte("same"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths[1], nil, 0o600); err != nil {
		t.Fatal(err)
	}
	paths = []string{paths[0], paths[1], paths[0]}

	var names, contents []string
	srv := newAttachmentUploadServer(t, nil, func(r *http.Request) {
		reader, err := r.MultipartReader()
		if err != nil {
			t.Fatalf("MultipartReader: %v", err)
		}
		for {
			part, err := reader.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("NextPart: %v", err)
			}
			body, err := io.ReadAll(part)
			if err != nil {
				t.Fatalf("read part: %v", err)
			}
			names = append(names, part.FileName())
			contents = append(contents, string(body))
		}
	})
	defer srv.Close()
	c, _ := New(srv.URL)
	if _, err := c.AddTaskAttachments(context.Background(), "t-1", "p1", paths); err != nil {
		t.Fatalf("AddTaskAttachments: %v", err)
	}
	if got, want := strings.Join(names, ","), "same.txt,empty.txt,same.txt"; got != want {
		t.Fatalf("part order = %q, want %q", got, want)
	}
	if got, want := strings.Join(contents, ","), "same,,same"; got != want {
		t.Fatalf("part contents = %q, want %q", got, want)
	}
}

func TestAddTaskAttachmentsUnblocksProducerOnEarlyFailureAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		transport func(*http.Request) (*http.Response, error)
		want      string
	}{
		{name: "rejection", want: "rejected", transport: func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodGet {
				return attachmentHTMLResponse(http.StatusOK), nil
			}
			resp := attachmentHTMLResponse(http.StatusRequestEntityTooLarge)
			resp.Body = io.NopCloser(strings.NewReader(`{"error":"rejected"}`))
			return resp, nil
		}},
		{name: "reset", want: "connection reset", transport: func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodGet {
				return attachmentHTMLResponse(http.StatusOK), nil
			}
			return nil, errors.New("connection reset")
		}},
		{name: "timeout", want: "context deadline exceeded", transport: func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodGet {
				return attachmentHTMLResponse(http.StatusOK), nil
			}
			<-r.Context().Done()
			return nil, r.Context().Err()
		}},
		{name: "cancellation", want: "context canceled", transport: func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodGet {
				return attachmentHTMLResponse(http.StatusOK), nil
			}
			<-r.Context().Done()
			return nil, r.Context().Err()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubAttachmentStat(t, 1<<30)
			originalOpen := openAttachmentFile
			defer func() { openAttachmentFile = originalOpen }()
			file := &attachmentTestFile{size: 1 << 30, read: func(p []byte) (int, error) {
				for i := range p {
					p[i] = 'x'
				}
				return len(p), nil
			}}
			var opens atomic.Int32
			openAttachmentFile = func(string) (attachmentFile, error) {
				opens.Add(1)
				return file, nil
			}
			c, _ := New("http://attachments.test")
			c.http.Transport = htmlTestRoundTripper(tc.transport)
			ctx := context.Background()
			var cancel context.CancelFunc
			switch tc.name {
			case "cancellation":
				ctx, cancel = context.WithCancel(ctx)
				time.AfterFunc(10*time.Millisecond, cancel)
			case "timeout":
				ctx, cancel = context.WithTimeout(ctx, 10*time.Millisecond)
			default:
				ctx, cancel = context.WithCancel(ctx)
			}
			defer cancel()
			started := time.Now()
			_, err := c.AddTaskAttachments(ctx, "t-1", "p1", []string{"fixture.bin"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("upload took %v after early failure", elapsed)
			}
			if file.closeCall.Load() != opens.Load() {
				t.Fatalf("open/close calls = %d/%d, want matching cleanup", opens.Load(), file.closeCall.Load())
			}
		})
	}
}

func TestAddTaskAttachmentsPreservesLocalFileErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		file *attachmentTestFile
		open error
		want string
	}{
		{name: "open", open: errors.New("open sentinel"), want: `open attachment "fixture.bin": open sentinel`},
		{name: "read", file: &attachmentTestFile{size: 1, read: func([]byte) (int, error) { return 0, errors.New("read sentinel") }}, want: `read attachment "fixture.bin": read sentinel`},
		{name: "close", file: &attachmentTestFile{read: func([]byte) (int, error) { return 0, io.EOF }, close: func() error { return errors.New("close sentinel") }}, want: `close attachment "fixture.bin": close sentinel`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			size := int64(0)
			if tc.file != nil {
				size = tc.file.size
			}
			stubAttachmentStat(t, size)
			originalOpen := openAttachmentFile
			defer func() { openAttachmentFile = originalOpen }()
			openAttachmentFile = func(string) (attachmentFile, error) { return tc.file, tc.open }
			c, _ := New("http://attachments.test")
			c.http.Transport = htmlTestRoundTripper(func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodGet {
					return attachmentHTMLResponse(http.StatusOK), nil
				}
				_, err := io.Copy(io.Discard, r.Body)
				if err != nil {
					return nil, err
				}
				return attachmentHTMLResponse(http.StatusOK), nil
			})
			_, err := c.AddTaskAttachments(context.Background(), "t-1", "p1", []string{"fixture.bin"})
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error = %q, want exact %q", err, tc.want)
			}
		})
	}
}

func TestAddTaskAttachmentsHTTPCancellationClosesBlockedBodyRead(t *testing.T) {
	stubAttachmentStat(t, 1<<20)
	originalOpen := openAttachmentFile
	defer func() { openAttachmentFile = originalOpen }()

	readStarted := make(chan struct{})
	closed := make(chan struct{})
	var readOnce, closeOnce sync.Once
	file := &attachmentTestFile{size: 1 << 20}
	file.read = func([]byte) (int, error) {
		readOnce.Do(func() { close(readStarted) })
		<-closed
		return 0, os.ErrClosed
	}
	file.close = func() error {
		closeOnce.Do(func() { close(closed) })
		return nil
	}
	openAttachmentFile = func(string) (attachmentFile, error) { return file, nil }

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, attachmentListMarkup("p1", ""))
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
	}))
	defer srv.Close()
	c, _ := New(srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.AddTaskAttachments(ctx, "t-1", "p1", []string{"fixture.bin"})
		done <- err
	}()
	select {
	case <-readStarted:
	case <-time.After(time.Second):
		t.Fatal("HTTP transport did not start reading the body")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) && (err == nil || !strings.Contains(err.Error(), context.Canceled.Error())) {
			t.Fatalf("AddTaskAttachments error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP cancellation did not unblock the body read")
	}
	select {
	case <-closed:
	default:
		t.Fatal("HTTP transport did not close the active attachment")
	}
	if got := file.closeCall.Load(); got != 1 {
		t.Fatalf("file close calls = %d, want 1", got)
	}
}

func TestAttachmentMultipartBodyCloseDuringLazyOpenPreservesCloseFailure(t *testing.T) {
	stubAttachmentStat(t, 1)
	originalOpen := openAttachmentFile
	defer func() { openAttachmentFile = originalOpen }()

	openStarted := make(chan struct{})
	allowOpen := make(chan struct{})
	closeErr := errors.New("lazy-open close sentinel")
	file := &attachmentTestFile{
		size: 1,
		read: func([]byte) (int, error) { return 0, io.EOF },
		close: func() error {
			return closeErr
		},
	}
	openAttachmentFile = func(string) (attachmentFile, error) {
		close(openStarted)
		<-allowOpen
		return file, nil
	}

	body, err := newAttachmentMultipartBody([]string{"fixture.bin"})
	if err != nil {
		t.Fatalf("newAttachmentMultipartBody: %v", err)
	}
	readDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, body)
		readDone <- err
	}()
	select {
	case <-openStarted:
	case <-time.After(time.Second):
		t.Fatal("body did not start lazy open")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- body.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before lazy open completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(allowOpen)
	for name, done := range map[string]<-chan error{"Read": readDone, "Close": closeDone} {
		select {
		case err := <-done:
			if !errors.Is(err, closeErr) {
				t.Fatalf("%s error = %v, want %v", name, err, closeErr)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s did not finish", name)
		}
	}
	if got := file.closeCall.Load(); got != 1 {
		t.Fatalf("underlying close calls = %d, want 1", got)
	}
}

func TestAttachmentMultipartBodyRejectsChangedFile(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*testing.T, string)
	}{
		{name: "grown", change: func(t *testing.T, path string) {
			t.Helper()
			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.WriteString("more"); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "replaced", change: func(t *testing.T, path string) {
			t.Helper()
			replacement := path + ".replacement"
			if err := os.WriteFile(replacement, []byte("swap"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(replacement, path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fixture.bin")
			if err := os.WriteFile(path, []byte("base"), 0o600); err != nil {
				t.Fatal(err)
			}
			body, err := newAttachmentMultipartBody([]string{path})
			if err != nil {
				t.Fatalf("newAttachmentMultipartBody: %v", err)
			}
			tc.change(t, path)
			_, err = io.Copy(io.Discard, body)
			if err == nil || !strings.Contains(err.Error(), "changed after upload preparation") {
				t.Fatalf("body error = %v, want changed-file failure", err)
			}
			if closeErr := body.Close(); closeErr == nil || closeErr.Error() != err.Error() {
				t.Fatalf("Close error = %v, want %v", closeErr, err)
			}
		})
	}
}

func TestAttachmentMultipartBodyDetectsGrowthDuringRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.bin")
	if err := os.WriteFile(path, []byte("base"), 0o600); err != nil {
		t.Fatal(err)
	}
	body, err := newAttachmentMultipartBody([]string{path})
	if err != nil {
		t.Fatalf("newAttachmentMultipartBody: %v", err)
	}
	header := make([]byte, len(body.headers[0]))
	if _, err := io.ReadFull(body, header); err != nil {
		t.Fatalf("read multipart header: %v", err)
	}
	payload := make([]byte, 2)
	if _, err := io.ReadFull(body, payload); err != nil {
		t.Fatalf("read initial payload: %v", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("more"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, body)
	if err == nil || !strings.Contains(err.Error(), "changed during upload") {
		t.Fatalf("body error = %v, want growth-during-upload failure", err)
	}
}

func TestAttachmentMultipartBodyConcurrentClosesWaitForFailure(t *testing.T) {
	stubAttachmentStat(t, 2)
	originalOpen := openAttachmentFile
	defer func() { openAttachmentFile = originalOpen }()

	closeStarted := make(chan struct{})
	allowClose := make(chan struct{})
	closeErr := errors.New("close sentinel")
	file := &attachmentTestFile{
		size: 2,
		read: func(p []byte) (int, error) {
			p[0] = 'x'
			return 1, nil
		},
		close: func() error {
			close(closeStarted)
			<-allowClose
			return closeErr
		},
	}
	openAttachmentFile = func(string) (attachmentFile, error) { return file, nil }

	body, err := newAttachmentMultipartBody([]string{"fixture.bin"})
	if err != nil {
		t.Fatalf("newAttachmentMultipartBody: %v", err)
	}
	if _, err := io.ReadFull(body, make([]byte, len(body.headers[0])+1)); err != nil {
		t.Fatalf("start multipart body: %v", err)
	}

	firstDone := make(chan error, 1)
	go func() { firstDone <- body.Close() }()
	select {
	case <-closeStarted:
	case <-time.After(time.Second):
		t.Fatal("first Close did not start underlying close")
	}
	secondDone := make(chan error, 1)
	go func() { secondDone <- body.Close() }()
	select {
	case err := <-secondDone:
		t.Fatalf("second Close returned before cleanup completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(allowClose)
	for i, done := range []<-chan error{firstDone, secondDone} {
		select {
		case err := <-done:
			if !errors.Is(err, closeErr) {
				t.Fatalf("Close %d error = %v, want %v", i+1, err, closeErr)
			}
		case <-time.After(time.Second):
			t.Fatalf("Close %d did not finish", i+1)
		}
	}
	if got := file.closeCall.Load(); got != 1 {
		t.Fatalf("underlying close calls = %d, want 1", got)
	}
}

func TestAddTaskAttachmentsWaitsForAsynchronousTransportBodyClose(t *testing.T) {
	stubAttachmentStat(t, 2)
	originalOpen := openAttachmentFile
	defer func() { openAttachmentFile = originalOpen }()

	closeStarted := make(chan struct{})
	allowClose := make(chan struct{})
	closeErr := errors.New("close sentinel")
	file := &attachmentTestFile{
		size: 2,
		read: func(p []byte) (int, error) {
			p[0] = 'x'
			return 1, nil
		},
		close: func() error {
			close(closeStarted)
			<-allowClose
			return closeErr
		},
	}
	openAttachmentFile = func(string) (attachmentFile, error) { return file, nil }

	c, _ := New("http://attachments.test")
	c.http.Transport = htmlTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet {
			resp := attachmentHTMLResponse(http.StatusOK)
			resp.Body = io.NopCloser(strings.NewReader(attachmentListMarkup("p1", "")))
			return resp, nil
		}
		if _, err := io.ReadFull(r.Body, make([]byte, len(r.Body.(*attachmentMultipartBody).headers[0])+1)); err != nil {
			return nil, err
		}
		go func() { _ = r.Body.Close() }()
		return attachmentHTMLResponse(http.StatusOK), nil
	})

	done := make(chan error, 1)
	go func() {
		_, err := c.AddTaskAttachments(context.Background(), "t-1", "p1", []string{"fixture.bin"})
		done <- err
	}()
	select {
	case <-closeStarted:
	case <-time.After(time.Second):
		t.Fatal("transport did not start asynchronous body close")
	}
	select {
	case err := <-done:
		t.Fatalf("AddTaskAttachments returned before transport cleanup completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(allowClose)
	select {
	case err := <-done:
		if !errors.Is(err, closeErr) {
			t.Fatalf("AddTaskAttachments error = %v, want %v", err, closeErr)
		}
	case <-time.After(time.Second):
		t.Fatal("AddTaskAttachments did not finish after body close")
	}
}

func TestAttachmentMultipartBodyMissingFilePreservesOpenError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.bin")
	body, err := newAttachmentMultipartBody([]string{path})
	if err != nil {
		t.Fatalf("newAttachmentMultipartBody returned early error: %v", err)
	}
	_, err = io.Copy(io.Discard, body)
	want := fmt.Sprintf(`open attachment %q: open %s:`, path, path)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("body error = %v, want %q", err, want)
	}
}

func TestAttachmentMultipartBodyLaterPathDoesNotPreemptEarlierLocalError(t *testing.T) {
	for _, tc := range []struct {
		name string
		file *attachmentTestFile
		open error
		want string
	}{
		{name: "open", open: errors.New("first open sentinel"), want: "first open sentinel"},
		{name: "read", file: &attachmentTestFile{size: 1, read: func([]byte) (int, error) { return 0, errors.New("first read sentinel") }}, want: "first read sentinel"},
		{name: "close", file: &attachmentTestFile{size: 1, read: func(p []byte) (int, error) { p[0] = 'x'; return 1, io.EOF }, close: func() error { return errors.New("first close sentinel") }}, want: "first close sentinel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			firstPath := filepath.Join(dir, "first.bin")
			if err := os.WriteFile(firstPath, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			missingPath := filepath.Join(dir, "missing.bin")
			originalOpen := openAttachmentFile
			defer func() { openAttachmentFile = originalOpen }()
			openAttachmentFile = func(path string) (attachmentFile, error) {
				if path == firstPath {
					return tc.file, tc.open
				}
				return os.Open(path)
			}

			body, err := newAttachmentMultipartBody([]string{firstPath, missingPath})
			if err != nil {
				t.Fatalf("later path preempted body processing: %v", err)
			}
			_, err = io.Copy(io.Discard, body)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("body error = %v, want earlier %s error %q", err, tc.name, tc.want)
			}
		})
	}
}

func TestAttachmentMultipartBodyConcurrentReadClose(t *testing.T) {
	stubAttachmentStat(t, 1<<20)
	originalOpen := openAttachmentFile
	defer func() { openAttachmentFile = originalOpen }()

	readStarted := make(chan struct{})
	closed := make(chan struct{})
	var closeOnce sync.Once
	file := &attachmentTestFile{size: 1 << 20}
	file.read = func([]byte) (int, error) {
		close(readStarted)
		<-closed
		return 0, os.ErrClosed
	}
	file.close = func() error {
		closeOnce.Do(func() { close(closed) })
		return nil
	}
	openAttachmentFile = func(string) (attachmentFile, error) { return file, nil }

	body, err := newAttachmentMultipartBody([]string{"fixture.bin"})
	if err != nil {
		t.Fatalf("newAttachmentMultipartBody: %v", err)
	}
	done := make(chan any, 1)
	go func() {
		defer func() { done <- recover() }()
		_, _ = io.Copy(io.Discard, body)
	}()
	select {
	case <-readStarted:
	case <-time.After(time.Second):
		t.Fatal("body read did not reach the file")
	}
	if err := body.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case recovered := <-done:
		if recovered != nil {
			t.Fatalf("concurrent Read/Close panic: %v", recovered)
		}
	case <-time.After(time.Second):
		t.Fatal("concurrent Close did not unblock Read")
	}
}

func TestAttachmentMultipartBodyBoundsOpenFiles(t *testing.T) {
	stubAttachmentStat(t, 1)
	originalOpen := openAttachmentFile
	defer func() { openAttachmentFile = originalOpen }()

	const fileCount = 32
	paths := make([]string, fileCount)
	var active atomic.Int32
	var maximum atomic.Int32
	openAttachmentFile = func(path string) (attachmentFile, error) {
		current := active.Add(1)
		for current > maximum.Load() && !maximum.CompareAndSwap(maximum.Load(), current) {
		}
		return &attachmentTestFile{
			size: 1,
			read: func(p []byte) (int, error) {
				if len(p) == 0 {
					return 0, nil
				}
				p[0] = 'x'
				return 1, io.EOF
			},
			close: func() error {
				active.Add(-1)
				return nil
			},
		}, nil
	}
	for i := range paths {
		paths[i] = fmt.Sprintf("fixture-%02d.bin", i)
	}

	body, err := newAttachmentMultipartBody(paths)
	if err != nil {
		t.Fatalf("newAttachmentMultipartBody: %v", err)
	}
	if _, err := io.Copy(io.Discard, body); err != nil {
		t.Fatalf("read multipart body: %v", err)
	}
	if err := body.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := maximum.Load(); got > 1 {
		t.Fatalf("maximum simultaneously open files = %d, want at most 1", got)
	}
	if got := active.Load(); got != 0 {
		t.Fatalf("files still open = %d", got)
	}
}

func TestAttachmentMultipartBodyUsesFixedLengthAndPullBackpressure(t *testing.T) {
	const payloadSize = 1024
	stubAttachmentStat(t, payloadSize)
	originalOpen := openAttachmentFile
	defer func() { openAttachmentFile = originalOpen }()

	var payloadRead atomic.Int64
	file := &attachmentTestFile{size: payloadSize, read: func(p []byte) (int, error) {
		remaining := payloadSize - int(payloadRead.Load())
		if remaining == 0 {
			return 0, io.EOF
		}
		if len(p) > remaining {
			p = p[:remaining]
		}
		for i := range p {
			p[i] = 'x'
		}
		payloadRead.Add(int64(len(p)))
		return len(p), nil
	}}
	openAttachmentFile = func(string) (attachmentFile, error) { return file, nil }
	body, err := newAttachmentMultipartBody([]string{"fixture.bin"})
	if err != nil {
		t.Fatalf("newAttachmentMultipartBody: %v", err)
	}
	defer body.Close()

	wantLength := int64(len(body.headers[0]) + payloadSize + len(body.trailer))
	if body.ContentLength() != wantLength {
		t.Fatalf("content length = %d, want %d", body.ContentLength(), wantLength)
	}
	header := make([]byte, len(body.headers[0]))
	if _, err := io.ReadFull(body, header); err != nil {
		t.Fatalf("read multipart header: %v", err)
	}
	if payloadRead.Load() != 0 {
		t.Fatalf("payload read ahead by %d bytes before consumer requested it", payloadRead.Load())
	}
	var one [1]byte
	if _, err := io.ReadFull(body, one[:]); err != nil {
		t.Fatalf("read first payload byte: %v", err)
	}
	if payloadRead.Load() != 1 {
		t.Fatalf("payload read = %d, want exactly one demanded byte", payloadRead.Load())
	}
}

func BenchmarkAddTaskAttachments(b *testing.B) {
	for _, sizeMiB := range []int{1, 32, 128} {
		for _, implementation := range []struct {
			name string
			add  func(*Client, string) error
		}{
			{name: "buffered", add: benchmarkBufferedAddTaskAttachment},
			{name: "streaming", add: func(c *Client, path string) error {
				_, err := c.AddTaskAttachments(context.Background(), "t-1", "p1", []string{path})
				return err
			}},
		} {
			b.Run(strconv.Itoa(sizeMiB)+"MiB/"+implementation.name, func(b *testing.B) {
				dir := b.TempDir()
				path := filepath.Join(dir, "fixture.bin")
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					b.Fatal(err)
				}
				if err := os.Truncate(path, int64(sizeMiB)<<20); err != nil {
					b.Fatal(err)
				}

				var started atomic.Int64
				var durationsMu sync.Mutex
				firstBodyDurations := make([]time.Duration, 0, b.N)
				totalDurations := make([]time.Duration, 0, b.N)
				srv := newAttachmentUploadServer(b, nil, func(r *http.Request) {
					var first [1]byte
					if _, err := io.ReadFull(r.Body, first[:]); err != nil {
						b.Errorf("read first upload byte: %v", err)
						return
					}
					duration := time.Since(time.Unix(0, started.Load()))
					durationsMu.Lock()
					firstBodyDurations = append(firstBodyDurations, duration)
					durationsMu.Unlock()
					if _, err := io.Copy(io.Discard, r.Body); err != nil {
						b.Errorf("read upload: %v", err)
					}
				})
				defer srv.Close()
				c, _ := New(srv.URL)
				b.ReportAllocs()
				b.SetBytes(int64(sizeMiB) << 20)
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					iterationStarted := time.Now()
					started.Store(iterationStarted.UnixNano())
					if err := implementation.add(c, path); err != nil {
						b.Fatal(err)
					}
					totalDurations = append(totalDurations, time.Since(iterationStarted))
				}
				b.StopTimer()
				durationsMu.Lock()
				if len(firstBodyDurations) > 0 {
					b.ReportMetric(float64(medianDuration(firstBodyDurations).Nanoseconds()), "first-body-ns")
				}
				durationsMu.Unlock()
				if len(totalDurations) > 0 {
					b.ReportMetric(float64(medianDuration(totalDurations).Nanoseconds()), "total-median-ns")
				}
			})
		}
	}
}
func medianDuration(durations []time.Duration) time.Duration {
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	middle := len(durations) / 2
	if len(durations)%2 != 0 {
		return durations[middle]
	}
	return durations[middle-1] + (durations[middle]-durations[middle-1])/2
}

func benchmarkBufferedAddTaskAttachment(c *Client, path string) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("files", filepath.Base(path))
	if err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	if _, err = io.Copy(part, file); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	if _, err = c.ListTaskAttachments(context.Background(), "t-1", "p1"); err != nil {
		return err
	}
	_, err = c.doMultipartHTML(context.Background(), http.MethodPost, "/tasks/t-1/attachments"+query("project_id", "p1"), &body, writer.FormDataContentType())
	return err
}

type attachmentTestingT interface {
	Helper()
	Errorf(string, ...any)
}

func newAttachmentUploadServer(t attachmentTestingT, beforeGet, onPost func(*http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.Method {
		case http.MethodGet:
			if beforeGet != nil {
				beforeGet(r)
			}
			_, _ = w.Write([]byte(attachmentListMarkup("p1", "")))
		case http.MethodPost:
			if onPost != nil {
				onPost(r)
			}
			rows := attachmentRowMarkup("att-1", "p1", "fixture.bin", "64 KB") + attachmentRowMarkup("att-2", "p1", "same.txt", "4 B") + attachmentRowMarkup("att-3", "p1", "empty.txt", "0 B") + attachmentRowMarkup("att-4", "p1", "same.txt", "4 B")
			_, _ = w.Write([]byte(attachmentListMarkup("p1", rows)))
		default:
			t.Errorf("method = %s, want GET or POST", r.Method)
		}
	}))
}
