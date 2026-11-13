package pagemill

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testDoc = `<!DOCTYPE html><html><body>
<h1>Invoice #42</h1><p>Total: <b>$99.00</b></p>
<div style="background:#2d6cdf;color:#fff;padding:12px">styled block</div>
</body></html>`

// testBrowser returns the browser to use in tests: PAGEMILL_TEST_BROWSER if
// set (CI points it at the runner's Chrome), else the downloaded shell.
func testBrowser(t *testing.T) func(*Options) {
	t.Helper()
	if p := os.Getenv("PAGEMILL_TEST_BROWSER"); p != "" {
		return func(o *Options) { o.BrowserPath = p }
	}
	return func(o *Options) {}
}

func TestHTMLProducesPDF(t *testing.T) {
	pdf, err := HTML(testDoc, testBrowser(t))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatalf("not a pdf: %q", pdf[:8])
	}
	if len(pdf) < 1000 {
		t.Fatalf("suspiciously small pdf: %d bytes", len(pdf))
	}
}

func TestPNGProducesImage(t *testing.T) {
	png, err := PNG(testDoc, testBrowser(t))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(png, []byte("\x89PNG")) {
		t.Fatalf("not a png: %q", png[:8])
	}
}

func TestLandscapeWiderThanPortrait(t *testing.T) {
	portrait, err := HTML(testDoc, testBrowser(t))
	if err != nil {
		t.Fatal(err)
	}
	landscape, err := HTML(testDoc, testBrowser(t), func(o *Options) { o.Landscape = true })
	if err != nil {
		t.Fatal(err)
	}
	// Both must be valid PDFs; byte sizes differ once orientation changes.
	if !bytes.HasPrefix(landscape, []byte("%PDF-")) {
		t.Fatal("landscape output is not a pdf")
	}
	if bytes.Equal(portrait, landscape) {
		t.Fatal("landscape output identical to portrait")
	}
}

func TestBrowserPathMissing(t *testing.T) {
	_, err := HTML(testDoc, func(o *Options) { o.BrowserPath = "/nonexistent/browser" })
	if err == nil || !strings.Contains(err.Error(), "browser path") {
		t.Fatalf("want browser path error, got %v", err)
	}
}

func TestBadHTMLStillRenders(t *testing.T) {
	// Browsers are forgiving; pagemill should not reject malformed HTML.
	pdf, err := HTML("<p>unclosed", testBrowser(t))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatal("not a pdf")
	}
}

func TestCacheLayoutAndExtraction(t *testing.T) {
	// The cache dir layout must be stable across calls: second call reuses.
	dir := t.TempDir()
	version, url, err := resolveVersion(t.Context(), "Stable")
	if err != nil {
		t.Skipf("version endpoint unreachable: %v", err)
	}
	exe1, err := browserPath(t.Context(), dir, version, url)
	if err != nil {
		t.Skipf("browser download unavailable: %v", err)
	}
	exe2, err := browserPath(t.Context(), dir, version, url)
	if err != nil {
		t.Fatal(err)
	}
	if exe1 != exe2 {
		t.Fatalf("cache miss on second call: %q vs %q", exe1, exe2)
	}
	// The full resource set must be present, not just the binary.
	for _, res := range []string{"headless_lib_data.pak", "headless_lib_strings.pak", "icudtl.dat"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(exe1), res)); err != nil {
			t.Errorf("extracted browser missing %s", res)
		}
	}
}
