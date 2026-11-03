package main

import (
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"soldproperty/internal/browser"
)

// Chrome Headers preset to mimic modern Chrome on macOS
const (
	defaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36"
	defaultSecChUa   = `"Chromium";v="128", "Not;A=Brand";v="24", "Google Chrome";v="128"`
	defaultTargetURL = "https://www.valuergeneral.nsw.gov.au/design/bulk_psi_content/bulk_psi"
)

type Config struct {
	TargetURL  string
	OutputDir  string
	Workers    int
	Cookie     string
	UserAgent  string
	Timeout    time.Duration
	Filter     string
	DryRun     bool
	Extract    bool
	Force      bool
	MaxRetries int
	UseBrowser bool
	Headless   bool
	Browser    string
}

type Downloader struct {
	config    Config
	client    *http.Client
	baseURL   *url.URL
	targetURL *url.URL
}

type ZipItem struct {
	URL      string
	Filename string
	Size     int64
}

func main() {
	var cfg Config
	var timeoutSec int

	flag.StringVar(&cfg.TargetURL, "url", defaultTargetURL, "Target webpage URL containing weekly NSW PSI zip links")
	flag.StringVar(&cfg.OutputDir, "out", "data/nsw_weekly", "Local directory to save downloaded zip files")
	flag.IntVar(&cfg.Workers, "workers", 3, "Number of concurrent download workers")
	flag.StringVar(&cfg.Cookie, "cookie", "", "Manual raw Cookie header (if provided, skips chromedp browser)")
	flag.StringVar(&cfg.UserAgent, "user-agent", defaultUserAgent, "Custom User-Agent string")
	flag.IntVar(&timeoutSec, "timeout", 60, "HTTP request and browser clearance timeout in seconds")
	flag.StringVar(&cfg.Filter, "filter", "", "Optional filter string for zip filenames (e.g. '2025', '2026', 'weekly')")
	flag.BoolVar(&cfg.DryRun, "dry-run", false, "Discover and print all zip URLs without downloading")
	flag.BoolVar(&cfg.Extract, "extract", false, "Automatically extract downloaded zip files after download")
	flag.BoolVar(&cfg.Force, "force", false, "Force re-download even if file already exists on disk")
	flag.IntVar(&cfg.MaxRetries, "retries", 3, "Maximum retry attempts per file on transient network failures")
	flag.BoolVar(&cfg.UseBrowser, "use-browser", true, "Use chromedp headless Chrome to automatically solve Cloudflare clearance")
	flag.BoolVar(&cfg.Headless, "headless", true, "Run chromedp in headless mode (set to false to see browser UI)")
	flag.StringVar(&cfg.Browser, "browser", "", "Path to Chrome/Chromium binary (default: chromedp autodetect)")
	flag.Parse()

	cfg.Timeout = time.Duration(timeoutSec) * time.Second

	fmt.Println("==================================================================")
	fmt.Println(" NSW Valuer General Bulk PSI Weekly Downloader (chromedp + Go)")
	fmt.Println("==================================================================")
	fmt.Printf(" Target URL:   %s\n", cfg.TargetURL)
	fmt.Printf(" Output Dir:   %s\n", cfg.OutputDir)
	fmt.Printf(" Concurrency:  %d workers\n", cfg.Workers)
	fmt.Printf(" Auto-Clear:   %v (Headless: %v)\n", cfg.UseBrowser && cfg.Cookie == "", cfg.Headless)
	if cfg.Filter != "" {
		fmt.Printf(" Filter:       '%s'\n", cfg.Filter)
	}
	if cfg.DryRun {
		fmt.Printf(" Mode:         DRY RUN (scan only)\n")
	}
	fmt.Println("------------------------------------------------------------------")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle graceful termination (Ctrl+C)
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\n[!] Received interrupt signal. Stopping gracefully...")
		cancel()
	}()

	var initialHTML string

	// Automatic Cloudflare Clearance via chromedp if no manual cookie is supplied
	if cfg.UseBrowser && cfg.Cookie == "" {
		clearanceRes, err := browser.FetchClearanceSession(ctx, cfg.TargetURL, cfg.Headless, cfg.Timeout, cfg.Browser)
		if err != nil {
			fmt.Printf("[!] chromedp clearance warning: %v\n", err)
			fmt.Println("[*] Falling back to direct HTTP requests...")
		} else {
			cfg.Cookie = clearanceRes.CookieHeader
			if clearanceRes.UserAgent != "" {
				cfg.UserAgent = clearanceRes.UserAgent
			}
			initialHTML = clearanceRes.RenderedHTML
		}
	}

	downloader, err := NewDownloader(cfg)
	if err != nil {
		log.Fatalf("Failed to initialize downloader: %v", err)
	}

	if err := downloader.Run(ctx, initialHTML); err != nil {
		log.Fatalf("Downloader encountered an error: %v", err)
	}
}

// NewDownloader sets up an HTTP client with CookieJar, TLS settings, and Chrome impersonation
func NewDownloader(cfg Config) (*Downloader, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("cookie jar error: %w", err)
	}

	parsedTarget, err := url.Parse(cfg.TargetURL)
	if err != nil {
		return nil, fmt.Errorf("invalid target URL: %w", err)
	}

	baseURL := &url.URL{
		Scheme: parsedTarget.Scheme,
		Host:   parsedTarget.Host,
	}

	// Custom Transport with TLS 1.2/1.3 and realistic cipher suite support
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
		DisableCompression: false,
		MaxIdleConns:       20,
		IdleConnTimeout:    30 * time.Second,
	}

	client := &http.Client{
		Jar:       jar,
		Transport: transport,
		Timeout:   cfg.Timeout,
	}

	return &Downloader{
		config:    cfg,
		client:    client,
		baseURL:   baseURL,
		targetURL: parsedTarget,
	}, nil
}

// setChromeHeaders injects authentic Google Chrome headers
func (d *Downloader) setChromeHeaders(req *http.Request, isDoc bool) {
	req.Header.Set("User-Agent", d.config.UserAgent)
	req.Header.Set("Sec-Ch-Ua", defaultSecChUa)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", `"macOS"`)
	req.Header.Set("Accept-Language", "en-AU,en-GB;q=0.9,en-US;q=0.8,en;q=0.7")
	// Only advertise gzip: the response decoder below handles gzip, and Go
	// does not auto-decompress when Accept-Encoding is set manually.
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("DNT", "1")
	req.Header.Set("Upgrade-Insecure-Requests", "1")

	if isDoc {
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7")
		req.Header.Set("Sec-Fetch-Dest", "document")
		req.Header.Set("Sec-Fetch-Mode", "navigate")
		req.Header.Set("Sec-Fetch-Site", "none")
		req.Header.Set("Sec-Fetch-User", "?1")
		req.Header.Set("Priority", "u=0, i")
	} else {
		req.Header.Set("Accept", "application/zip,application/octet-stream,*/*;q=0.8")
		req.Header.Set("Sec-Fetch-Dest", "document")
		req.Header.Set("Sec-Fetch-Mode", "navigate")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("Referer", d.config.TargetURL)
	}

	if d.config.Cookie != "" {
		req.Header.Set("Cookie", d.config.Cookie)
	}
}

// Run orchestrates the scraping of zip URLs and parallel download
func (d *Downloader) Run(ctx context.Context, initialHTML string) error {
	if err := os.MkdirAll(d.config.OutputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output dir: %w", err)
	}

	bodyContent := initialHTML
	if bodyContent == "" {
		fmt.Println("[*] Fetching page with Chrome headers:", d.config.TargetURL)
		var err error
		bodyContent, err = d.fetchPage(ctx, d.config.TargetURL)
		if err != nil {
			fmt.Printf("[!] Note: Page fetch returned: %v\n", err)
		}
	}

	zipLinks := d.extractZipLinks(bodyContent)
	if len(zipLinks) == 0 {
		fmt.Println("\n[!] Extracting standard NSW Valuer General weekly PSI URL structures...")
		zipLinks = d.generateKnownWeeklyURLs()
	}
	zipLinks = dedupeZipItemsByFilename(zipLinks)

	// Filter links if filter is specified
	if d.config.Filter != "" {
		var filtered []ZipItem
		filterLower := strings.ToLower(d.config.Filter)
		for _, item := range zipLinks {
			if strings.Contains(strings.ToLower(item.Filename), filterLower) || strings.Contains(strings.ToLower(item.URL), filterLower) {
				filtered = append(filtered, item)
			}
		}
		zipLinks = filtered
	}

	sort.Slice(zipLinks, func(i, j int) bool {
		return zipLinks[i].Filename < zipLinks[j].Filename
	})

	fmt.Printf("\n[+] Discovered %d zip file targets to process:\n", len(zipLinks))
	for i, item := range zipLinks {
		fmt.Printf("   [%2d] %s -> %s\n", i+1, item.Filename, item.URL)
	}

	if d.config.DryRun {
		fmt.Println("\n[✓] Dry run complete. No files were downloaded.")
		return nil
	}

	if len(zipLinks) == 0 {
		fmt.Println("\n[!] No zip files matched your criteria.")
		return nil
	}

	fmt.Printf("\n[*] Starting download queue (%d workers) into: %s\n\n", d.config.Workers, d.config.OutputDir)

	tasksChan := make(chan ZipItem, len(zipLinks))
	for _, item := range zipLinks {
		tasksChan <- item
	}
	close(tasksChan)

	var wg sync.WaitGroup
	var downloadedCount int64
	var skippedCount int64
	var errorCount int64
	var totalBytesDownloaded int64

	workers := d.config.Workers
	if workers < 1 {
		workers = 1
	}

	for i := 0; i < workers; i++ {
		workerID := i + 1
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case item, ok := <-tasksChan:
					if !ok {
						return
					}
					status, bytes, err := d.downloadFileWithRetry(ctx, workerID, item)
					if err != nil {
						atomic.AddInt64(&errorCount, 1)
						fmt.Printf("[Worker %d] [FAILED] %s: %v\n", workerID, item.Filename, err)
					} else {
						switch status {
						case "downloaded":
							atomic.AddInt64(&downloadedCount, 1)
							atomic.AddInt64(&totalBytesDownloaded, bytes)
							fmt.Printf("[Worker %d] [SAVED] %s (%.2f MB)\n", workerID, item.Filename, float64(bytes)/(1024*1024))
							if d.config.Extract {
								d.extractZip(filepath.Join(d.config.OutputDir, item.Filename))
							}
						case "skipped":
							atomic.AddInt64(&skippedCount, 1)
							fmt.Printf("[Worker %d] [SKIPPED] %s (already exists)\n", workerID, item.Filename)
						}
					}
				}
			}
		}()
	}

	wg.Wait()

	fmt.Println("\n==================================================================")
	fmt.Println(" Download Summary")
	fmt.Println("==================================================================")
	fmt.Printf(" Total Targets:        %d\n", len(zipLinks))
	fmt.Printf(" Files Downloaded:     %d\n", downloadedCount)
	fmt.Printf(" Files Skipped:        %d\n", skippedCount)
	fmt.Printf(" Failed / Unreachable: %d\n", errorCount)
	fmt.Printf(" Total Data Saved:     %.2f MB\n", float64(totalBytesDownloaded)/(1024*1024))
	fmt.Printf(" Storage Directory:    %s\n", d.config.OutputDir)
	fmt.Println("==================================================================")

	return nil
}

// fetchPage issues a GET request with Chrome document headers and reads the response body
func (d *Downloader) fetchPage(ctx context.Context, pageURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", pageURL, nil)
	if err != nil {
		return "", err
	}

	d.setChromeHeaders(req, true)

	resp, err := d.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var reader io.Reader = resp.Body
	switch strings.ToLower(resp.Header.Get("Content-Encoding")) {
	case "gzip":
		if gr, err := gzip.NewReader(resp.Body); err == nil {
			defer gr.Close()
			reader = gr
		}
	}

	bodyBytes, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return string(bodyBytes), fmt.Errorf("HTTP %d %s", resp.StatusCode, resp.Status)
	}

	return string(bodyBytes), nil
}

// extractZipLinks parses HTML content for .zip hyperlinks
func (d *Downloader) extractZipLinks(htmlContent string) []ZipItem {
	var items []ZipItem
	seen := make(map[string]bool)

	re := regexp.MustCompile(`(?i)href\s*=\s*["']([^"']+\.zip(?:\?[^"']*)?)["']`)
	matches := re.FindAllStringSubmatch(htmlContent, -1)

	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		rawHref := strings.TrimSpace(m[1])
		if rawHref == "" {
			continue
		}

		fullURL := d.resolveURL(rawHref)
		if seen[fullURL] {
			continue
		}
		seen[fullURL] = true

		filename := d.extractFilename(fullURL)
		items = append(items, ZipItem{
			URL:      fullURL,
			Filename: filename,
		})
	}

	return items
}

// generateKnownWeeklyURLs provides standard NSW Valuer General weekly PSI URL structures
func (d *Downloader) generateKnownWeeklyURLs() []ZipItem {
	var items []ZipItem
	seen := make(map[string]bool)

	currentYear := time.Now().Year()
	years := []int{currentYear, currentYear - 1}

	for _, year := range years {
		startDate := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
		for startDate.Weekday() != time.Monday {
			startDate = startDate.AddDate(0, 0, 1)
		}

		for w := 0; w < 52; w++ {
			weekDate := startDate.AddDate(0, 0, w*7)
			if weekDate.After(time.Now()) {
				break
			}

			dateStr := weekDate.Format("20060102")
			filename := fmt.Sprintf("%s.zip", dateStr)

			urlPatterns := []string{
				fmt.Sprintf("https://www.valuergeneral.nsw.gov.au/design/bulk_psi_content/%d/%s.zip", year, dateStr),
				fmt.Sprintf("https://www.valuergeneral.nsw.gov.au/services/property_sales/%d/%s.zip", year, dateStr),
			}

			for _, u := range urlPatterns {
				if !seen[u] {
					seen[u] = true
					items = append(items, ZipItem{
						URL:      u,
						Filename: filename,
					})
				}
			}
		}
	}

	return items
}

// resolveURL creates an absolute URL from a relative or absolute href
func (d *Downloader) resolveURL(href string) string {
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return href
	}

	parsedHref, err := url.Parse(href)
	if err != nil {
		return href
	}

	return d.baseURL.ResolveReference(parsedHref).String()
}

// extractFilename extracts a clean filename from a URL
func (d *Downloader) extractFilename(fileURL string) string {
	parsed, err := url.Parse(fileURL)
	if err != nil {
		return "weekly_data.zip"
	}

	base := filepath.Base(parsed.Path)
	base = strings.Split(base, "?")[0]
	if !strings.HasSuffix(strings.ToLower(base), ".zip") {
		base += ".zip"
	}
	return base
}

// downloadFileWithRetry downloads a zip file with exponential backoff retry logic
func (d *Downloader) downloadFileWithRetry(ctx context.Context, workerID int, item ZipItem) (string, int64, error) {
	var lastErr error
	destPath := filepath.Join(d.config.OutputDir, item.Filename)
	// A unique temporary path per worker prevents two workers (or a forced
	// duplicate target) from writing the same file concurrently.
	tmpPath := fmt.Sprintf("%s.%d.tmp", destPath, workerID)
	defer os.Remove(tmpPath)

	if !d.config.Force {
		if info, err := os.Stat(destPath); err == nil && info.Size() > 0 {
			return "skipped", info.Size(), nil
		}
	}

	for attempt := 1; attempt <= d.config.MaxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return "error", 0, ctx.Err()
		default:
		}

		written, err := d.downloadFile(ctx, item.URL, tmpPath)
		if err == nil {
			if validationErr := validateDownloadedZip(tmpPath); validationErr != nil {
				lastErr = fmt.Errorf("zip validation: %w", validationErr)
			} else if err := os.Rename(tmpPath, destPath); err != nil {
				return "error", 0, fmt.Errorf("rename tmp file error: %w", err)
			} else {
				return "downloaded", written, nil
			}
		} else {
			lastErr = err
		}

		if attempt < d.config.MaxRetries {
			backoff := time.Duration(attempt*attempt) * 500 * time.Millisecond
			time.Sleep(backoff)
		}
	}

	return "error", 0, lastErr
}

// validateDownloadedZip verifies that the completed temporary file is a readable
// zip archive before it replaces any existing file.
func validateDownloadedZip(path string) error {
	r, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	return r.Close()
}

// dedupeZipItemsByFilename keeps the first URL for each output filename. The
// fallback weekly URL generator can emit multiple mirrors with the same name.
func dedupeZipItemsByFilename(items []ZipItem) []ZipItem {
	seen := make(map[string]bool, len(items))
	out := make([]ZipItem, 0, len(items))
	for _, item := range items {
		name := item.Filename
		if name == "" {
			name = filepath.Base(item.URL)
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, item)
	}
	return out
}

// downloadFile streams the remote file to local disk
func (d *Downloader) downloadFile(ctx context.Context, fileURL string, tmpPath string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", fileURL, nil)
	if err != nil {
		return 0, err
	}

	d.setChromeHeaders(req, false)

	resp, err := d.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return 0, fmt.Errorf("HTTP 404 Not Found")
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d %s", resp.StatusCode, resp.Status)
	}

	out, err := os.Create(tmpPath)
	if err != nil {
		return 0, fmt.Errorf("cannot create file %s: %w", tmpPath, err)
	}
	defer out.Close()

	var reader io.Reader = resp.Body
	switch strings.ToLower(resp.Header.Get("Content-Encoding")) {
	case "gzip":
		if gr, err := gzip.NewReader(resp.Body); err == nil {
			defer gr.Close()
			reader = gr
		}
	}

	written, err := io.Copy(out, reader)
	if err != nil {
		return 0, fmt.Errorf("stream copy error: %w", err)
	}

	return written, nil
}

// extractZip unzips the contents of the archive into an extracted subfolder
func (d *Downloader) extractZip(zipFilePath string) {
	r, err := zip.OpenReader(zipFilePath)
	if err != nil {
		log.Printf("[!] Could not unzip %s: %v", zipFilePath, err)
		return
	}
	defer r.Close()

	destDir := strings.TrimSuffix(zipFilePath, ".zip")
	if err := os.MkdirAll(destDir, 0755); err != nil {
		log.Printf("[!] Could not create extract directory %s: %v", destDir, err)
		return
	}

	for _, f := range r.File {
		fpath := filepath.Join(destDir, f.Name)
		if !strings.HasPrefix(fpath, filepath.Clean(destDir)+string(os.PathSeparator)) {
			continue
		}

		if f.FileInfo().IsDir() {
			os.MkdirAll(fpath, os.ModePerm)
			continue
		}

		if err := os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
			continue
		}

		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			outFile.Close()
			continue
		}

		_, _ = io.Copy(outFile, rc)
		outFile.Close()
		rc.Close()
	}
}
