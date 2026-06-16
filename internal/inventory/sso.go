package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sso"
	ssotypes "github.com/aws/aws-sdk-go-v2/service/sso/types"
)

type ssoTokenCache struct {
	StartURL    string `json:"startUrl"`
	Region      string `json:"region"`
	AccessToken string `json:"accessToken"`
	ExpiresAt   string `json:"expiresAt"`
	Path        string `json:"-"`
}

type ssoAccount struct {
	ID    string
	Name  string
	Email string
}

func collectSSOAccounts(ctx context.Context, opts Options) ([]AssetRecord, error) {
	token, err := findSSOToken(opts.SSOStartURL, opts.SSORegion)
	if err != nil {
		return nil, err
	}
	region := strings.TrimSpace(opts.SSORegion)
	if region == "" {
		region = token.Region
	}
	if region == "" {
		return nil, fmt.Errorf("sso region is required; pass --sso-region or use an SSO cache containing region")
	}

	cfg, err := loadConfig(ctx, opts.Profile, region)
	if err != nil {
		return nil, fmt.Errorf("load sso config: %w", err)
	}
	ssoClient := sso.NewFromConfig(cfg)

	accounts, err := listSSOAccounts(ctx, ssoClient, token.AccessToken, opts.SSOAccountIDs)
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, fmt.Errorf("no SSO accounts found for the current login/token")
	}

	var records []AssetRecord
	var errs []error
	for _, account := range accounts {
		roleName, err := selectSSORole(ctx, ssoClient, token.AccessToken, account.ID, opts.SSORoleName)
		if err != nil {
			errs = append(errs, fmt.Errorf("sso account %s (%s): %w", account.ID, account.Name, err))
			continue
		}
		roleCreds, err := ssoClient.GetRoleCredentials(ctx, &sso.GetRoleCredentialsInput{
			AccessToken: aws.String(token.AccessToken),
			AccountId:   aws.String(account.ID),
			RoleName:    aws.String(roleName),
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("sso get role credentials %s/%s: %w", account.ID, roleName, err))
			continue
		}
		if roleCreds.RoleCredentials == nil {
			errs = append(errs, fmt.Errorf("sso get role credentials %s/%s: empty credentials", account.ID, roleName))
			continue
		}

		accountCfg := cfg
		accountCfg.Region = opts.Regions[0]
		accountCfg.Credentials = aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(
			aws.ToString(roleCreds.RoleCredentials.AccessKeyId),
			aws.ToString(roleCreds.RoleCredentials.SecretAccessKey),
			aws.ToString(roleCreds.RoleCredentials.SessionToken),
		))

		accountRecords, accountErr := collectWithBaseConfig(ctx, opts, accountCfg, account.ID, account.Name)
		records = append(records, accountRecords...)
		if accountErr != nil {
			errs = append(errs, fmt.Errorf("collect account %s (%s) role %s: %w", account.ID, account.Name, roleName, accountErr))
		}
	}

	return records, errors.Join(errs...)
}

func findSSOToken(startURL, region string) (ssoTokenCache, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return ssoTokenCache{}, err
	}
	matches, err := filepath.Glob(filepath.Join(home, ".aws", "sso", "cache", "*.json"))
	if err != nil {
		return ssoTokenCache{}, err
	}
	if len(matches) == 0 {
		return ssoTokenCache{}, fmt.Errorf("no AWS SSO cache files found; run `aws sso login` first")
	}

	startURL = strings.TrimSpace(startURL)
	region = strings.TrimSpace(region)
	now := time.Now().UTC()
	var candidates []ssoTokenCache
	for _, path := range matches {
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var token ssoTokenCache
		if json.Unmarshal(body, &token) != nil || token.AccessToken == "" {
			continue
		}
		expiresAt, err := parseSSOExpiresAt(token.ExpiresAt)
		if err != nil || !expiresAt.After(now) {
			continue
		}
		if startURL != "" && token.StartURL != startURL {
			continue
		}
		if region != "" && token.Region != "" && token.Region != region {
			continue
		}
		token.Path = path
		candidates = append(candidates, token)
	}
	if len(candidates) == 0 {
		return ssoTokenCache{}, fmt.Errorf("no valid AWS SSO token found; run `aws sso login` and optionally pass --sso-start-url/--sso-region")
	}
	sort.Slice(candidates, func(i, j int) bool {
		iExp, _ := parseSSOExpiresAt(candidates[i].ExpiresAt)
		jExp, _ := parseSSOExpiresAt(candidates[j].ExpiresAt)
		return iExp.After(jExp)
	})
	return candidates[0], nil
}

func parseSSOExpiresAt(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("empty expiresAt")
	}
	layouts := []string{time.RFC3339, "2006-01-02T15:04:05UTC", "2006-01-02T15:04:05Z0700"}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported expiresAt format %q", value)
}

func listSSOAccounts(ctx context.Context, client *sso.Client, accessToken string, accountIDs []string) ([]ssoAccount, error) {
	wanted := map[string]bool{}
	for _, id := range accountIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			wanted[id] = true
		}
	}

	var accounts []ssoAccount
	var nextToken *string
	for {
		out, err := client.ListAccounts(ctx, &sso.ListAccountsInput{
			AccessToken: aws.String(accessToken),
			MaxResults:  aws.Int32(100),
			NextToken:   nextToken,
		})
		if err != nil {
			return nil, fmt.Errorf("sso list accounts: %w", err)
		}
		for _, a := range out.AccountList {
			id := aws.ToString(a.AccountId)
			if len(wanted) > 0 && !wanted[id] {
				continue
			}
			accounts = append(accounts, ssoAccount{ID: id, Name: aws.ToString(a.AccountName), Email: aws.ToString(a.EmailAddress)})
		}
		if out.NextToken == nil || aws.ToString(out.NextToken) == "" {
			break
		}
		nextToken = out.NextToken
	}
	sort.Slice(accounts, func(i, j int) bool {
		return accounts[i].ID < accounts[j].ID
	})
	return accounts, nil
}

func selectSSORole(ctx context.Context, client *sso.Client, accessToken, accountID, preferredRole string) (string, error) {
	roles, err := listSSORoles(ctx, client, accessToken, accountID)
	if err != nil {
		return "", err
	}
	if len(roles) == 0 {
		return "", fmt.Errorf("no SSO roles available")
	}
	preferredRole = strings.TrimSpace(preferredRole)
	if preferredRole != "" {
		for _, r := range roles {
			if r == preferredRole {
				return r, nil
			}
		}
		return "", fmt.Errorf("role %q not available; available roles: %s", preferredRole, strings.Join(roles, ","))
	}
	return roles[0], nil
}

func listSSORoles(ctx context.Context, client *sso.Client, accessToken, accountID string) ([]string, error) {
	var roles []string
	var nextToken *string
	for {
		out, err := client.ListAccountRoles(ctx, &sso.ListAccountRolesInput{
			AccessToken: aws.String(accessToken),
			AccountId:   aws.String(accountID),
			MaxResults:  aws.Int32(100),
			NextToken:   nextToken,
		})
		if err != nil {
			return nil, fmt.Errorf("sso list account roles: %w", err)
		}
		for _, r := range out.RoleList {
			if name := roleName(r); name != "" {
				roles = append(roles, name)
			}
		}
		if out.NextToken == nil || aws.ToString(out.NextToken) == "" {
			break
		}
		nextToken = out.NextToken
	}
	sort.Strings(roles)
	return roles, nil
}

func roleName(role ssotypes.RoleInfo) string {
	return aws.ToString(role.RoleName)
}
