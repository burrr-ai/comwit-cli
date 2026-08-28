package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const (
	envProjectKey          = "COMWIT_PROJECT"
	envAppKey              = "COMWIT_APP"
	envCloudTokenKey       = "COMWIT_CLOUD_TOKEN"
	envDatabaseURLKey      = "DATABASE_URL"
	envStorageIDKey        = "COMWIT_STORAGE_ID"
	envStorageEndpointKey  = "COMWIT_STORAGE_ENDPOINT"
	envStorageBucketKey    = "COMWIT_STORAGE_BUCKET"
	envStoragePublicURLKey = "COMWIT_STORAGE_PUBLIC_BASE_URL"
)

var dotenvKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type envUpdate struct {
	Key   string
	Value string
}

// cwdDotEnvIdentifier deliberately permits only the non-secret project and App
// identifiers. In particular, command context resolution must never load the
// COMWIT_CLOUD_TOKEN value from a project file.
func cwdDotEnvIdentifier(key string) (string, error) {
	if key != envProjectKey && key != envAppKey {
		return "", nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve current directory: %w", err)
	}
	path, exists, err := validateDotEnvContextPath(filepath.Join(cwd, ".env"))
	if err != nil {
		return "", err
	}
	if !exists {
		return "", nil
	}
	value, err := readDotEnvValue(path, key)
	if err != nil {
		return "", fmt.Errorf("read .env context: %w", err)
	}
	return strings.TrimSpace(value), nil
}

func readDotEnvValue(path, wantedKey string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	value := ""
	found := false
	for scanner.Scan() {
		line := scanner.Text()
		key, raw, ok := parseDotEnvAssignment(line)
		if !ok {
			if dotEnvLineTargetsKey(line, wantedKey) {
				return "", fmt.Errorf("malformed %s assignment", wantedKey)
			}
			continue
		}
		if key != wantedKey {
			continue
		}
		if found {
			return "", fmt.Errorf("duplicate %s assignment", wantedKey)
		}
		parsed, err := parseDotEnvValue(raw)
		if err != nil {
			return "", fmt.Errorf("parse %s from .env: %w", wantedKey, err)
		}
		value = parsed
		found = true
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return value, nil
}

func dotEnvLineTargetsKey(line, wantedKey string) bool {
	trimmed := strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return false
	}
	if strings.HasPrefix(trimmed, "export ") || strings.HasPrefix(trimmed, "export\t") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "export"))
	}
	if !strings.HasPrefix(trimmed, wantedKey) {
		return false
	}
	if len(trimmed) == len(wantedKey) {
		return true
	}
	next := trimmed[len(wantedKey)]
	return !(next == '_' || next >= 'a' && next <= 'z' || next >= 'A' && next <= 'Z' || next >= '0' && next <= '9')
}

func parseDotEnvAssignment(line string) (string, string, bool) {
	trimmed := strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", false
	}
	if strings.HasPrefix(trimmed, "export ") || strings.HasPrefix(trimmed, "export\t") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "export"))
	}
	equals := strings.IndexByte(trimmed, '=')
	if equals < 0 {
		return "", "", false
	}
	key := strings.TrimSpace(trimmed[:equals])
	if !dotenvKeyPattern.MatchString(key) {
		return "", "", false
	}
	return key, trimmed[equals+1:], true
}

func parseDotEnvValue(raw string) (string, error) {
	raw = strings.TrimSpace(stripDotEnvComment(raw))
	if raw == "" {
		return "", nil
	}
	if raw[0] == '\'' {
		if len(raw) < 2 || raw[len(raw)-1] != '\'' {
			return "", errors.New("unterminated single-quoted value")
		}
		return raw[1 : len(raw)-1], nil
	}
	if raw[0] == '"' {
		if len(raw) < 2 || raw[len(raw)-1] != '"' {
			return "", errors.New("unterminated double-quoted value")
		}
		value, err := strconv.Unquote(raw)
		if err != nil {
			return "", errors.New("invalid double-quoted value")
		}
		return value, nil
	}
	return raw, nil
}

func stripDotEnvComment(raw string) string {
	quote := byte(0)
	escaped := false
	for i := 0; i < len(raw); i++ {
		char := raw[i]
		if escaped {
			escaped = false
			continue
		}
		if quote == '"' && char == '\\' {
			escaped = true
			continue
		}
		if char == '\'' || char == '"' {
			if quote == 0 {
				quote = char
			} else if quote == char {
				quote = 0
			}
			continue
		}
		if quote == 0 && char == '#' && (i == 0 || raw[i-1] == ' ' || raw[i-1] == '\t') {
			return strings.TrimRight(raw[:i], " \t")
		}
	}
	return raw
}

func prepareEnvOutput(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	return validateEnvOutputPath(path)
}

func validateDotEnvContextPath(path string) (string, bool, error) {
	absPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", false, fmt.Errorf("resolve .env context path: %w", err)
	}
	info, err := os.Lstat(absPath)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("inspect .env context: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", false, fmt.Errorf("refusing .env context symlink %s", absPath)
	}
	if !info.Mode().IsRegular() {
		return "", false, fmt.Errorf(".env context is not a regular file: %s", absPath)
	}
	validated, err := validateGitignoredUntrackedPath(absPath)
	if err != nil {
		return "", false, fmt.Errorf("refusing .env context: %w", err)
	}
	return validated, true, nil
}

func validateEnvOutputPath(path string) (string, error) {
	absPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve env output path: %w", err)
	}
	if info, err := os.Lstat(absPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("refusing env output symlink %s", absPath)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("env output is not a regular file: %s", absPath)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect env output: %w", err)
	}

	return validateGitignoredUntrackedPath(absPath)
}

func validateGitignoredUntrackedPath(absPath string) (string, error) {
	parent := filepath.Dir(absPath)
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", fmt.Errorf("resolve env directory: %w", err)
	}
	absPath = filepath.Join(resolvedParent, filepath.Base(absPath))
	parent = resolvedParent
	rootBytes, err := exec.Command("git", "-C", parent, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", errors.New("env file must be inside a Git worktree and ignored by Git")
	}
	root := strings.TrimSpace(string(rootBytes))
	if resolvedRoot, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
		root = resolvedRoot
	}
	relative, err := filepath.Rel(root, absPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("env file must stay inside its Git worktree")
	}
	gitPath := filepath.ToSlash(relative)

	tracked := exec.Command("git", "-C", root, "ls-files", "--error-unmatch", "--", gitPath)
	if err := tracked.Run(); err == nil {
		return "", fmt.Errorf("env file is tracked: %s", absPath)
	} else if exitCode(err) != 1 {
		return "", errors.New("could not verify whether env file is tracked")
	}

	ignored := exec.Command("git", "-C", root, "check-ignore", "--quiet", "--no-index", "--", gitPath)
	if err := ignored.Run(); err != nil {
		if exitCode(err) == 1 {
			return "", fmt.Errorf("env file is not gitignored: %s", absPath)
		}
		return "", errors.New("could not verify whether env file is gitignored")
	}
	return absPath, nil
}

func exitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func writeEnvUpdates(path string, updates ...envUpdate) ([]string, error) {
	absPath, err := validateEnvOutputPath(path)
	if err != nil {
		return nil, err
	}
	if len(updates) == 0 {
		return nil, nil
	}

	values := make(map[string]string, len(updates))
	keys := make([]string, 0, len(updates))
	for _, update := range updates {
		if !dotenvKeyPattern.MatchString(update.Key) {
			return nil, fmt.Errorf("invalid env key %q", update.Key)
		}
		if strings.ContainsAny(update.Value, "\x00\r\n") {
			return nil, fmt.Errorf("env value for %s contains a forbidden control character", update.Key)
		}
		if _, exists := values[update.Key]; !exists {
			keys = append(keys, update.Key)
		}
		values[update.Key] = update.Value
	}

	existing, err := os.ReadFile(absPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read env output: %w", err)
	}
	newline := "\n"
	if bytes.Contains(existing, []byte("\r\n")) {
		newline = "\r\n"
	}
	normalized := strings.ReplaceAll(string(existing), "\r\n", "\n")
	normalized = strings.TrimSuffix(normalized, "\n")
	lines := []string{}
	if normalized != "" {
		lines = strings.Split(normalized, "\n")
	}
	seen := make(map[string]bool, len(values))
	for index, line := range lines {
		key, _, ok := parseDotEnvAssignment(line)
		value, wanted := values[key]
		if !ok || !wanted {
			continue
		}
		lines[index] = replaceDotEnvAssignment(line, key, encodeDotEnvValue(value))
		seen[key] = true
	}
	for _, key := range keys {
		if !seen[key] {
			lines = append(lines, key+"="+encodeDotEnvValue(values[key]))
		}
	}
	content := strings.Join(lines, newline) + newline
	if err := writePrivateFileAtomic(absPath, []byte(content)); err != nil {
		return nil, fmt.Errorf("write env output: %w", err)
	}
	return keys, nil
}

func printUpdatedEnvKeys(stdout io.Writer, keys []string) {
	for _, key := range keys {
		fmt.Fprintf(stdout, "updated_env\t%s\n", key)
	}
}

func replaceDotEnvAssignment(line, key, encodedValue string) string {
	equals := strings.IndexByte(line, '=')
	if equals < 0 {
		return key + "=" + encodedValue
	}
	raw := line[equals+1:]
	comment := dotEnvCommentSuffix(raw)
	return line[:equals+1] + encodedValue + comment
}

func dotEnvCommentSuffix(raw string) string {
	stripped := stripDotEnvComment(raw)
	if len(stripped) == len(raw) {
		return ""
	}
	commentStart := len(stripped)
	for commentStart < len(raw) && (raw[commentStart] == ' ' || raw[commentStart] == '\t') {
		commentStart++
	}
	if commentStart >= len(raw) || raw[commentStart] != '#' {
		return ""
	}
	return " " + raw[commentStart:]
}

func encodeDotEnvValue(value string) string {
	if value == "" {
		return ""
	}
	if strings.IndexFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && !strings.ContainsRune("_./:@%+,-", r)
	}) == -1 {
		return value
	}
	return strconv.Quote(value)
}
