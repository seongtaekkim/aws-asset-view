package inventory

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/backup"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"
)

func (c *Collector) collectCloudWatch(ctx context.Context, cfg aws.Config, region string) ([]AssetRecord, []error) {
	var records []AssetRecord
	var errs []error

	cw := cloudwatch.NewFromConfig(cfg)
	ap := cloudwatch.NewDescribeAlarmsPaginator(cw, &cloudwatch.DescribeAlarmsInput{})
	for ap.HasMorePages() {
		page, err := ap.NextPage(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("cloudwatch describe alarms %s: %w", region, err))
			break
		}
		for _, alarm := range page.MetricAlarms {
			name := aws.ToString(alarm.AlarmName)
			rec := NewRecord(c.accountID, region, "cloudwatch", "metric_alarm", name)
			rec.Name = name
			rec.ARN = aws.ToString(alarm.AlarmArn)
			rec.State = string(alarm.StateValue)
			rec.ProductName = "Amazon CloudWatch"
			rec.DetailsJSON = JSONMap(map[string]any{"metric_name": aws.ToString(alarm.MetricName), "namespace": aws.ToString(alarm.Namespace), "statistic": cwStatistic(alarm.Statistic), "comparison_operator": string(alarm.ComparisonOperator), "threshold": alarm.Threshold, "period": alarm.Period, "evaluation_periods": alarm.EvaluationPeriods, "actions_enabled": aws.ToBool(alarm.ActionsEnabled), "alarm_actions": alarm.AlarmActions, "ok_actions": alarm.OKActions})
			rec.TagsJSON = "{}"
			records = append(records, rec)
		}
	}

	logsClient := cloudwatchlogs.NewFromConfig(cfg)
	lp := cloudwatchlogs.NewDescribeLogGroupsPaginator(logsClient, &cloudwatchlogs.DescribeLogGroupsInput{})
	for lp.HasMorePages() {
		page, err := lp.NextPage(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("cloudwatch logs describe log groups %s: %w", region, err))
			break
		}
		for _, lg := range page.LogGroups {
			name := aws.ToString(lg.LogGroupName)
			rec := NewRecord(c.accountID, region, "cloudwatch", "log_group", name)
			rec.Name = name
			rec.ARN = aws.ToString(lg.Arn)
			rec.ProductName = "Amazon CloudWatch Logs"
			if lg.RetentionInDays != nil {
				rec.Retention = strconv.Itoa(int(aws.ToInt32(lg.RetentionInDays))) + " days"
			}
			rec.DetailsJSON = JSONMap(map[string]any{"stored_bytes": lg.StoredBytes, "kms_key_id": aws.ToString(lg.KmsKeyId), "creation_time": lg.CreationTime, "log_group_class": string(lg.LogGroupClass)})
			rec.TagsJSON = "{}"
			records = append(records, rec)
		}
	}
	return records, errs
}

func cwStatistic(stat cwtypes.Statistic) string {
	if stat == "" {
		return ""
	}
	return string(stat)
}

func (c *Collector) collectEFS(ctx context.Context, cfg aws.Config, region string) ([]AssetRecord, []error) {
	client := efs.NewFromConfig(cfg)
	var records []AssetRecord
	var errs []error
	p := efs.NewDescribeFileSystemsPaginator(client, &efs.DescribeFileSystemsInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("efs describe file systems %s: %w", region, err))
			break
		}
		for _, fs := range page.FileSystems {
			id := aws.ToString(fs.FileSystemId)
			rec := NewRecord(c.accountID, region, "efs", "file_system", id)
			rec.Name = aws.ToString(fs.Name)
			rec.ARN = aws.ToString(fs.FileSystemArn)
			rec.State = string(fs.LifeCycleState)
			rec.ProductName = "Amazon EFS"
			rec.Encrypted = boolString(aws.ToBool(fs.Encrypted))
			backupPolicy := c.efsBackupPolicy(ctx, client, id)
			rec.BackupRetention = backupPolicy
			rec.DetailsJSON = JSONMap(map[string]any{"performance_mode": string(fs.PerformanceMode), "throughput_mode": string(fs.ThroughputMode), "provisioned_throughput_mibps": fs.ProvisionedThroughputInMibps, "size_bytes": fs.SizeInBytes, "number_of_mount_targets": fs.NumberOfMountTargets, "lifecycle": c.efsLifecycle(ctx, client, id), "backup_policy": backupPolicy, "mount_targets": c.efsMountTargets(ctx, client, id)})
			rec.TagsJSON = efsTagsJSON(fs.Tags)
			records = append(records, rec)
		}
	}
	return records, errs
}

func (c *Collector) efsLifecycle(ctx context.Context, client *efs.Client, fsID string) any {
	out, err := client.DescribeLifecycleConfiguration(ctx, &efs.DescribeLifecycleConfigurationInput{FileSystemId: aws.String(fsID)})
	if err != nil {
		return "unknown"
	}
	return out.LifecyclePolicies
}

func (c *Collector) efsBackupPolicy(ctx context.Context, client *efs.Client, fsID string) string {
	out, err := client.DescribeBackupPolicy(ctx, &efs.DescribeBackupPolicyInput{FileSystemId: aws.String(fsID)})
	if err != nil || out.BackupPolicy == nil {
		return "unknown"
	}
	return string(out.BackupPolicy.Status)
}

func (c *Collector) efsMountTargets(ctx context.Context, client *efs.Client, fsID string) any {
	p := efs.NewDescribeMountTargetsPaginator(client, &efs.DescribeMountTargetsInput{FileSystemId: aws.String(fsID)})
	var targets []map[string]string
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return targets
		}
		for _, mt := range page.MountTargets {
			targets = append(targets, map[string]string{"mount_target_id": aws.ToString(mt.MountTargetId), "subnet_id": aws.ToString(mt.SubnetId), "vpc_id": aws.ToString(mt.VpcId), "ip_address": aws.ToString(mt.IpAddress), "state": string(mt.LifeCycleState)})
		}
	}
	return targets
}

func efsTagsJSON(tags []efstypes.Tag) string {
	m := map[string]string{}
	for _, t := range tags {
		m[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	return JSONStringMap(m)
}

func (c *Collector) collectBackup(ctx context.Context, cfg aws.Config, region string) ([]AssetRecord, []error) {
	client := backup.NewFromConfig(cfg)
	var records []AssetRecord
	var errs []error

	vaults := backup.NewListBackupVaultsPaginator(client, &backup.ListBackupVaultsInput{})
	for vaults.HasMorePages() {
		page, err := vaults.NextPage(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("backup list vaults %s: %w", region, err))
			break
		}
		for _, vault := range page.BackupVaultList {
			name := aws.ToString(vault.BackupVaultName)
			rec := NewRecord(c.accountID, region, "backup", "backup_vault", name)
			rec.Name = name
			rec.ARN = aws.ToString(vault.BackupVaultArn)
			rec.ProductName = "AWS Backup"
			rec.Encrypted = boolString(aws.ToString(vault.EncryptionKeyArn) != "")
			rec.WORMEnabled = boolString(vault.Locked != nil && aws.ToBool(vault.Locked))
			rec.Retention = backupVaultRetention(vault.MinRetentionDays, vault.MaxRetentionDays)
			rec.DetailsJSON = JSONMap(map[string]any{"recovery_points": vault.NumberOfRecoveryPoints, "encryption_key_arn": aws.ToString(vault.EncryptionKeyArn), "creator_request_id": aws.ToString(vault.CreatorRequestId)})
			records = append(records, rec)
		}
	}

	plans := backup.NewListBackupPlansPaginator(client, &backup.ListBackupPlansInput{})
	for plans.HasMorePages() {
		page, err := plans.NextPage(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("backup list plans %s: %w", region, err))
			break
		}
		for _, plan := range page.BackupPlansList {
			id := aws.ToString(plan.BackupPlanId)
			rec := NewRecord(c.accountID, region, "backup", "backup_plan", id)
			rec.Name = aws.ToString(plan.BackupPlanName)
			rec.ProductName = "AWS Backup"
			rec.Version = aws.ToString(plan.VersionId)
			rec.DetailsJSON = JSONMap(map[string]any{"backup_plan_arn": aws.ToString(plan.BackupPlanArn), "creation_date": plan.CreationDate, "last_execution_date": plan.LastExecutionDate})
			records = append(records, rec)
		}
	}

	protected := backup.NewListProtectedResourcesPaginator(client, &backup.ListProtectedResourcesInput{})
	for protected.HasMorePages() {
		page, err := protected.NextPage(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("backup list protected resources %s: %w", region, err))
			break
		}
		for _, pr := range page.Results {
			arn := aws.ToString(pr.ResourceArn)
			rec := NewRecord(c.accountID, region, "backup", "protected_resource", arn)
			rec.Name = arn
			rec.ARN = arn
			rec.ProductName = "AWS Backup"
			rec.DetailsJSON = JSONMap(map[string]any{"resource_type": aws.ToString(pr.ResourceType), "last_backup_time": pr.LastBackupTime, "last_backup_vault_arn": aws.ToString(pr.LastBackupVaultArn), "last_recovery_point_arn": aws.ToString(pr.LastRecoveryPointArn)})
			records = append(records, rec)
		}
	}

	return records, errs
}

func backupVaultRetention(minDays, maxDays *int64) string {
	parts := []string{}
	if minDays != nil {
		parts = append(parts, fmt.Sprintf("min=%d days", aws.ToInt64(minDays)))
	}
	if maxDays != nil {
		parts = append(parts, fmt.Sprintf("max=%d days", aws.ToInt64(maxDays)))
	}
	return strings.Join(parts, ";")
}
