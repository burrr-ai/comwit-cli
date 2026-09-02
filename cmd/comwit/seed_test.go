package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDatabasesCreateWithoutFileKeepsSynchronousRequest(t *testing.T) {
	configureSeedTestCLI(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/projects/proj_1/databases" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if key := r.Header.Get("Idempotency-Key"); key != "" {
			t.Errorf("legacy create unexpectedly set Idempotency-Key %q", key)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode create body: %v", err)
		}
		if len(body) != 1 || body["name"] != "plain" {
			t.Errorf("create body = %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"database_id":"db_plain","database_url":"https://db.example/v1/db_plain","created":true,"database_token":"plain-token"}`)
	}))
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	var stdout bytes.Buffer
	if err := run([]string{"databases", "create", "--name", "plain"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); got != "db_plain\thttps://db.example/v1/db_plain\tcreated=true\ntoken\tplain-token\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestDatabasesCreateFromFileRunsThreeStepWorkflow(t *testing.T) {
	configureSeedTestCLI(t)
	useFastSeedTimings(t)
	fixture := createSQLiteSeedFixture(t, (1<<20)+8192)
	wantBytes, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest := sha256.Sum256(wantBytes)

	var mu sync.Mutex
	var postBodies [][]byte
	var idempotencyKeys []string
	var uploaded []byte
	var putContentLength int64
	var putTransferEncoding []string
	var operationGets int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/projects/proj_1/databases":
			body, readErr := io.ReadAll(r.Body)
			if readErr != nil {
				t.Errorf("read POST body: %v", readErr)
			}
			mu.Lock()
			postBodies = append(postBodies, body)
			idempotencyKeys = append(idempotencyKeys, r.Header.Get("Idempotency-Key"))
			attempt := len(postBodies)
			mu.Unlock()
			if attempt == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `{"code":"RUNNER_POOL_UNAVAILABLE"}`)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = fmt.Fprintf(w, `{"database_id":"db_seed","database_url":"%s/v1/db_seed","created":true,"database_token":"one-time-token","operation":{"operation_id":"seed_1","type":"database_seed","status":"awaiting_upload"},"upload_path":"/v1/projects/proj_1/databases/db_seed/operations/seed_1/content"}`, serverURL(r))
		case r.Method == http.MethodPut && r.URL.Path == "/v1/projects/proj_1/databases/db_seed/operations/seed_1/content":
			body, readErr := io.ReadAll(r.Body)
			if readErr != nil {
				t.Errorf("read PUT body: %v", readErr)
			}
			mu.Lock()
			uploaded = append([]byte(nil), body...)
			putContentLength = r.ContentLength
			putTransferEncoding = append([]string(nil), r.TransferEncoding...)
			mu.Unlock()
			if got := r.Header.Get("Content-Type"); got != "application/vnd.sqlite3" {
				t.Errorf("PUT Content-Type = %q", got)
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"operation":{"operation_id":"seed_1","type":"database_seed","status":"queued"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/projects/proj_1/databases/db_seed/operations/seed_1":
			mu.Lock()
			operationGets++
			mu.Unlock()
			_, _ = io.WriteString(w, `{"operation":{"operation_id":"seed_1","type":"database_seed","status":"succeeded"}}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	tokenPath := filepath.Join(t.TempDir(), "database.token")
	var stdout, stderr bytes.Buffer
	err = run([]string{"databases", "create", "--name", "seeded", "--from-file", fixture, "--token-out", tokenPath}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(postBodies) != 2 {
		t.Fatalf("POST attempts = %d, want 2", len(postBodies))
	}
	if len(idempotencyKeys) != 2 || idempotencyKeys[0] == "" || idempotencyKeys[0] != idempotencyKeys[1] {
		t.Fatalf("Idempotency-Key values = %#v", idempotencyKeys)
	}
	assertUUIDv4(t, idempotencyKeys[0])
	if !bytes.Equal(postBodies[0], postBodies[1]) {
		t.Fatal("retried POST body changed")
	}
	var createRequest databaseSeedCreateRequest
	if err := json.Unmarshal(postBodies[1], &createRequest); err != nil {
		t.Fatal(err)
	}
	if createRequest.Name != "seeded" || createRequest.Source.Type != "sqlite_file" {
		t.Fatalf("create request = %+v", createRequest)
	}
	if createRequest.Source.ContentLength != int64(len(wantBytes)) {
		t.Fatalf("source content_length = %d, want %d", createRequest.Source.ContentLength, len(wantBytes))
	}
	if createRequest.Source.SHA256 != hex.EncodeToString(wantDigest[:]) {
		t.Fatalf("source sha256 = %q, want %x", createRequest.Source.SHA256, wantDigest)
	}
	if putContentLength != int64(len(wantBytes)) {
		t.Fatalf("PUT Content-Length = %d, want %d", putContentLength, len(wantBytes))
	}
	if len(putTransferEncoding) != 0 {
		t.Fatalf("PUT Transfer-Encoding = %#v, want none", putTransferEncoding)
	}
	if !bytes.Equal(uploaded, wantBytes) {
		t.Fatalf("PUT bytes differ: got %d bytes, want %d", len(uploaded), len(wantBytes))
	}
	if operationGets != 1 {
		t.Fatalf("operation GETs = %d, want 1", operationGets)
	}
	for _, want := range []string{"database\tdb_seed", "url\t" + server.URL + "/v1/db_seed", "token\tone-time-token", "status\tawaiting_upload", "status\tqueued", "status\tsucceeded", "ready\tdb_seed"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q: %q", want, stdout.String())
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("non-TTY stderr = %q, want no progress output", stderr.String())
	}
	token, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(token) != "one-time-token\n" {
		t.Fatalf("token file = %q", token)
	}
	info, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("token mode = %04o, want 0600", info.Mode().Perm())
	}
	if newSeedUploadHTTPClient().Timeout != 0 {
		t.Fatal("upload client must not have a total timeout")
	}
}

func TestDatabasesCreateFromDumpConvertsAndRunsThreeStepWorkflow(t *testing.T) {
	configureSeedTestCLI(t)
	useFastSeedTimings(t)
	dumpPath := createSeedDumpFixture(t, `
PRAGMA defer_foreign_keys=TRUE;
PRAGMA foreign_keys=OFF;
BEGIN TRANSACTION;
CREATE TABLE parent (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL);
CREATE TABLE child (id INTEGER PRIMARY KEY, parent_id INTEGER NOT NULL REFERENCES parent(id));
INSERT INTO child(id, parent_id) VALUES (10, 1);
INSERT INTO parent(id, name) VALUES (1, 'one'), (2, 'two'), (3, 'three');
DELETE FROM sqlite_sequence;
INSERT INTO sqlite_sequence(name, seq) VALUES ('parent', 100);
COMMIT;
`)
	sqliteOut := filepath.Join(t.TempDir(), "converted.sqlite")

	var mu sync.Mutex
	var uploaded []byte
	var createRequest databaseSeedCreateRequest
	var operationGets int
	server := newSeedWorkflowServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			var request databaseSeedCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode create request: %v", err)
			}
			mu.Lock()
			createRequest = request
			mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"database_id":"db_seed","database_url":"https://db.example/v1/db_seed","created":true,"database_token":"one-time-token","operation":{"operation_id":"seed_1","type":"database_seed","status":"awaiting_upload"},"upload_path":"/v1/projects/proj_1/databases/db_seed/operations/seed_1/content"}`)
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read upload: %v", err)
			}
			mu.Lock()
			uploaded = append([]byte(nil), body...)
			mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"operation":{"operation_id":"seed_1","type":"database_seed","status":"queued"}}`)
		case http.MethodGet:
			mu.Lock()
			operationGets++
			mu.Unlock()
			_, _ = io.WriteString(w, `{"operation":{"operation_id":"seed_1","type":"database_seed","status":"succeeded"}}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	var stdout, stderr bytes.Buffer
	err := run([]string{"databases", "create", "--name", "seeded", "--from-dump", dumpPath, "--sqlite-out", sqliteOut}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	wantBytes, err := os.ReadFile(sqliteOut)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	gotUpload := append([]byte(nil), uploaded...)
	gotOperationGets := operationGets
	gotCreateRequest := createRequest
	mu.Unlock()
	if gotCreateRequest.Name != "seeded" || gotCreateRequest.Source.Type != "sqlite_file" {
		t.Fatalf("create request = %+v", gotCreateRequest)
	}
	if !bytes.Equal(gotUpload, wantBytes) {
		t.Fatalf("uploaded %d bytes, want converted file's %d bytes", len(gotUpload), len(wantBytes))
	}
	if gotCreateRequest.Source.ContentLength != int64(len(wantBytes)) {
		t.Fatalf("source content_length = %d, want %d", gotCreateRequest.Source.ContentLength, len(wantBytes))
	}
	if gotOperationGets != 1 {
		t.Fatalf("operation GETs = %d, want 1", gotOperationGets)
	}
	if _, err := preflightSQLiteSeedFile(sqliteOut, false); err != nil {
		t.Fatalf("converted file failed preflight: %v", err)
	}
	assertSQLiteRowCount(t, sqliteOut, "parent", 3)
	assertSQLiteRowCount(t, sqliteOut, "child", 1)
	dsn, err := sqliteFileDSN(sqliteOut, nil)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	var sequence int
	if err := db.QueryRow("SELECT seq FROM sqlite_sequence WHERE name = 'parent'").Scan(&sequence); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if sequence != 100 {
		t.Fatalf("sqlite_sequence seq = %d, want 100", sequence)
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(sqliteOut + suffix); !os.IsNotExist(err) {
			t.Fatalf("unexpected converted-file sidecar %s: %v", sqliteOut+suffix, err)
		}
	}
	for _, want := range []string{
		"convert\t7 statements",
		"convert_skipped\t3 statements",
		"sqlite_out\t" + sqliteOut,
		"database\tdb_seed",
		"status\tsucceeded",
		"ready\tdb_seed",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q: %q", want, stdout.String())
		}
	}
	if !strings.Contains(stderr.String(), "skipped\t3") || !strings.Contains(stderr.String(), "ignored transaction control statement") || strings.Contains(stderr.String(), "ignored sqlite_sequence maintenance statement") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestConvertDumpSkipsMissingSQLiteSequenceTable(t *testing.T) {
	dumpPath := createSeedDumpFixture(t, `
CREATE TABLE t(id INTEGER PRIMARY KEY, value TEXT);
DELETE FROM sqlite_sequence;
`)
	sqliteOut := filepath.Join(t.TempDir(), "converted.sqlite")
	var stdout, stderr bytes.Buffer
	if _, err := convertDumpToSQLite(dumpPath, sqliteOut, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	assertSQLiteRowCount(t, sqliteOut, "t", 0)
	if !strings.Contains(stdout.String(), "convert\t1 statements") || !strings.Contains(stdout.String(), "convert_skipped\t1 statements") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	for _, want := range []string{"skipped\t1", "skipped_statement\t2", "sqlite_sequence does not exist"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr missing %q: %q", want, stderr.String())
		}
	}
}

func TestFilterSQLiteSeedDumpStatementsTreatsEndAsTransactionControl(t *testing.T) {
	for _, sql := range []string{"END;", "END TRANSACTION;"} {
		t.Run(sql, func(t *testing.T) {
			statements := []dumpStatement{
				{Index: 1, SQL: "CREATE TABLE kept(id INTEGER);"},
				{Index: 2, SQL: sql},
			}
			filtered, skipped := filterSQLiteSeedDumpStatements(statements, nil)
			if len(filtered) != 1 || filtered[0].Index != 1 {
				t.Fatalf("filtered = %#v", filtered)
			}
			if len(skipped) != 1 || skipped[0].Index != 2 {
				t.Fatalf("skipped = %#v", skipped)
			}
		})
	}
}

func TestBuildDumpImportBatchesDoesNotTreatStandaloneEndAsTransaction(t *testing.T) {
	statements := []dumpStatement{
		{Index: 1, SQL: "END;"},
		{Index: 2, SQL: "SELECT 1;"},
	}
	if dumpContainsTransaction(statements) {
		t.Fatal("standalone END must not make import-dump transaction-wrapped")
	}
	batches, err := buildDumpImportBatches(statements, defaultImportMaxRequestBytes, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 2 {
		t.Fatalf("batches = %d, want 2", len(batches))
	}
}

func TestDatabasesCreateFromDumpValidatesFlagsAndInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "mutually exclusive inputs",
			args: []string{"databases", "create", "--name", "seeded", "--from-file", "source.sqlite", "--from-dump", "dump.sql"},
			want: "--from-file and --from-dump are mutually exclusive",
		},
		{
			name: "sqlite out requires dump",
			args: []string{"databases", "create", "--name", "seeded", "--sqlite-out", "converted.sqlite"},
			want: "--sqlite-out requires --from-dump",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := run(tc.args, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}

	configureSeedTestCLI(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	sqlitePath := createSQLiteSeedFixture(t, 0)
	err := run([]string{"databases", "create", "--name", "seeded", "--from-dump", sqlitePath}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "`comwit databases create --from-file <path>`") {
		t.Fatalf("SQLite --from-dump error = %v", err)
	}

	dumpPath := createSeedDumpFixture(t, "CREATE TABLE kept (id INTEGER PRIMARY KEY);")
	sqliteOut := filepath.Join(t.TempDir(), "existing.sqlite")
	if err := os.WriteFile(sqliteOut, []byte("do not overwrite"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = run([]string{"databases", "create", "--name", "seeded", "--from-dump", dumpPath, "--sqlite-out", sqliteOut}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "already exists; refusing to overwrite") {
		t.Fatalf("existing --sqlite-out error = %v", err)
	}
	contents, readErr := os.ReadFile(sqliteOut)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(contents) != "do not overwrite" {
		t.Fatalf("existing --sqlite-out was changed: %q", contents)
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid inputs made %d API requests", requests.Load())
	}
}

func TestDatabasesCreateRejectsCollidingSeedPaths(t *testing.T) {
	configureSeedTestCLI(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	t.Run("token out and from dump", func(t *testing.T) {
		dumpPath := createSeedDumpFixture(t, "CREATE TABLE kept(id INTEGER PRIMARY KEY);")
		wantDump, err := os.ReadFile(dumpPath)
		if err != nil {
			t.Fatal(err)
		}
		sqliteOut := filepath.Join(t.TempDir(), "converted.sqlite")
		err = run([]string{"databases", "create", "--name", "seeded", "--from-dump", dumpPath, "--sqlite-out", sqliteOut, "--token-out", dumpPath}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "--token-out must not overwrite --from-dump") {
			t.Fatalf("error = %v", err)
		}
		gotDump, readErr := os.ReadFile(dumpPath)
		if readErr != nil || !bytes.Equal(gotDump, wantDump) {
			t.Fatalf("dump changed: read=%v got=%q want=%q", readErr, gotDump, wantDump)
		}
		if _, statErr := os.Stat(sqliteOut); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("--sqlite-out was created: %v", statErr)
		}
	})

	t.Run("token out and sqlite out", func(t *testing.T) {
		dumpPath := createSeedDumpFixture(t, "CREATE TABLE kept(id INTEGER PRIMARY KEY);")
		sqliteOut := filepath.Join(t.TempDir(), "converted.sqlite")
		err := run([]string{"databases", "create", "--name", "seeded", "--from-dump", dumpPath, "--sqlite-out", sqliteOut, "--token-out", sqliteOut}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "--token-out must not overwrite --sqlite-out") {
			t.Fatalf("error = %v", err)
		}
		if _, statErr := os.Stat(sqliteOut); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("--sqlite-out was created: %v", statErr)
		}
	})

	t.Run("token out and from file", func(t *testing.T) {
		fromFile := createSQLiteSeedFixture(t, 0)
		wantFile, err := os.ReadFile(fromFile)
		if err != nil {
			t.Fatal(err)
		}
		err = run([]string{"databases", "create", "--name", "seeded", "--from-file", fromFile, "--token-out", fromFile}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "--token-out must not overwrite --from-file") {
			t.Fatalf("error = %v", err)
		}
		gotFile, readErr := os.ReadFile(fromFile)
		if readErr != nil || !bytes.Equal(gotFile, wantFile) {
			t.Fatalf("SQLite file changed: read=%v got=%d bytes want=%d", readErr, len(gotFile), len(wantFile))
		}
	})

	t.Run("sqlite out and from dump", func(t *testing.T) {
		dumpPath := createSeedDumpFixture(t, "CREATE TABLE kept(id INTEGER PRIMARY KEY);")
		wantDump, err := os.ReadFile(dumpPath)
		if err != nil {
			t.Fatal(err)
		}
		err = run([]string{"databases", "create", "--name", "seeded", "--from-dump", dumpPath, "--sqlite-out", dumpPath}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "--sqlite-out must not overwrite --from-dump") {
			t.Fatalf("error = %v", err)
		}
		gotDump, readErr := os.ReadFile(dumpPath)
		if readErr != nil || !bytes.Equal(gotDump, wantDump) {
			t.Fatalf("dump changed: read=%v got=%q want=%q", readErr, gotDump, wantDump)
		}
	})

	t.Run("same file alias", func(t *testing.T) {
		dumpPath := createSeedDumpFixture(t, "CREATE TABLE kept(id INTEGER PRIMARY KEY);")
		tokenPath := filepath.Join(t.TempDir(), "dump-hard-link.sql")
		if err := os.Link(dumpPath, tokenPath); err != nil {
			t.Fatal(err)
		}
		sqliteOut := filepath.Join(t.TempDir(), "converted.sqlite")
		err := run([]string{"databases", "create", "--name", "seeded", "--from-dump", dumpPath, "--sqlite-out", sqliteOut, "--token-out", tokenPath}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "--token-out must not overwrite --from-dump") {
			t.Fatalf("error = %v", err)
		}
		if _, statErr := os.Stat(sqliteOut); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("--sqlite-out was created: %v", statErr)
		}
	})

	if requests.Load() != 0 {
		t.Fatalf("colliding paths made %d API requests", requests.Load())
	}
}

func TestDatabasesCreateFromDumpValidatesSQLiteOutBeforeReadingDump(t *testing.T) {
	configureSeedTestCLI(t)
	missingDump := filepath.Join(t.TempDir(), "missing.sql")

	existingOut := filepath.Join(t.TempDir(), "existing.sqlite")
	if err := os.WriteFile(existingOut, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"databases", "create", "--name", "seeded", "--from-dump", missingDump, "--sqlite-out", existingOut}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "already exists; refusing to overwrite") {
		t.Fatalf("existing output error = %v", err)
	}

	missingParentOut := filepath.Join(t.TempDir(), "missing", "converted.sqlite")
	err = run([]string{"databases", "create", "--name", "seeded", "--from-dump", missingDump, "--sqlite-out", missingParentOut}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--sqlite-out parent directory") {
		t.Fatalf("missing parent error = %v", err)
	}
}

func TestDatabasesCreateFromDumpRejectsBadIdempotencyKeyBeforeConversion(t *testing.T) {
	configureSeedTestCLI(t)
	dumpPath := createSeedDumpFixture(t, "CREATE TABLE kept(id INTEGER PRIMARY KEY);")
	sqliteOut := filepath.Join(t.TempDir(), "converted.sqlite")
	err := run([]string{"databases", "create", "--name", "seeded", "--from-dump", dumpPath, "--sqlite-out", sqliteOut, "--idempotency-key", "bad\nkey"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "printable ASCII") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(sqliteOut); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("--sqlite-out was created: %v", statErr)
	}
}

func TestConvertDumpRejectsEmptyDatabase(t *testing.T) {
	dumpPath := createSeedDumpFixture(t, "PRAGMA foreign_keys=OFF;")
	sqliteOut := filepath.Join(t.TempDir(), "converted.sqlite")
	_, err := convertDumpToSQLite(dumpPath, sqliteOut, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || err.Error() != "dump produced an empty database (no tables)" {
		t.Fatalf("error = %v", err)
	}
}

func TestSQLiteSeedPreflightUsesSourceNeutralWording(t *testing.T) {
	_, err := preflightSQLiteSeedFile(filepath.Join(t.TempDir(), "missing.sqlite"), false)
	if err == nil || !strings.Contains(err.Error(), "the SQLite file") || strings.Contains(err.Error(), "--from-file") {
		t.Fatalf("error = %v", err)
	}
}

func TestDatabasesCreateFromDumpRemovesTemporaryFileOnSuccessAndFailure(t *testing.T) {
	configureSeedTestCLI(t)
	useFastSeedTimings(t)
	tempRoot := t.TempDir()
	t.Setenv("TMPDIR", tempRoot)

	server := newSeedWorkflowServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"database_id":"db_seed","database_url":"https://db.example/v1/db_seed","created":true,"database_token":"one-time-token","operation":{"operation_id":"seed_1","type":"database_seed","status":"awaiting_upload"},"upload_path":"/v1/projects/proj_1/databases/db_seed/operations/seed_1/content"}`)
		case http.MethodPut:
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"operation":{"operation_id":"seed_1","type":"database_seed","status":"queued"}}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	successDump := createSeedDumpFixture(t, "CREATE TABLE success (id INTEGER PRIMARY KEY);")
	if err := run([]string{"databases", "create", "--name", "seeded", "--from-dump", successDump, "--no-wait"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	assertNoTemporarySeedFiles(t, tempRoot)

	failingDump := createSeedDumpFixture(t, `
PRAGMA defer_foreign_keys=TRUE;
BEGIN TRANSACTION;
CREATE TABLE rolled_back (id INTEGER PRIMARY KEY);
INSERT INTO missing_table VALUES (1);
COMMIT;
`)
	var stderr bytes.Buffer
	err := run([]string{"databases", "create", "--name", "failed", "--from-dump", failingDump}, &bytes.Buffer{}, &stderr)
	if err == nil || !strings.Contains(err.Error(), "statement 4 failed") || !strings.Contains(err.Error(), "INSERT INTO missing_table") {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(err.Error(), "temporary converted SQLite file was removed") || !strings.Contains(err.Error(), "--sqlite-out") {
		t.Fatalf("error lacks inspection hint: %v", err)
	}
	assertNoTemporarySeedFiles(t, tempRoot)

	keptPath := filepath.Join(t.TempDir(), "failed.sqlite")
	var stdout bytes.Buffer
	err = run([]string{"databases", "create", "--name", "failed", "--from-dump", failingDump, "--sqlite-out", keptPath}, &stdout, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "statement 4 failed") {
		t.Fatalf("kept conversion error = %v", err)
	}
	if !strings.Contains(stdout.String(), "sqlite_out\t"+keptPath) {
		t.Fatalf("stdout = %q", stdout.String())
	}
	dsn, dsnErr := sqliteFileDSN(keptPath, nil)
	if dsnErr != nil {
		t.Fatal(dsnErr)
	}
	db, openErr := sql.Open("sqlite", dsn)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer db.Close()
	var tables int
	if queryErr := db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name = 'rolled_back'").Scan(&tables); queryErr != nil {
		t.Fatal(queryErr)
	}
	if tables != 0 {
		t.Fatalf("failed conversion committed %d rolled_back tables", tables)
	}
}

func TestDatabasesCreateFromDumpForeignKeyViolationFailsPreflight(t *testing.T) {
	configureSeedTestCLI(t)
	tempRoot := t.TempDir()
	t.Setenv("TMPDIR", tempRoot)
	dumpPath := createSeedDumpFixture(t, `
CREATE TABLE parent (id INTEGER PRIMARY KEY);
CREATE TABLE child (parent_id INTEGER REFERENCES parent(id));
INSERT INTO child(parent_id) VALUES (42);
`)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	err := run([]string{"databases", "create", "--name", "invalid-fk", "--from-dump", dumpPath}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.HasPrefix(err.Error(), "local SQLite foreign_key_check failed: the database contains foreign key violations") {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(err.Error(), "temporary converted SQLite file was removed") {
		t.Fatalf("error lacks temporary-file cleanup detail: %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("foreign-key preflight made %d API requests", requests.Load())
	}
	assertNoTemporarySeedFiles(t, tempRoot)
}

func TestSQLiteFileDSNUsesWindowsFileURL(t *testing.T) {
	dsn := sqliteFileDSNFromAbsolutePath("C:/seed files/app.sqlite", map[string][]string{"mode": {"ro"}})
	if dsn != "file:///C:/seed%20files/app.sqlite?mode=ro" {
		t.Fatalf("DSN = %q", dsn)
	}
}

func BenchmarkConvertDump500K(b *testing.B) {
	const insertCount = 500_000
	directory := b.TempDir()
	dumpPath := filepath.Join(directory, "synthetic-500k.sql")
	dump, err := os.OpenFile(dumpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		b.Fatal(err)
	}
	writer := bufio.NewWriterSize(dump, 1<<20)
	if _, err := io.WriteString(writer, "BEGIN TRANSACTION;\nCREATE TABLE rows_0 (id INTEGER PRIMARY KEY, value TEXT);\nCREATE TABLE rows_1 (id INTEGER PRIMARY KEY, value TEXT);\nCREATE TABLE rows_2 (id INTEGER PRIMARY KEY, value TEXT);\n"); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < insertCount; i++ {
		if _, err := fmt.Fprintf(writer, "INSERT INTO rows_%d VALUES (%d, 'value-%d');\n", i%3, i, i); err != nil {
			b.Fatal(err)
		}
	}
	if _, err := io.WriteString(writer, "COMMIT;\n"); err != nil {
		b.Fatal(err)
	}
	if err := writer.Flush(); err != nil {
		b.Fatal(err)
	}
	if err := dump.Close(); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sqliteOut := filepath.Join(directory, fmt.Sprintf("converted-%d.sqlite", i))
		converted, err := convertDumpToSQLite(dumpPath, sqliteOut, io.Discard, io.Discard)
		if err != nil {
			b.Fatal(err)
		}
		if err := removeConvertedSQLiteFiles(converted.Path); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64((insertCount+3)*b.N)/b.Elapsed().Seconds(), "statements/s")
}

func BenchmarkConvertDumpFile(b *testing.B) {
	dumpPath := os.Getenv("COMWIT_BENCH_DUMP")
	if dumpPath == "" {
		b.Skip("set COMWIT_BENCH_DUMP to benchmark a real SQL dump")
	}
	info, err := os.Stat(dumpPath)
	if err != nil {
		b.Fatal(err)
	}
	directory := b.TempDir()
	convertedFiles := make([]convertedSQLiteSeedFile, 0, b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sqliteOut := filepath.Join(directory, fmt.Sprintf("converted-%d.sqlite", i))
		converted, err := convertDumpToSQLite(dumpPath, sqliteOut, io.Discard, io.Discard)
		if err != nil {
			b.Fatal(err)
		}
		convertedFiles = append(convertedFiles, converted)
	}
	b.StopTimer()
	elapsed := b.Elapsed()
	for _, converted := range convertedFiles {
		if _, err := preflightSQLiteSeedFile(converted.Path, false); err != nil {
			b.Fatalf("converted file failed preflight: %v", err)
		}
		if referencePath := os.Getenv("COMWIT_BENCH_REFERENCE"); referencePath != "" {
			want, err := sqliteTableRowCounts(referencePath)
			if err != nil {
				b.Fatalf("read reference row counts: %v", err)
			}
			got, err := sqliteTableRowCounts(converted.Path)
			if err != nil {
				b.Fatalf("read converted row counts: %v", err)
			}
			if !maps.Equal(got, want) {
				b.Fatalf("converted row counts differ from reference:\ngot  %v\nwant %v", got, want)
			}
		}
		if err := removeConvertedSQLiteFiles(converted.Path); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(info.Size()*int64(b.N))/(1024*1024)/elapsed.Seconds(), "MiB/s")
}

func sqliteTableRowCounts(path string) (map[string]int64, error) {
	dsn, err := sqliteFileDSN(path, nil)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query("SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name != '_cf_METADATA' ORDER BY name")
	if err != nil {
		return nil, err
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			_ = rows.Close()
			return nil, err
		}
		tables = append(tables, table)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	counts := make(map[string]int64, len(tables))
	for _, table := range tables {
		quotedTable := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
		var count int64
		if err := db.QueryRow("SELECT count(*) FROM " + quotedTable).Scan(&count); err != nil {
			return nil, fmt.Errorf("count rows in %s: %w", table, err)
		}
		counts[table] = count
	}
	return counts, nil
}

func TestDatabasesCreateFromFilePreservesAPIURLPathPrefix(t *testing.T) {
	configureSeedTestCLI(t)
	useFastSeedTimings(t)
	fixture := createSQLiteSeedFixture(t, 0)

	var mu sync.Mutex
	var requests []string
	server := newSeedWorkflowServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/proxy/v1/projects/proj_1/databases":
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"database_id":"db_seed","database_url":"https://db.example/v1/db_seed","created":true,"database_token":"one-time-token","operation":{"operation_id":"seed_1","type":"database_seed","status":"awaiting_upload"},"upload_path":"/v1/projects/proj_1/databases/db_seed/operations/seed_1/content"}`)
		case r.Method == http.MethodPut && r.URL.Path == "/proxy/v1/projects/proj_1/databases/db_seed/operations/seed_1/content":
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"operation":{"operation_id":"seed_1","type":"database_seed","status":"queued"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/proxy/v1/projects/proj_1/databases/db_seed/operations/seed_1":
			_, _ = io.WriteString(w, `{"operation":{"operation_id":"seed_1","type":"database_seed","status":"succeeded"}}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL+"/proxy")

	if err := run([]string{"databases", "create", "--name", "seeded", "--from-file", fixture}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got, want := strings.Join(requests, ","), "POST /proxy/v1/projects/proj_1/databases,PUT /proxy/v1/projects/proj_1/databases/db_seed/operations/seed_1/content,GET /proxy/v1/projects/proj_1/databases/db_seed/operations/seed_1"; got != want {
		t.Fatalf("requests = %q, want %q", got, want)
	}
}

func TestDatabasesCreateFromFileFailedOperationUsesMappedMessage(t *testing.T) {
	tests := []struct {
		name        string
		code        string
		wantMessage string
	}{
		{
			name:        "integrity check failed",
			code:        "SEED_INTEGRITY_CHECK_FAILED",
			wantMessage: "failed its integrity check",
		},
		{
			name:        "executor lost",
			code:        "SEED_EXECUTOR_LOST",
			wantMessage: "the server worker handling the upload was lost before the file was stored; run the command again with a new idempotency key",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			configureSeedTestCLI(t)
			fixture := createSQLiteSeedFixture(t, 0)
			server := newSeedWorkflowServer(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodPost:
					w.WriteHeader(http.StatusAccepted)
					_, _ = io.WriteString(w, `{"database_id":"db_seed","database_url":"https://db.example/v1/db_seed","created":true,"database_token":"one-time-token","operation":{"operation_id":"seed_1","type":"database_seed","status":"awaiting_upload"},"upload_path":"/v1/projects/proj_1/databases/db_seed/operations/seed_1/content"}`)
				case http.MethodPut:
					_, _ = io.Copy(io.Discard, r.Body)
					w.WriteHeader(http.StatusAccepted)
					_, _ = io.WriteString(w, `{"operation":{"operation_id":"seed_1","type":"database_seed","status":"queued"}}`)
				case http.MethodGet:
					_, _ = fmt.Fprintf(w, `{"operation":{"operation_id":"seed_1","type":"database_seed","status":"failed","error":{"code":%q,"message":"validation failed"}}}`, tc.code)
				}
			})
			defer server.Close()
			t.Setenv("COMWIT_API_URL", server.URL)

			var stdout bytes.Buffer
			err := run([]string{"databases", "create", "--name", "seeded", "--from-file", fixture}, &stdout, &bytes.Buffer{})
			if err == nil {
				t.Fatal("expected failed operation error")
			}
			if !strings.Contains(err.Error(), tc.code) || !strings.Contains(err.Error(), tc.wantMessage) {
				t.Fatalf("error = %v", err)
			}
			if !strings.Contains(stdout.String(), "token\tone-time-token") || !strings.Contains(stdout.String(), "status\tfailed") {
				t.Fatalf("stdout = %q", stdout.String())
			}
		})
	}
}

func TestDatabasesCreateFromFileSurfacesProblemDetailsWithoutSeedCode(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		detail     string
		notContain string
	}{
		{
			name:   "idempotency conflict",
			status: http.StatusConflict,
			detail: "Idempotency-Key was already used with a different database name",
		},
		{
			name:       "create validation failure",
			status:     http.StatusUnprocessableEntity,
			detail:     "validation failed",
			notContain: "SHA-256",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			configureSeedTestCLI(t)
			fixture := createSQLiteSeedFixture(t, 0)
			server := newSeedWorkflowServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"title":  http.StatusText(tc.status),
					"status": tc.status,
					"detail": tc.detail,
				})
			})
			defer server.Close()
			t.Setenv("COMWIT_API_URL", server.URL)

			err := run([]string{"databases", "create", "--name", "seeded", "--from-file", fixture}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tc.detail) {
				t.Fatalf("error = %v, want detail %q", err, tc.detail)
			}
			if tc.notContain != "" && strings.Contains(err.Error(), tc.notContain) {
				t.Fatalf("error = %v, must not contain %q", err, tc.notContain)
			}
		})
	}
}

func TestDatabasesCreateFromFileNoWaitPrintsStatusCommand(t *testing.T) {
	configureSeedTestCLI(t)
	fixture := createSQLiteSeedFixture(t, 0)
	var operationGets atomic.Int32
	server := newSeedWorkflowServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"database_id":"db_seed","database_url":"https://db.example/v1/db_seed","created":true,"database_token":"one-time-token","operation":{"operation_id":"seed_1","type":"database_seed","status":"awaiting_upload"},"upload_path":"/v1/projects/proj_1/databases/db_seed/operations/seed_1/content"}`)
		case http.MethodPut:
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"operation":{"operation_id":"seed_1","type":"database_seed","status":"queued"}}`)
		case http.MethodGet:
			operationGets.Add(1)
			_, _ = io.WriteString(w, `{"operation":{"operation_id":"seed_1","type":"database_seed","status":"succeeded"}}`)
		}
	})
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	var stdout bytes.Buffer
	err := run([]string{"databases", "create", "--name", "seeded", "--from-file", fixture, "--no-wait"}, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	want := "comwit databases operation status --project proj_1 --database db_seed --operation seed_1 --wait"
	if !strings.Contains(stdout.String(), "operation\tseed_1") || !strings.Contains(stdout.String(), "check_status\t"+want) {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if operationGets.Load() != 0 {
		t.Fatalf("operation GETs = %d, want 0", operationGets.Load())
	}
}

func TestDatabasesCreateFromFileReplayExplainsMissingToken(t *testing.T) {
	configureSeedTestCLI(t)
	fixture := createSQLiteSeedFixture(t, 0)
	server := newSeedWorkflowServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected replay request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"database_id":"db_seed","database_url":"https://db.example/v1/db_seed","created":false,"database_token":null,"operation":{"operation_id":"seed_1","type":"database_seed","status":"succeeded"},"upload_path":"/v1/projects/proj_1/databases/db_seed/operations/seed_1/content"}`)
	})
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	tokenPath := filepath.Join(t.TempDir(), "replay.token")
	var stdout bytes.Buffer
	err := run([]string{"databases", "create", "--name", "seeded", "--from-file", fixture, "--token-out", tokenPath, "--no-wait", "--idempotency-key", "known-key"}, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "token_status\tunavailable: idempotent replay, rotate the token after the operation succeeds") || !strings.Contains(stdout.String(), "token_out\tnot written (") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "token\t") {
		t.Fatalf("replay emitted a credential-shaped token line: %q", stdout.String())
	}
	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Fatalf("replay token file should not exist, stat err = %v", err)
	}
}

func TestDatabasesCreateFromFileRetriesUploadFromByteZeroAfter503(t *testing.T) {
	configureSeedTestCLI(t)
	useFastSeedTimings(t)
	fixture := createSQLiteSeedFixture(t, 0)
	wantBytes, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var uploads [][]byte
	var operationGets int
	server := newSeedWorkflowServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"database_id":"db_seed","database_url":"https://db.example/v1/db_seed","created":true,"database_token":"one-time-token","operation":{"operation_id":"seed_1","type":"database_seed","status":"awaiting_upload"},"upload_path":"/v1/projects/proj_1/databases/db_seed/operations/seed_1/content"}`)
		case http.MethodPut:
			body, readErr := io.ReadAll(r.Body)
			if readErr != nil {
				t.Errorf("read PUT body: %v", readErr)
			}
			mu.Lock()
			uploads = append(uploads, append([]byte(nil), body...))
			attempt := len(uploads)
			mu.Unlock()
			if attempt == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `{"code":"SEED_UPLOAD_CAPACITY_EXHAUSTED"}`)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"operation":{"operation_id":"seed_1","type":"database_seed","status":"queued"}}`)
		case http.MethodGet:
			mu.Lock()
			operationGets++
			get := operationGets
			mu.Unlock()
			if get == 1 {
				_, _ = io.WriteString(w, `{"operation":{"operation_id":"seed_1","type":"database_seed","status":"awaiting_upload"}}`)
				return
			}
			_, _ = io.WriteString(w, `{"operation":{"operation_id":"seed_1","type":"database_seed","status":"succeeded"}}`)
		}
	})
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	if err := run([]string{"databases", "create", "--name", "seeded", "--from-file", fixture}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(uploads) != 2 {
		t.Fatalf("PUT attempts = %d, want 2", len(uploads))
	}
	for attempt, upload := range uploads {
		if !bytes.Equal(upload, wantBytes) {
			t.Fatalf("PUT attempt %d did not restart from byte zero", attempt+1)
		}
	}
	if operationGets != 2 {
		t.Fatalf("operation GETs = %d, want ambiguity check plus final poll", operationGets)
	}
}

func TestDatabasesCreateFromFileRecoversWhenRetryFindsUploadInProgress(t *testing.T) {
	configureSeedTestCLI(t)
	useFastSeedTimings(t)
	fixture := createSQLiteSeedFixture(t, 0)

	var mu sync.Mutex
	var requests []string
	var putAttempts atomic.Int32
	var operationGets atomic.Int32
	server := newSeedWorkflowServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method)
		mu.Unlock()
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"database_id":"db_seed","database_url":"https://db.example/v1/db_seed","created":true,"database_token":"one-time-token","operation":{"operation_id":"seed_1","type":"database_seed","status":"awaiting_upload"},"upload_path":"/v1/projects/proj_1/databases/db_seed/operations/seed_1/content"}`)
		case http.MethodPut:
			_, _ = io.Copy(io.Discard, r.Body)
			if putAttempts.Add(1) == 1 {
				w.Header().Set("Content-Length", "128")
				w.WriteHeader(http.StatusAccepted)
				_, _ = io.WriteString(w, `{"operation":`)
				return
			}
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"title":"Conflict","status":409,"detail":"upload is still landing","code":"SEED_UPLOAD_IN_PROGRESS"}`)
		case http.MethodGet:
			if operationGets.Add(1) == 1 {
				_, _ = io.WriteString(w, `{"operation":{"operation_id":"seed_1","type":"database_seed","status":"awaiting_upload"}}`)
				return
			}
			_, _ = io.WriteString(w, `{"operation":{"operation_id":"seed_1","type":"database_seed","status":"queued"}}`)
		}
	})
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	var stdout, stderr bytes.Buffer
	err := run([]string{"databases", "create", "--name", "seeded", "--from-file", fixture, "--no-wait"}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if putAttempts.Load() != 2 || operationGets.Load() != 2 {
		t.Fatalf("PUT attempts = %d, operation GETs = %d; want 2 and 2", putAttempts.Load(), operationGets.Load())
	}
	mu.Lock()
	gotRequests := strings.Join(requests, ",")
	mu.Unlock()
	if gotRequests != "POST,PUT,GET,PUT,GET" {
		t.Fatalf("request order = %q", gotRequests)
	}
	if !strings.Contains(stdout.String(), "status\tqueued") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	for _, want := range []string{"upload attempt 1/3 failed", "upload attempt 2/3 failed"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr missing %q: %q", want, stderr.String())
		}
	}
}

func TestDatabasesCreateFromFileReportsLastAmbiguousUploadError(t *testing.T) {
	configureSeedTestCLI(t)
	useFastSeedTimings(t)
	fixture := createSQLiteSeedFixture(t, 0)
	var putAttempts atomic.Int32
	var operationGets atomic.Int32
	server := newSeedWorkflowServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"database_id":"db_seed","database_url":"https://db.example/v1/db_seed","created":true,"database_token":"one-time-token","operation":{"operation_id":"seed_1","type":"database_seed","status":"awaiting_upload"},"upload_path":"/v1/projects/proj_1/databases/db_seed/operations/seed_1/content"}`)
		case http.MethodPut:
			putAttempts.Add(1)
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Length", "128")
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"operation":`)
		case http.MethodGet:
			operationGets.Add(1)
			_, _ = io.WriteString(w, `{"operation":{"operation_id":"seed_1","type":"database_seed","status":"awaiting_upload"}}`)
		}
	})
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	var stderr bytes.Buffer
	err := run([]string{"databases", "create", "--name", "seeded", "--from-file", fixture, "--no-wait"}, &bytes.Buffer{}, &stderr)
	if err == nil || !strings.Contains(err.Error(), "upload did not complete after 3 attempts") || !strings.Contains(err.Error(), "unexpected EOF") {
		t.Fatalf("error = %v", err)
	}
	if putAttempts.Load() != 3 || operationGets.Load() != 3 {
		t.Fatalf("PUT attempts = %d, operation GETs = %d; want 3 and 3", putAttempts.Load(), operationGets.Load())
	}
	if got := strings.Count(stderr.String(), "upload attempt "); got != 3 {
		t.Fatalf("stderr logged %d failed attempts, want 3: %q", got, stderr.String())
	}
}

func TestDatabasesCreateFromFilePreservesTokenBeforeUploadFailure(t *testing.T) {
	configureSeedTestCLI(t)
	fixture := createSQLiteSeedFixture(t, 0)
	server := newSeedWorkflowServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"database_id":"db_seed","database_url":"https://db.example/v1/db_seed","created":true,"database_token":"one-time-token","operation":{"operation_id":"seed_1","type":"database_seed","status":"awaiting_upload"},"upload_path":"/v1/projects/proj_1/databases/db_seed/operations/seed_1/content"}`)
		case http.MethodPut:
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = io.WriteString(w, `{"code":"SEED_DIGEST_MISMATCH"}`)
		default:
			t.Errorf("unexpected request after definitive upload failure: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	tokenPath := filepath.Join(t.TempDir(), "database.token")
	var stdout bytes.Buffer
	err := run([]string{"databases", "create", "--name", "seeded", "--from-file", fixture, "--token-out", tokenPath}, &stdout, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "SEED_DIGEST_MISMATCH") {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(stdout.String(), "token\tone-time-token") || !strings.Contains(stdout.String(), "token_out\t"+tokenPath) {
		t.Fatalf("stdout = %q", stdout.String())
	}
	token, readErr := os.ReadFile(tokenPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(token) != "one-time-token\n" {
		t.Fatalf("token file = %q", token)
	}
}

func TestPutDatabaseSeedAttemptHandlesEarlyResponseWhileBodyIsUploading(t *testing.T) {
	fixture := createSQLiteSeedFixture(t, (1<<20)+8192)
	file, err := preflightSQLiteSeedFile(fixture, false)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/upload" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"code":"SEED_CONTENT_ALREADY_FIXED"}`)
	}))

	stderr, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	if !writerIsTerminal(stderr) {
		_ = stderr.Close()
		server.Close()
		t.Fatalf("%s is not a character device", os.DevNull)
	}
	uploadClient := newSeedUploadHTTPClient()
	c := &client{apiURL: server.URL, token: "test-token"}
	var response databaseAsyncOperationResponse
	err = putDatabaseSeedAttempt(uploadClient, c, file, "/upload", stderr, &response)
	uploadClient.CloseIdleConnections()
	_ = stderr.Close()
	server.Close()
	if err == nil || seedAPIErrorCode(err) != "SEED_CONTENT_ALREADY_FIXED" {
		t.Fatalf("error = %v", err)
	}
}

func TestDatabasesCreateFromFilePreflightRejectionsDoNotCallAPI(t *testing.T) {
	configureSeedTestCLI(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	valid := createSQLiteSeedFixture(t, 0)
	wal := createSQLiteSeedFixture(t, 0)
	if err := os.WriteFile(wal+"-wal", []byte("sidecar"), 0o600); err != nil {
		t.Fatal(err)
	}
	shm := createSQLiteSeedFixture(t, 0)
	if err := os.WriteFile(shm+"-shm", []byte("sidecar"), 0o600); err != nil {
		t.Fatal(err)
	}
	tooSmall := filepath.Join(t.TempDir(), "small.sqlite")
	if err := os.WriteFile(tooSmall, append([]byte("SQLite format 3\x00"), make([]byte, 83)...), 0o600); err != nil {
		t.Fatal(err)
	}
	tooLarge := filepath.Join(t.TempDir(), "large.sqlite")
	if err := os.WriteFile(tooLarge, []byte("SQLite format 3\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(tooLarge, maxSQLiteSeedBytes+1); err != nil {
		t.Fatal(err)
	}
	notSQLite := filepath.Join(t.TempDir(), "text.sqlite")
	if err := os.WriteFile(notSQLite, bytes.Repeat([]byte("x"), 512), 0o600); err != nil {
		t.Fatal(err)
	}
	corrupt := createSQLiteSeedFixture(t, 0)
	corruptFile, err := os.OpenFile(corrupt, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := corruptFile.WriteAt([]byte{0}, 100); err != nil {
		_ = corruptFile.Close()
		t.Fatal(err)
	}
	if err := corruptFile.Close(); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "wal sidecar", path: wal, want: `sqlite3 src.db ".backup out.sqlite"`},
		{name: "shm sidecar", path: shm, want: `sqlite3 src.db ".backup out.sqlite"`},
		{name: "too small", path: tooSmall, want: "minimum 100 bytes"},
		{name: "too large", path: tooLarge, want: "maximum 2 GiB"},
		{name: "not sqlite", path: notSQLite, want: "not a SQLite database"},
		{name: "integrity failure", path: corrupt, want: "integrity_check failed"},
		{name: "not regular", path: filepath.Dir(valid), want: "regular file"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := requests.Load()
			err := run([]string{"databases", "create", "--name", "seeded", "--from-file", tc.path}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
			if requests.Load() != before {
				t.Fatalf("preflight contacted API for %s", tc.name)
			}
		})
	}

	if _, err := preflightSQLiteSeedFile(corrupt, true); err != nil {
		t.Fatalf("--skip-local-checks equivalent should accept header-valid corrupt fixture: %v", err)
	}
}

func TestSQLiteSeedPreflightRejectsForeignKeyViolations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "foreign-key.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"PRAGMA foreign_keys = OFF",
		"CREATE TABLE parent (id INTEGER PRIMARY KEY)",
		"CREATE TABLE child (parent_id INTEGER REFERENCES parent(id))",
		"INSERT INTO child(parent_id) VALUES (42)",
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = preflightSQLiteSeedFile(path, false)
	if err == nil || !strings.Contains(err.Error(), "foreign_key_check failed") {
		t.Fatalf("error = %v", err)
	}
}

func TestDatabaseOperationStatusRendersSeedAndRestoreErrors(t *testing.T) {
	tests := []struct {
		name     string
		response string
		wantType string
		wantCode string
		wantErr  string
	}{
		{
			name:     "seed object error",
			response: `{"operation":{"operation_id":"seed_1","type":"database_seed","status":"failed","error":{"code":"SEED_UPLOAD_EXPIRED","message":"expired"}}}`,
			wantType: "database_seed",
			wantCode: "SEED_UPLOAD_EXPIRED",
			wantErr:  "upload session expired",
		},
		{
			name:     "restore string error",
			response: `{"operation":{"operation_id":"restore_1","type":"database_restore","status":"failed","error":"restore failed safely"}}`,
			wantType: "database_restore",
			wantErr:  "restore failed safely",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			configureSeedTestCLI(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/operations/op_1") {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.response)
			}))
			defer server.Close()
			t.Setenv("COMWIT_API_URL", server.URL)

			var stdout bytes.Buffer
			err := run([]string{"databases", "operation", "status", "--database", "db_1", "--operation", "op_1"}, &stdout, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
			if !strings.Contains(stdout.String(), "type\t"+tc.wantType) || !strings.Contains(stdout.String(), "status\tfailed") {
				t.Fatalf("stdout = %q", stdout.String())
			}
			if tc.wantCode != "" && !strings.Contains(stdout.String(), "error_code\t"+tc.wantCode) {
				t.Fatalf("stdout = %q", stdout.String())
			}
		})
	}
}

func TestDatabasesRestoreStatusDecodesSeedOperationError(t *testing.T) {
	configureSeedTestCLI(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/operations/seed_1") {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"operation":{"operation_id":"seed_1","type":"database_seed","status":"failed","error":{"code":"SEED_UPLOAD_EXPIRED","message":"expired"}}}`)
	}))
	defer server.Close()
	t.Setenv("COMWIT_API_URL", server.URL)

	var stdout bytes.Buffer
	err := run([]string{"databases", "restore", "status", "--database", "db_1", "--operation", "seed_1"}, &stdout, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "SEED_UPLOAD_EXPIRED") || strings.Contains(err.Error(), "decode API response") {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(stdout.String(), "error_code\tSEED_UPLOAD_EXPIRED") || !strings.Contains(stdout.String(), "error\texpired") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func configureSeedTestCLI(t *testing.T) {
	t.Helper()
	t.Setenv("COMWIT_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("COMWIT_PROJECT", "")
	if _, err := saveConfig(configFile{Token: "test-token", DefaultProject: "proj_1"}); err != nil {
		t.Fatal(err)
	}
}

func useFastSeedTimings(t *testing.T) {
	t.Helper()
	oldRetry := seedRetryBaseDelay
	oldUploadRetry := seedUploadRetryDelays
	oldPollInitial := seedPollInitialDelay
	oldPollMaximum := seedPollMaximumDelay
	seedRetryBaseDelay = time.Millisecond
	seedUploadRetryDelays = [...]time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	seedPollInitialDelay = time.Millisecond
	seedPollMaximumDelay = 2 * time.Millisecond
	t.Cleanup(func() {
		seedRetryBaseDelay = oldRetry
		seedUploadRetryDelays = oldUploadRetry
		seedPollInitialDelay = oldPollInitial
		seedPollMaximumDelay = oldPollMaximum
	})
}

func createSQLiteSeedFixture(t *testing.T, blobBytes int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA journal_mode = DELETE"); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE payload (id INTEGER PRIMARY KEY, body BLOB)"); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if blobBytes > 0 {
		if _, err := db.Exec("INSERT INTO payload(body) VALUES (zeroblob(?))", blobBytes); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if blobBytes > 1<<20 {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() <= 1<<20 {
			t.Fatalf("fixture size = %d, want > 1 MiB", info.Size())
		}
	}
	return path
}

func createSeedDumpFixture(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dump.sql")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertSQLiteRowCount(t *testing.T, path, table string, want int) {
	t.Helper()
	dsn, err := sqliteFileDSN(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var got int
	if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s row count = %d, want %d", table, got, want)
	}
}

func assertNoTemporarySeedFiles(t *testing.T, directory string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(directory, "comwit-seed-*.sqlite*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary converted SQLite files remain: %v", matches)
	}
}

func newSeedWorkflowServer(t *testing.T, handler func(http.ResponseWriter, *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		handler(w, r)
	}))
}

func assertUUIDv4(t *testing.T, value string) {
	t.Helper()
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' || value[14] != '4' || !strings.ContainsRune("89ab", rune(value[19])) {
		t.Fatalf("Idempotency-Key = %q, want UUID v4", value)
	}
}
