package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const defaultGitHost = "git.cloud.comwit.io"

// gitCredential implements the stdin/stdout protocol used by Git credential helpers.
func gitCredential(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) != 1 || (args[0] != "get" && args[0] != "store" && args[0] != "erase") {
		return errors.New("usage: comwit git-credential <get|store|erase>")
	}

	var protocol, host string
	input := bufio.NewReader(stdin)
	for {
		line, err := input.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, "=")
		if ok {
			switch key {
			case "protocol":
				protocol = value
			case "host":
				host = value
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}

	if args[0] != "get" || protocol != "https" || host != gitCredentialHost() {
		return nil
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfg.Token) == "" || strings.ContainsAny(cfg.Token, "\r\n") {
		return nil
	}
	_, err = fmt.Fprintf(stdout, "username=comwit\npassword=%s\n\n", cfg.Token)
	return err
}

// The API override selects the matching Git service. Local API endpoints without
// an api. prefix use their own host, including any explicit port.
func gitCredentialHost() string {
	u, err := url.Parse(apiURL())
	if err != nil || u.Hostname() == "" {
		return defaultGitHost
	}
	host := u.Hostname()
	if strings.HasPrefix(host, "api.") {
		host = "git." + strings.TrimPrefix(host, "api.")
	}
	if port := u.Port(); port != "" {
		return net.JoinHostPort(host, port)
	}
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

func gitCredentialHelperValue(executable string) string {
	return "!'" + strings.ReplaceAll(executable, "'", "'\\''") + "' git-credential"
}

func configureGitCredentialHelper(stdout, stderr io.Writer) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		fmt.Fprintln(stderr, "warning: git not found on PATH; git access was not configured")
		return
	}
	executable, err := os.Executable()
	if err == nil {
		executable, err = filepath.EvalSymlinks(executable)
	}
	if err != nil {
		fmt.Fprintf(stderr, "warning: could not find comwit executable for git access: %v\n", err)
		return
	}
	host := gitCredentialHost()
	cmd := exec.Command(gitPath, "config", "--global", "credential.https://"+host+".helper", gitCredentialHelperValue(executable))
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(stderr, "warning: could not configure git access for %s: %v\n", host, err)
		return
	}
	fmt.Fprintf(stdout, "git access configured for %s\n", host)
}
