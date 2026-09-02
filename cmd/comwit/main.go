package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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

const (
	version             = "0.1.7"
	defaultAPIURL       = "https://api.cloud.comwit.io"
	defaultGitHubAPIURL = "https://api.github.com"
	defaultGitHubRepo   = "burrr-ai/comwit-cli"
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
	DeployedAtMS *uint64 `json:"deployed_at_ms,omitempty"`
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

type databaseStatusView struct {
	DatabaseID  string `json:"database_id"`
	DatabaseURL string `json:"database_url"`
	Status      string `json:"status"`
}

type databaseOperationResponse struct {
	OK       bool               `json:"ok"`
	Database databaseStatusView `json:"database"`
}

type databaseTokenResponse struct {
	DatabaseID    string `json:"database_id"`
	DatabaseURL   string `json:"database_url"`
	DatabaseToken string `json:"database_token"`
	Status        string `json:"status"`
}

type domainDelegationView struct {
	CheckedAt           string   `json:"checked_at"`
	Matched             bool     `json:"matched"`
	ObservedNameservers []string `json:"observed_nameservers"`
}

type domainView struct {
	ID           string               `json:"id"`
	ProjectID    string               `json:"project_id"`
	Domain       string               `json:"domain"`
	DNSMode      string               `json:"dns_mode"`
	Status       string               `json:"status"`
	HostedZoneID string               `json:"hosted_zone_id"`
	Nameservers  []string             `json:"nameservers"`
	Delegation   domainDelegationView `json:"delegation"`
	CreatedAt    string               `json:"created_at"`
	UpdatedAt    string               `json:"updated_at"`
}

type domainsResponse struct {
	Domains []domainView `json:"domains"`
}

type domainResponse struct {
	Domain domainView `json:"domain"`
}

type delegationCheckResponse struct {
	Domain  domainView `json:"domain"`
	Matched bool       `json:"matched"`
}

type dnsRecordView struct {
	ID                string   `json:"id"`
	ProjectDomainID   string   `json:"project_domain_id"`
	Name              string   `json:"name"`
	Type              string   `json:"type"`
	TTL               int64    `json:"ttl"`
	Values            []string `json:"values"`
	Owner             string   `json:"owner"`
	OwnerResourceType string   `json:"owner_resource_type"`
	OwnerResourceID   string   `json:"owner_resource_id"`
	Status            string   `json:"status"`
	Route53ChangeID   string   `json:"route53_change_id"`
	LastError         string   `json:"last_error"`
	CreatedAt         string   `json:"created_at"`
	UpdatedAt         string   `json:"updated_at"`
}

type dnsRecordsResponse struct {
	Records []dnsRecordView `json:"records"`
}

type dnsRecordResponse struct {
	Record dnsRecordView `json:"record"`
}

type deployResponse struct {
	AppID    string   `json:"app_id"`
	BuildID  string   `json:"build_id"`
	Hosts    []string `json:"hosts"`
	Uploaded bool     `json:"uploaded"`
}

type appEnvVar struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Secret bool   `json:"secret"`
}

type appEnvResponse struct {
	Variables []appEnvVar `json:"variables"`
}

type appSetEnvResponse struct {
	Key    string `json:"key"`
	Secret bool   `json:"secret"`
}

type appDNSRecord struct {
	RecordType string `json:"record_type"`
	Name       string `json:"name"`
	Value      string `json:"value"`
}

type appDomainView struct {
	Domain string `json:"domain"`
	AppID  string `json:"app_id"`
	Status string `json:"status"`
}

type appDomainsResponse struct {
	Domains []appDomainView `json:"domains"`
}

type appDomainResponse struct {
	Domain appDomainView `json:"domain"`
}

type appAddDomainResponse struct {
	Domain     string         `json:"domain"`
	AppID      string         `json:"app_id"`
	Status     string         `json:"status"`
	DNSMode    string         `json:"dns_mode"`
	DNSRecords []appDNSRecord `json:"dns_records"`
}

type appDeleteDomainResponse struct {
	Domain string `json:"domain"`
	AppID  string `json:"app_id"`
	Status string `json:"status"`
}

type appDeleteResponse struct {
	AppID                    string   `json:"app_id"`
	Complete                 bool     `json:"complete"`
	PointerDeleted           bool     `json:"pointer_deleted"`
	CloudFrontTenantsDeleted int      `json:"cloudfront_tenants_deleted"`
	SecretsDeleted           int      `json:"secrets_deleted"`
	ArtifactsDeleted         int      `json:"artifacts_deleted"`
	ArtifactsPresent         int      `json:"artifacts_present"`
	EnvKeysDeleted           int      `json:"env_keys_deleted"`
	DomainsDeleted           int      `json:"domains_deleted"`
	CatalogDeleted           bool     `json:"catalog_deleted"`
	Errors                   []string `json:"errors"`
}

type appLogRecord struct {
	Timestamp  string `json:"timestamp"`
	Level      string `json:"level"`
	Message    string `json:"message"`
	AppID      string `json:"app_id"`
	BuildID    string `json:"build_id,omitempty"`
	Route      string `json:"route,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
	TraceID    string `json:"trace_id,omitempty"`
	ErrorClass string `json:"error_class,omitempty"`
	ErrorStack string `json:"error_stack,omitempty"`
}

type appLogsResponse struct {
	Logs          []appLogRecord `json:"logs"`
	NextPageToken string         `json:"next_page_token,omitempty"`
}

type githubRelease struct {
	TagName string               `json:"tag_name"`
	Assets  []githubReleaseAsset `json:"assets"`
}

type githubReleaseAsset struct {
	URL                string `json:"url"`
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type updateOptions struct {
	Repo         string
	Version      string
	TargetPath   string
	GOOS         string
	GOARCH       string
	GitHubAPIURL string
	GitHubToken  string
	HTTPClient   *http.Client
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
		return databases(args[1:], stdout, stderr)
	case "domains":
		return domainsCommand(args[1:], stdout)
	case "storage":
		return storageCommand(args[1:], stdout)
	case "apps":
		return apps(args[1:], stdout)
	case "deploy":
		return deploy(args[1:], stdout, stderr)
	case "update":
		return updateCommand(args[1:], stdout)
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
  comwit databases create --project <id> --name <name> [--from-file <path> | --from-dump <path> [--sqlite-out <path>]] [--token-out <path> --skip-local-checks --idempotency-key <key> --no-wait]
  comwit databases import-dump --project <id> --name <name> --from-dump dump.sql [--keep-failed-db]
  comwit databases list --project <id>
  comwit databases execute --project <id> --database <id> (--command <sql>|--file <path>) [--json]
  comwit databases delete --project <id> --database <id>
  comwit databases token rotate --project <id> --database <id>
  comwit databases restore-points list --project <id> --database <id>
  comwit databases restore --project <id> --database <id> (--at <ts>|--generation <id>|--alias <name>) [--name <n>] [--token-out <path>] [--wait]
  comwit databases restore status --project <id> --database <id> --operation <op-id> [--wait]
  comwit databases operation status --project <id> --database <id> --operation <op-id> [--wait]
  comwit databases aliases list --project <id> --database <id>
  comwit databases aliases set --project <id> --database <id> --alias <name> (--at <ts>|--generation <id>)
  comwit databases aliases delete --project <id> --database <id> --alias <name>
  comwit domains list --project <id>
  comwit domains add --project <id> --domain example.com
  comwit domains check --project <id> --domain example.com
  comwit domains delete --project <id> --domain example.com
  comwit domains records list --project <id> --domain example.com
  comwit domains records create --project <id> --domain example.com --name www --type CNAME --value target.example.net --ttl 300
  comwit domains records update --project <id> --domain example.com --record <id> --value target2.example.net --ttl 300
  comwit domains records delete --project <id> --domain example.com --record <id>
  comwit storage create --project <id> --name <bucket-name> [--public] [--location-hint apac]
  comwit storage list --project <id>
  comwit storage get --project <id> --storage <id>
  comwit storage public <enable|disable> --project <id> --storage <id>
  comwit storage delete --project <id> --storage <id>
  comwit apps list --project <id>
  comwit apps create --project <id> --name <name>
  comwit apps builds --project <id> --app <id>
  comwit apps logs --project <id> --app <id> [--tail 100] [--since 30m] [--level error] [--json] [--follow]
  comwit apps env list --project <id> --app <id>
  comwit apps env set --project <id> --app <id> KEY VALUE
  comwit apps env unset --project <id> --app <id> KEY
  comwit apps domains list --project <id> --app <id>
  comwit apps domains add --project <id> --app <id> --domain app.example.com [--managed]
  comwit apps domains finalize --project <id> --app <id> --domain app.example.com
  comwit apps domains remove --project <id> --app <id> --domain app.example.com
  comwit apps delete --project <id> --app <id> [--wait]
  comwit deploy --project <id> --app <id> --package <dist/brrrd tar.zst or dir>
  comwit update [--version vX.Y.Z] [--repo owner/repo]

Environment:
  COMWIT_CONFIG   Override config file path.
  COMWIT_PROJECT  Default project id for commands that accept --project.
  COMWIT_API_URL  Override API URL for local testing.`)
}

func updateCommand(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	repo := fs.String("repo", defaultGitHubRepo, "GitHub owner/repo")
	releaseVersion := fs.String("version", "latest", "release tag; default latest")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: comwit update [--version vX.Y.Z] [--repo owner/repo]")
	}
	if runtime.GOOS == "windows" {
		return errors.New("comwit update is not supported on Windows yet; rerun the installer")
	}
	targetPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve current executable: %w", err)
	}
	return performUpdate(updateOptions{
		Repo:         *repo,
		Version:      *releaseVersion,
		TargetPath:   targetPath,
		GOOS:         runtime.GOOS,
		GOARCH:       runtime.GOARCH,
		GitHubAPIURL: githubAPIURL(),
		GitHubToken:  githubToken(),
		HTTPClient:   &http.Client{Timeout: 60 * time.Second},
	}, stdout)
}

func performUpdate(opts updateOptions, stdout io.Writer) error {
	opts.Repo = strings.TrimSpace(opts.Repo)
	if opts.Repo == "" {
		opts.Repo = defaultGitHubRepo
	}
	opts.Version = strings.TrimSpace(opts.Version)
	if opts.Version == "" {
		opts.Version = "latest"
	}
	opts.TargetPath = filepath.Clean(strings.TrimSpace(opts.TargetPath))
	if opts.TargetPath == "." || opts.TargetPath == "" {
		return errors.New("target executable path is required")
	}
	if opts.GOOS == "" {
		opts.GOOS = runtime.GOOS
	}
	if opts.GOARCH == "" {
		opts.GOARCH = runtime.GOARCH
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	opts.GitHubAPIURL = strings.TrimRight(strings.TrimSpace(opts.GitHubAPIURL), "/")
	if opts.GitHubAPIURL == "" {
		opts.GitHubAPIURL = defaultGitHubAPIURL
	}
	if opts.GOOS == "windows" {
		return errors.New("comwit update is not supported on Windows yet; rerun the installer")
	}

	assetName, err := updateAssetName(opts.GOOS, opts.GOARCH)
	if err != nil {
		return err
	}
	release, err := fetchGitHubRelease(opts.HTTPClient, opts.GitHubAPIURL, opts.GitHubToken, opts.Repo, opts.Version)
	if err != nil {
		return err
	}
	asset, ok := findReleaseAsset(release.Assets, assetName)
	if !ok {
		return fmt.Errorf("release %s does not include asset %s", release.TagName, assetName)
	}
	assetURL := assetDownloadURL(asset, opts.GitHubToken)
	if strings.TrimSpace(assetURL) == "" {
		return fmt.Errorf("release asset %s does not include a download URL", assetName)
	}
	if err := installUpdateAsset(opts.HTTPClient, assetURL, opts.GitHubToken, opts.TargetPath); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "updated comwit to %s\n", release.TagName)
	return nil
}

func updateAssetName(goos, goarch string) (string, error) {
	switch goos {
	case "darwin", "linux":
	default:
		return "", fmt.Errorf("unsupported update OS %q; rerun the installer", goos)
	}
	switch goarch {
	case "amd64", "arm64":
	default:
		return "", fmt.Errorf("unsupported update architecture %q; rerun the installer", goarch)
	}
	return fmt.Sprintf("comwit_%s_%s.tar.gz", goos, goarch), nil
}

func fetchGitHubRelease(httpClient *http.Client, apiURL, token, repo, releaseVersion string) (githubRelease, error) {
	path, err := githubReleasePath(repo, releaseVersion)
	if err != nil {
		return githubRelease{}, err
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(apiURL, "/")+path, nil)
	if err != nil {
		return githubRelease{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "comwit-cli/"+version)
	setGitHubAuth(req, token)

	resp, err := httpClient.Do(req)
	if err != nil {
		return githubRelease{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return githubRelease{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return githubRelease{}, fmt.Errorf("GitHub release lookup HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var release githubRelease
	if err := json.Unmarshal(data, &release); err != nil {
		return githubRelease{}, fmt.Errorf("decode GitHub release response: %w", err)
	}
	if strings.TrimSpace(release.TagName) == "" {
		release.TagName = strings.TrimSpace(releaseVersion)
	}
	return release, nil
}

func githubReleasePath(repo, releaseVersion string) (string, error) {
	owner, name, err := splitGitHubRepo(repo)
	if err != nil {
		return "", err
	}
	base := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/releases"
	releaseVersion = strings.TrimSpace(releaseVersion)
	if releaseVersion == "" || releaseVersion == "latest" {
		return base + "/latest", nil
	}
	return base + "/tags/" + url.PathEscape(releaseVersion), nil
}

func splitGitHubRepo(repo string) (string, string, error) {
	parts := strings.Split(strings.TrimSpace(repo), "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("invalid GitHub repo %q; use owner/repo", repo)
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}

func findReleaseAsset(assets []githubReleaseAsset, name string) (githubReleaseAsset, bool) {
	for _, asset := range assets {
		if asset.Name == name {
			return asset, true
		}
	}
	return githubReleaseAsset{}, false
}

func assetDownloadURL(asset githubReleaseAsset, token string) string {
	if strings.TrimSpace(token) != "" && strings.TrimSpace(asset.URL) != "" {
		return strings.TrimSpace(asset.URL)
	}
	return strings.TrimSpace(asset.BrowserDownloadURL)
}

func installUpdateAsset(httpClient *http.Client, assetURL, token, targetPath string) error {
	req, err := http.NewRequest(http.MethodGet, assetURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "comwit-cli/"+version)
	setGitHubAuth(req, token)
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("download release asset HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return replaceBinaryFromTarGz(resp.Body, targetPath)
}

func setGitHubAuth(req *http.Request, token string) {
	if token = strings.TrimSpace(token); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

func replaceBinaryFromTarGz(r io.Reader, targetPath string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("open release asset gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	dir := filepath.Dir(targetPath)
	tmp, err := os.CreateTemp(dir, ".comwit-update-*")
	if err != nil {
		return fmt.Errorf("create temporary executable: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	defer tmp.Close()

	found := false
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read release asset tar: %w", err)
		}
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != "comwit" {
			continue
		}
		if _, err := io.Copy(tmp, tr); err != nil {
			return fmt.Errorf("write updated executable: %w", err)
		}
		found = true
		break
	}
	if !found {
		return errors.New("release asset does not contain a comwit executable")
	}
	if err := tmp.Chmod(0o755); err != nil {
		return fmt.Errorf("mark updated executable: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync updated executable: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close updated executable: %w", err)
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		return fmt.Errorf("replace current executable: %w", err)
	}
	cleanup = false
	return nil
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
	c := &client{apiURL: apiURL(), httpClient: &http.Client{Timeout: 30 * time.Second}}

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

func databases(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: comwit databases <create|import-dump|list|execute|delete|token|restore-points|restore|operation|aliases>")
	}
	switch args[0] {
	case "create":
		return databaseCreateCommand(args[1:], stdout, stderr)
	case "import-dump", "import":
		return databaseImportDumpCommand(args[1:], stdout)
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
	case "execute", "query":
		return databaseExecuteCommand(args[1:], stdout)
	case "delete":
		fs := flag.NewFlagSet("databases delete", flag.ContinueOnError)
		project := fs.String("project", "", "project id")
		database := fs.String("database", "", "database id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		cfg, projectID, databaseID, err := databaseCommandContext(*project, *database)
		if err != nil {
			return err
		}
		var body databaseOperationResponse
		if err := newClient(cfg).deleteJSON(projectDatabasePath(projectID, databaseID), &body); err != nil {
			return err
		}
		printDatabaseOperation(stdout, body)
		return nil
	case "token":
		return databaseTokenCommand(args[1:], stdout)
	case "restore-points":
		return databaseRestorePointsCommand(args[1:], stdout)
	case "restore":
		if len(args) >= 2 && args[1] == "status" {
			return databaseRestoreStatusCommand(args[2:], stdout)
		}
		return databaseRestoreCommand(args[1:], stdout)
	case "operation":
		return databaseOperationCommand(args[1:], stdout)
	case "aliases":
		return databaseAliasesCommand(args[1:], stdout)
	default:
		return fmt.Errorf("unknown databases command %q", args[0])
	}
}

func databaseTokenCommand(args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] != "rotate" {
		return errors.New("usage: comwit databases token rotate --project <id> --database <id>")
	}
	fs := flag.NewFlagSet("databases token rotate", flag.ContinueOnError)
	project := fs.String("project", "", "project id")
	database := fs.String("database", "", "database id")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	cfg, projectID, databaseID, err := databaseCommandContext(*project, *database)
	if err != nil {
		return err
	}
	var body databaseTokenResponse
	if err := newClient(cfg).postJSON(projectDatabasePath(projectID, databaseID)+"/token/rotate", map[string]string{}, &body); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "database\t%s\n", body.DatabaseID)
	fmt.Fprintf(stdout, "url\t%s\n", body.DatabaseURL)
	fmt.Fprintf(stdout, "status\t%s\n", body.Status)
	fmt.Fprintf(stdout, "token\t%s\n", body.DatabaseToken)
	return nil
}

func domainsCommand(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: comwit domains <list|add|check|delete|records>")
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("domains list", flag.ContinueOnError)
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
		var body domainsResponse
		if err := newClient(cfg).getJSON(projectDomainsPath(projectID), &body); err != nil {
			return err
		}
		printDomains(stdout, body.Domains)
		return nil
	case "add":
		fs := flag.NewFlagSet("domains add", flag.ContinueOnError)
		project := fs.String("project", "", "project id")
		domain := fs.String("domain", "", "domain name")
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
		domainName := strings.TrimSpace(*domain)
		if domainName == "" {
			return errors.New("--domain is required")
		}
		var body domainResponse
		payload := map[string]string{"domain": domainName, "dns_mode": "route53_delegated"}
		if err := newClient(cfg).postJSON(projectDomainsPath(projectID), payload, &body); err != nil {
			return err
		}
		printDomainDetail(stdout, body.Domain)
		return nil
	case "check":
		fs := flag.NewFlagSet("domains check", flag.ContinueOnError)
		project := fs.String("project", "", "project id")
		domain := fs.String("domain", "", "domain name")
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
		domainName := strings.TrimSpace(*domain)
		if domainName == "" {
			return errors.New("--domain is required")
		}
		var body delegationCheckResponse
		if err := newClient(cfg).postJSON(projectDomainPath(projectID, domainName)+"/delegation-check", map[string]string{}, &body); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "matched\t%t\n", body.Matched)
		printDomainDetail(stdout, body.Domain)
		return nil
	case "delete":
		fs := flag.NewFlagSet("domains delete", flag.ContinueOnError)
		project := fs.String("project", "", "project id")
		domain := fs.String("domain", "", "domain name")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		cfg, projectID, domainName, err := domainCommandContext(*project, *domain)
		if err != nil {
			return err
		}
		var body domainResponse
		if err := newClient(cfg).deleteJSON(projectDomainPath(projectID, domainName), &body); err != nil {
			return err
		}
		printDomainDetail(stdout, body.Domain)
		return nil
	case "records":
		return domainRecordsCommand(args[1:], stdout)
	default:
		return fmt.Errorf("unknown domains command %q", args[0])
	}
}

func domainRecordsCommand(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: comwit domains records <list|create|update|delete>")
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("domains records list", flag.ContinueOnError)
		project := fs.String("project", "", "project id")
		domain := fs.String("domain", "", "domain name")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		cfg, projectID, domainName, err := domainCommandContext(*project, *domain)
		if err != nil {
			return err
		}
		var body dnsRecordsResponse
		if err := newClient(cfg).getJSON(projectDomainRecordsPath(projectID, domainName), &body); err != nil {
			return err
		}
		printDNSRecords(stdout, body.Records)
		return nil
	case "create":
		fs := flag.NewFlagSet("domains records create", flag.ContinueOnError)
		project := fs.String("project", "", "project id")
		domain := fs.String("domain", "", "domain name")
		name := fs.String("name", "", "record name")
		recordType := fs.String("type", "", "record type")
		ttl := fs.Int64("ttl", 300, "record ttl")
		values := stringSliceFlag{}
		fs.Var(&values, "value", "record value; may be repeated")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		cfg, projectID, domainName, err := domainCommandContext(*project, *domain)
		if err != nil {
			return err
		}
		payload, err := dnsRecordPayload(*name, *recordType, *ttl, []string(values))
		if err != nil {
			return err
		}
		var body dnsRecordResponse
		if err := newClient(cfg).postJSON(projectDomainRecordsPath(projectID, domainName), payload, &body); err != nil {
			return err
		}
		printDNSRecordDetail(stdout, body.Record)
		return nil
	case "update":
		fs := flag.NewFlagSet("domains records update", flag.ContinueOnError)
		project := fs.String("project", "", "project id")
		domain := fs.String("domain", "", "domain name")
		recordID := fs.String("record", "", "record id")
		name := fs.String("name", "", "record name")
		recordType := fs.String("type", "", "record type")
		ttl := fs.Int64("ttl", 300, "record ttl")
		values := stringSliceFlag{}
		fs.Var(&values, "value", "record value; may be repeated")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		cfg, projectID, domainName, err := domainCommandContext(*project, *domain)
		if err != nil {
			return err
		}
		id := strings.TrimSpace(*recordID)
		if id == "" {
			return errors.New("--record is required")
		}
		ttlSet := false
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "ttl" {
				ttlSet = true
			}
		})
		client := newClient(cfg)
		var listBody dnsRecordsResponse
		if err := client.getJSON(projectDomainRecordsPath(projectID, domainName), &listBody); err != nil {
			return err
		}
		existing, ok := findDNSRecord(listBody.Records, id)
		if !ok {
			return fmt.Errorf("record %s not found", id)
		}
		payload, err := dnsRecordUpdatePayload(existing, *name, *recordType, *ttl, ttlSet, []string(values))
		if err != nil {
			return err
		}
		var body dnsRecordResponse
		if err := client.putJSON(projectDomainRecordPath(projectID, domainName, id), payload, &body); err != nil {
			return err
		}
		printDNSRecordDetail(stdout, body.Record)
		return nil
	case "delete":
		fs := flag.NewFlagSet("domains records delete", flag.ContinueOnError)
		project := fs.String("project", "", "project id")
		domain := fs.String("domain", "", "domain name")
		recordID := fs.String("record", "", "record id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		cfg, projectID, domainName, err := domainCommandContext(*project, *domain)
		if err != nil {
			return err
		}
		id := strings.TrimSpace(*recordID)
		if id == "" {
			return errors.New("--record is required")
		}
		var body dnsRecordResponse
		if err := newClient(cfg).deleteJSON(projectDomainRecordPath(projectID, domainName, id), &body); err != nil {
			return err
		}
		printDNSRecordDetail(stdout, body.Record)
		return nil
	default:
		return fmt.Errorf("unknown domains records command %q", args[0])
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
		return errors.New("usage: comwit apps <list|create|builds|logs|env|domains|delete>")
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
		if err := newClient(cfg).getJSON(projectAppsPath(projectID), &body); err != nil {
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
		if err := newClient(cfg).postJSON(projectAppsPath(projectID), payload, &body); err != nil {
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
	case "logs":
		return appLogsCommand(args[1:], stdout)
	case "env":
		return appEnvCommand(args[1:], stdout)
	case "domains":
		return appDomainsCommand(args[1:], stdout)
	case "delete":
		return appDeleteCommand(args[1:], stdout)
	default:
		return fmt.Errorf("unknown apps command %q", args[0])
	}
}

func appLogsCommand(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("apps logs", flag.ContinueOnError)
	project := fs.String("project", "", "project id")
	app := fs.String("app", "", "app id")
	tail := fs.Int("tail", 100, "number of log rows")
	since := fs.String("since", "", "RFC3339 timestamp or duration, e.g. 30m")
	level := fs.String("level", "", "debug, info, warn, or error")
	build := fs.String("build", "", "build id")
	route := fs.String("route", "", "route or HTTP path")
	requestID := fs.String("request-id", "", "request id")
	jsonOut := fs.Bool("json", false, "print JSON")
	follow := fs.Bool("follow", false, "keep polling for new logs")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, projectID, appID, err := appCommandContext(*project, *app)
	if err != nil {
		return err
	}
	query := url.Values{}
	if *tail > 0 {
		query.Set("tail", strconv.Itoa(*tail))
	}
	if strings.TrimSpace(*since) != "" {
		query.Set("since", strings.TrimSpace(*since))
	}
	if strings.TrimSpace(*level) != "" {
		query.Set("level", strings.TrimSpace(*level))
	}
	if strings.TrimSpace(*build) != "" {
		query.Set("build_id", strings.TrimSpace(*build))
	}
	if strings.TrimSpace(*route) != "" {
		query.Set("route", strings.TrimSpace(*route))
	}
	if strings.TrimSpace(*requestID) != "" {
		query.Set("request_id", strings.TrimSpace(*requestID))
	}
	path := projectAppPath(projectID, appID) + "/logs"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	client := newClient(cfg)
	seen := map[string]struct{}{}
	for {
		var body appLogsResponse
		if err := client.getJSON(path, &body); err != nil {
			return err
		}
		logs := body.Logs
		if *follow {
			for i := len(logs) - 1; i >= 0; i-- {
				record := logs[i]
				key := logIdentity(record)
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				if *jsonOut {
					if err := json.NewEncoder(stdout).Encode(record); err != nil {
						return err
					}
				} else {
					printLogRecord(stdout, record)
				}
			}
			time.Sleep(2 * time.Second)
			continue
		}
		if *jsonOut {
			enc := json.NewEncoder(stdout)
			for _, record := range logs {
				if err := enc.Encode(record); err != nil {
					return err
				}
			}
			return nil
		}
		printLogs(stdout, logs)
		return nil
	}
}

func appEnvCommand(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: comwit apps env <list|set|unset>")
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("apps env list", flag.ContinueOnError)
		project := fs.String("project", "", "project id")
		app := fs.String("app", "", "app id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		cfg, projectID, appID, err := appCommandContext(*project, *app)
		if err != nil {
			return err
		}
		var body appEnvResponse
		if err := newClient(cfg).getJSON(projectAppEnvPath(projectID, appID), &body); err != nil {
			return err
		}
		printAppEnv(stdout, body.Variables)
		return nil
	case "set":
		fs := flag.NewFlagSet("apps env set", flag.ContinueOnError)
		project := fs.String("project", "", "project id")
		app := fs.String("app", "", "app id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		cfg, projectID, appID, err := appCommandContext(*project, *app)
		if err != nil {
			return err
		}
		key, value, err := parseEnvSetArgs(fs.Args())
		if err != nil {
			return err
		}
		var body appSetEnvResponse
		if err := newClient(cfg).putJSON(projectAppEnvPath(projectID, appID)+"/"+url.PathEscape(key), map[string]any{
			"value":  value,
			"secret": false,
		}, &body); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "key\t%s\n", body.Key)
		fmt.Fprintf(stdout, "secret\t%t\n", body.Secret)
		return nil
	case "unset", "delete", "remove":
		fs := flag.NewFlagSet("apps env unset", flag.ContinueOnError)
		project := fs.String("project", "", "project id")
		app := fs.String("app", "", "app id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		cfg, projectID, appID, err := appCommandContext(*project, *app)
		if err != nil {
			return err
		}
		if len(fs.Args()) != 1 {
			return errors.New("usage: comwit apps env unset --project <id> --app <id> KEY")
		}
		key := strings.TrimSpace(fs.Args()[0])
		if err := validateEnvKey(key); err != nil {
			return err
		}
		if err := newClient(cfg).deleteJSON(projectAppEnvPath(projectID, appID)+"/"+url.PathEscape(key), nil); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "deleted\t%s\n", key)
		return nil
	default:
		return fmt.Errorf("unknown apps env command %q", args[0])
	}
}

func appDomainsCommand(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: comwit apps domains <list|add|finalize|remove>")
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("apps domains list", flag.ContinueOnError)
		project := fs.String("project", "", "project id")
		app := fs.String("app", "", "app id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		cfg, projectID, appID, err := appCommandContext(*project, *app)
		if err != nil {
			return err
		}
		var body appDomainsResponse
		if err := newClient(cfg).getJSON(projectAppDomainsPath(projectID, appID), &body); err != nil {
			return err
		}
		printAppDomains(stdout, body.Domains)
		return nil
	case "add":
		fs := flag.NewFlagSet("apps domains add", flag.ContinueOnError)
		project := fs.String("project", "", "project id")
		app := fs.String("app", "", "app id")
		domain := fs.String("domain", "", "domain name")
		managed := fs.Bool("managed", false, "publish DNS in a matching Comwit-managed project domain")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		cfg, projectID, appID, err := appCommandContext(*project, *app)
		if err != nil {
			return err
		}
		domainName := strings.TrimSpace(*domain)
		if domainName == "" {
			return errors.New("--domain is required")
		}
		dnsMode := "external_records"
		if *managed {
			dnsMode = "managed_project_domain"
		}
		var body appAddDomainResponse
		if err := newClient(cfg).postJSON(projectAppDomainsPath(projectID, appID), map[string]string{
			"domain":   domainName,
			"dns_mode": dnsMode,
		}, &body); err != nil {
			return err
		}
		printAppAddDomain(stdout, body)
		return nil
	case "finalize":
		fs := flag.NewFlagSet("apps domains finalize", flag.ContinueOnError)
		project := fs.String("project", "", "project id")
		app := fs.String("app", "", "app id")
		domain := fs.String("domain", "", "domain name")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		cfg, projectID, appID, err := appCommandContext(*project, *app)
		if err != nil {
			return err
		}
		domainName := strings.TrimSpace(*domain)
		if domainName == "" {
			return errors.New("--domain is required")
		}
		var body appDomainResponse
		if err := newClient(cfg).postJSON(projectAppDomainPath(projectID, appID, domainName)+"/finalize", map[string]string{}, &body); err != nil {
			return err
		}
		printAppDomainDetail(stdout, body.Domain)
		return nil
	case "remove", "delete":
		fs := flag.NewFlagSet("apps domains remove", flag.ContinueOnError)
		project := fs.String("project", "", "project id")
		app := fs.String("app", "", "app id")
		domain := fs.String("domain", "", "domain name")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		cfg, projectID, appID, err := appCommandContext(*project, *app)
		if err != nil {
			return err
		}
		domainName := strings.TrimSpace(*domain)
		if domainName == "" {
			return errors.New("--domain is required")
		}
		var body appDeleteDomainResponse
		if err := newClient(cfg).deleteJSON(projectAppDomainPath(projectID, appID, domainName), &body); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "domain\t%s\n", body.Domain)
		fmt.Fprintf(stdout, "app\t%s\n", body.AppID)
		fmt.Fprintf(stdout, "status\t%s\n", body.Status)
		return nil
	default:
		return fmt.Errorf("unknown apps domains command %q", args[0])
	}
}

func appDeleteCommand(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("apps delete", flag.ContinueOnError)
	project := fs.String("project", "", "project id")
	app := fs.String("app", "", "app id")
	wait := fs.Bool("wait", false, "retry until the delete response reports complete")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, projectID, appID, err := appCommandContext(*project, *app)
	if err != nil {
		return err
	}
	client := newClient(cfg)
	for attempt := 0; ; attempt++ {
		var body appDeleteResponse
		if err := client.deleteJSON(projectAppPath(projectID, appID), &body); err != nil {
			return err
		}
		if !*wait || body.Complete || attempt >= 29 {
			printAppDelete(stdout, body)
			if *wait && !body.Complete {
				return errors.New("app delete did not complete before timeout")
			}
			return nil
		}
		time.Sleep(2 * time.Second)
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
		apiURL:     apiURL(),
		token:      cfg.Token,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

func apiURL() string {
	if value := strings.TrimSpace(os.Getenv("COMWIT_API_URL")); value != "" {
		return strings.TrimRight(value, "/")
	}
	return defaultAPIURL
}

func githubAPIURL() string {
	if value := strings.TrimSpace(os.Getenv("COMWIT_UPDATE_GITHUB_API_URL")); value != "" {
		return strings.TrimRight(value, "/")
	}
	return defaultGitHubAPIURL
}

func githubToken() string {
	for _, name := range []string{"COMWIT_UPDATE_GITHUB_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	output, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
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

func (c *client) putJSON(path string, payload any, out any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return c.do(http.MethodPut, path, data, "application/json", out)
}

func (c *client) deleteJSON(path string, out any) error {
	return c.do(http.MethodDelete, path, nil, "application/json", out)
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
		return newAPIError(resp.StatusCode, data)
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
	code   string
	detail string
}

func newAPIError(status int, body []byte) apiError {
	err := apiError{status: status, body: strings.TrimSpace(string(body))}
	var envelope struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	if json.Unmarshal(body, &envelope) == nil {
		err.code = strings.TrimSpace(envelope.Code)
		err.detail = envelope.Detail
	}
	return err
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

func domainCommandContext(project, domain string) (configFile, string, string, error) {
	cfg, err := loadConfig()
	if err != nil {
		return configFile{}, "", "", err
	}
	projectID := selectProject(project, cfg)
	if projectID == "" {
		return configFile{}, "", "", errors.New("--project is required")
	}
	domainName := strings.TrimSpace(domain)
	if domainName == "" {
		return configFile{}, "", "", errors.New("--domain is required")
	}
	return cfg, projectID, domainName, nil
}

func databaseCommandContext(project, database string) (configFile, string, string, error) {
	cfg, err := loadConfig()
	if err != nil {
		return configFile{}, "", "", err
	}
	projectID := selectProject(project, cfg)
	if projectID == "" {
		return configFile{}, "", "", errors.New("--project is required")
	}
	databaseID := strings.TrimSpace(database)
	if databaseID == "" {
		return configFile{}, "", "", errors.New("--database is required")
	}
	return cfg, projectID, databaseID, nil
}

func appCommandContext(project, app string) (configFile, string, string, error) {
	cfg, err := loadConfig()
	if err != nil {
		return configFile{}, "", "", err
	}
	projectID := selectProject(project, cfg)
	if projectID == "" {
		return configFile{}, "", "", errors.New("--project is required")
	}
	appID := strings.TrimSpace(app)
	if appID == "" {
		return configFile{}, "", "", errors.New("--app is required")
	}
	return cfg, projectID, appID, nil
}

func projectDomainsPath(projectID string) string {
	return "/v1/projects/" + url.PathEscape(projectID) + "/domains"
}

func projectDomainPath(projectID, domain string) string {
	return projectDomainsPath(projectID) + "/" + url.PathEscape(domain)
}

func projectDomainRecordsPath(projectID, domain string) string {
	return projectDomainPath(projectID, domain) + "/records"
}

func projectDomainRecordPath(projectID, domain, recordID string) string {
	return projectDomainRecordsPath(projectID, domain) + "/" + url.PathEscape(recordID)
}

func projectDatabasesPath(projectID string) string {
	return "/v1/projects/" + url.PathEscape(projectID) + "/databases"
}

func projectDatabasePath(projectID, databaseID string) string {
	return projectDatabasesPath(projectID) + "/" + url.PathEscape(databaseID)
}

func projectAppsPath(projectID string) string {
	return "/v1/projects/" + url.PathEscape(projectID) + "/apps"
}

func projectAppPath(projectID, appID string) string {
	return projectAppsPath(projectID) + "/" + url.PathEscape(appID)
}

func projectAppEnvPath(projectID, appID string) string {
	return projectAppPath(projectID, appID) + "/environment"
}

func projectAppDomainsPath(projectID, appID string) string {
	return projectAppPath(projectID, appID) + "/domains"
}

func projectAppDomainPath(projectID, appID, domain string) string {
	return projectAppDomainsPath(projectID, appID) + "/" + url.PathEscape(domain)
}

func parseEnvSetArgs(args []string) (string, string, error) {
	switch len(args) {
	case 1:
		key, value, ok := strings.Cut(args[0], "=")
		if !ok {
			return "", "", errors.New("usage: comwit apps env set --project <id> --app <id> KEY VALUE")
		}
		key = strings.TrimSpace(key)
		if err := validateEnvKey(key); err != nil {
			return "", "", err
		}
		return key, value, nil
	case 2:
		key := strings.TrimSpace(args[0])
		if err := validateEnvKey(key); err != nil {
			return "", "", err
		}
		return key, args[1], nil
	default:
		return "", "", errors.New("usage: comwit apps env set --project <id> --app <id> KEY VALUE")
	}
}

func validateEnvKey(key string) error {
	if key == "" {
		return errors.New("environment key is required")
	}
	for i, r := range key {
		switch {
		case r == '_':
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return fmt.Errorf("invalid environment key %q", key)
		}
	}
	return nil
}

type stringSliceFlag []string

func (f *stringSliceFlag) Set(value string) error {
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			*f = append(*f, part)
		}
	}
	return nil
}

func (f *stringSliceFlag) String() string {
	return strings.Join(*f, ",")
}

func dnsRecordPayload(name, recordType string, ttl int64, values []string) (map[string]any, error) {
	recordName := strings.TrimSpace(name)
	if recordName == "" {
		return nil, errors.New("--name is required")
	}
	typ := strings.TrimSpace(recordType)
	if typ == "" {
		return nil, errors.New("--type is required")
	}
	if len(values) == 0 {
		return nil, errors.New("--value is required")
	}
	return map[string]any{
		"name":   recordName,
		"type":   typ,
		"ttl":    ttl,
		"values": []string(values),
	}, nil
}

func dnsRecordUpdatePayload(existing dnsRecordView, name, recordType string, ttl int64, ttlSet bool, values []string) (map[string]any, error) {
	recordName := strings.TrimSpace(name)
	if recordName == "" {
		recordName = existing.Name
	}
	typ := strings.TrimSpace(recordType)
	if typ == "" {
		typ = existing.Type
	}
	if !ttlSet {
		ttl = existing.TTL
	}
	if len(values) == 0 {
		values = existing.Values
	}
	return dnsRecordPayload(recordName, typ, ttl, values)
}

func findDNSRecord(records []dnsRecordView, id string) (dnsRecordView, bool) {
	for _, record := range records {
		if record.ID == id {
			return record, true
		}
	}
	return dnsRecordView{}, false
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

func printDatabaseOperation(w io.Writer, body databaseOperationResponse) {
	fmt.Fprintf(w, "ok\t%t\n", body.OK)
	fmt.Fprintf(w, "database\t%s\n", body.Database.DatabaseID)
	fmt.Fprintf(w, "status\t%s\n", body.Database.Status)
	if body.Database.DatabaseURL != "" {
		fmt.Fprintf(w, "url\t%s\n", body.Database.DatabaseURL)
	}
}

func printDomains(w io.Writer, domains []domainView) {
	fmt.Fprintln(w, "DOMAIN ID\tDOMAIN\tSTATUS\tDNS MODE\tHOSTED ZONE")
	for _, domain := range domains {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", domain.ID, domain.Domain, domain.Status, domain.DNSMode, domain.HostedZoneID)
	}
}

func printDomainDetail(w io.Writer, domain domainView) {
	fmt.Fprintf(w, "domain\t%s\n", domain.Domain)
	fmt.Fprintf(w, "status\t%s\n", domain.Status)
	if domain.HostedZoneID != "" {
		fmt.Fprintf(w, "hosted_zone_id\t%s\n", domain.HostedZoneID)
	}
	if len(domain.Nameservers) > 0 {
		fmt.Fprintln(w, "nameservers")
		for _, ns := range domain.Nameservers {
			fmt.Fprintf(w, "%s\n", ns)
		}
	}
	fmt.Fprintf(w, "delegation_matched\t%t\n", domain.Delegation.Matched)
	if len(domain.Delegation.ObservedNameservers) > 0 {
		fmt.Fprintln(w, "observed_nameservers")
		for _, ns := range domain.Delegation.ObservedNameservers {
			fmt.Fprintf(w, "%s\n", ns)
		}
	}
}

func printDNSRecords(w io.Writer, records []dnsRecordView) {
	fmt.Fprintln(w, "RECORD ID\tNAME\tTYPE\tOWNER\tOWNER RESOURCE\tSTATUS\tTTL\tVALUES")
	for _, record := range records {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n", record.ID, record.Name, record.Type, record.Owner, dnsRecordOwnerResource(record), record.Status, record.TTL, strings.Join(record.Values, ","))
	}
}

func printDNSRecordDetail(w io.Writer, record dnsRecordView) {
	fmt.Fprintf(w, "record\t%s\n", record.ID)
	fmt.Fprintf(w, "name\t%s\n", record.Name)
	fmt.Fprintf(w, "type\t%s\n", record.Type)
	fmt.Fprintf(w, "owner\t%s\n", record.Owner)
	if ownerResource := dnsRecordOwnerResource(record); ownerResource != "" {
		fmt.Fprintf(w, "owner_resource\t%s\n", ownerResource)
	}
	fmt.Fprintf(w, "status\t%s\n", record.Status)
	fmt.Fprintf(w, "ttl\t%d\n", record.TTL)
	fmt.Fprintf(w, "values\t%s\n", strings.Join(record.Values, ","))
	if record.Route53ChangeID != "" {
		fmt.Fprintf(w, "route53_change_id\t%s\n", record.Route53ChangeID)
	}
	if record.LastError != "" {
		fmt.Fprintf(w, "last_error\t%s\n", record.LastError)
	}
}

func printAppEnv(w io.Writer, variables []appEnvVar) {
	fmt.Fprintln(w, "KEY\tSECRET\tVALUE")
	for _, variable := range variables {
		value := variable.Value
		if variable.Secret {
			value = "[secret]"
		}
		fmt.Fprintf(w, "%s\t%t\t%s\n", variable.Key, variable.Secret, value)
	}
}

func printAppDomains(w io.Writer, domains []appDomainView) {
	fmt.Fprintln(w, "DOMAIN\tAPP ID\tSTATUS")
	for _, domain := range domains {
		fmt.Fprintf(w, "%s\t%s\t%s\n", domain.Domain, domain.AppID, domain.Status)
	}
}

func printAppDomainDetail(w io.Writer, domain appDomainView) {
	fmt.Fprintf(w, "domain\t%s\n", domain.Domain)
	fmt.Fprintf(w, "app\t%s\n", domain.AppID)
	fmt.Fprintf(w, "status\t%s\n", domain.Status)
}

func printAppAddDomain(w io.Writer, body appAddDomainResponse) {
	fmt.Fprintf(w, "domain\t%s\n", body.Domain)
	fmt.Fprintf(w, "app\t%s\n", body.AppID)
	fmt.Fprintf(w, "status\t%s\n", body.Status)
	fmt.Fprintf(w, "dns_mode\t%s\n", body.DNSMode)
	if len(body.DNSRecords) > 0 {
		fmt.Fprintln(w, "dns_records")
		for _, record := range body.DNSRecords {
			fmt.Fprintf(w, "%s\t%s\t%s\n", record.RecordType, record.Name, record.Value)
		}
	}
}

func printAppDelete(w io.Writer, body appDeleteResponse) {
	fmt.Fprintf(w, "app\t%s\n", body.AppID)
	fmt.Fprintf(w, "complete\t%t\n", body.Complete)
	if !body.Complete {
		fmt.Fprintln(w, "status\tdelete_incomplete")
	}
	fmt.Fprintf(w, "pointer_deleted\t%t\n", body.PointerDeleted)
	fmt.Fprintf(w, "cloudfront_tenants_deleted\t%d\n", body.CloudFrontTenantsDeleted)
	fmt.Fprintf(w, "secrets_deleted\t%d\n", body.SecretsDeleted)
	fmt.Fprintf(w, "artifacts_deleted\t%d\n", body.ArtifactsDeleted)
	fmt.Fprintf(w, "artifacts_present\t%d\n", body.ArtifactsPresent)
	fmt.Fprintf(w, "env_keys_deleted\t%d\n", body.EnvKeysDeleted)
	fmt.Fprintf(w, "domains_deleted\t%d\n", body.DomainsDeleted)
	fmt.Fprintf(w, "catalog_deleted\t%t\n", body.CatalogDeleted)
	if len(body.Errors) > 0 {
		fmt.Fprintf(w, "errors\t%s\n", strings.Join(body.Errors, "; "))
	}
}

func printLogs(w io.Writer, logs []appLogRecord) {
	fmt.Fprintln(w, "TIMESTAMP\tLEVEL\tBUILD\tROUTE\tREQUEST\tMESSAGE")
	for _, record := range logs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", record.Timestamp, record.Level, record.BuildID, record.Route, record.RequestID, strings.ReplaceAll(record.Message, "\n", "\\n"))
		if record.ErrorStack != "" {
			fmt.Fprintf(w, "\t\t\t\t\t%s\n", strings.ReplaceAll(record.ErrorStack, "\n", "\\n"))
		}
	}
}

func printLogRecord(w io.Writer, record appLogRecord) {
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", record.Timestamp, record.Level, record.BuildID, record.Route, record.RequestID, strings.ReplaceAll(record.Message, "\n", "\\n"))
}

func logIdentity(record appLogRecord) string {
	parts := []string{record.Timestamp, record.Level, record.BuildID, record.Route, record.RequestID, record.Message, record.ErrorClass}
	return strings.Join(parts, "\x00")
}

func dnsRecordOwnerResource(record dnsRecordView) string {
	if record.OwnerResourceID == "" {
		return ""
	}
	if record.OwnerResourceType == "" {
		return record.OwnerResourceID
	}
	return record.OwnerResourceType + ":" + record.OwnerResourceID
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
		} else if build.DeployedAtMS != nil {
			createdAt = time.UnixMilli(int64(*build.DeployedAtMS)).UTC().Format(time.RFC3339)
		}
		artifactSize := ""
		if build.ArtifactSize != nil {
			artifactSize = strconv.FormatInt(*build.ArtifactSize, 10)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", build.BuildID, createdAt, artifactSize)
	}
}
