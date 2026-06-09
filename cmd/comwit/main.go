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
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// version is set at release build time via -ldflags "-X main.version=...".
var version = "0.1.0-dev"

const (
	defaultAPIURL = "https://api.cloud.comwit.io"
)

type configFile struct {
	Token          string `json:"token"`
	DefaultProject string `json:"default_project,omitempty"`
}

type client struct {
	apiURL     string
	token      string
	httpClient *http.Client
}

type projectView struct {
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
}

type projectsResponse struct {
	Projects []projectView `json:"projects"`
}

type appView struct {
	AppID         string  `json:"app_id"`
	Name          string  `json:"name"`
	ActiveBuildID *string `json:"active_build_id"`
}

type appsResponse struct {
	Apps []appView `json:"apps"`
}

type appResponse struct {
	App appView `json:"app"`
}

type buildView struct {
	BuildID      string  `json:"build_id"`
	CreatedAt    *string `json:"created_at,omitempty"`
	ArtifactSize *int64  `json:"artifact_size,omitempty"`
}

type buildsResponse struct {
	Builds []buildView `json:"builds"`
}

type databaseCreateResponse struct {
	DatabaseID    string  `json:"database_id"`
	DatabaseURL   string  `json:"database_url"`
	Created       bool    `json:"created"`
	DatabaseToken *string `json:"database_token,omitempty"`
}

type databaseListItem struct {
	DatabaseID  string `json:"database_id"`
	Name        string `json:"name"`
	DatabaseURL string `json:"database_url"`
	Status      string `json:"status"`
}

type databasesListResponse struct {
	Databases []databaseListItem `json:"databases"`
}

type deployResponse struct {
	AppID    string   `json:"app_id"`
	BuildID  string   `json:"build_id"`
	Hosts    []string `json:"hosts"`
	Uploaded bool     `json:"uploaded"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		usage(stdout)
		return nil
	}

	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, "comwit", version)
		return nil
	case "login":
		return login(args[1:], stdout)
	case "projects":
		return projects(args[1:], stdout)
	case "databases":
		return databases(args[1:], stdout)
	case "apps":
		return apps(args[1:], stdout)
	case "deploy":
		return deploy(args[1:], stdout, stderr)
	case "upgrade", "update", "self-update":
		return upgrade(args[1:], stdout)
	case "help", "-h", "--help":
		usage(stdout)
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  comwit version
  comwit login [--project <id>]               (browser device login)
  comwit login --token <token> [--project <id>]
  comwit projects list
  comwit databases create --project <id> --name <name>
  comwit databases list --project <id>
  comwit apps list --project <id>
  comwit apps create --project <id> --name <name>
  comwit apps builds --project <id> --app <id>
  comwit deploy --project <id> --app <id> --package <dist/brrrd tar.zst or dir>
  comwit upgrade                              (self-update to the latest release)

Environment:
  COMWIT_CONFIG   Override config file path.
  COMWIT_PROJECT  Default project id for commands that accept --project.`)
}

func login(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	token := fs.String("token", "", "API token")
	project := fs.String("project", "", "default project id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*token) == "" {
		return deviceLogin(stdout, strings.TrimSpace(*project))
	}

	cfg := configFile{
		Token:          strings.TrimSpace(*token),
		DefaultProject: strings.TrimSpace(*project),
	}
	path, err := saveConfig(cfg)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Saved credentials to %s\n", path)
	return nil
}

type deviceStartResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	Interval        int    `json:"interval"`
	ExpiresIn       int    `json:"expires_in"`
}

type devicePollResponse struct {
	Status string `json:"status"`
	Token  string `json:"token"`
}

func deviceLogin(stdout io.Writer, project string) error {
	c := &client{apiURL: defaultAPIURL, httpClient: &http.Client{Timeout: 30 * time.Second}}

	var start deviceStartResponse
	if err := c.postPublic("/v1/auth/device", nil, &start); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "To authorize the comwit CLI, open:\n\n  %s\n\nand enter the code: %s\n\n", start.VerificationURI, start.UserCode)
	_ = openBrowser(start.VerificationURI)

	interval := start.Interval
	if interval <= 0 {
		interval = 5
	}
	expires := start.ExpiresIn
	if expires <= 0 {
		expires = 600
	}
	deadline := time.Now().Add(time.Duration(expires) * time.Second)
	fmt.Fprintln(stdout, "Waiting for approval...")

	for time.Now().Before(deadline) {
		time.Sleep(time.Duration(interval) * time.Second)
		var poll devicePollResponse
		if err := c.postPublic("/v1/auth/device/token", map[string]string{"device_code": start.DeviceCode}, &poll); err != nil {
			return err
		}
		switch poll.Status {
		case "token":
			cfg := configFile{Token: poll.Token, DefaultProject: project}
			path, err := saveConfig(cfg)
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "Logged in. Saved credentials to %s\n", path)
			return nil
		case "pending":
			continue
		case "denied":
			return errors.New("authorization was denied")
		case "expired":
			return errors.New("device code expired; run `comwit login` again")
		}
	}
	return errors.New("device login timed out; run `comwit login` again")
}

func openBrowser(target string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{target}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", target}
	default:
		name, args = "xdg-open", []string{target}
	}
	return exec.Command(name, args...).Start()
}

func databases(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: comwit databases <create|list>")
	}
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("databases create", flag.ContinueOnError)
		project := fs.String("project", "", "project id")
		name := fs.String("name", "", "database name")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		projectID := selectProject(*project, cfg)
		if projectID == "" {
			return errors.New("--project is required")
		}
		if strings.TrimSpace(*name) == "" {
			return errors.New("--name is required")
		}
		var body databaseCreateResponse
		payload := map[string]string{"name": strings.TrimSpace(*name)}
		path := "/v1/projects/" + url.PathEscape(projectID) + "/databases"
		if err := newClient(cfg).postJSON(path, payload, &body); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s\t%s\tcreated=%t\n", body.DatabaseID, body.DatabaseURL, body.Created)
		if body.DatabaseToken != nil && strings.TrimSpace(*body.DatabaseToken) != "" {
			fmt.Fprintf(stdout, "token\t%s\n", *body.DatabaseToken)
		}
		return nil
	case "list":
		fs := flag.NewFlagSet("databases list", flag.ContinueOnError)
		project := fs.String("project", "", "project id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		projectID := selectProject(*project, cfg)
		if projectID == "" {
			return errors.New("--project is required")
		}
		var body databasesListResponse
		path := "/v1/projects/" + url.PathEscape(projectID) + "/databases"
		if err := newClient(cfg).getJSON(path, &body); err != nil {
			return err
		}
		printDatabases(stdout, body.Databases)
		return nil
	default:
		return fmt.Errorf("unknown databases command %q", args[0])
	}
}

func projects(args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] != "list" {
		return errors.New("usage: comwit projects list")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	c := newClient(cfg)
	var body projectsResponse
	if err := c.getJSON("/v1/projects", &body); err == nil {
		printProjects(stdout, body.Projects)
		return nil
	} else if !isStatus(err, http.StatusNotFound) && !isStatus(err, http.StatusNotImplemented) {
		return err
	}

	project := defaultProject(cfg)
	if project == "" {
		return errors.New("project listing is not enabled by this API token yet; set a default with COMWIT_PROJECT or `comwit login --project <id>`")
	}
	printProjects(stdout, []projectView{{
		ProjectID: project,
		Name:      "configured project",
	}})
	return nil
}

func apps(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: comwit apps <list|create|builds>")
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("apps list", flag.ContinueOnError)
		project := fs.String("project", "", "project id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		projectID := selectProject(*project, cfg)
		if projectID == "" {
			return errors.New("--project is required")
		}
		var body appsResponse
		if err := newClient(cfg).getJSON("/v1/projects/"+url.PathEscape(projectID)+"/apps", &body); err != nil {
			return err
		}
		printApps(stdout, body.Apps)
		return nil
	case "create":
		fs := flag.NewFlagSet("apps create", flag.ContinueOnError)
		project := fs.String("project", "", "project id")
		name := fs.String("name", "", "app name")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		projectID := selectProject(*project, cfg)
		if projectID == "" {
			return errors.New("--project is required")
		}
		if strings.TrimSpace(*name) == "" {
			return errors.New("--name is required")
		}
		var body appResponse
		payload := map[string]string{"name": strings.TrimSpace(*name)}
		if err := newClient(cfg).postJSON("/v1/projects/"+url.PathEscape(projectID)+"/apps", payload, &body); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s\t%s\n", body.App.AppID, body.App.Name)
		return nil
	case "builds":
		fs := flag.NewFlagSet("apps builds", flag.ContinueOnError)
		project := fs.String("project", "", "project id")
		app := fs.String("app", "", "app id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		projectID := selectProject(*project, cfg)
		if projectID == "" {
			return errors.New("--project is required")
		}
		appID := strings.TrimSpace(*app)
		if appID == "" {
			return errors.New("--app is required")
		}
		var body buildsResponse
		path := "/v1/projects/" + url.PathEscape(projectID) + "/apps/" + url.PathEscape(appID) + "/builds"
		if err := newClient(cfg).getJSON(path, &body); err != nil {
			return err
		}
		printBuilds(stdout, body.Builds)
		return nil
	default:
		return fmt.Errorf("unknown apps command %q", args[0])
	}
}

func deploy(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("deploy", flag.ContinueOnError)
	project := fs.String("project", "", "project id")
	app := fs.String("app", "", "app id")
	pkg := fs.String("package", "", "packaged .tar.zst file or brrrd dist directory")
	host := fs.String("host", "", "domain host to bind; may be repeated as comma-separated list")
	envRef := fs.String("env-ref", "", "runtime environment reference")
	maxConcurrent := fs.Uint("max-concurrent-requests", 0, "runtime max concurrent requests")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	projectID := selectProject(*project, cfg)
	if projectID == "" {
		return errors.New("--project is required")
	}
	if strings.TrimSpace(*app) == "" {
		return errors.New("--app is required")
	}
	if strings.TrimSpace(*pkg) == "" {
		return errors.New("--package is required")
	}

	packagePath, cleanup, err := packageForUpload(*pkg, stderr)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}
	data, err := os.ReadFile(packagePath)
	if err != nil {
		return err
	}

	query := url.Values{}
	if strings.TrimSpace(*host) != "" {
		query.Set("hosts", strings.TrimSpace(*host))
	}
	if strings.TrimSpace(*envRef) != "" {
		query.Set("env_ref", strings.TrimSpace(*envRef))
	}
	if *maxConcurrent > 0 {
		query.Set("max_concurrent_requests", strconv.FormatUint(uint64(*maxConcurrent), 10))
	}
	path := "/v1/projects/" + url.PathEscape(projectID) + "/apps/" + url.PathEscape(strings.TrimSpace(*app)) + "/deployments"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}

	var body deployResponse
	if err := newClient(cfg).postRaw(path, data, &body); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "deployed app=%s build=%s uploaded=%t\n", body.AppID, body.BuildID, body.Uploaded)
	if len(body.Hosts) > 0 {
		fmt.Fprintf(stdout, "hosts=%s\n", strings.Join(body.Hosts, ","))
	}
	return nil
}

func newClient(cfg configFile) *client {
	return &client{
		apiURL:     defaultAPIURL,
		token:      cfg.Token,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *client) getJSON(path string, out any) error {
	return c.do(http.MethodGet, path, nil, "application/json", out)
}

func (c *client) postJSON(path string, payload any, out any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return c.do(http.MethodPost, path, data, "application/json", out)
}

func (c *client) postRaw(path string, data []byte, out any) error {
	return c.do(http.MethodPost, path, data, "application/octet-stream", out)
}

// postPublic calls an unauthenticated endpoint (the device-login bootstrap).
func (c *client) postPublic(path string, payload any, out any) error {
	var body []byte
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = data
	}
	req, err := http.NewRequest(http.MethodPost, c.apiURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
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
	return json.Unmarshal(data, out)
}

func (c *client) do(method, path string, body []byte, contentType string, out any) error {
	if strings.TrimSpace(c.token) == "" {
		return errors.New("not logged in; run `comwit login --token <token>`")
	}
	req, err := http.NewRequest(method, c.apiURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.token))
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.httpClient.Do(req)
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
		return fmt.Errorf("decode API response: %w", err)
	}
	return nil
}

type apiError struct {
	status int
	body   string
}

func (e apiError) Error() string {
	if e.body != "" {
		return fmt.Sprintf("API HTTP %d: %s", e.status, e.body)
	}
	return fmt.Sprintf("API HTTP %d", e.status)
}

func isStatus(err error, status int) bool {
	var apiErr apiError
	return errors.As(err, &apiErr) && apiErr.status == status
}

func loadConfig() (configFile, error) {
	path, err := configPath()
	if err != nil {
		return configFile{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return configFile{}, nil
	}
	if err != nil {
		return configFile{}, err
	}
	var cfg configFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return configFile{}, err
	}
	return cfg, nil
}

func saveConfig(cfg configFile) (string, error) {
	path, err := configPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func configPath() (string, error) {
	if path := strings.TrimSpace(os.Getenv("COMWIT_CONFIG")); path != "" {
		return path, nil
	}
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return filepath.Join(dir, "comwit", "config.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "comwit", "config.json"), nil
}

func selectProject(project string, cfg configFile) string {
	if project = strings.TrimSpace(project); project != "" {
		return project
	}
	return defaultProject(cfg)
}

func defaultProject(cfg configFile) string {
	if project := strings.TrimSpace(os.Getenv("COMWIT_PROJECT")); project != "" {
		return project
	}
	return strings.TrimSpace(cfg.DefaultProject)
}

func packageForUpload(path string, stderr io.Writer) (string, func(), error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", nil, err
	}
	if !info.IsDir() {
		return path, nil, nil
	}
	tmp, err := os.CreateTemp("", "comwit-package-*.tar.zst")
	if err != nil {
		return "", nil, err
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", nil, err
	}
	fmt.Fprintf(stderr, "packing %s -> %s\n", path, tmpPath)
	cmd := exec.Command("tar", "--zstd", "-cf", tmpPath, "-C", path, ".")
	output, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.Remove(tmpPath)
		return "", nil, fmt.Errorf("package directory with tar --zstd: %w: %s; pass a prebuilt .tar.zst if local tar lacks zstd support", err, strings.TrimSpace(string(output)))
	}
	return tmpPath, func() { _ = os.Remove(tmpPath) }, nil
}

func printDatabases(w io.Writer, databases []databaseListItem) {
	fmt.Fprintln(w, "DATABASE ID\tNAME\tSTATUS\tURL")
	for _, database := range databases {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", database.DatabaseID, database.Name, database.Status, database.DatabaseURL)
	}
}

func printProjects(w io.Writer, projects []projectView) {
	fmt.Fprintln(w, "PROJECT ID\tNAME")
	for _, project := range projects {
		fmt.Fprintf(w, "%s\t%s\n", project.ProjectID, project.Name)
	}
}

func printApps(w io.Writer, apps []appView) {
	fmt.Fprintln(w, "APP ID\tNAME\tACTIVE BUILD")
	for _, app := range apps {
		active := ""
		if app.ActiveBuildID != nil {
			active = *app.ActiveBuildID
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", app.AppID, app.Name, active)
	}
}

func printBuilds(w io.Writer, builds []buildView) {
	fmt.Fprintln(w, "BUILD ID\tCREATED AT\tARTIFACT SIZE")
	for _, build := range builds {
		createdAt := ""
		if build.CreatedAt != nil {
			createdAt = *build.CreatedAt
		}
		artifactSize := ""
		if build.ArtifactSize != nil {
			artifactSize = strconv.FormatInt(*build.ArtifactSize, 10)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", build.BuildID, createdAt, artifactSize)
	}
}
