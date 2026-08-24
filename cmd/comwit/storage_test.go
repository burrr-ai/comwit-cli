package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

	const (
		storageID     = "stg_019fe590b65472b0a0d92a45d437bc4b"
		defaultDomain = "019fe590b65472b0a0d92a45d437bc4b.storage.comwit.link"
		publicBaseURL = "https://019fe590b65472b0a0d92a45d437bc4b.storage.comwit.link"
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cwt_test" {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		base := "/v1/projects/proj_1/storages"
		detail := `{"storage":{"storage_id":"` + storageID + `","project_id":"proj_1","name":"media-bucket","bucket":"media-bucket","provider":"r2","status":"ready","endpoint":"https://storage.cloud.comwit.io","region":"auto","default_domain":"` + defaultDomain + `","public_access":"enabled","public_domain_status":"active","public_tls_status":"active","public_base_url":"` + publicBaseURL + `"}}`
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
		case r.Method == http.MethodGet && r.URL.Path == base+"/"+storageID:
			_, _ = w.Write([]byte(detail))
		case r.Method == http.MethodPut && r.URL.Path == base+"/"+storageID+"/public-access":
			var payload map[string]bool
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if !payload["enabled"] {
				t.Error("public enable payload is false")
			}
			_, _ = w.Write([]byte(detail))
		case r.Method == http.MethodDelete && r.URL.Path == base+"/"+storageID:
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
		{"storage", "get", "--storage", storageID},
		{"storage", "public", "enable", "--storage", storageID},
		{"storage", "delete", "--storage", storageID},
	}
	for index, args := range commands {
		var out bytes.Buffer
		if err := run(args, &out, &bytes.Buffer{}); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if !strings.Contains(out.String(), "media-bucket") {
			t.Fatalf("%v output=%q", args, out.String())
		}
		if index != 1 && (!strings.Contains(out.String(), defaultDomain) || !strings.Contains(out.String(), publicBaseURL)) {
			t.Fatalf("%v output did not preserve API canonical locations: %q", args, out.String())
		}
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

func TestStoragePathsEscapeIdentifiers(t *testing.T) {
	if got := projectStoragesPath("project one"); got != "/v1/projects/project%20one/storages" {
		t.Fatalf("path=%q", got)
	}
	if got := projectStoragePath("project", "stg/one"); got != "/v1/projects/project/storages/stg%2Fone" {
		t.Fatalf("path=%q", got)
	}
}
