package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strings"
)

type storageView struct {
	StorageID          string `json:"storage_id"`
	ProjectID          string `json:"project_id"`
	Name               string `json:"name"`
	Bucket             string `json:"bucket"`
	Provider           string `json:"provider"`
	Status             string `json:"status"`
	Endpoint           string `json:"endpoint"`
	Region             string `json:"region"`
	DefaultDomain      string `json:"default_domain"`
	PublicAccess       string `json:"public_access"`
	PublicDomainStatus string `json:"public_domain_status"`
	PublicTLSStatus    string `json:"public_tls_status"`
	PublicBaseURL      string `json:"public_base_url,omitempty"`
	LastError          string `json:"last_error,omitempty"`
}

type storageResponse struct {
	Storage storageView `json:"storage"`
}
type storagesResponse struct {
	Storages []storageView `json:"storages"`
}

func storageCommand(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: comwit storage <create|list|get|public|delete>")
	}
	switch args[0] {
	case "help", "-h", "--help":
		storageUsage(stdout)
		return nil
	case "create":
		return storageCreateCommand(args[1:], stdout)
	case "list":
		return storageListCommand(args[1:], stdout)
	case "get":
		return storageGetCommand(args[1:], stdout)
	case "public":
		return storagePublicCommand(args[1:], stdout)
	case "delete":
		return storageDeleteCommand(args[1:], stdout)
	default:
		return fmt.Errorf("unknown storage command %q", args[0])
	}
}

func storageUsage(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  comwit storage create --project <id> --name <bucket-name> [--public] [--location-hint apac]
  comwit storage list --project <id>
  comwit storage get --project <id> --storage <id>
  comwit storage public <enable|disable> --project <id> --storage <id>
  comwit storage delete --project <id> --storage <id>`)
}

func storageCreateCommand(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("storage create", flag.ContinueOnError)
	project := fs.String("project", "", "project id")
	name := fs.String("name", "", "globally unique Storage/bucket name")
	public := fs.Bool("public", false, "enable the default public domain")
	location := fs.String("location-hint", "apac", "R2 placement hint")
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
	bucket := strings.TrimSpace(*name)
	if bucket == "" {
		return errors.New("--name is required")
	}
	payload := map[string]any{"name": bucket, "public": *public, "location_hint": strings.TrimSpace(*location)}
	var body storageResponse
	if err := newClient(cfg).postJSON(projectStoragesPath(projectID), payload, &body); err != nil {
		return err
	}
	printStorageDetail(stdout, body.Storage)
	return nil
}

func storageListCommand(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("storage list", flag.ContinueOnError)
	project := fs.String("project", "", "project id")
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
	var body storagesResponse
	if err := newClient(cfg).getJSON(projectStoragesPath(projectID), &body); err != nil {
		return err
	}
	printStorages(stdout, body.Storages)
	return nil
}

func storageGetCommand(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("storage get", flag.ContinueOnError)
	project := fs.String("project", "", "project id")
	storageID := fs.String("storage", "", "Storage id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, projectID, id, err := storageCommandContext(*project, *storageID)
	if err != nil {
		return err
	}
	var body storageResponse
	if err := newClient(cfg).getJSON(projectStoragePath(projectID, id), &body); err != nil {
		return err
	}
	printStorageDetail(stdout, body.Storage)
	return nil
}

func storagePublicCommand(args []string, stdout io.Writer) error {
	if len(args) == 0 || (args[0] != "enable" && args[0] != "disable") {
		return errors.New("usage: comwit storage public <enable|disable> --project <id> --storage <id>")
	}
	enabled := args[0] == "enable"
	fs := flag.NewFlagSet("storage public "+args[0], flag.ContinueOnError)
	project := fs.String("project", "", "project id")
	storageID := fs.String("storage", "", "Storage id")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	cfg, projectID, id, err := storageCommandContext(*project, *storageID)
	if err != nil {
		return err
	}
	var body storageResponse
	if err := newClient(cfg).putJSON(projectStoragePath(projectID, id)+"/public-access", map[string]bool{"enabled": enabled}, &body); err != nil {
		return err
	}
	printStorageDetail(stdout, body.Storage)
	return nil
}

func storageDeleteCommand(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("storage delete", flag.ContinueOnError)
	project := fs.String("project", "", "project id")
	storageID := fs.String("storage", "", "Storage id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, projectID, id, err := storageCommandContext(*project, *storageID)
	if err != nil {
		return err
	}
	var body storageResponse
	if err := newClient(cfg).deleteJSON(projectStoragePath(projectID, id), &body); err != nil {
		return err
	}
	printStorageDetail(stdout, body.Storage)
	return nil
}

func storageCommandContext(project, id string) (configFile, string, string, error) {
	cfg, err := loadConfig()
	if err != nil {
		return configFile{}, "", "", err
	}
	projectID := selectProject(project, cfg)
	if projectID == "" {
		return configFile{}, "", "", errors.New("--project is required")
	}
	storageID := strings.TrimSpace(id)
	if storageID == "" {
		return configFile{}, "", "", errors.New("--storage is required")
	}
	return cfg, projectID, storageID, nil
}
func projectStoragesPath(projectID string) string {
	return "/v1/projects/" + url.PathEscape(projectID) + "/storages"
}
func projectStoragePath(projectID, storageID string) string {
	return projectStoragesPath(projectID) + "/" + url.PathEscape(storageID)
}

func printStorages(w io.Writer, values []storageView) {
	fmt.Fprintln(w, "STORAGE ID\tNAME\tSTATUS\tPUBLIC ACCESS\tBUCKET")
	for _, value := range values {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", value.StorageID, value.Name, value.Status, value.PublicAccess, value.Bucket)
	}
}
func printStorageDetail(w io.Writer, value storageView) {
	fmt.Fprintf(w, "storage_id\t%s\n", value.StorageID)
	fmt.Fprintf(w, "bucket\t%s\n", value.Bucket)
	fmt.Fprintf(w, "status\t%s\n", value.Status)
	fmt.Fprintf(w, "endpoint\t%s\n", value.Endpoint)
	fmt.Fprintf(w, "region\t%s\n", value.Region)
	fmt.Fprintf(w, "default_domain\t%s\n", value.DefaultDomain)
	fmt.Fprintf(w, "public_access\t%s\n", value.PublicAccess)
	if value.PublicBaseURL != "" {
		fmt.Fprintf(w, "public_base_url\t%s\n", value.PublicBaseURL)
	}
	if value.LastError != "" {
		fmt.Fprintf(w, "last_error\t%s\n", value.LastError)
	}
}
