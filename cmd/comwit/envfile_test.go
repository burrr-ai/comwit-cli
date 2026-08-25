package main

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestProjectAndAppResolutionPriorityIncludesDotEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(strings.Join([]string{
		"COMWIT_CLOUD_TOKEN=cwt_must_not_be_loaded",
		"COMWIT_PROJECT=from-dotenv",
		"COMWIT_APP='app-from-dotenv' # project app",
		"COMWIT_PROJECT=from-dotenv-last",
		"",
	}, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Setenv("COMWIT_PROJECT", "")
	t.Setenv("COMWIT_APP", "")

	cfg := configFile{DefaultProject: "from-config"}
	if got := selectProject("from-flag", cfg); got != "from-flag" {
		t.Fatalf("flag project = %q", got)
	}
	if got := selectProject("", cfg); got != "from-dotenv-last" {
		t.Fatalf("dotenv project = %q", got)
	}
	if got := selectApp(""); got != "app-from-dotenv" {
		t.Fatalf("dotenv app = %q", got)
	}
	if got := cwdDotEnvIdentifier(envCloudTokenKey); got != "" {
		t.Fatalf("secret resolver returned %q", got)
	}

	t.Setenv("COMWIT_PROJECT", "from-shell")
	t.Setenv("COMWIT_APP", "app-from-shell")
	if got := selectProject("", cfg); got != "from-shell" {
		t.Fatalf("shell project = %q", got)
	}
	if got := selectApp(""); got != "app-from-shell" {
		t.Fatalf("shell app = %q", got)
	}
}

func TestAuthExportTokenWritesGitignoredEnvWithoutExposure(t *testing.T) {
	dir := initGitEnvRepo(t)
	t.Chdir(dir)
	t.Setenv("COMWIT_CONFIG", filepath.Join(dir, "config.json"))
	const secret = "cwt_0123456789secret"
	if _, err := saveConfig(configFile{Token: secret}); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"auth", "export-token", "--env-out", ".env"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), secret) {
		t.Fatalf("stdout exposed token: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "updated_env\tCOMWIT_CLOUD_TOKEN") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "COMWIT_CLOUD_TOKEN="+secret+"\n" {
		t.Fatalf("env file = %q", got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, ".env"))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("env mode = %04o", got)
		}
	}
	if matches, err := filepath.Glob(filepath.Join(dir, "..env.tmp-*")); err != nil || len(matches) != 0 {
		t.Fatalf("temporary files = %v, err = %v", matches, err)
	}
}

func TestEnvWriterPreservesCommentsAndReplacesEveryDuplicate(t *testing.T) {
	dir := initGitEnvRepo(t)
	path := filepath.Join(dir, ".env")
	original := "# project settings\nCOMWIT_PROJECT=proj_1\nCOMWIT_CLOUD_TOKEN=old-one # first\nOTHER=value\nexport COMWIT_CLOUD_TOKEN=old-two # active\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	keys, err := writeEnvUpdates(path, envUpdate{Key: envCloudTokenKey, Value: "cwt_replacement"})
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0] != envCloudTokenKey {
		t.Fatalf("keys = %v", keys)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"# project settings",
		"COMWIT_PROJECT=proj_1",
		"COMWIT_CLOUD_TOKEN=cwt_replacement # first",
		"OTHER=value",
		"export COMWIT_CLOUD_TOKEN=cwt_replacement # active",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("env file missing %q: %q", want, text)
		}
	}
	if strings.Contains(text, "old-one") || strings.Contains(text, "old-two") {
		t.Fatalf("env file retained old secret: %q", text)
	}
}

func TestEnvWriterRefusesTrackedUnignoredAndSymlinkTargets(t *testing.T) {
	dir := initGitEnvRepo(t)
	tracked := filepath.Join(dir, ".env")
	if err := os.WriteFile(tracked, []byte("SAFE=old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "-f", ".env")
	if _, err := writeEnvUpdates(tracked, envUpdate{Key: "SAFE", Value: "new"}); err == nil || !strings.Contains(err.Error(), "tracked") {
		t.Fatalf("tracked error = %v", err)
	}
	data, err := os.ReadFile(tracked)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "SAFE=old\n" {
		t.Fatalf("tracked file changed: %q", data)
	}

	unignored := filepath.Join(dir, "runtime.env")
	if _, err := writeEnvUpdates(unignored, envUpdate{Key: "SAFE", Value: "new"}); err == nil || !strings.Contains(err.Error(), "not gitignored") {
		t.Fatalf("unignored error = %v", err)
	}

	runGit(t, dir, "reset", "--", ".env")
	if err := os.Remove(tracked); err != nil {
		t.Fatal(err)
	}
	realPath := filepath.Join(dir, "real-secret")
	if err := os.WriteFile(realPath, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realPath, tracked); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if _, err := writeEnvUpdates(tracked, envUpdate{Key: "SAFE", Value: "new"}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestDatabaseCreateAndConfigureWriteConcreteURL(t *testing.T) {
	dir := initGitEnvRepo(t)
	t.Chdir(dir)
	t.Setenv("COMWIT_CONFIG", filepath.Join(dir, "config.json"))
	if _, err := saveConfig(configFile{Token: "cwt_test"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("# keep\nCOMWIT_PROJECT=proj_1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/projects/proj_1/databases" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPost:
			_, _ = w.Write([]byte(`{"database_id":"db_1","database_url":"libsql://db.example.test/db_1","created":true,"database_token":"legacy-one-time"}`))
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"databases":[{"database_id":"db_1","name":"app","database_url":"libsql://db.example.test/db_1-new","status":"cold"}]}`))
		default:
			t.Fatalf("method = %s", r.Method)
		}
	}))
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	var stdout bytes.Buffer
	if err := run([]string{"databases", "create", "--name", "app", "--env-out", ".env"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), "legacy-one-time") || strings.Contains(stdout.String(), "database_token") {
		t.Fatalf("env-out stdout exposed legacy database token: %q", stdout.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "DATABASE_URL=libsql://db.example.test/db_1") || strings.Contains(string(data), "legacy-one-time") {
		t.Fatalf("create env = %q", data)
	}

	stdout.Reset()
	if err := run([]string{"databases", "configure", "--database", "db_1", "--env-out", ".env"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "DATABASE_URL=") != 1 || !strings.Contains(string(data), "DATABASE_URL=libsql://db.example.test/db_1-new") {
		t.Fatalf("configure env = %q", data)
	}
	if !strings.Contains(string(data), "# keep") {
		t.Fatalf("comment lost: %q", data)
	}

	stdout.Reset()
	if err := run([]string{"databases", "create", "--name", "legacy-compatible"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "token\tlegacy-one-time") {
		t.Fatalf("legacy create stdout no longer includes its one-time token: %q", stdout.String())
	}
}

func TestDotEnvAppFallbackAndCWPDeploy(t *testing.T) {
	dir := initGitEnvRepo(t)
	t.Chdir(dir)
	t.Setenv("COMWIT_CONFIG", filepath.Join(dir, "config.json"))
	if _, err := saveConfig(configFile{Token: "cwp_project_machine"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("COMWIT_PROJECT=proj_1\nCOMWIT_APP=svc_1\nCOMWIT_CLOUD_TOKEN=must-not-be-context-auth\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(dir, "app.tar.zst")
	if err := os.WriteFile(artifact, []byte("artifact"), 0o600); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/projects/proj_1/apps/svc_1/deployments" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer cwp_project_machine" {
			t.Fatalf("authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"app_id":"svc_1","build_id":"bld_1","hosts":[],"uploaded":true}`))
	}))
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	if err := run([]string{"deploy", "--package", artifact}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}

func TestStorageCORSPendingDoesNotSendRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusTeapot)
	}))
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	err := run([]string{"storage", "cors", "get", "--project", "proj_1", "--storage", "stg_1"}, &bytes.Buffer{}, &bytes.Buffer{})
	var pending ExternalContractPendingError
	if !errors.As(err, &pending) {
		t.Fatalf("error = %v", err)
	}
	if pending.Feature != "storage_cors_contract" {
		t.Fatalf("pending feature = %q", pending.Feature)
	}
	if requests != 0 {
		t.Fatalf("pending command sent %d requests", requests)
	}
}

func TestConfigPathAndPrivatePermissions(t *testing.T) {
	path, err := configPathForOS(
		"windows",
		func(string) string { return "" },
		func() (string, error) { return "/home/ignored", nil },
		func() (string, error) { return "/AppData/Roaming", nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join("/AppData/Roaming", "comwit", "config.json") {
		t.Fatalf("windows config path = %q", path)
	}

	dir := t.TempDir()
	config := filepath.Join(dir, "config.json")
	if err := os.WriteFile(config, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMWIT_CONFIG", config)
	if _, err := saveConfig(configFile{Token: "cwt_secret"}); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(config)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("config mode = %04o", got)
		}
	}
}

func TestLoginAndProjectsListDoNotEchoToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COMWIT_CONFIG", filepath.Join(dir, "config.json"))
	const token = "cwt_no_echo"
	var loginOut bytes.Buffer
	if err := run([]string{"login", "--token", token}, &loginOut, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(loginOut.String(), token) {
		t.Fatalf("login stdout exposed token: %q", loginOut.String())
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/projects" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Fatalf("authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"projects":[{"project_id":"proj_1","name":"One"}]}`))
	}))
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)
	var projectsOut bytes.Buffer
	if err := run([]string{"projects", "list"}, &projectsOut, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(projectsOut.String(), "proj_1") || strings.Contains(projectsOut.String(), token) {
		t.Fatalf("projects stdout = %q", projectsOut.String())
	}
}

func TestDeviceLoginRemainsAvailableAndDoesNotEchoToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COMWIT_CONFIG", filepath.Join(dir, "config.json"))
	const token = "cwt_device_secret"
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/auth/device":
			_, _ = w.Write([]byte(`{"device_code":"device-1","user_code":"ABCD","verification_uri":"https://cloud.example.test/device","interval":1,"expires_in":60}`))
		case "/v1/auth/device/token":
			polls++
			_, _ = w.Write([]byte(`{"status":"token","token":"` + token + `"}`))
		default:
			t.Fatalf("path = %q", r.URL.Path)
		}
	}))
	defer server.Close()

	opened := ""
	var stdout bytes.Buffer
	err := runDeviceLogin(
		&stdout,
		"proj_device",
		&client{apiURL: server.URL, httpClient: server.Client()},
		func(target string) error {
			opened = target
			return nil
		},
		func(time.Duration) {},
		time.Now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if opened != "https://cloud.example.test/device" || polls != 1 {
		t.Fatalf("opened = %q, polls = %d", opened, polls)
	}
	if strings.Contains(stdout.String(), token) {
		t.Fatalf("device login stdout exposed token: %q", stdout.String())
	}
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != token || cfg.DefaultProject != "proj_device" {
		t.Fatalf("config = %+v", cfg)
	}
}

func initGitEnvRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	commandArgs := append([]string{"-C", dir}, args...)
	output, err := exec.Command("git", commandArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}
