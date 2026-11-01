package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	defaultGitHost  = "git.cloud.comwit.io"
	gitSetupCommand = "comwit auth setup-git"
)

// gitAccessTimeout bounds each Git call so a credential helper waiting on a
// dialog nobody can see cannot hang an agent-run login.
var gitAccessTimeout = 20 * time.Second

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

// Git runs `!` helpers through sh, including Git for Windows' bundled MSYS sh,
// so Windows paths use forward slashes there.
func gitCredentialHelperValue(goos, executable string) string {
	if goos == "windows" {
		executable = strings.ReplaceAll(executable, `\`, "/")
	}
	return "!'" + strings.ReplaceAll(executable, "'", "'\\''") + "' git-credential"
}

// gitAccessError carries the agent-readable reason and the one next step.
type gitAccessError struct {
	host, reason, next string
}

func (e *gitAccessError) Error() string {
	return "git access not configured for " + e.host
}

// configureGitAccess makes HTTPS Git use the CLI login for the Comwit Git host
// and proves it with `git credential fill`. The comwit helper is preferred so
// a later login rotates Git access too; when it cannot answer, the token is
// handed to Git's own credential store, as the legacy connect prompt did.
// The token is only ever passed on stdin, never printed.
func configureGitAccess(stdout io.Writer) error {
	host := gitCredentialHost()
	err := setupGitAccess(host)
	if err == nil {
		fmt.Fprintf(stdout, "git access configured for %s\n", host)
		return nil
	}
	var failure *gitAccessError
	if !errors.As(err, &failure) {
		failure = &gitAccessError{host: host, reason: err.Error(), next: "run `" + gitSetupCommand + "`"}
	}
	fmt.Fprintf(stdout, "git access not configured for %s\n", host)
	fmt.Fprintf(stdout, "reason: %s\n", failure.reason)
	fmt.Fprintf(stdout, "next: %s\n", failure.next)
	return failure
}

func setupGitAccess(host string) error {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return &gitAccessError{host, "git is not installed or not on PATH", "install Git, open a new shell, then run `" + gitSetupCommand + "`"}
	}
	cfg, err := loadConfig()
	token := strings.TrimSpace(cfg.Token)
	if err != nil || token == "" || strings.ContainsAny(token, "\r\n") {
		return &gitAccessError{host, "no CLI login found", "run `comwit login`"}
	}

	helperErr := installGitCredentialHelper(gitPath, host)
	if helperErr == nil {
		helperErr = verifyGitCredential(gitPath, host, token)
	}
	if helperErr == nil {
		return nil
	}
	storeErr := storeGitCredential(gitPath, host, token)
	if storeErr == nil {
		storeErr = verifyGitCredential(gitPath, host, token)
	}
	if storeErr == nil {
		return nil
	}
	return &gitAccessError{
		host,
		fmt.Sprintf("comwit credential helper: %v; Git credential store: %v", helperErr, storeErr),
		gitStoreHint(runtime.GOOS) + ", then run `" + gitSetupCommand + "`",
	}
}

// installGitCredentialHelper registers the comwit helper for the Git host. The
// empty value first resets helpers inherited from system or global config,
// such as Git Credential Manager on Windows, which would otherwise be asked
// first and prompt or fail where no terminal or desktop is available.
func installGitCredentialHelper(gitPath, host string) error {
	executable, err := os.Executable()
	if err == nil {
		executable, err = filepath.EvalSymlinks(executable)
	}
	if err != nil {
		return fmt.Errorf("cannot locate the comwit executable: %v", err)
	}
	key := "credential.https://" + host + ".helper"
	if _, err := runGit(gitPath, "", "config", "--global", "--replace-all", key, ""); err != nil {
		return err
	}
	_, err = runGit(gitPath, "", "config", "--global", "--add", key, gitCredentialHelperValue(runtime.GOOS, executable))
	return err
}

// storeGitCredential removes the comwit helper so the OS helper applies again,
// then replaces any saved credential for the host with the CLI login.
func storeGitCredential(gitPath, host, token string) error {
	// Exit status 5 only means the key was already absent.
	_, _ = runGit(gitPath, "", "config", "--global", "--unset-all", "credential.https://"+host+".helper")
	if _, err := runGit(gitPath, "protocol=https\nhost="+host+"\n\n", "credential", "reject"); err != nil {
		return err
	}
	_, err := runGit(gitPath, "protocol=https\nhost="+host+"\nusername=comwit\npassword="+token+"\n\n", "credential", "approve")
	return err
}

func verifyGitCredential(gitPath, host, token string) error {
	out, err := runGit(gitPath, "protocol=https\nhost="+host+"\n\n", "credential", "fill")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		if line == "password="+token {
			return nil
		}
	}
	return errors.New("git returned a different saved credential")
}

// runGit runs git without any way to prompt and returns stdout. Errors carry
// git's last stderr line only, because stdout may hold a credential.
func runGit(gitPath, stdin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitAccessTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, gitPath, args...)
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"GCM_INTERACTIVE=never",
		"GCM_GUI_PROMPT=false",
	)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", fmt.Errorf("git %s timed out after %s", args[0]+" "+args[1], gitAccessTimeout)
	}
	if err != nil {
		lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
		detail := strings.TrimSpace(lines[len(lines)-1])
		if detail == "" {
			detail = err.Error()
		}
		if len(detail) > 200 {
			detail = detail[:200]
		}
		return "", fmt.Errorf("git %s failed: %s", args[0]+" "+args[1], detail)
	}
	return stdout.String(), nil
}

// gitStoreHint names the command that enables Git's usual credential store.
func gitStoreHint(goos string) string {
	switch goos {
	case "windows":
		return "run `git config --global credential.helper manager`"
	case "darwin":
		return "run `git config --global credential.helper osxkeychain`"
	default:
		return "run `git config --global credential.helper store`"
	}
}
