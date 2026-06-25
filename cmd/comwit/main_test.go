package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	var stdout bytes.Buffer
	if err := run([]string{"version"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); !strings.Contains(got, "comwit") {
		t.Fatalf("stdout = %q", got)
	}
}

func TestLoginWritesConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COMWIT_CONFIG", filepath.Join(dir, "config.json"))

	var stdout bytes.Buffer
	if err := run([]string{"login", "--token", "test-token", "--project", "proj_1"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `"token": "test-token"`) {
		t.Fatalf("config missing token: %s", text)
	}
	if !strings.Contains(text, `"default_project": "proj_1"`) {
		t.Fatalf("config missing project: %s", text)
	}
}

func TestSelectProjectPrefersFlagThenEnvThenConfig(t *testing.T) {
	cfg := configFile{DefaultProject: "from-config"}
	if got := selectProject("from-flag", cfg); got != "from-flag" {
		t.Fatalf("flag project = %q", got)
	}
	t.Setenv("COMWIT_PROJECT", "from-env")
	if got := selectProject("", cfg); got != "from-env" {
		t.Fatalf("env project = %q", got)
	}
	t.Setenv("COMWIT_PROJECT", "")
	if got := selectProject("", cfg); got != "from-config" {
		t.Fatalf("config project = %q", got)
	}
}

func TestDatabasesCreateRequiresProject(t *testing.T) {
	t.Setenv("COMWIT_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("COMWIT_PROJECT", "")
	var stderr bytes.Buffer
	err := run([]string{"databases", "create", "--name", "db"}, &bytes.Buffer{}, &stderr)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "--project is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestAppsBuildsRequiresApp(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COMWIT_CONFIG", filepath.Join(dir, "config.json"))
	if _, err := saveConfig(configFile{Token: "test-token", DefaultProject: "proj_1"}); err != nil {
		t.Fatal(err)
	}

	err := run([]string{"apps", "builds"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "--app is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestDomainsAddRequiresProject(t *testing.T) {
	t.Setenv("COMWIT_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("COMWIT_PROJECT", "")
	err := run([]string{"domains", "add", "--domain", "example.com"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "--project is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestDomainsCheckRequiresDomain(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COMWIT_CONFIG", filepath.Join(dir, "config.json"))
	if _, err := saveConfig(configFile{Token: "test-token", DefaultProject: "proj_1"}); err != nil {
		t.Fatal(err)
	}

	err := run([]string{"domains", "check"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "--domain is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestDomainsRecordsCreateRequiresFields(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COMWIT_CONFIG", filepath.Join(dir, "config.json"))
	if _, err := saveConfig(configFile{Token: "test-token", DefaultProject: "proj_1"}); err != nil {
		t.Fatal(err)
	}

	err := run([]string{"domains", "records", "create", "--domain", "example.com", "--name", "www", "--type", "CNAME"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "--value is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestDNSRecordUpdatePayloadMergesExistingRecord(t *testing.T) {
	payload, err := dnsRecordUpdatePayload(dnsRecordView{
		ID:     "dnsrec_1",
		Name:   "www.example.com",
		Type:   "CNAME",
		TTL:    300,
		Values: []string{"old.example.net"},
	}, "", "", 0, false, []string{"target2.example.net"})
	if err != nil {
		t.Fatal(err)
	}
	if payload["name"] != "www.example.com" || payload["type"] != "CNAME" || payload["ttl"] != int64(300) {
		t.Fatalf("payload = %+v", payload)
	}
	values, ok := payload["values"].([]string)
	if !ok || len(values) != 1 || values[0] != "target2.example.net" {
		t.Fatalf("values = %#v", payload["values"])
	}
}

func TestDomainCommandPaths(t *testing.T) {
	if got, want := projectDomainsPath("proj 1"), "/v1/projects/proj%201/domains"; got != want {
		t.Fatalf("projectDomainsPath() = %q, want %q", got, want)
	}
	if got, want := projectDomainPath("proj_1", "example.com"), "/v1/projects/proj_1/domains/example.com"; got != want {
		t.Fatalf("projectDomainPath() = %q, want %q", got, want)
	}
	if got, want := projectDomainRecordsPath("proj_1", "example.com"), "/v1/projects/proj_1/domains/example.com/records"; got != want {
		t.Fatalf("projectDomainRecordsPath() = %q, want %q", got, want)
	}
	if got, want := projectDomainRecordPath("proj_1", "example.com", "dnsrec_1"), "/v1/projects/proj_1/domains/example.com/records/dnsrec_1"; got != want {
		t.Fatalf("projectDomainRecordPath() = %q, want %q", got, want)
	}
}

func TestAppsEnvSetCallsProjectAppEnvironmentAPI(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COMWIT_CONFIG", filepath.Join(dir, "config.json"))
	if _, err := saveConfig(configFile{Token: "test-token", DefaultProject: "proj_1"}); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/v1/projects/proj_1/apps/svc_1/environment/DATABASE_URL" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("auth = %q", got)
		}
		var body struct {
			Value  string `json:"value"`
			Secret bool   `json:"secret"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Value != "libsql://db" || body.Secret {
			t.Fatalf("body = %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"key":"DATABASE_URL","secret":false}`))
	}))
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	var stdout bytes.Buffer
	if err := run([]string{"apps", "env", "set", "--app", "svc_1", "DATABASE_URL", "libsql://db"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "DATABASE_URL") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestAppsLogsJSONCallsProjectAppLogsAPI(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COMWIT_CONFIG", filepath.Join(dir, "config.json"))
	if _, err := saveConfig(configFile{Token: "test-token", DefaultProject: "proj_1"}); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/projects/proj_1/apps/svc_1/logs" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("tail"); got != "5" {
			t.Fatalf("tail = %q", got)
		}
		if got := r.URL.Query().Get("build_id"); got != "bld_1" {
			t.Fatalf("build_id = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"logs":[{"timestamp":"2026-06-19T00:00:00Z","level":"error","message":"boom","app_id":"svc_1","build_id":"bld_1"}]}`))
	}))
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	var stdout bytes.Buffer
	if err := run([]string{"apps", "logs", "--app", "svc_1", "--tail", "5", "--build", "bld_1", "--json"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"message":"boom"`) {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestDatabaseDeleteAndTokenRotateUseProjectScopedAPI(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COMWIT_CONFIG", filepath.Join(dir, "config.json"))
	if _, err := saveConfig(configFile{Token: "test-token", DefaultProject: "proj_1"}); err != nil {
		t.Fatal(err)
	}

	var sawDelete bool
	var sawRotate bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/projects/proj_1/databases/db_1":
			sawDelete = true
			_, _ = w.Write([]byte(`{"ok":true,"database":{"database_id":"db_1","database_url":"https://db.example.test/v1/db_1","status":"deleted"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/projects/proj_1/databases/db_1/token/rotate":
			sawRotate = true
			_, _ = w.Write([]byte(`{"database_id":"db_1","database_url":"https://db.example.test/v1/db_1","database_token":"new-token","status":"cold"}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	var deleteOut bytes.Buffer
	if err := run([]string{"databases", "delete", "--database", "db_1"}, &deleteOut, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var rotateOut bytes.Buffer
	if err := run([]string{"databases", "token", "rotate", "--database", "db_1"}, &rotateOut, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !sawDelete || !sawRotate {
		t.Fatalf("sawDelete=%t sawRotate=%t", sawDelete, sawRotate)
	}
	if !strings.Contains(rotateOut.String(), "new-token") {
		t.Fatalf("rotate stdout = %q", rotateOut.String())
	}
}

func TestUpdateInstallsLatestReleaseAsset(t *testing.T) {
	target := filepath.Join(t.TempDir(), "comwit")
	if err := os.WriteFile(target, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	asset := makeUpdateTarball(t, "comwit", []byte("new-binary"))

	var sawRelease bool
	var sawDownload bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/burrr-ai/comwit-cloud/releases/latest":
			sawRelease = true
			if got := r.Header.Get("Authorization"); got != "Bearer ghs_test" {
				t.Fatalf("auth = %q", got)
			}
			if got := r.Header.Get("User-Agent"); !strings.Contains(got, "comwit-cli/") {
				t.Fatalf("user-agent = %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tag_name":"v1.2.3","assets":[{"url":"` + serverURL(r) + `/asset-api","name":"comwit_darwin_arm64.tar.gz","browser_download_url":"` + serverURL(r) + `/download"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/asset-api":
			sawDownload = true
			if got := r.Header.Get("Authorization"); got != "Bearer ghs_test" {
				t.Fatalf("download auth = %q", got)
			}
			if got := r.Header.Get("Accept"); got != "application/octet-stream" {
				t.Fatalf("download accept = %q", got)
			}
			w.Header().Set("Content-Type", "application/gzip")
			_, _ = w.Write(asset)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := performUpdate(updateOptions{
		Repo:         defaultGitHubRepo,
		Version:      "latest",
		TargetPath:   target,
		GOOS:         "darwin",
		GOARCH:       "arm64",
		GitHubAPIURL: server.URL,
		GitHubToken:  "ghs_test",
		HTTPClient:   server.Client(),
	}, &stdout)
	if err != nil {
		t.Fatal(err)
	}
	if !sawRelease || !sawDownload {
		t.Fatalf("sawRelease=%t sawDownload=%t", sawRelease, sawDownload)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new-binary" {
		t.Fatalf("target contents = %q", string(data))
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("target mode = %v, want executable bit", info.Mode().Perm())
	}
	if !strings.Contains(stdout.String(), "updated comwit to v1.2.3") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestUpdateReportsMissingPlatformAsset(t *testing.T) {
	target := filepath.Join(t.TempDir(), "comwit")
	if err := os.WriteFile(target, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/repos/burrr-ai/comwit-cloud/releases/tags/v1.2.3" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v1.2.3","assets":[{"name":"comwit_linux_amd64.tar.gz","browser_download_url":"` + serverURL(r) + `/download"}]}`))
	}))
	defer server.Close()

	err := performUpdate(updateOptions{
		Repo:         defaultGitHubRepo,
		Version:      "v1.2.3",
		TargetPath:   target,
		GOOS:         "darwin",
		GOARCH:       "arm64",
		GitHubAPIURL: server.URL,
		HTTPClient:   server.Client(),
	}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "comwit_darwin_arm64.tar.gz") {
		t.Fatalf("error = %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old-binary" {
		t.Fatalf("target contents = %q", string(data))
	}
}

func TestUpdateAssetAndReleasePath(t *testing.T) {
	got, err := updateAssetName("linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if want := "comwit_linux_amd64.tar.gz"; got != want {
		t.Fatalf("updateAssetName() = %q, want %q", got, want)
	}
	if _, err := updateAssetName("freebsd", "amd64"); err == nil {
		t.Fatal("expected unsupported OS error")
	}
	if got, want := mustGitHubReleasePath(t, "burrr-ai/comwit-cloud", "latest"), "/repos/burrr-ai/comwit-cloud/releases/latest"; got != want {
		t.Fatalf("latest path = %q, want %q", got, want)
	}
	if got, want := mustGitHubReleasePath(t, "burrr-ai/comwit-cloud", "v1.2.3"), "/repos/burrr-ai/comwit-cloud/releases/tags/v1.2.3"; got != want {
		t.Fatalf("tag path = %q, want %q", got, want)
	}
}

func makeUpdateTarball(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: name,
		Mode: 0o755,
		Size: int64(len(body)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func mustGitHubReleasePath(t *testing.T, repo, version string) string {
	t.Helper()
	path, err := githubReleasePath(repo, version)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func serverURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
