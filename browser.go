package pagemill

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// chrome-headless-shell is the headless-only Chrome for Testing build:
// much smaller than full Chrome and exactly what rendering needs.
const cftEndpoint = "https://googlechromelabs.github.io/chrome-for-testing/last-known-good-versions-with-downloads.json"

type cftResponse struct {
	Channels map[string]struct {
		Version   string `json:"version"`
		Downloads map[string][]struct {
			Platform string `json:"platform"`
			URL      string `json:"url"`
		} `json:"downloads"`
	} `json:"channels"`
}

// resolveVersion returns the current stable chrome-headless-shell version
// and its download URL for the current platform.
func resolveVersion(ctx context.Context, channel string) (version, url string, err error) {
	if channel == "" {
		channel = "Stable"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cftEndpoint, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("pagemill: fetch chrome-for-testing versions: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("pagemill: chrome-for-testing endpoint returned %s", resp.Status)
	}
	var data cftResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", "", fmt.Errorf("pagemill: parse versions: %w", err)
	}
	ch, ok := data.Channels[channel]
	if !ok {
		return "", "", fmt.Errorf("pagemill: unknown channel %q", channel)
	}
	platform := cftPlatform()
	if platform == "" {
		return "", "", fmt.Errorf("pagemill: unsupported platform %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	for _, d := range ch.Downloads["chrome-headless-shell"] {
		if d.Platform == platform {
			return ch.Version, d.URL, nil
		}
	}
	return "", "", fmt.Errorf("pagemill: no chrome-headless-shell download for %s", platform)
}

func cftPlatform() string {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64":
		return "linux64"
	case "linux/arm64":
		return "linux-arm64"
	case "darwin/amd64":
		return "mac-x64"
	case "darwin/arm64":
		return "mac-arm64"
	case "windows/amd64":
		return "win64"
	case "windows/386":
		return "win32"
	}
	return ""
}

// browserPath returns the cached executable for version, downloading and
// extracting it into cacheDir if missing. Cache layout:
//
//	<cacheDir>/chrome-headless-shell-<version>-<platform>/chrome-headless-shell
func browserPath(ctx context.Context, cacheDir, version, url string) (string, error) {
	if cacheDir == "" {
		d, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("pagemill: no cache dir: %w", err)
		}
		cacheDir = filepath.Join(d, "pagemill")
	}
	dir := filepath.Join(cacheDir, "chrome-headless-shell-"+version+"-"+cftPlatform())
	exe := filepath.Join(dir, exeName())
	if _, err := os.Stat(exe); err == nil {
		return exe, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	zipPath := filepath.Join(dir, "download.zip")
	if err := download(ctx, url, zipPath); err != nil {
		return "", err
	}
	defer os.Remove(zipPath)
	if err := extractHeadlessShell(zipPath, dir); err != nil {
		return "", err
	}
	if _, err := os.Stat(exe); err != nil {
		return "", fmt.Errorf("pagemill: extracted archive missing %s", exeName())
	}
	return exe, nil
}

func exeName() string {
	if runtime.GOOS == "windows" {
		return "chrome-headless-shell.exe"
	}
	return "chrome-headless-shell"
}

func download(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("pagemill: download browser: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pagemill: browser download returned %s", resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

// extractHeadlessShell unpacks the whole archive into dir, stripping the
// single platform-prefixed top-level directory (e.g.
// "chrome-headless-shell-linux64/"). All shipped files are needed: the
// binary, the .pak resources (the UA stylesheet lives in
// headless_lib_data.pak), icu data, locales and hyphenation data.
func extractHeadlessShell(zipPath, dir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("pagemill: open browser archive: %w", err)
	}
	defer r.Close()
	for _, f := range r.File {
		name := f.Name
		// Strip the top-level platform directory.
		if i := strings.Index(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		if name == "" {
			continue
		}
		clean := filepath.Clean(name)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return errors.New("pagemill: unsafe path in archive")
		}
		out := filepath.Join(dir, clean)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(out, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		mode := f.Mode()
		if mode == 0 {
			mode = 0o644
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		w, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			rc.Close()
			return err
		}
		_, copyErr := io.Copy(w, rc)
		closeErr := w.Close()
		rc.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	// Zip entries may not carry the executable bit.
	if err := os.Chmod(filepath.Join(dir, exeName()), 0o755); err != nil {
		return err
	}
	return nil
}
