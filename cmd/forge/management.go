package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"

	"github.com/JBraunsmaJr/forge/internal/api"
	"github.com/JBraunsmaJr/forge/internal/secrets"
	"github.com/spf13/cobra"
)

func init() {
	orgCmd := &cobra.Command{
		Use:   "org",
		Short: "Org management",
	}
	orgCmd.AddCommand(&cobra.Command{
		Use:   "create <name>",
		Short: "create an organisation",
		Args:  cobra.ExactArgs(1),
		Run:   runOrgCreate,
	})
	orgCmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "list all organisations",
		Run:   runOrgList,
	})
	rootCmd.AddCommand(orgCmd)

	projectCmd := &cobra.Command{
		Use:   "project",
		Short: "Project management",
	}
	projectCmd.AddCommand(&cobra.Command{
		Use:   "add <name> <repo-url>",
		Short: "add a project",
		Args:  cobra.ExactArgs(2),
		Run:   runProjectAdd,
	})
	projectCmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "list projects",
		Run:   runProjectList,
	})
	projectCmd.AddCommand(&cobra.Command{
		Use:   "update <id>",
		Short: "update a project",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("project update not implemented in this preview")
		},
	})
	projectCmd.AddCommand(&cobra.Command{
		Use:   "get-build-format <project-id> <pipeline-name>",
		Short: "view the build-number format configured for a pipeline",
		Args:  cobra.ExactArgs(2),
		Run:   runProjectGetBuildFormat,
	})
	projectCmd.AddCommand(&cobra.Command{
		Use:   "set-build-format <project-id> <pipeline-name> <format>",
		Short: "set the build-number format for a pipeline, e.g. '%year%-%month%.%counter%'",
		Args:  cobra.ExactArgs(3),
		Run:   runProjectSetBuildFormat,
	})
	projectCmd.AddCommand(&cobra.Command{
		Use:   "get-version <project-id> <pipeline-name>",
		Short: "view the major/minor version configured for a pipeline",
		Args:  cobra.ExactArgs(2),
		Run:   runProjectGetVersion,
	})
	projectCmd.AddCommand(&cobra.Command{
		Use:   "set-version <project-id> <pipeline-name> <major> <minor>",
		Short: "manually set the major/minor version for a pipeline",
		Args:  cobra.ExactArgs(4),
		Run:   runProjectSetVersion,
	})
	projectCmd.AddCommand(&cobra.Command{
		Use:   "set-version-tag-filter <project-id> <pipeline-name> <branch-filter>",
		Short: "restrict which branches' tag pushes may update the tag-derived version (empty = default branch)",
		Args:  cobra.ExactArgs(3),
		Run:   runProjectSetVersionTagFilter,
	})
	rootCmd.AddCommand(projectCmd)

	policyCmd := &cobra.Command{
		Use:   "policy",
		Short: "Policy management",
	}
	policyCmd.AddCommand(&cobra.Command{
		Use:   "create <name> <steps.json>",
		Short: "create a static policy",
		Args:  cobra.ExactArgs(2),
		Run:   runPolicyCreate,
	})
	policyCmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "list policies",
		Run:   runPolicyList,
	})
	policyCmd.AddCommand(&cobra.Command{
		Use:   "delete <id>",
		Short: "delete a policy",
		Args:  cobra.ExactArgs(1),
		Run:   runPolicyDelete,
	})
	rootCmd.AddCommand(policyCmd)

	secretCmd := &cobra.Command{
		Use:   "secret",
		Short: "Secret management",
	}
	secretCmd.AddCommand(&cobra.Command{
		Use:   "set <NAME> <VALUE>",
		Short: "set a secret",
		Args:  cobra.ExactArgs(2),
		Run:   runSecretSet,
	})
	secretCmd.AddCommand(&cobra.Command{
		Use:   "get <NAME>",
		Short: "get a secret",
		Args:  cobra.ExactArgs(1),
		Run:   runSecretGet,
	})
	secretCmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "list secrets",
		Run:   runSecretList,
	})
	rootCmd.AddCommand(secretCmd)
}

func runProjectAdd(cmd *cobra.Command, args []string) {
	schedulerURL := cliSchedulerURL()
	req := api.CreateProjectRequest{
		Name:    args[0],
		RepoURL: args[1],
	}
	body, _ := json.Marshal(req)
	resp, err := cliPost(schedulerURL+"/api/v1/projects", "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	checkResp(resp, http.StatusCreated)
	fmt.Println("✓ project created")
}

func runProjectList(cmd *cobra.Command, args []string) {
	schedulerURL := cliSchedulerURL()
	resp, err := cliGet(schedulerURL + "/api/v1/projects")
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	var projects []api.ProjectInfo
	json.NewDecoder(resp.Body).Decode(&projects)
	for _, p := range projects {
		fmt.Printf("%s  %s\n", p.ID, p.Name)
	}
}

func runProjectGetBuildFormat(cmd *cobra.Command, args []string) {
	projectID, pipelineName := args[0], args[1]
	schedulerURL := cliSchedulerURL()
	u := fmt.Sprintf("%s/api/v1/projects/%s/build-format?pipeline=%s", schedulerURL, projectID, url.QueryEscape(pipelineName))

	resp, err := cliGet(u)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	checkResp(resp, http.StatusOK)

	var info api.BuildFormatInfo
	json.NewDecoder(resp.Body).Decode(&info)
	fmt.Printf("format: %s\n", info.Format)
	fmt.Printf("sample build number: %s\n", info.SampleBuildNumber)
}

func runProjectSetBuildFormat(cmd *cobra.Command, args []string) {
	projectID, pipelineName, format := args[0], args[1], args[2]
	schedulerURL := cliSchedulerURL()

	req := api.SetBuildFormatRequest{PipelineName: pipelineName, Format: format}
	body, _ := json.Marshal(req)
	u := fmt.Sprintf("%s/api/v1/projects/%s/build-format", schedulerURL, projectID)

	resp, err := cliPut(u, "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	checkResp(resp, http.StatusNoContent)
	fmt.Println("✓ build format updated")
}

func runProjectGetVersion(cmd *cobra.Command, args []string) {
	projectID, pipelineName := args[0], args[1]
	schedulerURL := cliSchedulerURL()
	u := fmt.Sprintf("%s/api/v1/projects/%s/build-format?pipeline=%s", schedulerURL, projectID, url.QueryEscape(pipelineName))

	resp, err := cliGet(u)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	checkResp(resp, http.StatusOK)

	var info api.BuildFormatInfo
	json.NewDecoder(resp.Body).Decode(&info)
	fmt.Printf("major.minor: %d.%d\n", info.Major, info.Minor)
	if info.VersionSource != "" {
		fmt.Printf("source: %s (by %s)\n", info.VersionSource, info.VersionSetBy)
	} else {
		fmt.Println("source: (never set)")
	}
	if info.VersionTagFilter != "" {
		fmt.Printf("tag filter: %s\n", info.VersionTagFilter)
	} else {
		fmt.Println("tag filter: (project default branch)")
	}
}

func runProjectSetVersion(cmd *cobra.Command, args []string) {
	projectID, pipelineName := args[0], args[1]
	major, err := strconv.Atoi(args[2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ invalid major version %q: %v\n", args[2], err)
		os.Exit(1)
	}
	minor, err := strconv.Atoi(args[3])
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ invalid minor version %q: %v\n", args[3], err)
		os.Exit(1)
	}

	schedulerURL := cliSchedulerURL()
	req := api.SetVersionRequest{PipelineName: pipelineName, Major: major, Minor: minor}
	body, _ := json.Marshal(req)
	u := fmt.Sprintf("%s/api/v1/projects/%s/version", schedulerURL, projectID)

	resp, err := cliPut(u, "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	checkResp(resp, http.StatusNoContent)
	fmt.Printf("✓ version set to %d.%d\n", major, minor)
}

func runProjectSetVersionTagFilter(cmd *cobra.Command, args []string) {
	projectID, pipelineName, filter := args[0], args[1], args[2]
	schedulerURL := cliSchedulerURL()

	req := api.SetVersionTagFilterRequest{PipelineName: pipelineName, Filter: filter}
	body, _ := json.Marshal(req)
	u := fmt.Sprintf("%s/api/v1/projects/%s/version-tag-filter", schedulerURL, projectID)

	resp, err := cliPut(u, "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	checkResp(resp, http.StatusNoContent)
	fmt.Println("✓ version tag filter updated")
}

func runPolicyList(cmd *cobra.Command, args []string) {
	schedulerURL := cliSchedulerURL()
	resp, err := cliGet(schedulerURL + "/api/v1/policies")
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	var policies []api.PolicyInfo
	json.NewDecoder(resp.Body).Decode(&policies)
	for _, p := range policies {
		fmt.Printf("%s  %s\n", p.ID, p.Name)
	}
}

func runPolicyCreate(cmd *cobra.Command, args []string) {
	schedulerURL := cliSchedulerURL()
	name := args[0]
	stepsFile := args[1]
	content, err := os.ReadFile(stepsFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}
	var steps []api.StepDef
	if err := json.Unmarshal(content, &steps); err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}
	req := api.CreatePolicyRequest{
		Name:  name,
		Steps: steps,
	}
	body, _ := json.Marshal(req)
	resp, err := cliPost(schedulerURL+"/api/v1/policies", "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	checkResp(resp, http.StatusCreated)
	fmt.Println("✓ policy created")
}

func runPolicyDelete(cmd *cobra.Command, args []string) {
	schedulerURL := cliSchedulerURL()
	resp, err := cliDelete(schedulerURL + "/api/v1/policies/" + args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	checkResp(resp, http.StatusOK)
	fmt.Println("✓ policy deleted")
}

func runSecretSet(cmd *cobra.Command, args []string) {
	addr := os.Getenv("FORGE_VAULT_ADDR")
	token := os.Getenv("FORGE_VAULT_TOKEN")
	client := secrets.NewClient(addr, token)
	err := client.Set(secrets.GlobalScopePath(), args[0], args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✓ secret set")
}

func runSecretGet(cmd *cobra.Command, args []string) {
	addr := os.Getenv("FORGE_VAULT_ADDR")
	token := os.Getenv("FORGE_VAULT_TOKEN")
	client := secrets.NewClient(addr, token)
	val, err := client.Get(secrets.GlobalScopePath(), args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}
	fmt.Println(val)
}

func runSecretList(cmd *cobra.Command, args []string) {
	addr := os.Getenv("FORGE_VAULT_ADDR")
	token := os.Getenv("FORGE_VAULT_TOKEN")
	client := secrets.NewClient(addr, token)
	names, err := client.List(secrets.GlobalScopePath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}
	for _, n := range names {
		fmt.Println(n)
	}
}

func runOrgCreate(cmd *cobra.Command, args []string) {
	schedulerURL := cliSchedulerURL()
	body, _ := json.Marshal(api.CreateOrgRequest{Name: args[0]})
	resp, err := cliPost(schedulerURL+"/api/v1/orgs", "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	checkResp(resp, http.StatusCreated)
	var org api.OrgInfo
	json.NewDecoder(resp.Body).Decode(&org)
	fmt.Printf("✓ org created\n  ID:   %s\n  Name: %s\n\n  Set for this session:\n  $env:FORGE_ORG = '%s'\n", org.ID, org.Name, org.ID)
}

func runOrgList(cmd *cobra.Command, args []string) {
	schedulerURL := cliSchedulerURL()
	resp, err := cliGet(schedulerURL + "/api/v1/orgs")
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	var orgs []api.OrgInfo
	json.NewDecoder(resp.Body).Decode(&orgs)
	if len(orgs) == 0 {
		fmt.Println("no orgs")
		return
	}
	for _, o := range orgs {
		fmt.Printf("%s  %s\n", o.ID, o.Name)
	}
}
