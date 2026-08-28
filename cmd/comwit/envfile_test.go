package main

import (
	"bytes"
	"fmt"
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

func TestContextAndStoredResourceResolutionPriorityIncludesDotEnv(t *testing.T) {
	dir := initGitEnvRepo(t)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(strings.Join([]string{
		"COMWIT_CLOUD_TOKEN='malformed-and-must-not-be-read",
		"COMWIT_PROJECT=from-dotenv",
		"COMWIT_APP='app-from-dotenv' # project app",
		"COMWIT_DATABASE_ID=db-from-dotenv",
		"COMWIT_STORAGE_ID=stg-from-dotenv",
		"",
	}, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Setenv("COMWIT_PROJECT", "")
	t.Setenv("COMWIT_APP", "")
	t.Setenv("COMWIT_DATABASE_ID", "")
	t.Setenv("COMWIT_STORAGE_ID", "")

	cfg := configFile{DefaultProject: "from-config"}
	if got, err := selectProject("from-flag", cfg); err != nil || got != "from-flag" {
		t.Fatalf("flag project = %q, err = %v", got, err)
	}
	if got, err := selectProject("", cfg); err != nil || got != "from-dotenv" {
		t.Fatalf("dotenv project = %q, err = %v", got, err)
	}
	if got, err := selectApp(""); err != nil || got != "app-from-dotenv" {
		t.Fatalf("dotenv app = %q, err = %v", got, err)
	}
	if got, err := selectStoredResourceIdentifier("db-from-flag", envDatabaseIDKey); err != nil || got != "db-from-flag" {
		t.Fatalf("flag database = %q, err = %v", got, err)
	}
	if got, err := selectStoredResourceIdentifier("stg-from-flag", envStorageIDKey); err != nil || got != "stg-from-flag" {
		t.Fatalf("flag Storage = %q, err = %v", got, err)
	}
	if got, err := selectStoredResourceIdentifier("", envDatabaseIDKey); err != nil || got != "db-from-dotenv" {
		t.Fatalf("dotenv database = %q, err = %v", got, err)
	}
	if got, err := selectStoredResourceIdentifier("", envStorageIDKey); err != nil || got != "stg-from-dotenv" {
		t.Fatalf("dotenv Storage = %q, err = %v", got, err)
	}
	if got, err := cwdDotEnvIdentifier(envCloudTokenKey); err != nil || got != "" {
		t.Fatalf("secret resolver returned %q, err = %v", got, err)
	}
	if got, err := selectStoredResourceIdentifier("", envCloudTokenKey); err == nil || got != "" {
		t.Fatalf("stored resource resolver accepted token key: %q, err = %v", got, err)
	}

	t.Setenv("COMWIT_PROJECT", "from-shell")
	t.Setenv("COMWIT_APP", "app-from-shell")
	t.Setenv("COMWIT_DATABASE_ID", "db-from-shell")
	t.Setenv("COMWIT_STORAGE_ID", "stg-from-shell")
	if got, err := selectProject("", cfg); err != nil || got != "from-shell" {
		t.Fatalf("shell project = %q, err = %v", got, err)
	}
	if got, err := selectApp(""); err != nil || got != "app-from-shell" {
		t.Fatalf("shell app = %q, err = %v", got, err)
	}
	if got, err := selectStoredResourceIdentifier("db-explicit", envDatabaseIDKey); err != nil || got != "db-explicit" {
		t.Fatalf("explicit database override = %q, err = %v", got, err)
	}
	if got, err := selectStoredResourceIdentifier("stg-explicit", envStorageIDKey); err != nil || got != "stg-explicit" {
		t.Fatalf("explicit Storage override = %q, err = %v", got, err)
	}
	if got, err := selectStoredResourceIdentifier("", envDatabaseIDKey); err != nil || got != "db-from-dotenv" {
		t.Fatalf("stale shell database overrode cwd binding: %q, err = %v", got, err)
	}
	if got, err := selectStoredResourceIdentifier("", envStorageIDKey); err != nil || got != "stg-from-dotenv" {
		t.Fatalf("stale shell Storage overrode cwd binding: %q, err = %v", got, err)
	}
}

func TestStoredResourceIdentifierIgnoresShellWithoutDotEnv(t *testing.T) {
	dir := initGitEnvRepo(t)
	t.Chdir(dir)
	t.Setenv("COMWIT_DATABASE_ID", "db-stale-shell")
	t.Setenv("COMWIT_STORAGE_ID", "stg-stale-shell")

	if got, err := selectStoredResourceIdentifier("", envDatabaseIDKey); err != nil || got != "" {
		t.Fatalf("database = %q, err = %v", got, err)
	}
	if got, err := selectStoredResourceIdentifier("", envStorageIDKey); err != nil || got != "" {
		t.Fatalf("Storage = %q, err = %v", got, err)
	}
}

func TestDotEnvContextRejectsUnsafeMalformedAndDuplicateFiles(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T) string
		wantErr string
	}{
		{
			name: "outside git worktree",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("COMWIT_PROJECT=outside\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			wantErr: "inside a Git worktree",
		},
		{
			name: "tracked",
			setup: func(t *testing.T) string {
				dir := initGitEnvRepo(t)
				if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("COMWIT_PROJECT=tracked\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				runGit(t, dir, "add", "-f", ".env")
				return dir
			},
			wantErr: "tracked",
		},
		{
			name: "not gitignored",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				runGit(t, dir, "init", "-q")
				if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("COMWIT_PROJECT=unignored\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			wantErr: "not gitignored",
		},
		{
			name: "not regular",
			setup: func(t *testing.T) string {
				dir := initGitEnvRepo(t)
				if err := os.Mkdir(filepath.Join(dir, ".env"), 0o700); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			wantErr: "not a regular file",
		},
		{
			name: "malformed target",
			setup: func(t *testing.T) string {
				dir := initGitEnvRepo(t)
				if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("COMWIT_PROJECT='unterminated\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			wantErr: "parse COMWIT_PROJECT",
		},
		{
			name: "malformed target without assignment",
			setup: func(t *testing.T) string {
				dir := initGitEnvRepo(t)
				if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("COMWIT_PROJECT malformed\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			wantErr: "malformed COMWIT_PROJECT",
		},
		{
			name: "duplicate target",
			setup: func(t *testing.T) string {
				dir := initGitEnvRepo(t)
				if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("COMWIT_PROJECT=one\nCOMWIT_PROJECT=two\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			wantErr: "duplicate COMWIT_PROJECT",
		},
		{
			name: "read error",
			setup: func(t *testing.T) string {
				dir := initGitEnvRepo(t)
				tooLong := strings.Repeat("x", 1024*1024+1) + "\nCOMWIT_PROJECT=unreachable\n"
				if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(tooLong), 0o600); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			wantErr: "read .env context",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := test.setup(t)
			t.Chdir(dir)
			t.Setenv("COMWIT_PROJECT", "")
			got, err := selectProject("", configFile{DefaultProject: "must-not-fallback"})
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("project = %q, err = %v", got, err)
			}
		})
	}
}

func TestDotEnvContextRejectsSymlink(t *testing.T) {
	dir := initGitEnvRepo(t)
	realPath := filepath.Join(dir, "real.env")
	if err := os.WriteFile(realPath, []byte("COMWIT_PROJECT=symlinked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realPath, filepath.Join(dir, ".env")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}
	t.Chdir(dir)
	if got, err := selectProject("", configFile{DefaultProject: "must-not-fallback"}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("project = %q, err = %v", got, err)
	}
}

func TestStoredResourceDotEnvRejectsTrackedMalformedAndSymlink(t *testing.T) {
	t.Run("tracked", func(t *testing.T) {
		dir := initGitEnvRepo(t)
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("COMWIT_DATABASE_ID=db_tracked\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runGit(t, dir, "add", "-f", ".env")
		t.Chdir(dir)
		t.Setenv("COMWIT_DATABASE_ID", "db-stale-shell")
		if got, err := selectStoredResourceIdentifier("", envDatabaseIDKey); err == nil || !strings.Contains(err.Error(), "tracked") {
			t.Fatalf("database = %q, err = %v", got, err)
		}
	})

	t.Run("malformed", func(t *testing.T) {
		dir := initGitEnvRepo(t)
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("COMWIT_DATABASE_ID='unterminated\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Chdir(dir)
		t.Setenv("COMWIT_DATABASE_ID", "")
		if got, err := selectStoredResourceIdentifier("", envDatabaseIDKey); err == nil || !strings.Contains(err.Error(), "parse COMWIT_DATABASE_ID") {
			t.Fatalf("database = %q, err = %v", got, err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		dir := initGitEnvRepo(t)
		realPath := filepath.Join(dir, "real.env")
		if err := os.WriteFile(realPath, []byte("COMWIT_STORAGE_ID=stg_symlinked\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(realPath, filepath.Join(dir, ".env")); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("symlink unavailable: %v", err)
			}
			t.Fatal(err)
		}
		t.Chdir(dir)
		t.Setenv("COMWIT_STORAGE_ID", "")
		if got, err := selectStoredResourceIdentifier("", envStorageIDKey); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("Storage = %q, err = %v", got, err)
		}
	})
}

func TestDotEnvAppContextRejectsDuplicateTarget(t *testing.T) {
	dir := initGitEnvRepo(t)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("COMWIT_APP=one\nCOMWIT_APP=two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if got, err := selectApp(""); err == nil || !strings.Contains(err.Error(), "duplicate COMWIT_APP") {
		t.Fatalf("app = %q, err = %v", got, err)
	}
}

func TestMutatingCommandFailsClosedOnInvalidDotEnvContext(t *testing.T) {
	dir := initGitEnvRepo(t)
	t.Chdir(dir)
	t.Setenv("COMWIT_CONFIG", filepath.Join(dir, "config.json"))
	if _, err := saveConfig(configFile{Token: "cwt_test", DefaultProject: "must-not-fallback"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("COMWIT_PROJECT=one\nCOMWIT_PROJECT=two\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusTeapot)
	}))
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	err := run([]string{"storage", "delete", "--storage", "stg_1"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "duplicate COMWIT_PROJECT") {
		t.Fatalf("error = %v", err)
	}
	if requests != 0 {
		t.Fatalf("invalid .env context sent %d requests", requests)
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

func TestDatabaseCreateAndConfigureWriteCompleteEnvironmentPair(t *testing.T) {
	dir := initGitEnvRepo(t)
	t.Chdir(dir)
	t.Setenv("COMWIT_CONFIG", filepath.Join(dir, "config.json"))
	t.Setenv("COMWIT_DATABASE_ID", "db_stale_shell")
	if _, err := saveConfig(configFile{Token: "cwt_test"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("# keep\nCOMWIT_PROJECT=proj_1\nCOMWIT_DATABASE_ID=db_stale\nDATABASE_URL=libsql://stale\n"), 0o600); err != nil {
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
	if !strings.Contains(stdout.String(), "updated_env\tCOMWIT_DATABASE_ID") || !strings.Contains(stdout.String(), "updated_env\tDATABASE_URL") {
		t.Fatalf("create stdout did not report the complete database pair: %q", stdout.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "COMWIT_DATABASE_ID=") != 1 ||
		strings.Count(string(data), "DATABASE_URL=") != 1 ||
		!strings.Contains(string(data), "COMWIT_DATABASE_ID=db_1") ||
		!strings.Contains(string(data), "DATABASE_URL=libsql://db.example.test/db_1") ||
		strings.Contains(string(data), "legacy-one-time") {
		t.Fatalf("create env = %q", data)
	}

	stdout.Reset()
	if err := run([]string{"databases", "configure", "--env-out", ".env"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "COMWIT_DATABASE_ID=") != 1 ||
		strings.Count(string(data), "DATABASE_URL=") != 1 ||
		!strings.Contains(string(data), "COMWIT_DATABASE_ID=db_1") ||
		!strings.Contains(string(data), "DATABASE_URL=libsql://db.example.test/db_1-new") {
		t.Fatalf("configure env = %q", data)
	}
	if !strings.Contains(stdout.String(), "updated_env\tCOMWIT_DATABASE_ID") || !strings.Contains(stdout.String(), "updated_env\tDATABASE_URL") {
		t.Fatalf("configure stdout did not report the complete database pair: %q", stdout.String())
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

func TestDatabaseConfigureStopsOnMissingOrUnknownStoredIdentifier(t *testing.T) {
	t.Run("missing id", func(t *testing.T) {
		dir := initGitEnvRepo(t)
		t.Chdir(dir)
		t.Setenv("COMWIT_CONFIG", filepath.Join(dir, "config.json"))
		t.Setenv("COMWIT_DATABASE_ID", "db_stale_shell")
		if _, err := saveConfig(configFile{Token: "cwt_test"}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("COMWIT_PROJECT=proj_1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		requests := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			w.WriteHeader(http.StatusTeapot)
		}))
		defer server.Close()
		t.Setenv("COMWIT_API_URL", server.URL)

		err := run([]string{"databases", "configure", "--env-out", ".env"}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "--database is required") {
			t.Fatalf("error = %v", err)
		}
		if requests != 0 {
			t.Fatalf("missing database id sent %d requests", requests)
		}
	})

	t.Run("stored id absent from Cloud", func(t *testing.T) {
		dir := initGitEnvRepo(t)
		t.Chdir(dir)
		t.Setenv("COMWIT_CONFIG", filepath.Join(dir, "config.json"))
		t.Setenv("COMWIT_DATABASE_ID", "db_stale_shell")
		if _, err := saveConfig(configFile{Token: "cwt_test"}); err != nil {
			t.Fatal(err)
		}
		original := "COMWIT_PROJECT=proj_1\nCOMWIT_DATABASE_ID=db_missing\nDATABASE_URL=libsql://stale\n"
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(original), 0o600); err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/v1/projects/proj_1/databases" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"databases":[{"database_id":"db_other","name":"other","database_url":"libsql://other","status":"ready"}]}`))
		}))
		defer server.Close()
		t.Setenv("COMWIT_API_URL", server.URL)

		err := run([]string{"databases", "configure", "--env-out", ".env"}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "database db_missing was not found") {
			t.Fatalf("error = %v", err)
		}
		data, readErr := os.ReadFile(filepath.Join(dir, ".env"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if got := string(data); got != original {
			t.Fatalf("unknown stored id changed env: %q", got)
		}
	})
}

func TestWriteDatabaseEnvRejectsIncompletePairWithoutChangingFile(t *testing.T) {
	dir := initGitEnvRepo(t)
	path := filepath.Join(dir, ".env")
	original := "COMWIT_DATABASE_ID=db_existing\nDATABASE_URL=libsql://existing\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name        string
		databaseID  string
		databaseURL string
	}{
		{name: "missing id", databaseURL: "libsql://new"},
		{name: "missing url", databaseID: "db_new"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := writeDatabaseEnv(path, test.databaseID, test.databaseURL); err == nil || !strings.Contains(err.Error(), "database_id or database_url") {
				t.Fatalf("error = %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(data); got != original {
				t.Fatalf("incomplete pair changed env: %q", got)
			}
		})
	}
}

func TestDatabaseCreateIncompletePairDoesNotExposeTokenOrChangeEnv(t *testing.T) {
	dir := initGitEnvRepo(t)
	t.Chdir(dir)
	t.Setenv("COMWIT_CONFIG", filepath.Join(dir, "config.json"))
	if _, err := saveConfig(configFile{Token: "cwt_test"}); err != nil {
		t.Fatal(err)
	}
	original := "COMWIT_PROJECT=proj_1\nCOMWIT_DATABASE_ID=db_existing\nDATABASE_URL=libsql://existing\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/projects/proj_1/databases" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"database_url":"libsql://new","created":true,"database_token":"must-not-leak"}`))
	}))
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	var stdout bytes.Buffer
	err := run([]string{"databases", "create", "--name", "app", "--env-out", ".env"}, &stdout, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "database_id or database_url") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(stdout.String(), "must-not-leak") || strings.Contains(stdout.String(), "database_token") {
		t.Fatalf("stdout exposed database token: %q", stdout.String())
	}
	data, readErr := os.ReadFile(filepath.Join(dir, ".env"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got := string(data); got != original {
		t.Fatalf("incomplete create response changed env: %q", got)
	}
}

func TestResourceEnvOutHelpCapabilities(t *testing.T) {
	tests := []struct {
		name             string
		command          []string
		envKeys          string
		resourceIDSource bool
	}{
		{
			name:    "database create",
			command: []string{"databases", "create"},
			envKeys: "COMWIT_DATABASE_ID,DATABASE_URL",
		},
		{
			name:             "database configure",
			command:          []string{"databases", "configure"},
			envKeys:          "COMWIT_DATABASE_ID,DATABASE_URL",
			resourceIDSource: true,
		},
		{
			name:    "storage create",
			command: []string{"storage", "create"},
			envKeys: "COMWIT_STORAGE_ID,COMWIT_STORAGE_ENDPOINT,COMWIT_STORAGE_BUCKET,COMWIT_STORAGE_PUBLIC_BASE_URL",
		},
		{
			name:             "storage get",
			command:          []string{"storage", "get"},
			envKeys:          "COMWIT_STORAGE_ID,COMWIT_STORAGE_ENDPOINT,COMWIT_STORAGE_BUCKET,COMWIT_STORAGE_PUBLIC_BASE_URL",
			resourceIDSource: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			helpArgs := append(append([]string{}, test.command...), "--help")
			if err := run(helpArgs, &stdout, &bytes.Buffer{}); err != nil {
				t.Fatalf("help error = %v", err)
			}
			output := stdout.String()
			if !strings.Contains(output, "env-out") || !strings.Contains(output, test.envKeys) {
				t.Fatalf("help missing env-out contract %q: %q", test.envKeys, output)
			}
			markerCount := strings.Count(output, "resource-id-source: explicit,cwd-env")
			if test.resourceIDSource && markerCount != 1 {
				t.Fatalf("help marker count = %d, output = %q", markerCount, output)
			}
			if !test.resourceIDSource && markerCount != 0 {
				t.Fatalf("create help unexpectedly exposed resource id marker: %q", output)
			}

			stdout.Reset()
			unknownArgs := append(append([]string{}, test.command...), "--definitely-unknown")
			err := run(unknownArgs, &stdout, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
				t.Fatalf("unknown flag error = %v", err)
			}
		})
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

func TestConfigPathPreservesLegacyLocationAndPrivatePermissions(t *testing.T) {
	path, err := configPathForOS(
		"windows",
		func(string) string { return "" },
		func() (string, error) { return "/home/ignored", nil },
		func() (string, error) { return "/AppData/Roaming", nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join("/home/ignored", ".config", "comwit", "config.json") {
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

func TestDeviceLoginRejectsInvalidUserToken(t *testing.T) {
	for _, token := range []string{"", "cwp_project_token"} {
		t.Run(token, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.json")
			t.Setenv("COMWIT_CONFIG", configPath)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/v1/auth/device":
					_, _ = w.Write([]byte(`{"device_code":"device-1","user_code":"ABCD","verification_uri":"https://cloud.example.test/device","interval":1,"expires_in":60}`))
				case "/v1/auth/device/token":
					_, _ = fmt.Fprintf(w, `{"status":"token","token":%q}`, token)
				default:
					t.Fatalf("path = %q", r.URL.Path)
				}
			}))
			defer server.Close()

			err := runDeviceLogin(
				&bytes.Buffer{},
				"",
				&client{apiURL: server.URL, httpClient: server.Client()},
				func(string) error { return nil },
				func(time.Duration) {},
				time.Now,
			)
			if err == nil || !strings.Contains(err.Error(), "invalid user token") {
				t.Fatalf("error = %v", err)
			}
			if _, err := os.Stat(configPath); !os.IsNotExist(err) {
				t.Fatalf("device login wrote config for invalid token: %v", err)
			}
		})
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
