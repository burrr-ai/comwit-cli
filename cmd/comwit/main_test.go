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
	"time"
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

func TestSplitSQLDumpFixtures(t *testing.T) {
	tests := []struct {
		name      string
		wantMin   int
		wantSkip  int
		wantMatch string
	}{
		{name: "sqlite_dump.sql", wantMin: 5, wantMatch: "hello; sqlite"},
		{name: "d1_dump.sql", wantMin: 5, wantSkip: 2, wantMatch: "Grace; Hopper"},
		{name: "turso_dump.sql", wantMin: 6, wantMatch: "CREATE TRIGGER"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			statements, skipped, err := loadDumpStatements(filepath.Join("testdata", tt.name))
			if err != nil {
				t.Fatal(err)
			}
			if len(statements) < tt.wantMin {
				t.Fatalf("statements = %d, want at least %d: %#v", len(statements), tt.wantMin, statements)
			}
			if len(skipped) != tt.wantSkip {
				t.Fatalf("skipped = %d, want %d: %#v", len(skipped), tt.wantSkip, skipped)
			}
			var joined strings.Builder
			for _, statement := range statements {
				joined.WriteString(statement.SQL)
				joined.WriteByte('\n')
			}
			if !strings.Contains(joined.String(), tt.wantMatch) {
				t.Fatalf("joined statements missing %q:\n%s", tt.wantMatch, joined.String())
			}
		})
	}
}

func TestSplitSQLDumpTriggerBodyStaysOneStatement(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "turso_dump.sql"))
	if err != nil {
		t.Fatal(err)
	}
	statements, err := splitSQLDump(string(data))
	if err != nil {
		t.Fatal(err)
	}
	var triggerCount int
	for _, statement := range statements {
		if strings.Contains(statement.SQL, "CREATE TRIGGER") {
			triggerCount++
			if !strings.Contains(statement.SQL, "trigger; body") || !strings.Contains(statement.SQL, "CASE WHEN") {
				t.Fatalf("trigger statement was truncated: %s", statement.SQL)
			}
		}
	}
	if triggerCount != 1 {
		t.Fatalf("triggerCount = %d, want 1: %#v", triggerCount, statements)
	}
}

func TestSplitSQLDumpTriggerWithCaseStatement(t *testing.T) {
	input := `CREATE TRIGGER audit_ai AFTER INSERT ON audit
BEGIN
  INSERT INTO audit_log VALUES(CASE WHEN new.name = 'x' THEN 'x;y' ELSE 'z' END);
  INSERT INTO audit_log VALUES('done');
END;
CREATE TABLE after_trigger(id INTEGER);`
	statements, err := splitSQLDump(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(statements) != 2 {
		t.Fatalf("statements = %d, want 2: %#v", len(statements), statements)
	}
	if !strings.Contains(statements[0].SQL, "INSERT INTO audit_log VALUES('done')") {
		t.Fatalf("trigger statement split early: %s", statements[0].SQL)
	}
	if !strings.Contains(statements[1].SQL, "CREATE TABLE after_trigger") {
		t.Fatalf("second statement = %s", statements[1].SQL)
	}
}

func TestSplitSQLDumpRejectsSQLiteDatabaseFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.db")
	if err := os.WriteFile(path, append([]byte("SQLite format 3\x00"), []byte("rest")...), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := loadDumpStatements(path)
	if err == nil {
		t.Fatal("expected .db rejection")
	}
	if !strings.Contains(err.Error(), "looks like a SQLite .db") {
		t.Fatalf("error = %v", err)
	}
}

func TestDatabasesImportDumpCreatesDatabaseAndReplaysHranaBatch(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COMWIT_CONFIG", filepath.Join(dir, "config.json"))
	if _, err := saveConfig(configFile{Token: "test-token", DefaultProject: "proj_1"}); err != nil {
		t.Fatal(err)
	}

	var sawCreate bool
	var sawHrana bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/projects/proj_1/databases":
			sawCreate = true
			if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
				t.Fatalf("platform auth = %q", got)
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["name"] != "imported" {
				t.Fatalf("create body = %#v", body)
			}
			_, _ = w.Write([]byte(`{"database_id":"db_1","database_url":"` + serverURL(r) + `/v1/db_1","created":true,"database_token":"tenant-token"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/db_1/v3/pipeline":
			sawHrana = true
			if got := r.Header.Get("Authorization"); got != "Bearer tenant-token" {
				t.Fatalf("database auth = %q", got)
			}
			var request hranaPipelineRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if len(request.Requests) != 1 || request.Requests[0].Type != "batch" || request.Requests[0].Batch == nil {
				t.Fatalf("request = %#v", request)
			}
			steps := request.Requests[0].Batch.Steps
			if len(steps) < 5 {
				t.Fatalf("steps = %d, want at least 5", len(steps))
			}
			if steps[1].Condition == nil || steps[1].Condition.Type != "ok" || steps[1].Condition.Step != 0 {
				t.Fatalf("step condition = %#v", steps[1].Condition)
			}
			if !strings.Contains(steps[3].Stmt.SQL, "hello; sqlite") {
				t.Fatalf("insert statement = %q", steps[3].Stmt.SQL)
			}
			_, _ = w.Write([]byte(`{"baton":null,"base_url":null,"results":[{"type":"ok","response":{"type":"batch","result":{"step_errors":[null,null,null,null,null]}}}]}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	var stdout bytes.Buffer
	err := run([]string{"databases", "import-dump", "--name", "imported", "--from-dump", filepath.Join("testdata", "sqlite_dump.sql")}, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if !sawCreate || !sawHrana {
		t.Fatalf("sawCreate=%t sawHrana=%t", sawCreate, sawHrana)
	}
	if !strings.Contains(stdout.String(), "imported\tdb_1") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "token\ttenant-token") {
		t.Fatalf("stdout missing token = %q", stdout.String())
	}
}

func TestDatabasesImportDumpDeletesCreatedDatabaseOnFailure(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COMWIT_CONFIG", filepath.Join(dir, "config.json"))
	if _, err := saveConfig(configFile{Token: "test-token", DefaultProject: "proj_1"}); err != nil {
		t.Fatal(err)
	}
	dumpPath := filepath.Join(dir, "dump.sql")
	if err := os.WriteFile(dumpPath, []byte("CREATE TABLE t(id INTEGER PRIMARY KEY);\nINSERT INTO t VALUES(1);\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var sawDelete bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/projects/proj_1/databases":
			_, _ = w.Write([]byte(`{"database_id":"db_1","database_url":"` + serverURL(r) + `/v1/db_1","created":true,"database_token":"tenant-token"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/db_1/v3/pipeline":
			_, _ = w.Write([]byte(`{"baton":null,"base_url":null,"results":[{"type":"ok","response":{"type":"batch","result":{"step_errors":[null,{"message":"constraint failed","code":"SQL_ERROR"}]}}}]}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/projects/proj_1/databases/db_1":
			sawDelete = true
			_, _ = w.Write([]byte(`{"ok":true,"database":{"database_id":"db_1","database_url":"` + serverURL(r) + `/v1/db_1","status":"deleted"}}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	var stdout bytes.Buffer
	err := run([]string{"databases", "import-dump", "--name", "imported", "--from-dump", dumpPath}, &stdout, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected import error")
	}
	if !sawDelete {
		t.Fatal("created database was not deleted")
	}
	if !strings.Contains(err.Error(), "statement 2 failed") || !strings.Contains(err.Error(), "INSERT INTO t") {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(stdout.String(), "failed_database_deleted\tdb_1") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestDatabasesExecuteCommandPostsProjectQueryAndPrintsRows(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COMWIT_CONFIG", filepath.Join(dir, "config.json"))
	if _, err := saveConfig(configFile{Token: "test-token", DefaultProject: "proj_1"}); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/projects/proj_1/databases/db_1/query" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("auth = %q", got)
		}
		var body databaseQueryRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.SQL != "select 1 as ok;" || len(body.Batch) != 0 {
			t.Fatalf("body = %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"statement":1,"success":true,"columns":[{"name":"ok","decltype":"INTEGER"}],"rows":[[{"type":"integer","value":"1"}]],"affected_row_count":0,"rows_read":1,"rows_written":0,"query_duration_ms":0.125}]}`))
	}))
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	var stdout bytes.Buffer
	err := run([]string{"databases", "execute", "--database", "db_1", "--command", "select 1 as ok"}, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"statement 1", "ok", "1", "rows_read\t1", "duration_ms\t0.125"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("stdout missing %q: %s", expected, stdout.String())
		}
	}
}

func TestDatabasesExecuteFileSendsSQLiteAwareBatchAndPrintsJSONOnly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COMWIT_CONFIG", filepath.Join(dir, "config.json"))
	if _, err := saveConfig(configFile{Token: "test-token", DefaultProject: "proj_1"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "query.sql")
	if err := os.WriteFile(path, []byte("insert into notes(body) values ('hello; world');\nselect body from notes"), 0o600); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body databaseQueryRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.SQL != "" || len(body.Batch) != 2 {
			t.Fatalf("body = %#v", body)
		}
		if !strings.Contains(body.Batch[0].SQL, "hello; world") || body.Batch[1].SQL != "select body from notes;" {
			t.Fatalf("batch = %#v", body.Batch)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"statement":1,"success":true,"columns":[],"rows":[],"affected_row_count":1,"last_insert_rowid":"7","rows_read":0,"rows_written":1,"query_duration_ms":1.5},{"statement":2,"success":true,"columns":[{"name":"body","decltype":"TEXT"}],"rows":[[{"type":"text","value":"hello; world"}]],"affected_row_count":0,"rows_read":1,"rows_written":0,"query_duration_ms":0.5}]}`))
	}))
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	var stdout bytes.Buffer
	err := run([]string{"databases", "execute", "--database", "db_1", "--file", path, "--json", "--remote"}, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	var response databaseQueryResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("stdout is not JSON-only: %v\n%s", err, stdout.String())
	}
	if len(response.Results) != 2 || response.Results[0].LastInsertRowID == nil || *response.Results[0].LastInsertRowID != "7" {
		t.Fatalf("response = %#v", response)
	}
	if !strings.Contains(stdout.String(), `"columns": []`) || !strings.Contains(stdout.String(), `"rows": []`) {
		t.Fatalf("JSON output did not preserve empty success fields: %s", stdout.String())
	}
}

func TestDatabasesExecuteReturnsNonZeroForSQLFailure(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COMWIT_CONFIG", filepath.Join(dir, "config.json"))
	if _, err := saveConfig(configFile{Token: "test-token", DefaultProject: "proj_1"}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"statement":1,"success":false,"error":{"code":"SQL_ERROR","message":"no such table: missing\u001b[31m"}}]}`))
	}))
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	var stdout bytes.Buffer
	err := run([]string{"databases", "execute", "--database", "db_1", "--command", "select * from missing"}, &stdout, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "statement 1 failed: no such table: missing") {
		t.Fatalf("error = %v", err)
	}
	if strings.ContainsRune(err.Error(), '\x1b') || !strings.Contains(err.Error(), `\x1b[31m`) {
		t.Fatalf("error did not escape terminal control: %q", err.Error())
	}
	if !strings.Contains(stdout.String(), "status\tfailed") {
		t.Fatalf("stdout = %s", stdout.String())
	}
	if strings.ContainsRune(stdout.String(), '\x1b') {
		t.Fatalf("stdout contains raw terminal control: %q", stdout.String())
	}

	stdout.Reset()
	err = run([]string{"databases", "execute", "--database", "db_1", "--command", "select * from missing", "--json"}, &stdout, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected JSON-mode SQL failure")
	}
	var envelope struct {
		Results []map[string]any `json:"results"`
	}
	if decodeErr := json.Unmarshal(stdout.Bytes(), &envelope); decodeErr != nil || len(envelope.Results) != 1 {
		t.Fatalf("JSON failure envelope: decode=%v output=%s", decodeErr, stdout.String())
	}
	for _, absent := range []string{"affected_row_count", "rows_read", "rows_written", "query_duration_ms"} {
		if _, ok := envelope.Results[0][absent]; ok {
			t.Fatalf("failed result invented %s: %s", absent, stdout.String())
		}
	}
}

func TestSplitSQLForExecutionAllowsTrailingLineCommentWithoutSemicolon(t *testing.T) {
	statements, err := splitSQLForExecution("select 1 -- trailing comment")
	if err != nil {
		t.Fatal(err)
	}
	if len(statements) != 1 || !strings.Contains(statements[0].SQL, "select 1 -- trailing comment") {
		t.Fatalf("statements = %#v", statements)
	}
}

func TestDatabasesExecuteReturnsNonZeroWhenAPIRollsBackOpenTransaction(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COMWIT_CONFIG", filepath.Join(dir, "config.json"))
	if _, err := saveConfig(configFile{Token: "test-token", DefaultProject: "proj_1"}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"status":422,"detail":"database query left an open transaction; changes were rolled back"}`))
	}))
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	var stdout bytes.Buffer
	err := run([]string{"databases", "execute", "--database", "db_1", "--command", "BEGIN; INSERT INTO notes VALUES (1);"}, &stdout, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "HTTP 422") || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("error = %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestDatabasesExecuteValidatesInputMode(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COMWIT_CONFIG", filepath.Join(dir, "config.json"))
	if _, err := saveConfig(configFile{Token: "test-token", DefaultProject: "proj_1"}); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"databases", "execute", "--database", "db_1"},
		{"databases", "execute", "--database", "db_1", "--command", "select 1", "--file", "query.sql"},
	} {
		err := run(args, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "--command <sql>|--file <path>") {
			t.Fatalf("args=%v error=%v", args, err)
		}
	}

	err := run([]string{"databases", "execute", "--database", "db_1", "--command", "select 1", "--local"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "remote by default") {
		t.Fatalf("local error = %v", err)
	}
}

func TestLoadExecuteSQLRejectsOversizedFileBeforeParsing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized.sql")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), maxExecuteSQLBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadExecuteSQL("", path)
	if err == nil || !strings.Contains(err.Error(), "request limit") {
		t.Fatalf("error = %v", err)
	}
}

func TestPrintableQueryTextEscapesTerminalControls(t *testing.T) {
	input := "safe\x1b[31mred\x00\x7f\u0085\nnext"
	got := printableQueryText(input)
	if strings.ContainsRune(got, '\x1b') || strings.ContainsRune(got, '\x00') || strings.ContainsRune(got, '\x7f') || strings.ContainsRune(got, '\u0085') {
		t.Fatalf("raw terminal control remained in %q", got)
	}
	for _, escaped := range []string{`\x1b`, `\x00`, `\x7f`, `\x85`, `\n`} {
		if !strings.Contains(got, escaped) {
			t.Fatalf("output missing %q: %q", escaped, got)
		}
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
		case r.Method == http.MethodGet && r.URL.Path == "/repos/burrr-ai/comwit-cli/releases/latest":
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
		if r.Method != http.MethodGet || r.URL.Path != "/repos/burrr-ai/comwit-cli/releases/tags/v1.2.3" {
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
	if got, want := mustGitHubReleasePath(t, "burrr-ai/comwit-cli", "latest"), "/repos/burrr-ai/comwit-cli/releases/latest"; got != want {
		t.Fatalf("latest path = %q, want %q", got, want)
	}
	if got, want := mustGitHubReleasePath(t, "burrr-ai/comwit-cli", "v1.2.3"), "/repos/burrr-ai/comwit-cli/releases/tags/v1.2.3"; got != want {
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

func TestDatabasesRestoreFlow(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COMWIT_CONFIG", filepath.Join(dir, "config.json"))
	if _, err := saveConfig(configFile{Token: "test-token", DefaultProject: "proj_1"}); err != nil {
		t.Fatal(err)
	}

	var sawSelector map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/projects/proj_1/databases/db_1/restore-points":
			_, _ = w.Write([]byte(`{"restore_points":[{"generation_id":"gen-1","created_at_ms":1000,"base_frame_no":0,"pinned":false,"precise_until_ms":null}],"aliases":[{"alias":"nightly","target_kind":"generation","target_value":"gen-1","created_at_ms":1000}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/projects/proj_1/databases/db_1/restore":
			var body struct {
				RestoreTo map[string]any `json:"restore_to"`
				Name      string         `json:"name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			sawSelector = body.RestoreTo
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"operation":{"operation_id":"op_1","type":"database_restore","status":"pending","resolved_restore_point":"gen-1"},"database":{"database_id":"db_1-pitr-x","name":"before","database_url":"https://db.example.test/v1/db_1-pitr-x","status":"creating"},"database_token":"restored-token"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/projects/proj_1/databases/db_1/operations/op_1":
			_, _ = w.Write([]byte(`{"operation":{"operation_id":"op_1","type":"database_restore","status":"succeeded","resolved_restore_point":"gen-1"}}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	var pointsOut bytes.Buffer
	if err := run([]string{"databases", "restore-points", "list", "--database", "db_1"}, &pointsOut, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pointsOut.String(), "gen-1") || !strings.Contains(pointsOut.String(), "up to now") {
		t.Fatalf("restore-points stdout = %q", pointsOut.String())
	}

	tokenPath := filepath.Join(dir, "token.txt")
	var restoreOut bytes.Buffer
	if err := run([]string{"databases", "restore", "--database", "db_1", "--at", "1500", "--name", "before", "--token-out", tokenPath, "--wait"}, &restoreOut, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if ms, ok := sawSelector["timestamp_ms"]; !ok || ms.(float64) != 1500 {
		t.Fatalf("selector = %+v", sawSelector)
	}
	out := restoreOut.String()
	for _, want := range []string{"op_1", "db_1-pitr-x", "restored-token", "succeeded"} {
		if !strings.Contains(out, want) {
			t.Fatalf("restore stdout missing %q: %q", want, out)
		}
	}
	tokenData, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(tokenData)) != "restored-token" {
		t.Fatalf("token file = %q", string(tokenData))
	}
}

func TestWriteTokenOutRestrictsExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.txt")
	if err := os.WriteFile(path, []byte("old-token\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeTokenOut(path, "new-token"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("token file mode = %04o, want 0600", got)
	}
}

func TestDatabasesRestoreRequiresExactlyOneSelector(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COMWIT_CONFIG", filepath.Join(dir, "config.json"))
	if _, err := saveConfig(configFile{Token: "test-token", DefaultProject: "proj_1"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMWIT_API_URL", "http://127.0.0.1:1")
	err := run([]string{"databases", "restore", "--database", "db_1"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("err = %v, want exactly-one selector error", err)
	}
}

func TestDatabasesRestoreRejectsFutureTimestamp(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COMWIT_CONFIG", filepath.Join(dir, "config.json"))
	if _, err := saveConfig(configFile{Token: "test-token", DefaultProject: "proj_1"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMWIT_API_URL", "http://127.0.0.1:1")
	future := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	err := run([]string{"databases", "restore", "--database", "db_1", "--at", future}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "future") {
		t.Fatalf("err = %v, want future timestamp error", err)
	}
}
