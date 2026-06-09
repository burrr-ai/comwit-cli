package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const upgradeRepo = "burrr-ai/comwit-cli"

// upgrade self-updates the running binary to the latest GitHub release.
func upgrade(_ []string, stdout io.Writer) error {
	latest, err := latestReleaseTag(upgradeRepo)
	if err != nil {
		return fmt.Errorf("check latest release: %w", err)
	}
	if strings.TrimPrefix(version, "v") == strings.TrimPrefix(latest, "v") {
		fmt.Fprintf(stdout, "comwit %s is already up to date\n", version)
		return nil
	}

	asset := fmt.Sprintf("comwit_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	base := fmt.Sprintf("https://github.com/%s/releases/download/%s", upgradeRepo, latest)
	fmt.Fprintf(stdout, "upgrading comwit %s -> %s ...\n", version, latest)

	archive, err := httpGetBytes(base + "/" + asset)
	if err != nil {
		return fmt.Errorf("download %s: %w", asset, err)
	}
	sums, err := httpGetBytes(base + "/checksums.txt")
	if err != nil {
		return fmt.Errorf("download checksums: %w", err)
	}
	want := checksumFor(string(sums), asset)
	if want == "" {
		return fmt.Errorf("no checksum for %s in release %s", asset, latest)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(archive)); got != want {
		return fmt.Errorf("checksum mismatch for %s", asset)
	}

	bin, err := extractTarGzFile(archive, "comwit")
	if err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	// Write the new binary next to the current one, then atomically rename over
	// it (allowed on unix even while running).
	tmp, err := os.CreateTemp(dir, ".comwit-upgrade-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s (try sudo, or re-run the install script): %w", dir, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	tmp.Close()
	if err := os.Chmod(tmpName, 0o755); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, exe); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("replace %s (try sudo, or re-run the install script): %w", exe, err)
	}

	fmt.Fprintf(stdout, "upgraded to comwit %s\n", strings.TrimPrefix(latest, "v"))
	return nil
}

func latestReleaseTag(repo string) (string, error) {
	body, err := httpGetBytes(fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo))
	if err != nil {
		return "", err
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &rel); err != nil {
		return "", err
	}
	if rel.TagName == "" {
		return "", fmt.Errorf("no tag_name in latest release")
	}
	return rel.TagName, nil
}

func httpGetBytes(url string) ([]byte, error) {
	c := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "comwit-cli")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}
	return io.ReadAll(resp.Body)
}

func checksumFor(sums, asset string) string {
	for _, line := range strings.Split(sums, "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == asset {
			return f[0]
		}
	}
	return ""
}

func extractTarGzFile(data []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("%s not found in archive", name)
		}
		if err != nil {
			return nil, err
		}
		if filepath.Base(hdr.Name) == name {
			return io.ReadAll(tr)
		}
	}
}
