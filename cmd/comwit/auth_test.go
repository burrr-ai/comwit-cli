package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const exportTestToken = "cwt_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestAuthExportTokenMergesPrivately(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "config.json")
	t.Setenv("COMWIT_CONFIG", config)
	if _, err := saveConfig(configFile{Token: exportTestToken, DefaultProject: "project_1"}); err != nil {
		t.Fatal(err)
	}
	configBefore, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, exists := range []bool{false, true} {
		name := "create"
		if exists {
			name = "merge"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".env")
			prefix := ""
			if exists {
				prefix = "# Project settings\nCOMWIT_PROJECT=project_1\nDATABASE_URL='https://db.example.test'\n"
				if err := os.WriteFile(path, []byte(prefix+"COMWIT_CLOUD_TOKEN=old-secret\nexport COMWIT_CLOUD_TOKEN=duplicate-secret\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			for attempt := 0; attempt < 2; attempt++ {
				var stdout, stderr bytes.Buffer
				if err := run([]string{"auth", "export-token", "--env-out", path}, &stdout, &stderr); err != nil {
					t.Fatal(err)
				}
				if stdout.String() != "Saved COMWIT_CLOUD_TOKEN to env file.\n" || stderr.Len() != 0 {
					t.Fatalf("unexpected output: stdout=%q stderr=%q", stdout.String(), stderr.String())
				}
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if string(data) != prefix+"COMWIT_CLOUD_TOKEN="+exportTestToken+"\n" {
					t.Fatal("env does not contain exactly the expected settings and one token")
				}
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
					t.Fatalf("mode = %o, want 600", info.Mode().Perm())
				}
			}
			entries, err := os.ReadDir(filepath.Dir(path))
			if err != nil || len(entries) != 1 {
				t.Fatalf("temporary file left behind: entries=%v err=%v", entries, err)
			}
		})
	}
	configAfter, err := os.ReadFile(config)
	if err != nil || !bytes.Equal(configBefore, configAfter) {
		t.Fatal("export changed login config")
	}
}

func TestMergeTokenEnvPreservesDotenvRecords(t *testing.T) {
	assignment := "COMWIT_CLOUD_TOKEN=" + exportTestToken
	for _, tt := range []struct{ name, input, want string }{
		{"empty", "", assignment + "\n"},
		{"append newline", "PORT=3000", "PORT=3000\n" + assignment + "\n"},
		{"CRLF", "# comment\r\nPORT=3000\r\n", "# comment\r\nPORT=3000\r\n" + assignment + "\r\n"},
		{"no final newline", "COMWIT_CLOUD_TOKEN=old", assignment},
		{"duplicates", "  export COMWIT_CLOUD_TOKEN = 'old' # token\nPORT=3000\nCOMWIT_CLOUD_TOKEN=older\n", assignment + "\nPORT=3000\n"},
		{"quoted old token", "COMWIT_CLOUD_TOKEN=\"old\nvalue\"\r\nOTHER=yes\r\n", assignment + "\r\nOTHER=yes\r\n"},
		{"multiline unrelated", "PRIVATE_KEY='start\nCOMWIT_CLOUD_TOKEN=inside-value\nend'\n", "PRIVATE_KEY='start\nCOMWIT_CLOUD_TOKEN=inside-value\nend'\n" + assignment + "\n"},
		{"escaped quote", "VALUE=\"a\\\"b\nCOMWIT_CLOUD_TOKEN=inside\" # note\n", "VALUE=\"a\\\"b\nCOMWIT_CLOUD_TOKEN=inside\" # note\n" + assignment + "\n"},
		{"backtick", "VALUE=`a\nb`\n", "VALUE=`a\nb`\n" + assignment + "\n"},
		{"BOM", "\ufeffCOMWIT_CLOUD_TOKEN=old\n", "\ufeff" + assignment + "\n"},
		{"empty BOM", "\ufeff", "\ufeff" + assignment + "\n"},
		{"similar key", "# COMWIT_CLOUD_TOKEN=comment\nCOMWIT_CLOUD_TOKEN_BACKUP=other\n", "# COMWIT_CLOUD_TOKEN=comment\nCOMWIT_CLOUD_TOKEN_BACKUP=other\n" + assignment + "\n"},
		{"empty value", "EMPTY=\nCOMWIT_CLOUD_TOKEN=old\n", "EMPTY=\n" + assignment + "\n"},
		{"long value", "LONG=" + strings.Repeat("x", 70000) + "\n", "LONG=" + strings.Repeat("x", 70000) + "\n" + assignment + "\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mergeTokenEnv(tt.input, exportTestToken)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatal("merge did not preserve expected dotenv records")
			}
			again, err := mergeTokenEnv(got, exportTestToken)
			if err != nil || again != got {
				t.Fatal("merge is not idempotent")
			}
		})
	}
}

func TestAuthExportTokenFailuresDoNotWriteOrLeak(t *testing.T) {
	for _, tt := range []struct{ name, config, env string }{
		{"missing login", "", "PORT=3000\n"},
		{"empty token", `{"token":""}`, "PORT=3000\n"},
		{"malformed config", `{"token":"secret`, "PORT=3000\n"},
		{"invalid config type", `{"token":123}`, "PORT=3000\n"},
		{"operator token", `{"token":"operator-secret"}`, "PORT=3000\n"},
		{"project token", `{"token":"cwp_` + strings.Repeat("a", 64) + `"}`, "PORT=3000\n"},
		{"dotenv injection", `{"token":"cwt_a\nOTHER=secret"}`, "PORT=3000\n"},
		{"unterminated quote", `{"token":"` + exportTestToken + `"}`, "OTHER='secret\nCOMWIT_CLOUD_TOKEN=old\n"},
		{"unsupported syntax", `{"token":"` + exportTestToken + `"}`, "source secret\n"},
		{"trailing syntax", `{"token":"` + exportTestToken + `"}`, "OTHER='secret' invalid\n"},
		{"nul", `{"token":"` + exportTestToken + `"}`, "OTHER=secret\x00\n"},
		{"non UTF-8", `{"token":"` + exportTestToken + `"}`, "OTHER=secret\xff\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("COMWIT_CONFIG", filepath.Join(dir, "config.json"))
			if tt.config != "" {
				if err := os.WriteFile(os.Getenv("COMWIT_CONFIG"), []byte(tt.config), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(dir, ".env")
			if err := os.WriteFile(path, []byte(tt.env), 0o644); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			err := run([]string{"auth", "export-token", "--env-out", path}, &stdout, &stderr)
			if err == nil {
				t.Fatal("expected an error")
			}
			if stdout.Len() != 0 || stderr.Len() != 0 || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), exportTestToken) {
				t.Fatal("failure exposed a value or printed success")
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil || string(data) != tt.env {
				t.Fatal("failure changed env file")
			}
		})
	}
}

func TestAuthExportTokenRejectsUnsafeDestination(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "config.json")
	t.Setenv("COMWIT_CONFIG", config)
	if _, err := saveConfig(configFile{Token: exportTestToken}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(config)
	paths := []string{config, dir, filepath.Join(dir, "missing", ".env")}
	if runtime.GOOS != "windows" {
		link := filepath.Join(dir, "symlink")
		if err := os.Symlink(config, link); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, link)
		hardlink := filepath.Join(dir, "hardlink")
		if err := os.Link(config, hardlink); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, hardlink)
	}
	for _, path := range paths {
		var stdout, stderr bytes.Buffer
		if err := run([]string{"auth", "export-token", "--env-out", path}, &stdout, &stderr); err == nil || stdout.Len() != 0 || stderr.Len() != 0 {
			t.Fatalf("unsafe destination accepted or printed output: %s", filepath.Base(path))
		}
	}
	after, err := os.ReadFile(config)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("unsafe destination changed login config")
	}
}

func TestAuthExportTokenArgumentsAndHelp(t *testing.T) {
	for _, args := range [][]string{
		{"auth"}, {"auth", "unknown"}, {"auth", "export-token"},
		{"auth", "export-token", "--env-out"},
		{"auth", "export-token", "--env-out", ""},
		{"auth", "export-token", "--env-out", ".env", exportTestToken},
		{"auth", "export-token", "--token=" + exportTestToken},
	} {
		var stdout, stderr bytes.Buffer
		err := run(args, &stdout, &stderr)
		if err == nil || err.Error() != exportTokenUsage || stdout.Len() != 0 || stderr.Len() != 0 {
			t.Fatal("invalid arguments should return only safe usage")
		}
	}
	for _, args := range [][]string{{"--help"}, {"auth", "--help"}, {"auth", "export-token", "--help"}} {
		var stdout bytes.Buffer
		if err := run(args, &stdout, &bytes.Buffer{}); err != nil || !strings.Contains(stdout.String(), "env-out") {
			t.Fatal("export command missing from help")
		}
	}
}

func TestAuthExportTokenUsesXDGConfig(t *testing.T) {
	t.Setenv("COMWIT_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := saveConfig(configFile{Token: exportTestToken}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), ".env")
	if err := run([]string{"auth", "export-token", "--env-out", path}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "COMWIT_CLOUD_TOKEN="+exportTestToken+"\n" {
		t.Fatal("export did not use XDG login config")
	}
}
