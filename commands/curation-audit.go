package commands

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jfrog/jfrog-cli-core/v2/common/commands"
	"github.com/jfrog/jfrog-cli-core/v2/plugins/components"
	"github.com/jfrog/jfrog-cli-core/v2/utils/config"
	"github.com/jfrog/jfrog-client-go/utils/log"
)

// csvHeader matches the native curation audit CSV column order exactly.
var csvHeader = []string{
	"id", "created_at", "action", "reason", "package_type", "package_name", "package_version", "package_url",
	"curated_repo_server_name", "curated_repo_name", "curated_repo_project_key", "user_name", "user_mail",
	"origin_repo_server_name", "origin_repo_name", "origin_repo_project_key", "public_repo_name", "public_repo_url",
	"ecosystem", "ondemand", "policy_id", "policy_name", "condition_name", "condition_category", "dry_run", "result", "policy_reason",
}

type curationPolicy struct {
	ID                int64  `json:"policy_id"`
	Name              string `json:"policy_name"`
	ConditionName     string `json:"condition_name"`
	ConditionCategory string `json:"condition_category"`
	DryRun            bool   `json:"dry_run"`
	Result            string `json:"result"`
	Reason            string `json:"policy_reason"`
}

type curationPackage struct {
	ID                    int64             `json:"id"`
	CreatedAt             string            `json:"created_at"`
	Action                string            `json:"action"`
	Reason                string            `json:"reason"`
	PackageType           string            `json:"package_type"`
	PackageName           string            `json:"package_name"`
	PackageVersion        string            `json:"package_version"`
	PackageURL            string            `json:"package_url"`
	CuratedRepoServerName string            `json:"curated_repository_server_name"`
	CuratedRepoName       string            `json:"curated_repository_name"`
	CuratedProjectKey     string            `json:"curated_project"`
	Username              string            `json:"username"`
	UserMail              string            `json:"user_mail"`
	OriginRepoServerName  string            `json:"origin_repository_server_name"`
	OriginRepoName        string            `json:"origin_repository_name"`
	OriginProjectKey      string            `json:"origin_project"`
	PublicRepoName        string            `json:"public_repo_name"`
	PublicRepoURL         string            `json:"public_repo_url"`
	Ecosystem             string            `json:"ecosystem"`
	OnDemand              bool              `json:"ondemand"`
	Policies              []curationPolicy  `json:"policies"`
}

type curationAuditResponse struct {
	Data []curationPackage `json:"data"`
	Meta struct {
		ResultCount int `json:"result_count"`
	} `json:"meta"`
}

func GetCurationAuditCommand() components.Command {
	return components.Command{
		Name:        "ce",
		Description: "Download the Curation audit log as CSV",
		Flags:       getCurationAuditFlags(),
		Action: func(c *components.Context) error {
			return CurationAuditCmd(c)
		},
	}
}

func getCurationAuditFlags() []components.Flag {
	return []components.Flag{
		components.StringFlag{
			Name:        "from",
			Description: "Start date, format YYYY-MM-DD. Defaults to 7 days ago if omitted.",
		},
		components.StringFlag{
			Name:        "to",
			Description: "End date, format YYYY-MM-DD. Defaults to now if omitted.",
		},
		components.StringFlag{
			Name:        "project",
			Description: "Only keep rows whose curated repo name starts with this project key",
		},
		components.StringFlag{
			Name:        "output",
			Description: "Output CSV file path (default: curation-audit-<from>_<to>.csv)",
		},
		components.StringFlag{
			Name:        "status",
			Description: "Comma-separated filter: blocked, approved, not-inspected, dry-run (any combination). Default: all real events.",
		},
	}
}

////////////////// ACTIONS / COMMANDS

func CurationAuditCmd(c *components.Context) error {

	from := c.GetStringFlagValue("from")
	to := c.GetStringFlagValue("to")

	rtDetails, err := commands.GetConfig("", false)
	if err != nil {
		return err
	}

	var packages []curationPackage
	if status := c.GetStringFlagValue("status"); status != "" {
		packages, err = fetchByStatus(rtDetails, from, to, status)
	} else {
		packages, err = fetchCurationAuditRange(rtDetails, from, to, false)
	}
	if err != nil {
		return err
	}

	if project := c.GetStringFlagValue("project"); project != "" {
		packages = filterByProject(packages, project)
	}

	output := c.GetStringFlagValue("output")
	if output == "" {
		rangeLabel := from + "_" + to
		if from == "" && to == "" {
			rangeLabel = "last-7-days"
		}
		output = "curation-audit-" + rangeLabel + ".csv"
	}

	if err := writeCurationCSV(output, packages); err != nil {
		return err
	}

	log.Info("Wrote " + output)
	return nil
}

var validStatuses = map[string]bool{"blocked": true, "approved": true, "passed": true}

// fetchByStatus splits the comma-separated --status list into real-data action filters
// (blocked/approved/not-inspected, i.e. action=passed) and the dry-run bucket, fetching each
// only if requested and merging the results.
func fetchByStatus(rtDetails *config.ServerDetails, from, to, status string) ([]curationPackage, error) {
	wantReal := map[string]bool{}
	wantDryRun := false

	for _, s := range strings.Split(status, ",") {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		if s == "not-inspected" {
			s = "passed"
		}
		if s == "dry-run" {
			wantDryRun = true
			continue
		}
		if !validStatuses[s] {
			return nil, fmt.Errorf("invalid --status value %q, expected any of: blocked, approved, not-inspected, dry-run", s)
		}
		wantReal[s] = true
	}

	var all []curationPackage
	if len(wantReal) > 0 {
		real, err := fetchCurationAuditRange(rtDetails, from, to, false)
		if err != nil {
			return nil, err
		}
		for _, p := range real {
			if wantReal[p.Action] {
				all = append(all, p)
			}
		}
	}
	if wantDryRun {
		dryRunPackages, err := fetchCurationAuditRange(rtDetails, from, to, true)
		if err != nil {
			return nil, err
		}
		all = append(all, dryRunPackages...)
	}
	return all, nil
}

const dateLayout = "2006-01-02"

// The curation audit API rejects any single request spanning more than 168 hours (7 days),
// so fetchCurationAuditRange splits a wider [from, to] into 7-day windows and merges the results.
// If from and to are both empty, the date range is left to the API's own default (last 7 days).
func fetchCurationAuditRange(rtDetails *config.ServerDetails, from, to string, dryRun bool) ([]curationPackage, error) {
	if from == "" && to == "" {
		return fetchCurationAudit(rtDetails, "", "", dryRun)
	}

	start, err := time.Parse(dateLayout, from)
	if err != nil {
		return nil, fmt.Errorf("invalid --from date %q, expected YYYY-MM-DD", from)
	}
	end, err := time.Parse(dateLayout, to)
	if err != nil {
		return nil, fmt.Errorf("invalid --to date %q, expected YYYY-MM-DD", to)
	}
	if end.Before(start) {
		return nil, fmt.Errorf("--to (%s) is before --from (%s)", to, from)
	}

	const maxWindow = 7 * 24 * time.Hour
	var all []curationPackage
	for chunkStart := start; !chunkStart.After(end); chunkStart = chunkStart.Add(maxWindow) {
		chunkEnd := chunkStart.Add(maxWindow - 24*time.Hour)
		if chunkEnd.After(end) {
			chunkEnd = end
		}
		packages, err := fetchCurationAudit(rtDetails, chunkStart.Format(dateLayout), chunkEnd.Format(dateLayout), dryRun)
		if err != nil {
			return nil, err
		}
		all = append(all, packages...)
	}
	return all, nil
}

// The API caps each response at numOfRows (max 2000) and never returns more than that in one
// call, so fetchCurationAudit pages through offset until a short page signals there's no more data.
const numOfRowsPerPage = 2000

func fetchCurationAudit(rtDetails *config.ServerDetails, from, to string, dryRun bool) ([]curationPackage, error) {
	var all []curationPackage

	for offset := 0; ; offset += numOfRowsPerPage {
		url := strings.TrimSuffix(rtDetails.GetXrayUrl(), "/") + "/api/v1/curation/audit/packages" +
			"?dry_run=" + fmt.Sprintf("%t", dryRun) +
			"&num_of_rows=" + fmt.Sprintf("%d", numOfRowsPerPage) +
			"&offset=" + fmt.Sprintf("%d", offset)
		if from != "" {
			url += "&created_at_start=" + from + "T00:00:00Z"
		}
		if to != "" {
			url += "&created_at_end=" + to + "T23:59:59Z"
		}

		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		setAuthHeader(req, rtDetails)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("curation audit request failed (%d): %s", resp.StatusCode, string(body))
		}

		var parsed curationAuditResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, err
		}

		all = append(all, parsed.Data...)

		if len(parsed.Data) < numOfRowsPerPage {
			break
		}
	}

	return all, nil
}

func setAuthHeader(req *http.Request, rtDetails *config.ServerDetails) {
	if token := rtDetails.GetAccessToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		return
	}
	req.SetBasicAuth(rtDetails.GetUser(), rtDetails.GetPassword())
}

func filterByProject(packages []curationPackage, project string) []curationPackage {
	prefix := project + "-"
	filtered := make([]curationPackage, 0)
	for _, p := range packages {
		if strings.HasPrefix(p.CuratedRepoName, prefix) {
			filtered = append(filtered, p)
		}
	}
	return filtered
}

func writeCurationCSV(path string, packages []curationPackage) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	if err := w.Write(csvHeader); err != nil {
		return err
	}

	for _, p := range packages {
		if len(p.Policies) == 0 {
			if err := w.Write(curationRow(p, nil)); err != nil {
				return err
			}
			continue
		}
		for _, policy := range p.Policies {
			if err := w.Write(curationRow(p, &policy)); err != nil {
				return err
			}
		}
	}

	return w.Error()
}

func curationRow(p curationPackage, policy *curationPolicy) []string {
	row := []string{
		fmt.Sprintf("%d", p.ID),
		p.CreatedAt,
		p.Action,
		p.Reason,
		p.PackageType,
		p.PackageName,
		p.PackageVersion,
		p.PackageURL,
		p.CuratedRepoServerName,
		p.CuratedRepoName,
		p.CuratedProjectKey,
		p.Username,
		p.UserMail,
		p.OriginRepoServerName,
		p.OriginRepoName,
		p.OriginProjectKey,
		p.PublicRepoName,
		p.PublicRepoURL,
		p.Ecosystem,
		fmt.Sprintf("%t", p.OnDemand),
	}

	if policy == nil {
		return append(row, "", "", "", "", "", "", "")
	}

	return append(row,
		fmt.Sprintf("%d", policy.ID),
		policy.Name,
		policy.ConditionName,
		policy.ConditionCategory,
		fmt.Sprintf("%t", policy.DryRun),
		policy.Result,
		policy.Reason,
	)
}
