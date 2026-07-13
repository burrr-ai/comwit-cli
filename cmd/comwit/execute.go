package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
)

const (
	maxExecuteStatements     = 250
	maxExecuteStatementBytes = 50 * 1024
	maxExecuteSQLBytes       = 900 * 1024
)

type databaseQueryStatement struct {
	SQL string `json:"sql"`
}

type databaseQueryRequest struct {
	SQL   string                   `json:"sql,omitempty"`
	Batch []databaseQueryStatement `json:"batch,omitempty"`
}

type databaseQueryColumn struct {
	Name     string `json:"name"`
	Decltype string `json:"decltype"`
}

type databaseQueryCell struct {
	Type   string  `json:"type"`
	Value  *string `json:"value,omitempty"`
	Base64 *string `json:"base64,omitempty"`
}

type databaseQueryError struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

type databaseQueryResult struct {
	Statement        int                    `json:"statement"`
	Success          bool                   `json:"success"`
	Skipped          bool                   `json:"skipped,omitempty"`
	Columns          *[]databaseQueryColumn `json:"columns,omitempty"`
	Rows             *[][]databaseQueryCell `json:"rows,omitempty"`
	AffectedRowCount *uint64                `json:"affected_row_count,omitempty"`
	LastInsertRowID  *string                `json:"last_insert_rowid,omitempty"`
	RowsRead         *uint64                `json:"rows_read,omitempty"`
	RowsWritten      *uint64                `json:"rows_written,omitempty"`
	QueryDurationMS  *float64               `json:"query_duration_ms,omitempty"`
	Error            *databaseQueryError    `json:"error,omitempty"`
}

type databaseQueryResponse struct {
	Results []databaseQueryResult `json:"results"`
}

func databaseExecuteCommand(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("databases execute", flag.ContinueOnError)
	project := fs.String("project", "", "project id")
	database := fs.String("database", "", "database id")
	command := fs.String("command", "", "SQL command to execute")
	file := fs.String("file", "", "SQLite-compatible SQL file to execute")
	jsonOutput := fs.Bool("json", false, "print the public API JSON result envelope")
	remote := fs.Bool("remote", false, "accepted for Wrangler compatibility; Comwit execution is always remote")
	local := fs.Bool("local", false, "unsupported; Comwit CLI has no local database engine")
	if err := fs.Parse(args); err != nil {
		return err
	}
	_ = *remote
	if fs.NArg() != 0 {
		return executeUsageError()
	}
	if *local {
		return errors.New("--local is not supported; Comwit database execution is remote by default")
	}

	cfg, projectID, databaseID, err := databaseCommandContext(*project, *database)
	if err != nil {
		return err
	}
	sqlInput, err := loadExecuteSQL(*command, *file)
	if err != nil {
		return err
	}
	statements, err := splitSQLForExecution(sqlInput)
	if err != nil {
		return err
	}
	if err := validateExecuteStatements(statements); err != nil {
		return err
	}

	payload := databaseQueryRequest{}
	if len(statements) == 1 {
		payload.SQL = statements[0].SQL
	} else {
		payload.Batch = make([]databaseQueryStatement, 0, len(statements))
		for _, statement := range statements {
			payload.Batch = append(payload.Batch, databaseQueryStatement{SQL: statement.SQL})
		}
	}

	var response databaseQueryResponse
	path := projectDatabasePath(projectID, databaseID) + "/query"
	if err := newClient(cfg).postJSON(path, payload, &response); err != nil {
		return err
	}
	if len(response.Results) != len(statements) {
		return fmt.Errorf("query response included %d results for %d statements", len(response.Results), len(statements))
	}
	if err := validateDatabaseQueryResponse(response.Results); err != nil {
		return err
	}

	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(response); err != nil {
			return err
		}
	} else {
		printDatabaseQueryResults(stdout, response.Results)
	}

	for _, result := range response.Results {
		if !result.Success {
			message := "SQL execution failed"
			if result.Error != nil && strings.TrimSpace(result.Error.Message) != "" {
				message = strings.TrimSpace(result.Error.Message)
			}
			message = printableQueryText(message)
			if result.Skipped {
				return fmt.Errorf("statement %d was skipped: %s", result.Statement, message)
			}
			return fmt.Errorf("statement %d failed: %s", result.Statement, message)
		}
	}
	return nil
}

func executeUsageError() error {
	return errors.New("usage: comwit databases execute --project <id> --database <id> (--command <sql>|--file <path>) [--json]")
}

func loadExecuteSQL(command, path string) (string, error) {
	command = strings.TrimSpace(command)
	path = strings.TrimSpace(path)
	if (command == "") == (path == "") {
		return "", executeUsageError()
	}
	if command != "" {
		return command, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxExecuteSQLBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxExecuteSQLBytes {
		return "", fmt.Errorf("SQL file is larger than the %d-byte request limit", maxExecuteSQLBytes)
	}
	if looksLikeSQLiteDatabaseFile(data) {
		return "", errors.New("--file expects SQL text, but the file looks like a SQLite .db database")
	}
	return string(data), nil
}

func validateDatabaseQueryResponse(results []databaseQueryResult) error {
	for i, result := range results {
		if result.Statement != i+1 {
			return fmt.Errorf("query response result %d has invalid statement number %d", i+1, result.Statement)
		}
		if result.Success {
			if result.Skipped || result.Error != nil || result.Columns == nil || result.Rows == nil || result.AffectedRowCount == nil || result.RowsRead == nil || result.RowsWritten == nil || result.QueryDurationMS == nil {
				return fmt.Errorf("query response result %d is missing successful statement fields", result.Statement)
			}
			continue
		}
		if result.Error == nil {
			return fmt.Errorf("query response result %d is missing an error", result.Statement)
		}
	}
	return nil
}

func splitSQLForExecution(input string) ([]dumpStatement, error) {
	input = strings.TrimPrefix(input, "\ufeff")
	if !statementHasSQL(input) {
		return nil, errors.New("SQL input contains no statements")
	}
	statements, err := splitSQLDump(input)
	if err == nil {
		return statements, nil
	}

	// sqlite3_complete requires a terminating semicolon even for an otherwise
	// complete statement. Match common shell behavior by accepting the final
	// semicolon as optional, while still rejecting unterminated strings/triggers.
	trimmed := strings.TrimSpace(input)
	statements, withTerminatorErr := splitSQLDump(trimmed + ";")
	if withTerminatorErr == nil {
		return statements, nil
	}
	// If a trailing `-- comment` swallowed the first synthesized terminator,
	// put another one on a new line.
	statements, afterLineCommentErr := splitSQLDump(trimmed + "\n;")
	if afterLineCommentErr == nil {
		return statements, nil
	}
	return nil, err
}

func validateExecuteStatements(statements []dumpStatement) error {
	if len(statements) == 0 {
		return errors.New("SQL input contains no statements")
	}
	if len(statements) > maxExecuteStatements {
		return fmt.Errorf("SQL input contains %d statements; maximum is %d", len(statements), maxExecuteStatements)
	}
	total := 0
	for _, statement := range statements {
		size := len([]byte(statement.SQL))
		if size > maxExecuteStatementBytes {
			return fmt.Errorf("statement %d is %d bytes; maximum is %d", statement.Index, size, maxExecuteStatementBytes)
		}
		total += size
	}
	if total > maxExecuteSQLBytes {
		return fmt.Errorf("SQL input is %d bytes; maximum is %d", total, maxExecuteSQLBytes)
	}
	return nil
}

func printDatabaseQueryResults(w io.Writer, results []databaseQueryResult) {
	totalDuration := 0.0
	totalRead := uint64(0)
	totalWritten := uint64(0)
	for i, result := range results {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "statement %d\n", result.Statement)
		if !result.Success {
			status := "failed"
			if result.Skipped {
				status = "skipped"
			}
			message := "SQL execution failed"
			if result.Error != nil && result.Error.Message != "" {
				message = result.Error.Message
			}
			fmt.Fprintf(w, "status\t%s\nerror\t%s\n", status, printableQueryText(message))
			continue
		}

		columns := *result.Columns
		rows := *result.Rows
		if len(columns) > 0 {
			tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
			for columnIndex, column := range columns {
				if columnIndex > 0 {
					fmt.Fprint(tw, "\t")
				}
				fmt.Fprint(tw, printableQueryText(column.Name))
			}
			fmt.Fprintln(tw)
			for _, row := range rows {
				for columnIndex := range columns {
					if columnIndex > 0 {
						fmt.Fprint(tw, "\t")
					}
					if columnIndex < len(row) {
						fmt.Fprint(tw, printableQueryText(formatDatabaseQueryCell(row[columnIndex])))
					} else {
						fmt.Fprint(tw, "NULL")
					}
				}
				fmt.Fprintln(tw)
			}
			_ = tw.Flush()
		} else {
			fmt.Fprintln(w, "status\tok")
		}
		fmt.Fprintf(w, "rows_affected\t%d\n", *result.AffectedRowCount)
		if result.LastInsertRowID != nil {
			fmt.Fprintf(w, "last_insert_rowid\t%s\n", *result.LastInsertRowID)
		}
		fmt.Fprintf(w, "rows_read\t%d\nrows_written\t%d\nduration_ms\t%.3f\n", *result.RowsRead, *result.RowsWritten, *result.QueryDurationMS)
		totalDuration += *result.QueryDurationMS
		totalRead += *result.RowsRead
		totalWritten += *result.RowsWritten
	}
	if len(results) > 1 {
		fmt.Fprintf(w, "\nsummary\tstatements=%d rows_read=%d rows_written=%d duration_ms=%.3f\n", len(results), totalRead, totalWritten, totalDuration)
	}
}

func formatDatabaseQueryCell(cell databaseQueryCell) string {
	switch cell.Type {
	case "null":
		return "NULL"
	case "blob":
		if cell.Base64 == nil {
			return "base64:"
		}
		return "base64:" + *cell.Base64
	default:
		if cell.Value == nil {
			return ""
		}
		return *cell.Value
	}
}

func printableQueryText(value string) string {
	var clean strings.Builder
	for _, r := range value {
		switch r {
		case '\r':
			clean.WriteString("\\r")
		case '\n':
			clean.WriteString("\\n")
		case '\t':
			clean.WriteString("\\t")
		default:
			if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
				fmt.Fprintf(&clean, "\\x%02x", r)
				continue
			}
			clean.WriteRune(r)
		}
	}
	return clean.String()
}
