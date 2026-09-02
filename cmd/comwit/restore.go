package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Point-in-time recovery (PITR) CLI commands. These call the product-shaped
// platform-api restore routes; the CLI never talks to Louhi directly. See
// ../comwit-cloud/docs/decisions/0013-database-restore-pitr.md.

const restoreWaitAttempts = 300 // 300 * 2s ≈ 10 minutes, matching the client display timeout.

type restorePointItem struct {
	GenerationID   string  `json:"generation_id"`
	CreatedAtMS    uint64  `json:"created_at_ms"`
	BaseFrameNo    uint64  `json:"base_frame_no"`
	Pinned         bool    `json:"pinned"`
	PreciseUntilMS *uint64 `json:"precise_until_ms"`
}

type restoreAliasItem struct {
	Alias       string `json:"alias"`
	TargetKind  string `json:"target_kind"`
	TargetValue string `json:"target_value"`
	CreatedAtMS uint64 `json:"created_at_ms"`
}

type restorePointsResponse struct {
	RestorePoints []restorePointItem `json:"restore_points"`
	Aliases       []restoreAliasItem `json:"aliases"`
}

type restoreOperationItem struct {
	OperationID          string                       `json:"operation_id"`
	Type                 string                       `json:"type"`
	Status               string                       `json:"status"`
	ResolvedRestorePoint string                       `json:"resolved_restore_point"`
	Error                *databaseAsyncOperationError `json:"error,omitempty"`
	CreatedAtMS          uint64                       `json:"created_at_ms"`
	UpdatedAtMS          uint64                       `json:"updated_at_ms"`
}

type restoredDatabaseView struct {
	DatabaseID  string `json:"database_id"`
	Name        string `json:"name"`
	DatabaseURL string `json:"database_url"`
	Status      string `json:"status"`
}

type restoreResponse struct {
	Operation     restoreOperationItem `json:"operation"`
	Database      restoredDatabaseView `json:"database"`
	DatabaseToken *string              `json:"database_token,omitempty"`
}

type restoreOperationResponse struct {
	Operation restoreOperationItem `json:"operation"`
}

type restoreAliasResponse struct {
	DatabaseID string `json:"database_id"`
	Alias      string `json:"alias"`
}

func projectDatabaseRestorePointsPath(projectID, databaseID string) string {
	return projectDatabasePath(projectID, databaseID) + "/restore-points"
}

func projectDatabaseRestorePath(projectID, databaseID string) string {
	return projectDatabasePath(projectID, databaseID) + "/restore"
}

func projectDatabaseOperationPath(projectID, databaseID, operationID string) string {
	return projectDatabasePath(projectID, databaseID) + "/operations/" + url.PathEscape(operationID)
}

func projectDatabaseAliasPath(projectID, databaseID, alias string) string {
	return projectDatabasePath(projectID, databaseID) + "/aliases/" + url.PathEscape(alias)
}

func databaseRestorePointsCommand(args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] != "list" {
		return errors.New("usage: comwit databases restore-points list --project <id> --database <id>")
	}
	fs := flag.NewFlagSet("databases restore-points list", flag.ContinueOnError)
	project := fs.String("project", "", "project id")
	database := fs.String("database", "", "database id")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	cfg, projectID, databaseID, err := databaseCommandContext(*project, *database)
	if err != nil {
		return err
	}
	var body restorePointsResponse
	if err := newClient(cfg).getJSON(projectDatabaseRestorePointsPath(projectID, databaseID), &body); err != nil {
		return err
	}
	printRestorePoints(stdout, body.RestorePoints)
	return nil
}

func databaseRestoreCommand(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("databases restore", flag.ContinueOnError)
	project := fs.String("project", "", "project id")
	database := fs.String("database", "", "source database id")
	at := fs.String("at", "", "restore point timestamp (unix ms or RFC3339)")
	generation := fs.String("generation", "", "restore point generation id")
	alias := fs.String("alias", "", "restore point alias")
	name := fs.String("name", "", "display name for the restored database")
	tokenOut := fs.String("token-out", "", "write the restored database's one-time token to this file (0600)")
	wait := fs.Bool("wait", false, "poll the restore operation until it succeeds or fails")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, projectID, databaseID, err := databaseCommandContext(*project, *database)
	if err != nil {
		return err
	}
	selector, err := buildRestoreSelector(*at, *generation, *alias)
	if err != nil {
		return err
	}
	payload := map[string]any{"restore_to": selector}
	if trimmed := strings.TrimSpace(*name); trimmed != "" {
		payload["name"] = trimmed
	}

	client := newClient(cfg)
	var body restoreResponse
	if err := client.postJSON(projectDatabaseRestorePath(projectID, databaseID), payload, &body); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "operation\t%s\n", body.Operation.OperationID)
	fmt.Fprintf(stdout, "status\t%s\n", body.Operation.Status)
	fmt.Fprintf(stdout, "database\t%s\n", body.Database.DatabaseID)
	fmt.Fprintf(stdout, "url\t%s\n", body.Database.DatabaseURL)
	token := ""
	if body.DatabaseToken != nil {
		token = strings.TrimSpace(*body.DatabaseToken)
	}
	if token != "" {
		fmt.Fprintf(stdout, "token\t%s\n", token)
		if path := strings.TrimSpace(*tokenOut); path != "" {
			if err := writeTokenOut(path, token); err != nil {
				return fmt.Errorf("write token to %s: %w", path, err)
			}
			fmt.Fprintf(stdout, "token_out\t%s\n", path)
		}
	}

	if *wait {
		return waitRestoreOperation(client, projectID, databaseID, body.Operation.OperationID, stdout)
	}
	return nil
}

func databaseRestoreStatusCommand(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("databases restore status", flag.ContinueOnError)
	project := fs.String("project", "", "project id")
	database := fs.String("database", "", "source database id")
	operation := fs.String("operation", "", "restore operation id")
	wait := fs.Bool("wait", false, "poll until the operation succeeds or fails")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, projectID, databaseID, err := databaseCommandContext(*project, *database)
	if err != nil {
		return err
	}
	operationID := strings.TrimSpace(*operation)
	if operationID == "" {
		return errors.New("--operation is required")
	}
	client := newClient(cfg)
	if *wait {
		return waitRestoreOperation(client, projectID, databaseID, operationID, stdout)
	}
	var body restoreOperationResponse
	if err := client.getJSON(projectDatabaseOperationPath(projectID, databaseID, operationID), &body); err != nil {
		return err
	}
	printRestoreOperation(stdout, body.Operation)
	if strings.EqualFold(body.Operation.Status, "failed") {
		return restoreFailedError(body.Operation)
	}
	return nil
}

func databaseAliasesCommand(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: comwit databases aliases <list|set|delete>")
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("databases aliases list", flag.ContinueOnError)
		project := fs.String("project", "", "project id")
		database := fs.String("database", "", "database id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		cfg, projectID, databaseID, err := databaseCommandContext(*project, *database)
		if err != nil {
			return err
		}
		var body restorePointsResponse
		if err := newClient(cfg).getJSON(projectDatabaseRestorePointsPath(projectID, databaseID), &body); err != nil {
			return err
		}
		printRestoreAliases(stdout, body.Aliases)
		return nil
	case "set":
		fs := flag.NewFlagSet("databases aliases set", flag.ContinueOnError)
		project := fs.String("project", "", "project id")
		database := fs.String("database", "", "database id")
		alias := fs.String("alias", "", "alias name")
		at := fs.String("at", "", "restore point timestamp (unix ms or RFC3339)")
		generation := fs.String("generation", "", "restore point generation id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		cfg, projectID, databaseID, err := databaseCommandContext(*project, *database)
		if err != nil {
			return err
		}
		aliasName := strings.TrimSpace(*alias)
		if aliasName == "" {
			return errors.New("--alias is required")
		}
		selector, err := buildAliasSelector(*at, *generation)
		if err != nil {
			return err
		}
		var body restoreAliasResponse
		if err := newClient(cfg).postJSON(projectDatabaseAliasPath(projectID, databaseID, aliasName), map[string]any{"restore_to": selector}, &body); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "alias\t%s\n", body.Alias)
		fmt.Fprintf(stdout, "database\t%s\n", body.DatabaseID)
		return nil
	case "delete":
		fs := flag.NewFlagSet("databases aliases delete", flag.ContinueOnError)
		project := fs.String("project", "", "project id")
		database := fs.String("database", "", "database id")
		alias := fs.String("alias", "", "alias name")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		cfg, projectID, databaseID, err := databaseCommandContext(*project, *database)
		if err != nil {
			return err
		}
		aliasName := strings.TrimSpace(*alias)
		if aliasName == "" {
			return errors.New("--alias is required")
		}
		var body restoreAliasResponse
		if err := newClient(cfg).deleteJSON(projectDatabaseAliasPath(projectID, databaseID, aliasName), &body); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "deleted\t%s\n", body.Alias)
		return nil
	default:
		return fmt.Errorf("unknown databases aliases command %q", args[0])
	}
}

func buildRestoreSelector(at, generation, alias string) (map[string]any, error) {
	selector := map[string]any{}
	count := 0
	if strings.TrimSpace(at) != "" {
		ms, err := parseRestoreTimestampMS(at)
		if err != nil {
			return nil, err
		}
		selector["timestamp_ms"] = ms
		count++
	}
	if trimmed := strings.TrimSpace(generation); trimmed != "" {
		selector["generation"] = trimmed
		count++
	}
	if trimmed := strings.TrimSpace(alias); trimmed != "" {
		selector["alias"] = trimmed
		count++
	}
	if count != 1 {
		return nil, errors.New("set exactly one of --at, --generation, or --alias")
	}
	return selector, nil
}

func buildAliasSelector(at, generation string) (map[string]any, error) {
	selector := map[string]any{}
	count := 0
	if strings.TrimSpace(at) != "" {
		ms, err := parseRestoreTimestampMS(at)
		if err != nil {
			return nil, err
		}
		selector["timestamp_ms"] = ms
		count++
	}
	if trimmed := strings.TrimSpace(generation); trimmed != "" {
		selector["generation"] = trimmed
		count++
	}
	if count != 1 {
		return nil, errors.New("set exactly one of --at or --generation")
	}
	return selector, nil
}

func parseRestoreTimestampMS(value string) (uint64, error) {
	value = strings.TrimSpace(value)
	var ms uint64
	if ms, err := strconv.ParseUint(value, 10, 64); err == nil {
		if ms > uint64(time.Now().UnixMilli()) {
			return 0, errors.New("--at must not be in the future")
		}
		return ms, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil || parsed.UnixMilli() < 0 {
		return 0, fmt.Errorf("invalid --at %q; use unix milliseconds or an RFC3339 timestamp", value)
	}
	ms = uint64(parsed.UnixMilli())
	if ms > uint64(time.Now().UnixMilli()) {
		return 0, errors.New("--at must not be in the future")
	}
	return ms, nil
}

func writeTokenOut(path, token string) error {
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return err
	}
	// WriteFile keeps the mode of an existing file, so enforce the documented
	// secret-only permissions after both create and overwrite.
	return os.Chmod(path, 0o600)
}

func waitRestoreOperation(c *client, projectID, databaseID, operationID string, stdout io.Writer) error {
	last := ""
	for attempt := 0; ; attempt++ {
		var body restoreOperationResponse
		if err := c.getJSON(projectDatabaseOperationPath(projectID, databaseID, operationID), &body); err != nil {
			return err
		}
		op := body.Operation
		if op.Status != last {
			fmt.Fprintf(stdout, "status\t%s\n", op.Status)
			last = op.Status
		}
		switch strings.ToLower(op.Status) {
		case "succeeded":
			fmt.Fprintf(stdout, "restored\t%s\n", operationID)
			return nil
		case "failed":
			return restoreFailedError(op)
		}
		if attempt >= restoreWaitAttempts-1 {
			return fmt.Errorf("restore operation %s did not finish before timeout; re-check with `comwit databases restore status --project %s --database %s --operation %s`", operationID, projectID, databaseID, operationID)
		}
		time.Sleep(2 * time.Second)
	}
}

func restoreFailedError(op restoreOperationItem) error {
	message := ""
	code := ""
	if op.Error != nil {
		message = strings.TrimSpace(op.Error.Message)
		code = strings.TrimSpace(op.Error.Code)
	}
	if message == "" {
		message = "restore failed"
	}
	if code != "" {
		return fmt.Errorf("restore operation %s failed (%s): %s", op.OperationID, code, message)
	}
	return fmt.Errorf("restore operation %s failed: %s", op.OperationID, message)
}

func printRestorePoints(w io.Writer, points []restorePointItem) {
	fmt.Fprintln(w, "GENERATION\tCREATED AT\tPINNED\tPRECISE UNTIL")
	for _, point := range points {
		fmt.Fprintf(w, "%s\t%s\t%t\t%s\n", point.GenerationID, formatUnixMS(point.CreatedAtMS), point.Pinned, formatPreciseUntil(point.PreciseUntilMS))
	}
}

func printRestoreAliases(w io.Writer, aliases []restoreAliasItem) {
	fmt.Fprintln(w, "ALIAS\tTARGET KIND\tTARGET VALUE\tCREATED AT")
	for _, alias := range aliases {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", alias.Alias, alias.TargetKind, alias.TargetValue, formatUnixMS(alias.CreatedAtMS))
	}
}

func printRestoreOperation(w io.Writer, op restoreOperationItem) {
	fmt.Fprintf(w, "operation\t%s\n", op.OperationID)
	fmt.Fprintf(w, "type\t%s\n", op.Type)
	fmt.Fprintf(w, "status\t%s\n", op.Status)
	if op.ResolvedRestorePoint != "" {
		fmt.Fprintf(w, "resolved_restore_point\t%s\n", op.ResolvedRestorePoint)
	}
	printDatabaseAsyncOperationError(w, op.Error)
}

func formatUnixMS(ms uint64) string {
	if ms == 0 {
		return ""
	}
	return time.UnixMilli(int64(ms)).UTC().Format(time.RFC3339)
}

func formatPreciseUntil(ms *uint64) string {
	if ms == nil {
		return "up to now"
	}
	return formatUnixMS(*ms)
}
