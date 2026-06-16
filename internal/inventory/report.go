package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/identitystore"
	"github.com/aws/aws-sdk-go-v2/service/ssoadmin"
	ssoadmintypes "github.com/aws/aws-sdk-go-v2/service/ssoadmin/types"
)

type Report struct {
	Assets         []AssetRecord
	SecurityRules  []SecurityGroupRuleRecord
	SSOPermissions []SSOPermissionRecord
}

type SecurityGroupRuleRecord struct {
	AccountID     string
	AccountName   string
	SourceProfile string
	Region        string
	GroupID       string
	GroupName     string
	Direction     string
	Priority      string
	RuleName      string
	Protocol      string
	Port          string
	Source        string
	Destination   string
	Access        string
	Note          string
}

type SSOPermissionRecord struct {
	AccountID     string
	AccountName   string
	PermissionSet string
	PrincipalType string
	PrincipalID   string
	UserName      string
	DisplayName   string
	Email         string
	GroupName     string
	PermissionARN string
	SourceProfile string
	IdentityStore string
	InstanceARN   string
	Note          string
}

func CollectReport(ctx context.Context, opts Options) (Report, error) {
	assets, err := Collect(ctx, opts)
	report := Report{Assets: assets, SecurityRules: BuildSecurityGroupRules(assets)}
	if opts.IncludeSSOPermissions {
		permissions, perr := CollectSSOPermissions(ctx, opts)
		report.SSOPermissions = permissions
		if perr != nil {
			report.SSOPermissions = append(report.SSOPermissions, SSOPermissionRecord{SourceProfile: firstNonEmpty(opts.SSOAdminProfile, opts.Profile), Note: perr.Error()})
		}
	}
	return report, err
}

func BuildSecurityGroupRules(assets []AssetRecord) []SecurityGroupRuleRecord {
	var rows []SecurityGroupRuleRecord
	for _, asset := range assets {
		if asset.ResourceType != "security_group" || asset.DetailsJSON == "" {
			continue
		}
		var details map[string]any
		if json.Unmarshal([]byte(asset.DetailsJSON), &details) != nil {
			continue
		}
		rows = append(rows, flattenSGDirection(asset, "inbound", details["ingress"])...)
		rows = append(rows, flattenSGDirection(asset, "outbound", details["egress"])...)
	}
	return rows
}

func flattenSGDirection(asset AssetRecord, direction string, raw any) []SecurityGroupRuleRecord {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	var rows []SecurityGroupRuleRecord
	for _, item := range items {
		perm, ok := item.(map[string]any)
		if !ok {
			continue
		}
		protocol := strFromAny(perm["protocol"])
		port := portRange(perm["from_port"], perm["to_port"])
		if protocol == "-1" || protocol == "" {
			protocol = "all"
			port = "all"
		}
		for _, cidr := range cidrsFromAny(perm["ipv4"], "CidrIp", "Description") {
			rows = append(rows, sgRow(asset, direction, protocol, port, cidr.value, cidr.description))
		}
		for _, cidr := range cidrsFromAny(perm["ipv6"], "CidrIpv6", "Description") {
			rows = append(rows, sgRow(asset, direction, protocol, port, cidr.value, cidr.description))
		}
		for _, sg := range cidrsFromAny(perm["sg_refs"], "GroupId", "Description") {
			rows = append(rows, sgRow(asset, direction, protocol, port, sg.value, sg.description))
		}
		if len(rows) == 0 {
			rows = append(rows, sgRow(asset, direction, protocol, port, "", ""))
		}
	}
	return rows
}

func sgRow(asset AssetRecord, direction, protocol, port, peer, description string) SecurityGroupRuleRecord {
	row := SecurityGroupRuleRecord{
		AccountID:     asset.AccountID,
		AccountName:   asset.AccountName,
		SourceProfile: asset.SourceProfile,
		Region:        asset.Region,
		GroupID:       asset.ResourceID,
		GroupName:     asset.Name,
		Direction:     direction,
		Protocol:      protocol,
		Port:          port,
		Access:        "allow",
		RuleName:      description,
		Note:          description,
	}
	if direction == "inbound" {
		row.Source = peer
	} else {
		row.Destination = peer
	}
	return row
}

type peerValue struct{ value, description string }

func cidrsFromAny(raw any, valueKey, descKey string) []peerValue {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]peerValue, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, peerValue{value: strFromAny(m[valueKey]), description: strFromAny(m[descKey])})
	}
	return out
}

func portRange(from, to any) string {
	f, t := strFromAny(from), strFromAny(to)
	if f == "" && t == "" {
		return "all"
	}
	if f == t || t == "" {
		return f
	}
	if f == "" {
		return t
	}
	return f + "-" + t
}

func strFromAny(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%.0f", t), ".0"), ".")
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprint(t)
	}
}

func CollectSSOPermissions(ctx context.Context, opts Options) ([]SSOPermissionRecord, error) {
	profile := strings.TrimSpace(opts.SSOAdminProfile)
	if profile == "" {
		profile = strings.TrimSpace(opts.Profile)
	}
	if profile == "" && len(opts.Profiles) > 0 {
		profile = strings.TrimSpace(opts.Profiles[0])
	}
	cfg, err := loadConfig(ctx, profile, firstNonEmpty(opts.SSORegion, opts.Regions[0], "us-east-1"))
	if err != nil {
		return nil, err
	}
	admin := ssoadmin.NewFromConfig(cfg)
	instances, err := admin.ListInstances(ctx, &ssoadmin.ListInstancesInput{})
	if err != nil {
		return nil, fmt.Errorf("sso-admin list instances: %w", err)
	}
	var rows []SSOPermissionRecord
	for _, inst := range instances.Instances {
		instanceARN := aws.ToString(inst.InstanceArn)
		identityStoreID := aws.ToString(inst.IdentityStoreId)
		if instanceARN == "" || identityStoreID == "" {
			continue
		}
		store := identitystore.NewFromConfig(cfg)
		ps, err := listPermissionSets(ctx, admin, instanceARN)
		if err != nil {
			return rows, err
		}
		for _, permissionSetARN := range ps {
			desc, _ := admin.DescribePermissionSet(ctx, &ssoadmin.DescribePermissionSetInput{InstanceArn: aws.String(instanceARN), PermissionSetArn: aws.String(permissionSetARN)})
			psName := permissionSetARN
			if desc != nil && desc.PermissionSet != nil {
				psName = aws.ToString(desc.PermissionSet.Name)
			}
			accounts, err := listAccountsForPermissionSet(ctx, admin, instanceARN, permissionSetARN)
			if err != nil {
				continue
			}
			for _, accountID := range accounts {
				assignments, err := listAccountAssignments(ctx, admin, instanceARN, permissionSetARN, accountID)
				if err != nil {
					continue
				}
				for _, a := range assignments {
					row := SSOPermissionRecord{AccountID: accountID, PermissionSet: psName, PermissionARN: permissionSetARN, PrincipalID: aws.ToString(a.PrincipalId), PrincipalType: string(a.PrincipalType), SourceProfile: profile, IdentityStore: identityStoreID, InstanceARN: instanceARN}
					enrichPrincipal(ctx, store, identityStoreID, &row)
					rows = append(rows, row)
				}
			}
		}
	}
	return rows, nil
}

func listPermissionSets(ctx context.Context, client *ssoadmin.Client, instanceARN string) ([]string, error) {
	var out []string
	var next *string
	for {
		page, err := client.ListPermissionSets(ctx, &ssoadmin.ListPermissionSetsInput{InstanceArn: aws.String(instanceARN), NextToken: next, MaxResults: aws.Int32(100)})
		if err != nil {
			return out, err
		}
		out = append(out, page.PermissionSets...)
		if page.NextToken == nil || aws.ToString(page.NextToken) == "" {
			break
		}
		next = page.NextToken
	}
	return out, nil
}

func listAccountsForPermissionSet(ctx context.Context, client *ssoadmin.Client, instanceARN, permissionSetARN string) ([]string, error) {
	var out []string
	var next *string
	for {
		page, err := client.ListAccountsForProvisionedPermissionSet(ctx, &ssoadmin.ListAccountsForProvisionedPermissionSetInput{InstanceArn: aws.String(instanceARN), PermissionSetArn: aws.String(permissionSetARN), NextToken: next, MaxResults: aws.Int32(100), ProvisioningStatus: ssoadmintypes.ProvisioningStatusLatestPermissionSetProvisioned})
		if err != nil {
			return out, err
		}
		out = append(out, page.AccountIds...)
		if page.NextToken == nil || aws.ToString(page.NextToken) == "" {
			break
		}
		next = page.NextToken
	}
	return out, nil
}

func listAccountAssignments(ctx context.Context, client *ssoadmin.Client, instanceARN, permissionSetARN, accountID string) ([]ssoadmintypes.AccountAssignment, error) {
	var out []ssoadmintypes.AccountAssignment
	var next *string
	for {
		page, err := client.ListAccountAssignments(ctx, &ssoadmin.ListAccountAssignmentsInput{InstanceArn: aws.String(instanceARN), PermissionSetArn: aws.String(permissionSetARN), AccountId: aws.String(accountID), NextToken: next, MaxResults: aws.Int32(100)})
		if err != nil {
			return out, err
		}
		out = append(out, page.AccountAssignments...)
		if page.NextToken == nil || aws.ToString(page.NextToken) == "" {
			break
		}
		next = page.NextToken
	}
	return out, nil
}

func enrichPrincipal(ctx context.Context, store *identitystore.Client, identityStoreID string, row *SSOPermissionRecord) {
	switch row.PrincipalType {
	case string(ssoadmintypes.PrincipalTypeUser):
		user, err := store.DescribeUser(ctx, &identitystore.DescribeUserInput{IdentityStoreId: aws.String(identityStoreID), UserId: aws.String(row.PrincipalID)})
		if err != nil {
			row.Note = err.Error()
			return
		}
		row.UserName = aws.ToString(user.UserName)
		row.DisplayName = aws.ToString(user.DisplayName)
		if len(user.Emails) > 0 {
			row.Email = aws.ToString(user.Emails[0].Value)
		}
	case string(ssoadmintypes.PrincipalTypeGroup):
		group, err := store.DescribeGroup(ctx, &identitystore.DescribeGroupInput{IdentityStoreId: aws.String(identityStoreID), GroupId: aws.String(row.PrincipalID)})
		if err != nil {
			row.Note = err.Error()
			return
		}
		row.GroupName = aws.ToString(group.DisplayName)
	}
}
