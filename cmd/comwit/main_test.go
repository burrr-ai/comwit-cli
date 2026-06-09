package main

import (
	"bytes"
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
