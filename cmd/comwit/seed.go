package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	minSQLiteSeedBytes     int64 = 100
	maxSQLiteSeedBytes     int64 = 2 << 30
	seedRequestMaxAttempts       = 3
	seedResponseMaxBytes   int64 = 1 << 20
	seedWaitTimeout              = 70 * time.Minute
)

var (
	seedRetryBaseDelay    = 250 * time.Millisecond
	seedUploadRetryDelays = [...]time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second}
	seedPollInitialDelay  = 2 * time.Second
	seedPollMaximumDelay  = 5 * time.Second
)

type sqliteSeedFile struct {
	Path   string
	Size   int64
	SHA256 string
}

type databaseSeedSource struct {
	Type          string `json:"type"`
	ContentLength int64  `json:"content_length"`
	SHA256        string `json:"sha256"`
}

type databaseSeedCreateRequest struct {
	Name   string             `json:"name"`
	Source databaseSeedSource `json:"source"`
}

type databaseAsyncOperationError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func (e *databaseAsyncOperationError) UnmarshalJSON(data []byte) error {
	var message string
	if err := json.Unmarshal(data, &message); err == nil {
		e.Message = message
		return nil
	}
	var value struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Detail  string `json:"detail"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	e.Code = value.Code
	e.Message = value.Message
	if e.Message == "" {
		e.Message = value.Detail
	}
	return nil
}

type databaseAsyncOperation struct {
	OperationID          string                       `json:"operation_id"`
	Type                 string                       `json:"type"`
	Status               string                       `json:"status"`
	ResolvedRestorePoint string                       `json:"resolved_restore_point,omitempty"`
	UploadExpiresAtMS    uint64                       `json:"upload_expires_at_ms,omitempty"`
	CreatedAtMS          uint64                       `json:"created_at_ms,omitempty"`
	UpdatedAtMS          uint64                       `json:"updated_at_ms,omitempty"`
	Error                *databaseAsyncOperationError `json:"error,omitempty"`
}

type databaseSeedCreateResponse struct {
	DatabaseID    string                 `json:"database_id"`
	DatabaseURL   string                 `json:"database_url"`
	Created       bool                   `json:"created"`
	DatabaseToken *string                `json:"database_token"`
	Operation     databaseAsyncOperation `json:"operation"`
	UploadPath    string                 `json:"upload_path"`
}

type databaseAsyncOperationResponse struct {
	Operation databaseAsyncOperation `json:"operation"`
}

type seedAmbiguousRequestError struct {
	err error
}

func (e seedAmbiguousRequestError) Error() string { return e.err.Error() }
func (e seedAmbiguousRequestError) Unwrap() error { return e.err }

func databaseCreateCommand(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("databases create", flag.ContinueOnError)
	project := fs.String("project", "", "project id")
	name := fs.String("name", "", "database name")
	fromFile := fs.String("from-file", "", "create the database from this SQLite file")
	tokenOut := fs.String("token-out", "", "write the database's one-time token to this file (0600)")
	skipLocalChecks := fs.Bool("skip-local-checks", false, "skip local SQLite integrity and foreign-key checks")
	idempotencyKey := fs.String("idempotency-key", "", "stable key for safely retrying file-based creation")
	noWait := fs.Bool("no-wait", false, "return after the upload is accepted instead of waiting until ready")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: comwit databases create --project <id> --name <name> [--from-file <path> --token-out <path> --skip-local-checks --idempotency-key <key> --no-wait]")
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

	if *fromFile == "" {
		if strings.TrimSpace(*tokenOut) != "" || *skipLocalChecks || *idempotencyKey != "" || *noWait {
			return errors.New("--token-out, --skip-local-checks, --idempotency-key, and --no-wait require --from-file")
		}
		var body databaseCreateResponse
		if err := newClient(cfg).postJSON(projectDatabasesPath(projectID), map[string]string{"name": databaseName}, &body); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s\t%s\tcreated=%t\n", body.DatabaseID, body.DatabaseURL, body.Created)
		if body.DatabaseToken != nil && strings.TrimSpace(*body.DatabaseToken) != "" {
			fmt.Fprintf(stdout, "token\t%s\n", *body.DatabaseToken)
		}
		return nil
	}

	filePath := *fromFile
	tokenPath := strings.TrimSpace(*tokenOut)
	if err := validateSeedTokenOutputPath(filePath, tokenPath); err != nil {
		return err
	}
	key := *idempotencyKey
	if key == "" {
		key, err = newUUIDv4()
		if err != nil {
			return fmt.Errorf("generate idempotency key: %w", err)
		}
	}
	if err := validateSeedIdempotencyKey(key); err != nil {
		return err
	}

	file, err := preflightSQLiteSeedFile(filePath, *skipLocalChecks)
	if err != nil {
		return err
	}
	apiClient := newClient(cfg)
	request := databaseSeedCreateRequest{
		Name: databaseName,
		Source: databaseSeedSource{
			Type:          "sqlite_file",
			ContentLength: file.Size,
			SHA256:        file.SHA256,
		},
	}
	created, err := createDatabaseSeed(apiClient, projectID, key, request)
	if err != nil {
		return err
	}

	// The token is one-time output. Print and persist it before validating the
	// follow-up fields or starting the upload, so a later failure cannot hide it.
	fmt.Fprintf(stdout, "database\t%s\n", created.DatabaseID)
	fmt.Fprintf(stdout, "url\t%s\n", created.DatabaseURL)
	fmt.Fprintf(stdout, "created\t%t\n", created.Created)
	token := ""
	if created.DatabaseToken != nil {
		token = strings.TrimSpace(*created.DatabaseToken)
	}
	if token != "" {
		fmt.Fprintf(stdout, "token\t%s\n", token)
		if tokenPath != "" {
			if err := writeTokenOut(tokenPath, token); err != nil {
				return fmt.Errorf("write token to %s: %w", tokenPath, err)
			}
			fmt.Fprintf(stdout, "token_out\t%s\n", tokenPath)
		}
	} else {
		fmt.Fprintln(stdout, "token_status\tunavailable: idempotent replay, rotate the token after the operation succeeds")
		if tokenPath != "" {
			fmt.Fprintln(stdout, "token_out\tnot written (one-time token unavailable on idempotent replay)")
		}
	}
	fmt.Fprintf(stdout, "operation\t%s\n", created.Operation.OperationID)
	lastStatus := ""
	printDatabaseOperationStatusChange(stdout, created.Operation.Status, &lastStatus)

	if strings.TrimSpace(created.DatabaseID) == "" || strings.TrimSpace(created.DatabaseURL) == "" || strings.TrimSpace(created.Operation.OperationID) == "" {
		return errors.New("database create response omitted database_id, database_url, or operation_id")
	}
	status := strings.ToLower(strings.TrimSpace(created.Operation.Status))
	if status == "failed" {
		return databaseSeedFailedError(created.Operation)
	}
	if status == "awaiting_upload" {
		if strings.TrimSpace(created.UploadPath) == "" {
			return errors.New("database create response omitted upload_path")
		}
		operation, err := uploadDatabaseSeed(apiClient, file, created.UploadPath, projectID, created.DatabaseID, created.Operation.OperationID, stderr)
		if err != nil {
			return err
		}
		created.Operation = operation
		printDatabaseOperationStatusChange(stdout, operation.Status, &lastStatus)
	} else if !databaseSeedUploadLanded(status) {
		return fmt.Errorf("database create returned unknown operation status %q", created.Operation.Status)
	}

	if strings.EqualFold(created.Operation.Status, "failed") {
		return databaseSeedFailedError(created.Operation)
	}
	if *noWait {
		fmt.Fprintf(stdout, "check_status\t%s\n", databaseOperationStatusCommand(projectID, created.DatabaseID, created.Operation.OperationID, true))
		return nil
	}
	if strings.EqualFold(created.Operation.Status, "succeeded") {
		fmt.Fprintf(stdout, "ready\t%s\t%s\n", created.DatabaseID, created.DatabaseURL)
		return nil
	}
	return waitForDatabaseSeed(apiClient, projectID, created.DatabaseID, created.DatabaseURL, created.Operation.OperationID, stdout, lastStatus)
}

func validateSeedTokenOutputPath(filePath, tokenPath string) error {
	if tokenPath == "" {
		return nil
	}
	fileAbs, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("resolve --from-file path: %w", err)
	}
	tokenAbs, err := filepath.Abs(tokenPath)
	if err != nil {
		return fmt.Errorf("resolve --token-out path: %w", err)
	}
	if filepath.Clean(fileAbs) == filepath.Clean(tokenAbs) {
		return errors.New("--token-out must not overwrite --from-file")
	}
	fileInfo, fileErr := os.Stat(fileAbs)
	tokenInfo, tokenErr := os.Stat(tokenAbs)
	if fileErr == nil && tokenErr == nil && os.SameFile(fileInfo, tokenInfo) {
		return errors.New("--token-out must not overwrite --from-file")
	}
	return nil
}

func validateSeedIdempotencyKey(key string) error {
	if len(key) < 1 || len(key) > 128 {
		return errors.New("--idempotency-key must contain 1 to 128 printable ASCII characters")
	}
	for _, r := range key {
		if r < 0x20 || r > 0x7e {
			return errors.New("--idempotency-key must contain 1 to 128 printable ASCII characters")
		}
	}
	return nil
}

func newUUIDv4() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}

func preflightSQLiteSeedFile(path string, skipLocalChecks bool) (sqliteSeedFile, error) {
	info, err := os.Stat(path)
	if err != nil {
		return sqliteSeedFile{}, fmt.Errorf("inspect --from-file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return sqliteSeedFile{}, fmt.Errorf("--from-file %s must be a regular file", path)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		sidecar := path + suffix
		if _, err := os.Lstat(sidecar); err == nil {
			return sqliteSeedFile{}, fmt.Errorf("SQLite sidecar %s exists; create a standalone copy with `sqlite3 src.db \".backup out.sqlite\"`", sidecar)
		} else if !errors.Is(err, os.ErrNotExist) {
			return sqliteSeedFile{}, fmt.Errorf("inspect SQLite sidecar %s: %w", sidecar, err)
		}
	}
	if info.Size() < minSQLiteSeedBytes {
		return sqliteSeedFile{}, fmt.Errorf("--from-file is too small: %d bytes (minimum 100 bytes)", info.Size())
	}
	if info.Size() > maxSQLiteSeedBytes {
		return sqliteSeedFile{}, fmt.Errorf("--from-file is too large: %d bytes (maximum 2 GiB)", info.Size())
	}

	source, err := os.Open(path)
	if err != nil {
		return sqliteSeedFile{}, fmt.Errorf("open --from-file %s: %w", path, err)
	}
	var header [16]byte
	_, readErr := io.ReadFull(source, header[:])
	closeErr := source.Close()
	if readErr != nil {
		return sqliteSeedFile{}, fmt.Errorf("read SQLite header from %s: %w", path, readErr)
	}
	if closeErr != nil {
		return sqliteSeedFile{}, fmt.Errorf("close --from-file %s: %w", path, closeErr)
	}
	if !looksLikeSQLiteDatabaseFile(header[:]) || !bytes.Equal(header[:], []byte("SQLite format 3\x00")) {
		return sqliteSeedFile{}, errors.New("--from-file is not a SQLite database (expected a SQLite format 3 header)")
	}

	if !skipLocalChecks {
		if err := checkSQLiteSeedIntegrity(path); err != nil {
			return sqliteSeedFile{}, err
		}
	}
	digest, err := sha256File(path)
	if err != nil {
		return sqliteSeedFile{}, err
	}
	return sqliteSeedFile{Path: path, Size: info.Size(), SHA256: digest}, nil
}

func checkSQLiteSeedIntegrity(path string) error {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve SQLite file path: %w", err)
	}
	dsn := &url.URL{Scheme: "file", Path: filepath.ToSlash(absolutePath)}
	query := dsn.Query()
	query.Set("immutable", "1")
	query.Set("mode", "ro")
	dsn.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return fmt.Errorf("open SQLite file for local checks: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	var integrity string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		return fmt.Errorf("local SQLite integrity_check failed: %w", err)
	}
	if strings.TrimSpace(integrity) != "ok" {
		return fmt.Errorf("local SQLite integrity_check failed: %s", strings.TrimSpace(integrity))
	}
	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("local SQLite foreign_key_check failed: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("local SQLite foreign_key_check failed: the database contains foreign key violations")
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("local SQLite foreign_key_check failed: %w", err)
	}
	return nil
}

func sha256File(path string) (string, error) {
	source, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open --from-file for SHA-256: %w", err)
	}
	defer source.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, source); err != nil {
		return "", fmt.Errorf("compute --from-file SHA-256: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func createDatabaseSeed(c *client, projectID, idempotencyKey string, request databaseSeedCreateRequest) (databaseSeedCreateResponse, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return databaseSeedCreateResponse{}, err
	}
	path := projectDatabasesPath(projectID)
	httpClient := *c.httpClient
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	var lastErr error
	for attempt := 1; attempt <= seedRequestMaxAttempts; attempt++ {
		var response databaseSeedCreateResponse
		err := doSeedJSONRequest(&httpClient, c, http.MethodPost, path, bytes.NewReader(payload), int64(len(payload)), "application/json", map[string]string{"Idempotency-Key": idempotencyKey}, http.StatusAccepted, &response)
		if err == nil {
			return response, nil
		}
		lastErr = err
		if !retryableSeedRequestError(err) || attempt == seedRequestMaxAttempts {
			break
		}
		time.Sleep(seedRetryDelay(attempt))
	}
	return databaseSeedCreateResponse{}, mapSeedAPIError("open database upload session", lastErr)
}

func uploadDatabaseSeed(c *client, file sqliteSeedFile, uploadPath, projectID, databaseID, operationID string, stderr io.Writer) (databaseAsyncOperation, error) {
	uploadClient := newSeedUploadHTTPClient()
	defer uploadClient.CloseIdleConnections()
	var lastAttemptErr error
	for attempt := 1; attempt <= seedRequestMaxAttempts; attempt++ {
		var response databaseAsyncOperationResponse
		err := putDatabaseSeedAttempt(uploadClient, c, file, uploadPath, stderr, &response)
		if err == nil {
			if !databaseSeedUploadLanded(response.Operation.Status) {
				return databaseAsyncOperation{}, fmt.Errorf("upload response returned unexpected operation status %q", response.Operation.Status)
			}
			if strings.TrimSpace(response.Operation.OperationID) == "" {
				response.Operation.OperationID = operationID
			}
			return response.Operation, nil
		}
		lastAttemptErr = err
		fmt.Fprintf(stderr, "upload attempt %d/%d failed: %v\n", attempt, seedRequestMaxAttempts, mapSeedAPIError("upload SQLite file", err))
		earlierUploadStillLanding := attempt > 1 && seedUploadInProgressError(err)
		if definitiveSeedUploadError(err) && !earlierUploadStillLanding {
			return databaseAsyncOperation{}, mapSeedAPIError("upload SQLite file", err)
		}
		waitedBeforeStatusCheck := false
		if earlierUploadStillLanding {
			time.Sleep(seedUploadRetryDelay(attempt))
			waitedBeforeStatusCheck = true
		}

		operation, statusErr := getDatabaseAsyncOperation(c, projectID, databaseID, operationID)
		if statusErr != nil {
			return databaseAsyncOperation{}, fmt.Errorf("upload outcome is unknown and operation status could not be checked: %w; check with `%s`", mapSeedAPIError("check database operation", statusErr), databaseOperationStatusCommand(projectID, databaseID, operationID, false))
		}
		status := strings.ToLower(strings.TrimSpace(operation.Status))
		if databaseSeedUploadLanded(status) {
			return operation, nil
		}
		if status != "awaiting_upload" {
			return databaseAsyncOperation{}, fmt.Errorf("database operation returned unknown status %q after an ambiguous upload", operation.Status)
		}
		if attempt == seedRequestMaxAttempts {
			return databaseAsyncOperation{}, seedUploadDidNotCompleteError(projectID, databaseID, operationID, lastAttemptErr)
		}
		if !waitedBeforeStatusCheck {
			time.Sleep(seedUploadRetryDelay(attempt))
		}
	}
	return databaseAsyncOperation{}, seedUploadDidNotCompleteError(projectID, databaseID, operationID, lastAttemptErr)
}

func putDatabaseSeedAttempt(httpClient *http.Client, c *client, file sqliteSeedFile, uploadPath string, stderr io.Writer, out *databaseAsyncOperationResponse) error {
	source, err := os.Open(file.Path)
	if err != nil {
		return fmt.Errorf("reopen --from-file for upload: %w", err)
	}
	var body io.ReadCloser = source
	if writerIsTerminal(stderr) {
		body = newSeedProgressReadCloser(source, stderr, file.Size)
	}
	return doSeedJSONRequest(httpClient, c, http.MethodPut, uploadPath, body, file.Size, "application/vnd.sqlite3", nil, http.StatusAccepted, out)
}

func doSeedJSONRequest(httpClient *http.Client, c *client, method, path string, body io.Reader, contentLength int64, contentType string, headers map[string]string, expectedStatus int, out any) error {
	closeBodyBeforeRequest := func() {
		if closer, ok := body.(io.Closer); ok {
			_ = closer.Close()
		}
	}
	if strings.TrimSpace(c.token) == "" {
		closeBodyBeforeRequest()
		return errors.New("not logged in; run `comwit login --token <token>`")
	}
	requestURL, err := seedRequestURL(c.apiURL, path)
	if err != nil {
		closeBodyBeforeRequest()
		return err
	}
	req, err := http.NewRequest(method, requestURL, body)
	if err != nil {
		closeBodyBeforeRequest()
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.token))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	if body != nil {
		req.ContentLength = contentLength
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return seedAmbiguousRequestError{err: err}
	}
	responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, seedResponseMaxBytes+1))
	closeErr := resp.Body.Close()
	if readErr != nil {
		return seedAmbiguousRequestError{err: fmt.Errorf("read API response: %w", readErr)}
	}
	if closeErr != nil {
		return seedAmbiguousRequestError{err: fmt.Errorf("close API response: %w", closeErr)}
	}
	if int64(len(responseBody)) > seedResponseMaxBytes {
		return fmt.Errorf("API response exceeds %d bytes", seedResponseMaxBytes)
	}
	if resp.StatusCode != expectedStatus {
		return newAPIError(resp.StatusCode, responseBody)
	}
	if out == nil || len(responseBody) == 0 {
		return nil
	}
	if err := json.Unmarshal(responseBody, out); err != nil {
		return seedAmbiguousRequestError{err: fmt.Errorf("decode API response: %w", err)}
	}
	return nil
}

func seedRequestURL(apiBase, path string) (string, error) {
	if !strings.HasPrefix(path, "/") {
		return "", errors.New("API returned an invalid upload_path")
	}
	reference, err := url.Parse(path)
	if err != nil {
		return "", fmt.Errorf("invalid API path: %w", err)
	}
	if reference.IsAbs() || reference.Host != "" {
		return "", errors.New("API returned an invalid upload_path")
	}
	return apiBase + path, nil
}

func newSeedUploadHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.ResponseHeaderTimeout = 0
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func getDatabaseAsyncOperation(c *client, projectID, databaseID, operationID string) (databaseAsyncOperation, error) {
	var response databaseAsyncOperationResponse
	if err := c.getJSON(projectDatabaseOperationPath(projectID, databaseID, operationID), &response); err != nil {
		return databaseAsyncOperation{}, err
	}
	return response.Operation, nil
}

func waitForDatabaseSeed(c *client, projectID, databaseID, databaseURL, operationID string, stdout io.Writer, lastStatus string) error {
	deadline := time.Now().Add(seedWaitTimeout)
	delay := seedPollInitialDelay
	for {
		operation, err := getDatabaseAsyncOperation(c, projectID, databaseID, operationID)
		if err == nil {
			printDatabaseOperationStatusChange(stdout, operation.Status, &lastStatus)
			switch strings.ToLower(strings.TrimSpace(operation.Status)) {
			case "succeeded":
				fmt.Fprintf(stdout, "ready\t%s\t%s\n", databaseID, databaseURL)
				return nil
			case "failed":
				return databaseSeedFailedError(operation)
			}
		} else if !retryableSeedRequestError(err) {
			return mapSeedAPIError("poll database operation", err)
		}
		if time.Now().Add(delay).After(deadline) {
			return fmt.Errorf("database operation %s did not finish before the 70 minute timeout; re-check with `%s`", operationID, databaseOperationStatusCommand(projectID, databaseID, operationID, true))
		}
		time.Sleep(delay)
		delay = growSeedPollDelay(delay)
	}
}

func databaseOperationCommand(args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] != "status" {
		return errors.New("usage: comwit databases operation status --project <id> --database <id> --operation <id> [--wait]")
	}
	fs := flag.NewFlagSet("databases operation status", flag.ContinueOnError)
	project := fs.String("project", "", "project id")
	database := fs.String("database", "", "database id")
	operation := fs.String("operation", "", "operation id")
	wait := fs.Bool("wait", false, "poll until the operation succeeds or fails")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: comwit databases operation status --project <id> --database <id> --operation <id> [--wait]")
	}
	cfg, projectID, databaseID, err := databaseCommandContext(*project, *database)
	if err != nil {
		return err
	}
	operationID := strings.TrimSpace(*operation)
	if operationID == "" {
		return errors.New("--operation is required")
	}
	c := newClient(cfg)
	if *wait {
		return waitForGenericDatabaseOperation(c, projectID, databaseID, operationID, stdout)
	}
	value, err := getDatabaseAsyncOperation(c, projectID, databaseID, operationID)
	if err != nil {
		return err
	}
	printDatabaseAsyncOperation(stdout, value)
	if strings.EqualFold(value.Status, "failed") {
		return databaseOperationFailedError(value)
	}
	return nil
}

func waitForGenericDatabaseOperation(c *client, projectID, databaseID, operationID string, stdout io.Writer) error {
	deadline := time.Now().Add(seedWaitTimeout)
	delay := seedPollInitialDelay
	lastStatus := ""
	printedIdentity := false
	for {
		operation, err := getDatabaseAsyncOperation(c, projectID, databaseID, operationID)
		if err == nil {
			if !printedIdentity {
				fmt.Fprintf(stdout, "operation\t%s\n", operation.OperationID)
				fmt.Fprintf(stdout, "type\t%s\n", operation.Type)
				printedIdentity = true
			}
			printDatabaseOperationStatusChange(stdout, operation.Status, &lastStatus)
			switch strings.ToLower(strings.TrimSpace(operation.Status)) {
			case "succeeded":
				fmt.Fprintf(stdout, "completed\t%s\n", operation.OperationID)
				return nil
			case "failed":
				printDatabaseAsyncOperationError(stdout, operation.Error)
				return databaseOperationFailedError(operation)
			}
		} else if !retryableSeedRequestError(err) {
			return err
		}
		if time.Now().Add(delay).After(deadline) {
			return fmt.Errorf("database operation %s did not finish before the 70 minute timeout", operationID)
		}
		time.Sleep(delay)
		delay = growSeedPollDelay(delay)
	}
}

func printDatabaseAsyncOperation(w io.Writer, operation databaseAsyncOperation) {
	fmt.Fprintf(w, "operation\t%s\n", operation.OperationID)
	fmt.Fprintf(w, "type\t%s\n", operation.Type)
	fmt.Fprintf(w, "status\t%s\n", operation.Status)
	if operation.ResolvedRestorePoint != "" {
		fmt.Fprintf(w, "resolved_restore_point\t%s\n", operation.ResolvedRestorePoint)
	}
	printDatabaseAsyncOperationError(w, operation.Error)
}

func printDatabaseAsyncOperationError(w io.Writer, operationError *databaseAsyncOperationError) {
	if operationError == nil {
		return
	}
	if strings.TrimSpace(operationError.Code) != "" {
		fmt.Fprintf(w, "error_code\t%s\n", operationError.Code)
	}
	if strings.TrimSpace(operationError.Message) != "" {
		fmt.Fprintf(w, "error\t%s\n", operationError.Message)
	}
}

func printDatabaseOperationStatusChange(w io.Writer, status string, last *string) {
	status = strings.TrimSpace(status)
	if status != "" && status != *last {
		fmt.Fprintf(w, "status\t%s\n", status)
		*last = status
	}
}

func databaseOperationStatusCommand(projectID, databaseID, operationID string, wait bool) string {
	command := fmt.Sprintf("comwit databases operation status --project %s --database %s --operation %s", projectID, databaseID, operationID)
	if wait {
		command += " --wait"
	}
	return command
}

func databaseSeedUploadLanded(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "queued", "validating", "publishing", "succeeded", "failed":
		return true
	default:
		return false
	}
}

func definitiveSeedUploadError(err error) bool {
	var apiErr apiError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.status >= 200 && apiErr.status < 300 {
		// The upload contract has one successful response: 202 Accepted.
		return true
	}
	return apiErr.status >= 400 && apiErr.status < 500 && apiErr.status != http.StatusRequestTimeout && apiErr.status != http.StatusTooManyRequests
}

func seedUploadInProgressError(err error) bool {
	var apiErr apiError
	return errors.As(err, &apiErr) && apiErr.status == http.StatusConflict && strings.EqualFold(apiErr.code, "SEED_UPLOAD_IN_PROGRESS")
}

func retryableSeedRequestError(err error) bool {
	if err == nil {
		return false
	}
	var ambiguous seedAmbiguousRequestError
	if errors.As(err, &ambiguous) {
		return true
	}
	var apiErr apiError
	if errors.As(err, &apiErr) {
		return apiErr.status == http.StatusTooManyRequests || apiErr.status >= 500
	}
	var networkErr net.Error
	return errors.As(err, &networkErr)
}

func seedRetryDelay(attempt int) time.Duration {
	return time.Duration(attempt) * seedRetryBaseDelay
}

func seedUploadRetryDelay(attempt int) time.Duration {
	index := attempt - 1
	if index < 0 {
		index = 0
	}
	if index >= len(seedUploadRetryDelays) {
		index = len(seedUploadRetryDelays) - 1
	}
	return seedUploadRetryDelays[index]
}

func seedUploadDidNotCompleteError(projectID, databaseID, operationID string, lastAttemptErr error) error {
	command := databaseOperationStatusCommand(projectID, databaseID, operationID, false)
	if lastAttemptErr != nil {
		return fmt.Errorf("upload did not complete after %d attempts; check with `%s`; last upload attempt failed: %w", seedRequestMaxAttempts, command, lastAttemptErr)
	}
	return fmt.Errorf("upload did not complete after %d attempts; check with `%s`", seedRequestMaxAttempts, command)
}

func growSeedPollDelay(delay time.Duration) time.Duration {
	delay += time.Second
	if delay > seedPollMaximumDelay {
		return seedPollMaximumDelay
	}
	return delay
}

func databaseOperationFailedError(operation databaseAsyncOperation) error {
	if strings.EqualFold(operation.Type, "database_seed") {
		return databaseSeedFailedError(operation)
	}
	message := "database operation failed"
	code := ""
	if operation.Error != nil {
		code = strings.TrimSpace(operation.Error.Code)
		if strings.TrimSpace(operation.Error.Message) != "" {
			message = strings.TrimSpace(operation.Error.Message)
		}
	}
	if code != "" {
		return fmt.Errorf("database operation %s failed (%s): %s", operation.OperationID, code, message)
	}
	return fmt.Errorf("database operation %s failed: %s", operation.OperationID, message)
}

func databaseSeedFailedError(operation databaseAsyncOperation) error {
	code := ""
	message := ""
	if operation.Error != nil {
		code = strings.ToUpper(strings.TrimSpace(operation.Error.Code))
		message = strings.TrimSpace(operation.Error.Message)
	}
	if code == "" {
		code = knownSeedErrorCode(message)
	}
	userMessage := seedErrorMessage(code)
	if userMessage == "" {
		userMessage = message
	}
	if userMessage == "" {
		userMessage = "the database could not be created from the SQLite file"
	}
	if code != "" {
		return fmt.Errorf("database creation operation %s failed (%s): %s", operation.OperationID, code, userMessage)
	}
	return fmt.Errorf("database creation operation %s failed: %s", operation.OperationID, userMessage)
}

func mapSeedAPIError(action string, err error) error {
	if err == nil {
		return nil
	}
	code := seedAPIErrorCode(err)
	if message := seedErrorMessage(code); message != "" {
		return fmt.Errorf("%s failed (%s): %s", action, code, message)
	}
	var apiErr apiError
	if errors.As(err, &apiErr) {
		if strings.TrimSpace(apiErr.detail) != "" {
			return fmt.Errorf("%s failed: %s", action, apiErr.detail)
		}
		return fmt.Errorf("%s failed: API HTTP %d", action, apiErr.status)
	}
	return fmt.Errorf("%s failed: %w", action, err)
}

func seedAPIErrorCode(err error) string {
	var apiErr apiError
	if !errors.As(err, &apiErr) {
		return ""
	}
	return strings.ToUpper(strings.TrimSpace(apiErr.code))
}

func knownSeedErrorCode(value string) string {
	upper := strings.ToUpper(value)
	for _, code := range []string{
		"TENANT_SEED_TARGET_EXISTS",
		"IDEMPOTENCY_MISMATCH",
		"SEED_FILE_TOO_SMALL",
		"SEED_FILE_TOO_LARGE",
		"SEED_INTEGRITY_CHECK_FAILED",
		"SEED_FOREIGN_KEY_CHECK_FAILED",
		"SEED_GENERATED_COLUMN_UNSUPPORTED",
		"SEED_ENGINE_INCOMPATIBLE",
		"SEED_UPLOAD_EXPIRED",
		"SEED_CONTENT_LENGTH_REQUIRED",
		"SEED_CONTENT_TYPE_UNSUPPORTED",
		"SEED_LENGTH_MISMATCH",
		"SEED_DIGEST_MISMATCH",
		"SEED_CONTENT_ALREADY_FIXED",
		"SEED_UPLOAD_IN_PROGRESS",
		"SEED_EXECUTOR_LOST",
		"SEED_JOB_FAILED",
		"SEED_STAGING_QUOTA_EXHAUSTED",
		"SEED_UPLOAD_CAPACITY_EXHAUSTED",
		"RUNNER_POOL_UNAVAILABLE",
	} {
		if strings.Contains(upper, code) {
			return code
		}
	}
	return ""
}

func seedErrorMessage(code string) string {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "TENANT_SEED_TARGET_EXISTS":
		return "a database with this name already exists"
	case "IDEMPOTENCY_MISMATCH":
		return "this idempotency key was already used with different database creation parameters"
	case "SEED_FILE_TOO_SMALL":
		return "the file is too small to be a SQLite database (minimum 100 bytes)"
	case "SEED_FILE_TOO_LARGE":
		return "the SQLite file is too large (maximum 2 GiB)"
	case "SEED_INTEGRITY_CHECK_FAILED":
		return "the SQLite file failed its integrity check and may be corrupt"
	case "SEED_FOREIGN_KEY_CHECK_FAILED":
		return "the SQLite file contains foreign key violations"
	case "SEED_GENERATED_COLUMN_UNSUPPORTED":
		return "the SQLite schema contains generated columns, which are unsupported"
	case "SEED_ENGINE_INCOMPATIBLE":
		return "the SQLite schema uses features unsupported by the Comwit database engine"
	case "SEED_UPLOAD_EXPIRED":
		return "the upload session expired; run the command again with a new idempotency key"
	case "SEED_CONTENT_LENGTH_REQUIRED":
		return "the upload requires an exact Content-Length"
	case "SEED_CONTENT_TYPE_UNSUPPORTED":
		return "the upload requires Content-Type application/vnd.sqlite3"
	case "SEED_LENGTH_MISMATCH":
		return "the uploaded byte count did not match the file size recorded during preflight"
	case "SEED_DIGEST_MISMATCH":
		return "the uploaded bytes did not match the preflight SHA-256; retry with an unchanged file"
	case "SEED_CONTENT_ALREADY_FIXED":
		return "different content was already uploaded for this database operation"
	case "SEED_UPLOAD_IN_PROGRESS":
		return "another upload is already in progress for this database operation"
	case "SEED_EXECUTOR_LOST":
		return "the server worker handling the upload was lost before the file was stored; run the command again with a new idempotency key"
	case "SEED_JOB_FAILED":
		return "this database creation operation has already failed; retry with a new idempotency key"
	case "SEED_STAGING_QUOTA_EXHAUSTED", "SEED_UPLOAD_CAPACITY_EXHAUSTED", "RUNNER_POOL_UNAVAILABLE":
		return "database upload capacity is temporarily unavailable; retry later"
	default:
		return ""
	}
}

func writerIsTerminal(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

type seedProgressReadCloser struct {
	mu       sync.Mutex
	source   *os.File
	output   io.Writer
	total    int64
	read     int64
	lastDraw time.Time
	finished bool
	closed   bool
	closeErr error
}

func newSeedProgressReadCloser(source *os.File, output io.Writer, total int64) *seedProgressReadCloser {
	fmt.Fprintf(output, "upload\t0/%d bytes (0.0%%)", total)
	return &seedProgressReadCloser{source: source, output: output, total: total, lastDraw: time.Now()}
}

func (r *seedProgressReadCloser) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, os.ErrClosed
	}
	n, err := r.source.Read(p)
	r.read += int64(n)
	now := time.Now()
	if !r.finished && (r.read >= r.total || now.Sub(r.lastDraw) >= 250*time.Millisecond) {
		r.drawLocked(r.read >= r.total)
		r.lastDraw = now
	}
	if errors.Is(err, io.EOF) && !r.finished {
		r.drawLocked(true)
	}
	return n, err
}

func (r *seedProgressReadCloser) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return r.closeErr
	}
	r.closed = true
	if !r.finished {
		r.drawLocked(true)
	}
	r.closeErr = r.source.Close()
	return r.closeErr
}

func (r *seedProgressReadCloser) drawLocked(final bool) {
	percent := float64(0)
	if r.total > 0 {
		percent = float64(r.read) * 100 / float64(r.total)
	}
	if percent > 100 {
		percent = 100
	}
	fmt.Fprintf(r.output, "\rupload\t%d/%d bytes (%.1f%%)", r.read, r.total, percent)
	if final {
		fmt.Fprintln(r.output)
		r.finished = true
	}
}
