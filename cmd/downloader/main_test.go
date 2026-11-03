package main

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestDedupeZipItemsByFilename(t *testing.T) {
	items := []ZipItem{
		{URL: "https://example.test/a/20260101.zip", Filename: "20260101.zip"},
		{URL: "https://example.test/b/20260101.zip", Filename: "20260101.zip"},
		{URL: "https://example.test/c/20260108.zip", Filename: "20260108.zip"},
	}
	got := dedupeZipItemsByFilename(items)
	if len(got) != 2 {
		t.Fatalf("deduped items = %d, want 2", len(got))
	}
	if got[0].URL != items[0].URL || got[1].URL != items[2].URL {
		t.Errorf("unexpected dedupe output: %+v", got)
	}
}

func TestConcurrentForceDownloadsUseUniqueTempFiles(t *testing.T) {
	payload := buildTestZip(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	outDir := t.TempDir()
	d, err := NewDownloader(Config{
		TargetURL:  server.URL,
		OutputDir:  outDir,
		Workers:    2,
		Force:      true,
		MaxRetries: 1,
		Timeout:    5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewDownloader: %v", err)
	}

	item := ZipItem{URL: server.URL + "/20260101.zip", Filename: "20260101.zip"}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for workerID := 1; workerID <= 2; workerID++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			status, _, err := d.downloadFileWithRetry(context.Background(), id, item)
			if err != nil {
				errs <- err
				return
			}
			if status != "downloaded" {
				errs <- &unexpectedStatus{status: status}
			}
		}(workerID)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("download error: %v", err)
	}

	if err := validateDownloadedZip(filepath.Join(outDir, item.Filename)); err != nil {
		t.Errorf("downloaded file is not a valid zip: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(outDir, "*.tmp"))
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("temporary files left behind: %v", matches)
	}
}

func TestInvalidZipIsNotCommitted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not a zip"))
	}))
	defer server.Close()

	outDir := t.TempDir()
	d, err := NewDownloader(Config{
		TargetURL:  server.URL,
		OutputDir:  outDir,
		Workers:    1,
		MaxRetries: 1,
		Timeout:    5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewDownloader: %v", err)
	}

	item := ZipItem{URL: server.URL + "/bad.zip", Filename: "bad.zip"}
	if _, _, err := d.downloadFileWithRetry(context.Background(), 1, item); err == nil {
		t.Fatalf("expected invalid zip to fail")
	}
	if _, err := os.Stat(filepath.Join(outDir, item.Filename)); err == nil {
		t.Errorf("invalid zip was committed to its destination")
	}
	matches, _ := filepath.Glob(filepath.Join(outDir, "*.tmp"))
	if len(matches) != 0 {
		t.Errorf("temporary files left behind: %v", matches)
	}
}

type unexpectedStatus struct{ status string }

func (e *unexpectedStatus) Error() string { return "unexpected download status: " + e.status }

func buildTestZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("sample.dat")
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := w.Write([]byte("B;003;123;1;1;0;0;14;BELLBOWER CL;GREEN POINT;2251\n")); err != nil {
		t.Fatalf("write zip entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}
