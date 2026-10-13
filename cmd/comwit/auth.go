package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

const exportTokenUsage = "usage: comwit auth export-token --env-out <path>"

var personalTokenPattern = regexp.MustCompile(`^cwt_[0-9a-f]{64}$`)
var envAssignmentPattern = regexp.MustCompile(`^[ \t]*(?:export[ \t]+)?([A-Za-z_][A-Za-z0-9_.-]*)[ \t]*=[ \t]*`)

func authCommand(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New(exportTokenUsage)
	}
	if args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		fmt.Fprintln(stdout, exportTokenUsage)
		return nil
	}
	if args[0] != "export-token" {
		return errors.New(exportTokenUsage)
	}
	fs := flag.NewFlagSet("auth export-token", flag.ContinueOnError)
	// Flag errors may contain supplied values; never echo an accidental token.
	fs.SetOutput(io.Discard)
	envOut := fs.String("env-out", "", "merge COMWIT_CLOUD_TOKEN into this dotenv file (0600)")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stdout, exportTokenUsage)
			fs.SetOutput(stdout)
			fs.PrintDefaults()
			return nil
		}
		return errors.New(exportTokenUsage)
	}
	if strings.TrimSpace(*envOut) == "" || fs.NArg() != 0 {
		return errors.New(exportTokenUsage)
	}
	cfg, err := loadConfig()
	if err != nil {
		return errors.New("cannot read CLI login config; run `comwit login`")
	}
	if cfg.Token == "" {
		return errors.New("not logged in; run `comwit login`")
	}
	if !personalTokenPattern.MatchString(cfg.Token) {
		return errors.New("export requires a personal cwt_ login token; run `comwit login`")
	}
	if err := exportTokenEnv(*envOut, cfg.Token); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Saved COMWIT_CLOUD_TOKEN to env file.")
	return nil
}

func exportTokenEnv(path, token string) error {
	info, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect --env-out destination")
	}
	if info != nil && !info.Mode().IsRegular() {
		return errors.New("--env-out must be a regular file, not a symlink or directory")
	}
	config, err := configPath()
	if err != nil {
		return errors.New("cannot locate CLI login config")
	}
	configInfo, err := os.Stat(config)
	if err != nil {
		return errors.New("cannot inspect CLI login config")
	}
	if info != nil && os.SameFile(info, configInfo) {
		return errors.New("--env-out must not overwrite CLI login config")
	}
	var data []byte
	if info != nil {
		data, err = os.ReadFile(path)
		if err != nil {
			return errors.New("cannot read --env-out file")
		}
	}
	merged, err := mergeTokenEnv(string(data), token)
	if err != nil {
		return err
	}
	// A same-directory temporary file keeps the previous file intact on failure
	// and never exposes an intermediate partially written credential file.
	f, err := os.CreateTemp(filepath.Dir(path), ".comwit-env-*")
	if err != nil {
		return errors.New("cannot create private env file in destination directory")
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := io.WriteString(f, merged); err != nil {
		return errors.New("cannot write private env file")
	}
	if err := f.Sync(); err != nil {
		return errors.New("cannot sync private env file")
	}
	if err := f.Close(); err != nil {
		return errors.New("cannot close private env file")
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return errors.New("cannot replace --env-out file")
	}
	return nil
}

// Parse records rather than replacing lines: a token-looking line inside a
// quoted multiline value belongs to that value and must not be touched. Unknown
// syntax fails closed so a new assignment cannot end up inside a broken value.
func mergeTokenEnv(data, token string) (string, error) {
	if !utf8.ValidString(data) || strings.ContainsRune(data, '\x00') {
		return "", errors.New("--env-out must contain UTF-8 dotenv text")
	}
	var out strings.Builder
	if strings.HasPrefix(data, "\ufeff") {
		out.WriteString("\ufeff")
		data = strings.TrimPrefix(data, "\ufeff")
	}
	newline := "\n"
	if i := strings.IndexByte(data, '\n'); i > 0 && data[i-1] == '\r' {
		newline = "\r\n"
	}
	found := false
	for pos := 0; pos < len(data); {
		start := pos
		end := dotenvLineEnd(data, start)
		line := data[start:end]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			out.WriteString(line)
			pos = end
			continue
		}
		match := envAssignmentPattern.FindStringSubmatchIndex(line)
		if match == nil {
			return "", errors.New("unsupported dotenv syntax in --env-out; file left unchanged")
		}
		key := line[match[2]:match[3]]
		valueStart := start + match[1]
		if valueStart < len(data) && strings.ContainsRune("\"'`", rune(data[valueStart])) {
			quote := data[valueStart]
			closeAt := valueStart + 1
			for ; closeAt < len(data); closeAt++ {
				if data[closeAt] == '\\' && closeAt+1 < len(data) {
					closeAt++
				} else if data[closeAt] == quote {
					break
				}
			}
			if closeAt == len(data) {
				return "", errors.New("unterminated dotenv quote in --env-out; file left unchanged")
			}
			end = dotenvLineEnd(data, closeAt+1)
			tail := strings.TrimSpace(data[closeAt+1 : end])
			if tail != "" && !strings.HasPrefix(tail, "#") {
				return "", errors.New("unsupported dotenv syntax after quote in --env-out; file left unchanged")
			}
		}
		if key != "COMWIT_CLOUD_TOKEN" {
			out.WriteString(data[start:end])
		} else if !found {
			out.WriteString("COMWIT_CLOUD_TOKEN=" + token)
			if strings.HasSuffix(data[start:end], "\r\n") {
				out.WriteString("\r\n")
			} else if strings.HasSuffix(data[start:end], "\n") {
				out.WriteByte('\n')
			}
			found = true
		}
		pos = end
	}
	if !found {
		if data != "" && !strings.HasSuffix(data, "\n") {
			out.WriteString(newline)
		}
		out.WriteString("COMWIT_CLOUD_TOKEN=" + token + newline)
	}
	return out.String(), nil
}

func dotenvLineEnd(data string, start int) int {
	if i := strings.IndexByte(data[start:], '\n'); i >= 0 {
		return start + i + 1
	}
	return len(data)
}
