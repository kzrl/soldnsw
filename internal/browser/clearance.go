package browser

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// ClearanceResult holds the extracted browser session credentials and rendered content
type ClearanceResult struct {
	CookieHeader string
	UserAgent    string
	RenderedHTML string
	Title        string
}

// FetchClearanceSession launches Chrome via chromedp, solves/passes Cloudflare, and extracts cookies & HTML
func FetchClearanceSession(ctx context.Context, targetURL string, headless bool, timeout time.Duration) (*ClearanceResult, error) {
	fmt.Printf("[*] Launching Chrome via chromedp (headless: %v) to acquire Cloudflare clearance...\n", headless)

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", headless),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("disable-infobars", true),
		chromedp.Flag("window-size", "1280,800"),
		chromedp.UserAgent("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36"),
	)

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelAlloc()

	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx, chromedp.WithLogf(log.Printf))
	defer cancelBrowser()

	timeoutCtx, cancelTimeout := context.WithTimeout(browserCtx, timeout)
	defer cancelTimeout()

	var renderedHTML string
	var pageTitle string
	var detectedUA string

	fmt.Printf("[*] Navigating to %s ...\n", targetURL)

	// Stealth injection to hide navigator.webdriver
	stealthScript := `
		Object.defineProperty(navigator, 'webdriver', { get: () => undefined });
		window.chrome = { runtime: {} };
	`

	err := chromedp.Run(timeoutCtx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			return network.Enable().Do(ctx)
		}),
		chromedp.Navigate(targetURL),
		chromedp.Evaluate(stealthScript, nil),
		chromedp.Evaluate(`navigator.userAgent`, &detectedUA),
	)
	if err != nil {
		return nil, fmt.Errorf("initial navigation error: %w", err)
	}

	fmt.Println("[*] Waiting for Cloudflare verification to resolve...")

	// Poll until title changes from "Just a moment..." and content loads
	resolved := false
	startTime := time.Now()

	for time.Since(startTime) < timeout {
		var title string
		var html string

		_ = chromedp.Run(timeoutCtx,
			chromedp.Title(&title),
			chromedp.OuterHTML("html", &html),
		)

		pageTitle = title
		renderedHTML = html

		// Check if we are past the challenge page
		if !strings.Contains(strings.ToLower(title), "just a moment") &&
			!strings.Contains(strings.ToLower(title), "attention required") &&
			len(html) > 500 {
			resolved = true
			fmt.Printf("[+] Cloudflare challenge successfully passed! Page title: '%s'\n", title)
			break
		}

		time.Sleep(1 * time.Second)
	}

	if !resolved {
		fmt.Printf("[!] Note: Page title is still '%s' after timeout. Extracting current session state anyway.\n", pageTitle)
	}

	// Extract all cookies from the browser session
	var cookieHeader strings.Builder
	var cfClearanceFound bool

	err = chromedp.Run(timeoutCtx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			cookies, err := network.GetCookies().Do(ctx)
			if err != nil {
				return err
			}

			for i, c := range cookies {
				if i > 0 {
					cookieHeader.WriteString("; ")
				}
				cookieHeader.WriteString(fmt.Sprintf("%s=%s", c.Name, c.Value))
				if c.Name == "cf_clearance" {
					cfClearanceFound = true
				}
			}
			return nil
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("cookie extraction error: %w", err)
	}

	resCookie := cookieHeader.String()
	if cfClearanceFound {
		fmt.Println("[+] Successfully captured 'cf_clearance' cookie!")
	} else if len(resCookie) > 0 {
		fmt.Printf("[+] Captured %d session cookies.\n", len(strings.Split(resCookie, ";")))
	}

	return &ClearanceResult{
		CookieHeader: resCookie,
		UserAgent:    detectedUA,
		RenderedHTML: renderedHTML,
		Title:        pageTitle,
	}, nil
}
