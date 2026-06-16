package inventory

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// AssetRecord is the normalized row written to CSV.
// Service-specific fields that do not fit common columns are kept in DetailsJSON.
type AssetRecord struct {
	CollectedAt      string
	AccountID        string
	AccountName      string
	Region           string
	Service          string
	ResourceType     string
	ResourceID       string
	Name             string
	ARN              string
	State            string
	SKU              string
	VCPU             string
	MemoryMiB        string
	Architecture     string
	VPCID            string
	SubnetIDs        string
	SecurityGroupIDs string
	PublicAccess     string
	Encrypted        string
	DetailsJSON      string
	TagsJSON         string
}

func NewRecord(accountID, region, service, resourceType, id string) AssetRecord {
	return AssetRecord{
		CollectedAt:  time.Now().UTC().Format(time.RFC3339),
		AccountID:    accountID,
		Region:       region,
		Service:      service,
		ResourceType: resourceType,
		ResourceID:   id,
	}
}

func csvHeader() []string {
	return []string{
		"collected_at",
		"account_id",
		"account_name",
		"region",
		"service",
		"resource_type",
		"resource_id",
		"name",
		"arn",
		"state",
		"sku",
		"vcpu",
		"memory_mib",
		"architecture",
		"vpc_id",
		"subnet_ids",
		"security_group_ids",
		"public_access",
		"encrypted",
		"details_json",
		"tags_json",
	}
}

func (r AssetRecord) csvRow() []string {
	return []string{
		r.CollectedAt,
		r.AccountID,
		r.AccountName,
		r.Region,
		r.Service,
		r.ResourceType,
		r.ResourceID,
		r.Name,
		r.ARN,
		r.State,
		r.SKU,
		r.VCPU,
		r.MemoryMiB,
		r.Architecture,
		r.VPCID,
		r.SubnetIDs,
		r.SecurityGroupIDs,
		r.PublicAccess,
		r.Encrypted,
		r.DetailsJSON,
		r.TagsJSON,
	}
}

func Join(values []string) string {
	clean := make([]string, 0, len(values))
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			clean = append(clean, v)
		}
	}
	sort.Strings(clean)
	return strings.Join(clean, ";")
}

func JSONMap(values map[string]any) string {
	if len(values) == 0 {
		return "{}"
	}
	b, err := json.Marshal(values)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func JSONStringMap(values map[string]string) string {
	if len(values) == 0 {
		return "{}"
	}
	b, err := json.Marshal(values)
	if err != nil {
		return "{}"
	}
	return string(b)
}
