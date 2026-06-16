package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"aws-asset-view/internal/inventory"
)

func main() {
	var (
		profile         string
		profilesFlag    string
		regionsFlag     string
		servicesFlag    string
		output          string
		includeGlobal   bool
		rdsPricing      bool
		ssoAllAccounts  bool
		ssoRegion       string
		ssoStartURL     string
		ssoRoleName     string
		ssoAccounts     string
		includeSSOPerms bool
		ssoAdminProfile string
		timeout         time.Duration
	)

	flag.StringVar(&profile, "profile", "", "AWS shared config profile name")
	flag.StringVar(&profilesFlag, "profiles", "", "Comma-separated AWS shared config profiles to scan; useful when each account has a different SSO role")
	flag.StringVar(&regionsFlag, "regions", "ap-northeast-2", "Comma-separated AWS regions to scan")
	flag.StringVar(&servicesFlag, "services", "all", "Comma-separated services: all,ec2,eks,rds,s3,efs,backup,cloudwatch,logs,lb,route53,vpc,subnet,routetable,sg,vpn,flowlog,waf,lambda")
	flag.StringVar(&output, "output", "assets.csv", "CSV or XLSX output path, or '-' for CSV stdout")
	flag.BoolVar(&includeGlobal, "include-global", true, "Include global services such as Route53 and CloudFront-scope WAF")
	flag.BoolVar(&rdsPricing, "rds-pricing", false, "Use AWS Pricing API to fill RDS vCPU/memory; slower and may require pricing:GetProducts")
	flag.BoolVar(&ssoAllAccounts, "sso-all-accounts", false, "Collect every account available through the current AWS SSO login")
	flag.StringVar(&ssoRegion, "sso-region", "", "AWS SSO/IAM Identity Center region, inferred from cache when possible")
	flag.StringVar(&ssoStartURL, "sso-start-url", "", "AWS SSO start URL used to choose the cached login token")
	flag.StringVar(&ssoRoleName, "sso-role-name", "", "SSO permission-set role name to use in every account; defaults to the first available role per account")
	flag.StringVar(&ssoAccounts, "sso-account-ids", "", "Optional comma-separated account IDs to include when --sso-all-accounts is set")
	flag.BoolVar(&includeSSOPerms, "include-sso-permissions", true, "Include an XLSX sso_permissions sheet using SSO Admin / Identity Store APIs when output is .xlsx")
	flag.StringVar(&ssoAdminProfile, "sso-admin-profile", "", "AWS profile with sso-admin/identitystore permissions; defaults to --profile or first --profiles entry")
	flag.DurationVar(&timeout, "timeout", 10*time.Minute, "Collection timeout")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	opts := inventory.Options{
		Profile:               profile,
		Profiles:              splitCSV(profilesFlag),
		Regions:               splitCSV(regionsFlag),
		Services:              serviceSet(servicesFlag),
		IncludeGlobal:         includeGlobal,
		RDSPricing:            rdsPricing,
		SSOAllAccounts:        ssoAllAccounts,
		SSORegion:             ssoRegion,
		SSOStartURL:           ssoStartURL,
		SSORoleName:           ssoRoleName,
		SSOAccountIDs:         splitCSV(ssoAccounts),
		IncludeSSOPermissions: includeSSOPerms && strings.HasSuffix(strings.ToLower(output), ".xlsx"),
		SSOAdminProfile:       ssoAdminProfile,
	}

	report, err := inventory.CollectReport(ctx, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "collection completed with errors: %v\n", err)
	}
	records := report.Assets

	sort.Slice(records, func(i, j int) bool {
		li := records[i].AccountID + records[i].AccountName + records[i].SourceProfile + records[i].Region + records[i].Service + records[i].ResourceType + records[i].ResourceID
		lj := records[j].AccountID + records[j].AccountName + records[j].SourceProfile + records[j].Region + records[j].Service + records[j].ResourceType + records[j].ResourceID
		return li < lj
	})
	report.Assets = records

	if strings.HasSuffix(strings.ToLower(output), ".xlsx") {
		if werr := inventory.WriteXLSX(output, report); werr != nil {
			fmt.Fprintf(os.Stderr, "write xlsx: %v\n", werr)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "wrote %d asset rows, %d security group rule rows, %d sso permission rows to %s\n", len(report.Assets), len(report.SecurityRules), len(report.SSOPermissions), output)
		if err != nil {
			os.Exit(2)
		}
		return
	}

	if output == "-" {
		if werr := inventory.WriteCSV(os.Stdout, records); werr != nil {
			fmt.Fprintf(os.Stderr, "write csv: %v\n", werr)
			os.Exit(1)
		}
		return
	}

	f, ferr := os.Create(output)
	if ferr != nil {
		fmt.Fprintf(os.Stderr, "create output: %v\n", ferr)
		os.Exit(1)
	}
	defer f.Close()
	if werr := inventory.WriteCSV(f, records); werr != nil {
		fmt.Fprintf(os.Stderr, "write csv: %v\n", werr)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "wrote %d asset rows to %s\n", len(records), output)
	if err != nil {
		os.Exit(2)
	}
}

func splitCSV(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func serviceSet(v string) map[string]bool {
	set := map[string]bool{}
	for _, s := range splitCSV(strings.ToLower(v)) {
		set[s] = true
	}
	return set
}
