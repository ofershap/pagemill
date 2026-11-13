package pagemill

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/chromedp/chromedp"
)

// Options controls rendering and browser acquisition.
type Options struct {
	// BrowserPath uses an existing chrome-headless-shell or Chrome/Chromium
	// executable instead of downloading one. Useful in containers where you
	// bake the browser into the image.
	BrowserPath string
	// Version pins the chrome-headless-shell version to download
	// (e.g. "157.0.8103.78"). Empty resolves the current Stable channel.
	Version string
	// CacheDir holds downloaded browsers. Empty uses the user cache dir.
	CacheDir string
	// Timeout bounds the whole render, including any download.
	// Default 2 minutes.
	Timeout time.Duration
	// Paper size in inches. A4 by default (8.27 x 11.69).
	PaperWidth  float64
	PaperHeight float64
	// Margin in inches applied to all sides. Default 0.4.
	Margin float64
	// Landscape orientation.
	Landscape bool
	// Scale of the webpage rendering. Default 1.
	Scale float64
	// ExtraFlags passes additional command-line flags to the browser.
	ExtraFlags []string
}

func (o *Options) defaults() {
	if o.Timeout == 0 {
		o.Timeout = 2 * time.Minute
	}
	if o.PaperWidth == 0 {
		o.PaperWidth = 8.27
	}
	if o.PaperHeight == 0 {
		o.PaperHeight = 11.69
	}
	if o.Margin == 0 {
		o.Margin = 0.4
	}
	if o.Scale == 0 {
		o.Scale = 1
	}
}

// HTML renders an HTML document to a PDF and returns the bytes.
//
//	pdf, err := pagemill.HTML(invoiceHTML)
//
// On first use it downloads a chrome-headless-shell build (~40MB
// compressed) into the user cache; later renders reuse it. Each call runs
// a fresh browser instance, so calls are independent of each other.
// Browser startup dominates the cost of a single render; see the README
// for measured numbers and container usage.
func HTML(html string, opts ...func(*Options)) ([]byte, error) {
	o := Options{}
	for _, f := range opts {
		f(&o)
	}
	o.defaults()
	ctx, cancel := context.WithTimeout(context.Background(), o.Timeout)
	defer cancel()

	exe, err := ensureBrowser(ctx, &o)
	if err != nil {
		return nil, err
	}

	allocCtx, allocCancel := newAllocator(ctx, exe, &o)
	defer allocCancel()

	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	defer browserCancel()

	pdfOpts := []chromedp.PDFOption{
		chromedp.PDFPaper(chromedp.PaperSize{Width: o.PaperWidth, Height: o.PaperHeight}),
		chromedp.PDFMargin(o.Margin),
		chromedp.PDFPrintBackground(),
		chromedp.PDFScale(o.Scale),
	}
	if o.Landscape {
		pdfOpts = append(pdfOpts, chromedp.PDFLandscape())
	}

	if err := chromedp.Do(browserCtx, chromedp.Navigate(dataURL(html))); err != nil {
		return nil, fmt.Errorf("pagemill: load html: %w", err)
	}
	pdf, err := chromedp.Run(browserCtx, chromedp.PrintToPDF(pdfOpts...))
	if err != nil {
		return nil, fmt.Errorf("pagemill: render pdf: %w", err)
	}
	if len(pdf) == 0 {
		return nil, fmt.Errorf("pagemill: browser returned an empty pdf")
	}
	return pdf, nil
}

// PNG renders an HTML document to a full-page PNG screenshot and returns
// the bytes. The viewport is 1280x720; the capture extends beyond the
// viewport to fit the content.
func PNG(html string, opts ...func(*Options)) ([]byte, error) {
	o := Options{}
	for _, f := range opts {
		f(&o)
	}
	o.defaults()
	ctx, cancel := context.WithTimeout(context.Background(), o.Timeout)
	defer cancel()

	exe, err := ensureBrowser(ctx, &o)
	if err != nil {
		return nil, err
	}

	allocCtx, allocCancel := newAllocator(ctx, exe, &o)
	defer allocCancel()

	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	defer browserCancel()

	if err := chromedp.Do(browserCtx,
		chromedp.EmulateViewport(1280, 720),
		chromedp.Navigate(dataURL(html)),
	); err != nil {
		return nil, fmt.Errorf("pagemill: load html: %w", err)
	}
	png, err := chromedp.Run(browserCtx, chromedp.FullScreenshot(100))
	if err != nil {
		return nil, fmt.Errorf("pagemill: render png: %w", err)
	}
	if len(png) == 0 {
		return nil, fmt.Errorf("pagemill: browser returned an empty png")
	}
	return png, nil
}

// ensureBrowser returns the browser executable path: the user-supplied
// BrowserPath, or a downloaded chrome-headless-shell from the cache.
func ensureBrowser(ctx context.Context, o *Options) (string, error) {
	if o.BrowserPath != "" {
		if _, err := os.Stat(o.BrowserPath); err != nil {
			return "", fmt.Errorf("pagemill: browser path: %w", err)
		}
		return o.BrowserPath, nil
	}
	version := o.Version
	if version == "" {
		v, url, err := resolveVersion(ctx, "Stable")
		if err != nil {
			return "", err
		}
		return browserPath(ctx, o.CacheDir, v, url)
	}
	// Pinned version: construct the well-known download URL shape.
	platform := cftPlatform()
	if platform == "" {
		return "", fmt.Errorf("pagemill: unsupported platform")
	}
	url := fmt.Sprintf("https://storage.googleapis.com/chrome-for-testing-public/%s/%s/chrome-headless-shell-%s.zip", version, platform, platform)
	return browserPath(ctx, o.CacheDir, version, url)
}

// newAllocator starts the browser with container-safe defaults.
// --no-sandbox and --disable-dev-shm-usage are required in most
// Docker/CI environments; they are documented in the README, not hidden.
func newAllocator(ctx context.Context, exe string, o *Options) (context.Context, context.CancelFunc) {
	flags := []chromedp.ExecAllocatorOption{
		chromedp.ExecPath(exe),
		chromedp.Headless, // chrome-headless-shell is headless-only; this covers a full Chrome passed via BrowserPath
		chromedp.NoSandbox,
		chromedp.DisableGPU,
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("hide-scrollbars", true),
		chromedp.Flag("mute-audio", true),
		chromedp.UserDataDir(os.TempDir() + string(os.PathSeparator) + "pagemill-profile-" + randSuffix()),
	}
	for _, f := range o.ExtraFlags {
		flags = append(flags, chromedp.Flag(f, true))
	}
	return chromedp.NewExecAllocator(ctx, flags...)
}

func dataURL(html string) string {
	return "data:text/html;charset=utf-8," + url.PathEscape(html)
}
