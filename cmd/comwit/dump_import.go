package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

const (
	defaultImportMaxRequestBytes = 900 * 1024
	defaultImportMaxBatchSteps   = 250
)

type dumpStatement struct {
	Index int
	SQL   string
}

type skippedDumpStatement struct {
	Index  int
	Reason string
}

type dumpImportBatch struct {
	Number     int
	Statements []dumpStatement
	Body       []byte
}

type hranaPipelineRequest struct {
	Baton    *string              `json:"baton"`
	Requests []hranaStreamRequest `json:"requests"`
}

type hranaStreamRequest struct {
	Type  string      `json:"type"`
	Batch *hranaBatch `json:"batch,omitempty"`
}

type hranaBatch struct {
	Steps []hranaBatchStep `json:"steps"`
}

type hranaBatchStep struct {
	Condition *hranaBatchCond `json:"condition,omitempty"`
	Stmt      hranaStmt       `json:"stmt"`
}

type hranaBatchCond struct {
	Type string `json:"type"`
	Step uint32 `json:"step"`
}

type hranaStmt struct {
	SQL      string `json:"sql"`
	WantRows bool   `json:"want_rows"`
}

type hranaPipelineResponse struct {
	Results []hranaStreamResult `json:"results"`
}

type hranaStreamResult struct {
	Type     string               `json:"type"`
	Error    *hranaError          `json:"error,omitempty"`
	Response *hranaStreamResponse `json:"response,omitempty"`
}

type hranaStreamResponse struct {
	Type   string            `json:"type"`
	Result *hranaBatchResult `json:"result,omitempty"`
}

type hranaBatchResult struct {
	StepErrors []json.RawMessage `json:"step_errors"`
}

type hranaError struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

type dumpImportExecutionError struct {
	StatementIndex int
	StatementSQL   string
	Message        string
}

func (e dumpImportExecutionError) Error() string {
	if e.StatementIndex > 0 {
		return fmt.Sprintf("statement %d failed: %s\nsql\t%s", e.StatementIndex, e.Message, sqlPreview(e.StatementSQL))
	}
	return e.Message
}

func databaseImportDumpCommand(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("databases import-dump", flag.ContinueOnError)
	project := fs.String("project", "", "project id")
	name := fs.String("name", "", "new database name")
	fromDump := fs.String("from-dump", "", "SQLite-compatible SQL dump path")
	keepFailedDB := fs.Bool("keep-failed-db", false, "preserve the newly-created database if import fails")
	yes := fs.Bool("yes", false, "skip confirmation; import-dump only creates a new database")
	if err := fs.Parse(args); err != nil {
		return err
	}
	_ = *yes
	if fs.NArg() != 0 {
		return errors.New("usage: comwit databases import-dump --project <id> --name <name> --from-dump dump.sql [--keep-failed-db]")
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	projectID := selectProject(*project, cfg)
	if projectID == "" {
		return errors.New("--project is required")
	}
	databaseName := strings.TrimSpace(*name)
	if databaseName == "" {
		return errors.New("--name is required")
	}
	dumpPath := strings.TrimSpace(*fromDump)
	if dumpPath == "" {
		return errors.New("--from-dump is required")
	}

	statements, skipped, err := loadDumpStatements(dumpPath)
	if err != nil {
		return err
	}
	if len(statements) == 0 {
		return errors.New("dump contains no SQL statements to import")
	}
	batches, err := buildDumpImportBatches(statements, defaultImportMaxRequestBytes, defaultImportMaxBatchSteps)
	if err != nil {
		return err
	}

	apiClient := newClient(cfg)
	var created databaseCreateResponse
	if err := apiClient.postJSON(projectDatabasesPath(projectID), map[string]string{"name": databaseName}, &created); err != nil {
		return err
	}
	token := ""
	if created.DatabaseToken != nil {
		token = strings.TrimSpace(*created.DatabaseToken)
	}
	if strings.TrimSpace(created.DatabaseID) == "" || strings.TrimSpace(created.DatabaseURL) == "" || token == "" {
		if strings.TrimSpace(created.DatabaseID) != "" {
			_ = apiClient.deleteJSON(projectDatabasePath(projectID, created.DatabaseID), nil)
		}
		return errors.New("database create response did not include database_id, database_url, and one-time database_token")
	}

	fmt.Fprintf(stdout, "database\t%s\n", created.DatabaseID)
	fmt.Fprintf(stdout, "url\t%s\n", created.DatabaseURL)
	fmt.Fprintf(stdout, "token\t%s\n", token)
	fmt.Fprintf(stdout, "statements\t%d\n", len(statements))
	if len(skipped) > 0 {
		printSkippedDumpStatements(stdout, skipped)
	}
	fmt.Fprintf(stdout, "batches\t%d\n", len(batches))

	if err := replayDumpBatches(stdout, created.DatabaseURL, token, batches); err != nil {
		_ = rollbackDumpImport(created.DatabaseURL, token)
		if *keepFailedDB {
			fmt.Fprintf(stdout, "failed_database_preserved\t%s\n", created.DatabaseID)
			return err
		}
		var deleteBody databaseOperationResponse
		if deleteErr := apiClient.deleteJSON(projectDatabasePath(projectID, created.DatabaseID), &deleteBody); deleteErr != nil {
			fmt.Fprintf(stdout, "failed_database_preserved\t%s\n", created.DatabaseID)
			return fmt.Errorf("%w\ncleanup failed for database %s: %v", err, created.DatabaseID, deleteErr)
		}
		fmt.Fprintf(stdout, "failed_database_deleted\t%s\n", created.DatabaseID)
		return err
	}

	fmt.Fprintf(stdout, "imported\t%s\t%d statements\n", created.DatabaseID, len(statements))
	return nil
}

func loadDumpStatements(path string) ([]dumpStatement, []skippedDumpStatement, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	if looksLikeSQLiteDatabaseFile(data) {
		return nil, nil, errors.New("--from-dump expects a SQL dump, but the file looks like a SQLite .db database; use --from-file for an existing SQLite database")
	}
	statements, err := splitSQLDump(string(data))
	if err != nil {
		return nil, nil, err
	}
	filtered := make([]dumpStatement, 0, len(statements))
	var skipped []skippedDumpStatement
	for _, statement := range statements {
		if reason, ok := ignoredDumpStatementReason(statement.SQL); ok {
			skipped = append(skipped, skippedDumpStatement{Index: statement.Index, Reason: reason})
			continue
		}
		filtered = append(filtered, statement)
	}
	return filtered, skipped, nil
}

func printSkippedDumpStatements(w io.Writer, skipped []skippedDumpStatement) {
	fmt.Fprintf(w, "skipped\t%d\n", len(skipped))
	for _, statement := range skipped {
		fmt.Fprintf(w, "skipped_statement\t%d\t%s\n", statement.Index, statement.Reason)
	}
}

func looksLikeSQLiteDatabaseFile(data []byte) bool {
	if bytes.HasPrefix(data, []byte("SQLite format 3\x00")) {
		return true
	}
	limit := len(data)
	if limit > 512 {
		limit = 512
	}
	return bytes.Contains(data[:limit], []byte{0})
}

func buildDumpImportBatches(statements []dumpStatement, maxRequestBytes, maxBatchSteps int) ([]dumpImportBatch, error) {
	if len(statements) == 0 {
		return nil, nil
	}
	if dumpContainsTransaction(statements) {
		batch, err := makeDumpImportBatch(1, statements)
		if err != nil {
			return nil, err
		}
		if len(batch.Body) > maxRequestBytes {
			return nil, fmt.Errorf("dump transaction is %d bytes after JSON encoding, above the CLI import request limit of %d bytes; split the dump into smaller transaction-free imports or use a future server-side import", len(batch.Body), maxRequestBytes)
		}
		return []dumpImportBatch{batch}, nil
	}

	var batches []dumpImportBatch
	var current []dumpStatement
	flush := func() error {
		if len(current) == 0 {
			return nil
		}
		batch, err := makeDumpImportBatch(len(batches)+1, current)
		if err != nil {
			return err
		}
		if len(batch.Body) > maxRequestBytes {
			return fmt.Errorf("statement %d creates a %d-byte import request, above the CLI import request limit of %d bytes", current[0].Index, len(batch.Body), maxRequestBytes)
		}
		batches = append(batches, batch)
		current = nil
		return nil
	}
	for _, statement := range statements {
		candidate := append(append([]dumpStatement(nil), current...), statement)
		candidateBatch, err := makeDumpImportBatch(len(batches)+1, candidate)
		if err != nil {
			return nil, err
		}
		if len(current) > 0 && (len(candidate) > maxBatchSteps || len(candidateBatch.Body) > maxRequestBytes) {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		current = append(current, statement)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return batches, nil
}

func makeDumpImportBatch(number int, statements []dumpStatement) (dumpImportBatch, error) {
	steps := make([]hranaBatchStep, 0, len(statements))
	for i, statement := range statements {
		step := hranaBatchStep{
			Stmt: hranaStmt{SQL: statement.SQL, WantRows: false},
		}
		if i > 0 {
			step.Condition = &hranaBatchCond{Type: "ok", Step: uint32(i - 1)}
		}
		steps = append(steps, step)
	}
	request := hranaPipelineRequest{
		Baton: nil,
		Requests: []hranaStreamRequest{{
			Type:  "batch",
			Batch: &hranaBatch{Steps: steps},
		}},
	}
	body, err := json.Marshal(request)
	if err != nil {
		return dumpImportBatch{}, err
	}
	return dumpImportBatch{Number: number, Statements: append([]dumpStatement(nil), statements...), Body: body}, nil
}

func replayDumpBatches(stdout io.Writer, databaseURL, token string, batches []dumpImportBatch) error {
	httpClient := &http.Client{Timeout: 120 * time.Second}
	for _, batch := range batches {
		first := batch.Statements[0].Index
		last := batch.Statements[len(batch.Statements)-1].Index
		fmt.Fprintf(stdout, "importing_batch\t%d/%d\tstatements=%d-%d\n", batch.Number, len(batches), first, last)
		var response hranaPipelineResponse
		if err := postHranaPipeline(httpClient, databaseURL, token, batch.Body, &response); err != nil {
			return err
		}
		if err := firstHranaBatchError(batch, response); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "imported_batch\t%d/%d\tstatements=%d-%d\n", batch.Number, len(batches), first, last)
	}
	return nil
}

func postHranaPipeline(httpClient *http.Client, databaseURL, token string, body []byte, out any) error {
	endpoint, err := hranaPipelineURL(databaseURL)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiError{status: resp.StatusCode, body: strings.TrimSpace(string(data))}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode Hrana response: %w", err)
	}
	return nil
}

func hranaPipelineURL(databaseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(databaseURL))
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid database_url %q", databaseURL)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/v3/pipeline"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func firstHranaBatchError(batch dumpImportBatch, response hranaPipelineResponse) error {
	if len(response.Results) == 0 {
		return errors.New("Hrana response did not include a batch result")
	}
	result := response.Results[0]
	if result.Type == "error" {
		message := "Hrana batch failed"
		if result.Error != nil && strings.TrimSpace(result.Error.Message) != "" {
			message = result.Error.Message
		}
		return dumpImportExecutionError{Message: message}
	}
	if result.Response == nil || result.Response.Result == nil {
		return errors.New("Hrana response did not include batch step results")
	}
	for i, raw := range result.Response.Result.StepErrors {
		if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			continue
		}
		var hranaErr hranaError
		if err := json.Unmarshal(raw, &hranaErr); err != nil {
			return fmt.Errorf("decode Hrana step error: %w", err)
		}
		message := hranaErr.Message
		if hranaErr.Code != "" {
			message = message + " (" + hranaErr.Code + ")"
		}
		statement := dumpStatement{}
		if i < len(batch.Statements) {
			statement = batch.Statements[i]
		}
		return dumpImportExecutionError{
			StatementIndex: statement.Index,
			StatementSQL:   statement.SQL,
			Message:        strings.TrimSpace(message),
		}
	}
	return nil
}

func rollbackDumpImport(databaseURL, token string) error {
	batch, err := makeDumpImportBatch(1, []dumpStatement{{Index: 0, SQL: "ROLLBACK;"}})
	if err != nil {
		return err
	}
	return postHranaPipeline(&http.Client{Timeout: 30 * time.Second}, databaseURL, token, batch.Body, nil)
}

func dumpContainsTransaction(statements []dumpStatement) bool {
	for _, statement := range statements {
		if isTransactionControlStatement(statement.SQL) {
			return true
		}
	}
	return false
}

func isTransactionControlStatement(sql string) bool {
	words := sqlLeadingWords(sql, 2)
	if len(words) == 0 {
		return false
	}
	return words[0] == "begin" || words[0] == "commit" || words[0] == "end" || words[0] == "rollback"
}

func ignoredDumpStatementReason(sql string) (string, bool) {
	words := sqlLeadingWords(sql, 4)
	if len(words) >= 2 && words[0] == "pragma" && words[1] == "defer_foreign_keys" {
		return "ignored D1-specific PRAGMA defer_foreign_keys", true
	}
	if len(words) >= 3 && words[0] == "delete" && words[1] == "from" && words[2] == "sqlite_sequence" {
		return "ignored sqlite_sequence maintenance statement", true
	}
	if len(words) >= 3 && words[0] == "insert" && words[1] == "into" && words[2] == "sqlite_sequence" {
		return "ignored sqlite_sequence maintenance statement", true
	}
	return "", false
}

func sqlPreview(sql string) string {
	fields := strings.Fields(sql)
	preview := strings.Join(fields, " ")
	if len(preview) > 160 {
		preview = preview[:157] + "..."
	}
	return preview
}

func splitSQLDump(input string) ([]dumpStatement, error) {
	input = strings.TrimPrefix(input, "\ufeff")
	var statements []dumpStatement
	var current strings.Builder
	statementIndex := 1
	tls := libc.NewTLS()
	defer tls.Close()

	for i := 0; i < len(input); i++ {
		ch := input[i]
		current.WriteByte(ch)
		if ch != ';' {
			continue
		}
		complete, err := sqliteStatementComplete(tls, current.String())
		if err != nil {
			return nil, err
		}
		if complete {
			sql := strings.TrimSpace(current.String())
			if statementHasSQL(sql) {
				statements = append(statements, dumpStatement{Index: statementIndex, SQL: sql})
				statementIndex++
			}
			current.Reset()
		}
	}

	remaining := strings.TrimSpace(current.String())
	if statementHasSQL(remaining) {
		return nil, fmt.Errorf("incomplete SQL statement near: %s", sqlPreview(remaining))
	}
	return statements, nil
}

func sqliteStatementComplete(tls *libc.TLS, sql string) (bool, error) {
	cString, err := libc.CString(sql)
	if err != nil {
		return false, err
	}
	defer libc.Xfree(tls, cString)
	return sqlite3.Xsqlite3_complete(tls, cString) != 0, nil
}

type sqlScannerMode int

const (
	sqlModeNormal sqlScannerMode = iota
	sqlModeSingleQuote
	sqlModeDoubleQuote
	sqlModeBacktick
	sqlModeBracket
	sqlModeLineComment
	sqlModeBlockComment
)

func isSQLWordByte(ch byte) bool {
	return ch == '_' || ch == '$' || ch >= '0' && ch <= '9' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z'
}

func statementHasSQL(sql string) bool {
	return len(sqlLeadingWords(sql, 1)) > 0
}

func sqlLeadingWords(sql string, limit int) []string {
	var words []string
	mode := sqlModeNormal
	var word strings.Builder
	flush := func() bool {
		if word.Len() == 0 {
			return false
		}
		words = append(words, word.String())
		word.Reset()
		return limit > 0 && len(words) >= limit
	}
	for i := 0; i < len(sql); i++ {
		ch := sql[i]
		var next byte
		if i+1 < len(sql) {
			next = sql[i+1]
		}
		switch mode {
		case sqlModeLineComment:
			if ch == '\n' || ch == '\r' {
				mode = sqlModeNormal
			}
			continue
		case sqlModeBlockComment:
			if ch == '*' && next == '/' {
				mode = sqlModeNormal
				i++
			}
			continue
		case sqlModeSingleQuote:
			if ch == '\'' {
				if next == '\'' {
					i++
				} else {
					mode = sqlModeNormal
				}
			}
			continue
		case sqlModeDoubleQuote:
			if ch == '"' {
				if next == '"' {
					i++
				} else {
					mode = sqlModeNormal
				}
			}
			continue
		case sqlModeBacktick:
			if ch == '`' {
				mode = sqlModeNormal
			}
			continue
		case sqlModeBracket:
			if ch == ']' {
				mode = sqlModeNormal
			}
			continue
		}
		switch {
		case ch == '-' && next == '-':
			if flush() {
				return words
			}
			mode = sqlModeLineComment
			i++
		case ch == '/' && next == '*':
			if flush() {
				return words
			}
			mode = sqlModeBlockComment
			i++
		case ch == '\'':
			if flush() {
				return words
			}
			mode = sqlModeSingleQuote
		case ch == '"':
			if flush() {
				return words
			}
			mode = sqlModeDoubleQuote
		case ch == '`':
			if flush() {
				return words
			}
			mode = sqlModeBacktick
		case ch == '[':
			if flush() {
				return words
			}
			mode = sqlModeBracket
		case isSQLWordByte(ch):
			word.WriteByte(byte(unicode.ToLower(rune(ch))))
		default:
			if flush() {
				return words
			}
		}
	}
	flush()
	return words
}
