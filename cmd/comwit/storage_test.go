package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStorageCommandsUsePlatformAPI(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COMWIT_CONFIG", filepath.Join(dir, "config.json"))
	if _, err := saveConfig(configFile{Token: "cwt_test", DefaultProject: "proj_1"}); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cwt_test" {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		base := "/v1/projects/proj_1/storages"
		detail := `{"storage":{"storage_id":"stg_123","project_id":"proj_1","name":"media-bucket","bucket":"media-bucket","provider":"r2","status":"ready","endpoint":"https://storage.cloud.comwit.io","region":"auto","default_domain":"stg-123.comwit.link","public_access":"disabled","public_domain_status":"disabled","public_tls_status":"disabled"}}`
		switch {
		case r.Method == http.MethodPost && r.URL.Path == base:
			var payload struct {
				Name     string `json:"name"`
				Public   bool   `json:"public"`
				Location string `json:"location_hint"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.Name != "media-bucket" || !payload.Public || payload.Location != "apac" {
				t.Errorf("payload=%+v", payload)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(detail))
		case r.Method == http.MethodGet && r.URL.Path == base:
			_, _ = w.Write([]byte(`{"storages":[` + strings.TrimPrefix(strings.TrimSuffix(detail, "}"), `{"storage":`) + `]}`))
		case r.Method == http.MethodGet && r.URL.Path == base+"/stg_123":
			_, _ = w.Write([]byte(detail))
		case r.Method == http.MethodPut && r.URL.Path == base+"/stg_123/public-access":
			var payload map[string]bool
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if !payload["enabled"] {
				t.Error("public enable payload is false")
			}
			_, _ = w.Write([]byte(detail))
		case r.Method == http.MethodDelete && r.URL.Path == base+"/stg_123":
			_, _ = w.Write([]byte(detail))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	commands := [][]string{
		{"storage", "create", "--name", "media-bucket", "--public"},
		{"storage", "list"},
		{"storage", "get", "--storage", "stg_123"},
		{"storage", "public", "enable", "--storage", "stg_123"},
		{"storage", "delete", "--storage", "stg_123"},
	}
	for _, args := range commands {
		var out bytes.Buffer
		if err := run(args, &out, &bytes.Buffer{}); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if !strings.Contains(out.String(), "media-bucket") {
			t.Fatalf("%v output=%q", args, out.String())
		}
	}
}

func TestStorageGetWritesConnectionEnvironment(t *testing.T) {
	dir := initGitEnvRepo(t)
	t.Chdir(dir)
	t.Setenv("COMWIT_CONFIG", filepath.Join(dir, "config.json"))
	if _, err := saveConfig(configFile{Token: "cwt_test"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("COMWIT_PROJECT=proj_1\nUNRELATED=keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/projects/proj_1/storages/stg_1" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"storage":{"storage_id":"stg_1","project_id":"proj_1","name":"media","bucket":"media","provider":"r2","status":"ready","endpoint":"https://storage.cloud.comwit.io","region":"auto","default_domain":"stg-1.comwit.link","public_access":"enabled","public_domain_status":"active","public_tls_status":"active","public_base_url":"https://stg-1.comwit.link"}}`))
	}))
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	var stdout bytes.Buffer
	if err := run([]string{"storage", "get", "--storage", "stg_1", "--env-out", ".env"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"UNRELATED=keep",
		"COMWIT_STORAGE_ID=stg_1",
		"COMWIT_STORAGE_ENDPOINT=https://storage.cloud.comwit.io",
		"COMWIT_STORAGE_BUCKET=media",
		"COMWIT_STORAGE_PUBLIC_BASE_URL=https://stg-1.comwit.link",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("env missing %q: %q", want, text)
		}
	}
	if strings.Contains(text, "COMWIT_CLOUD_TOKEN") {
		t.Fatalf("storage command wrote token: %q", text)
	}
}

func TestStorageCommandsValidateRequiredArguments(t *testing.T) {
	t.Setenv("COMWIT_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	if err := run([]string{"storage", "create", "--name", "bucket"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "--project is required") {
		t.Fatalf("err=%v", err)
	}
	if _, err := saveConfig(configFile{Token: "token", DefaultProject: "proj"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"storage", "get"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "--storage is required") {
		t.Fatalf("err=%v", err)
	}
}

func TestStorageHelp(t *testing.T) {
	var stdout bytes.Buffer
	if err := run([]string{"storage", "--help"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if text := stdout.String(); !strings.Contains(text, "comwit storage create") || !strings.Contains(text, "comwit storage public") {
		t.Fatalf("unexpected help output: %q", text)
	}
}

func TestStorageCORSCommandRemainsRemoved(t *testing.T) {
	err := run([]string{"storage", "cors", "get", "--project", "proj_1", "--storage", "stg_1"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), `unknown storage command "cors"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestStoragePathsEscapeIdentifiers(t *testing.T) {
	if got := projectStoragesPath("project one"); got != "/v1/projects/project%20one/storages" {
		t.Fatalf("path=%q", got)
	}
	if got := projectStoragePath("project", "stg/one"); got != "/v1/projects/project/storages/stg%2Fone" {
		t.Fatalf("path=%q", got)
	}
}
