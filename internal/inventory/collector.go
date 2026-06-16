package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/pricing"
	pricingtypes "github.com/aws/aws-sdk-go-v2/service/pricing/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	route53types "github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/aws-sdk-go-v2/service/wafv2"
	waftypes "github.com/aws/aws-sdk-go-v2/service/wafv2/types"
)

type Options struct {
	Profile       string
	Regions       []string
	Services      map[string]bool
	IncludeGlobal bool
	RDSPricing    bool

	SSOAllAccounts bool
	SSORegion      string
	SSOStartURL    string
	SSORoleName    string
	SSOAccountIDs  []string
}

type Collector struct {
	opts       Options
	accountID  string
	ec2Specs   map[string]InstanceSpec
	rdsSpecs   map[string]InstanceSpec
	pricingCfg aws.Config
}

type InstanceSpec struct {
	VCPU         string
	MemoryMiB    string
	Architecture string
}

func Collect(ctx context.Context, opts Options) ([]AssetRecord, error) {
	opts = normalizeOptions(opts)
	if opts.SSOAllAccounts {
		return collectSSOAccounts(ctx, opts)
	}

	baseCfg, err := loadConfig(ctx, opts.Profile, opts.Regions[0])
	if err != nil {
		return nil, err
	}
	identity, err := sts.NewFromConfig(baseCfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, fmt.Errorf("get caller identity: %w", err)
	}
	return collectWithBaseConfig(ctx, opts, baseCfg, aws.ToString(identity.Account), "")
}

func normalizeOptions(opts Options) Options {
	if len(opts.Regions) == 0 {
		opts.Regions = []string{"ap-northeast-2"}
	}
	if len(opts.Services) == 0 {
		opts.Services = map[string]bool{"all": true}
	}
	return opts
}

func collectWithBaseConfig(ctx context.Context, opts Options, baseCfg aws.Config, accountID, accountName string) ([]AssetRecord, error) {
	c := &Collector{
		opts:      opts,
		accountID: accountID,
		ec2Specs:  make(map[string]InstanceSpec),
		rdsSpecs:  make(map[string]InstanceSpec),
	}

	if opts.RDSPricing {
		pricingCfg := baseCfg
		pricingCfg.Region = "us-east-1"
		c.pricingCfg = pricingCfg
	}

	var records []AssetRecord
	var errs []error
	for _, region := range opts.Regions {
		cfg := baseCfg
		cfg.Region = region
		regionRecords, regionErrs := c.collectRegion(ctx, cfg, region)
		records = append(records, regionRecords...)
		errs = append(errs, regionErrs...)
	}

	if c.enabled("s3") {
		s3Records, s3Errs := c.collectS3(ctx, baseCfg)
		records = append(records, s3Records...)
		errs = append(errs, s3Errs...)
	}

	if opts.IncludeGlobal {
		globalRecords, globalErrs := c.collectGlobal(ctx, baseCfg)
		records = append(records, globalRecords...)
		errs = append(errs, globalErrs...)
	}

	for i := range records {
		records[i].AccountName = accountName
	}
	return records, errors.Join(errs...)
}

func loadConfig(ctx context.Context, profile, region string) (aws.Config, error) {
	options := []func(*awscfg.LoadOptions) error{awscfg.WithRegion(region)}
	if strings.TrimSpace(profile) != "" {
		options = append(options, awscfg.WithSharedConfigProfile(profile))
	}
	return awscfg.LoadDefaultConfig(ctx, options...)
}

func (c *Collector) enabled(service string) bool {
	return c.opts.Services["all"] || c.opts.Services[strings.ToLower(service)]
}

func (c *Collector) collectRegion(ctx context.Context, cfg aws.Config, region string) ([]AssetRecord, []error) {
	var records []AssetRecord
	var errs []error

	if c.enabled("ec2") || c.enabled("vpc") || c.enabled("subnet") || c.enabled("routetable") || c.enabled("sg") || c.enabled("vpn") {
		rs, es := c.collectEC2AndNetwork(ctx, cfg, region)
		records = append(records, rs...)
		errs = append(errs, es...)
	}
	if c.enabled("eks") {
		rs, es := c.collectEKS(ctx, cfg, region)
		records = append(records, rs...)
		errs = append(errs, es...)
	}
	if c.enabled("rds") {
		rs, es := c.collectRDS(ctx, cfg, region)
		records = append(records, rs...)
		errs = append(errs, es...)
	}
	if c.enabled("lb") || c.enabled("elbv2") {
		rs, es := c.collectELBv2(ctx, cfg, region)
		records = append(records, rs...)
		errs = append(errs, es...)
	}
	if c.enabled("waf") {
		rs, es := c.collectWAF(ctx, cfg, region, waftypes.ScopeRegional)
		records = append(records, rs...)
		errs = append(errs, es...)
	}
	if c.enabled("lambda") {
		rs, es := c.collectLambda(ctx, cfg, region)
		records = append(records, rs...)
		errs = append(errs, es...)
	}

	return records, errs
}

func (c *Collector) collectGlobal(ctx context.Context, cfg aws.Config) ([]AssetRecord, []error) {
	cfg.Region = "us-east-1"
	var records []AssetRecord
	var errs []error
	if c.enabled("route53") {
		rs, es := c.collectRoute53(ctx, cfg)
		records = append(records, rs...)
		errs = append(errs, es...)
	}
	if c.enabled("waf") {
		rs, es := c.collectWAF(ctx, cfg, "global", waftypes.ScopeCloudfront)
		records = append(records, rs...)
		errs = append(errs, es...)
	}
	return records, errs
}

func (c *Collector) collectEC2AndNetwork(ctx context.Context, cfg aws.Config, region string) ([]AssetRecord, []error) {
	client := ec2.NewFromConfig(cfg)
	var records []AssetRecord
	var errs []error

	if c.enabled("ec2") {
		p := ec2.NewDescribeInstancesPaginator(client, &ec2.DescribeInstancesInput{})
		for p.HasMorePages() {
			page, err := p.NextPage(ctx)
			if err != nil {
				errs = append(errs, fmt.Errorf("ec2 describe instances %s: %w", region, err))
				break
			}
			for _, res := range page.Reservations {
				for _, inst := range res.Instances {
					id := aws.ToString(inst.InstanceId)
					rec := NewRecord(c.accountID, region, "ec2", "instance", id)
					rec.Name = ec2Name(inst.Tags)
					rec.ARN = arn(region, c.accountID, "ec2", "instance/"+id)
					rec.State = string(inst.State.Name)
					rec.SKU = string(inst.InstanceType)
					rec.VPCID = aws.ToString(inst.VpcId)
					rec.SubnetIDs = aws.ToString(inst.SubnetId)
					rec.SecurityGroupIDs = ec2SGIDs(inst.SecurityGroups)
					rec.PublicAccess = boolString(aws.ToString(inst.PublicIpAddress) != "")
					rec.Architecture = string(inst.Architecture)
					if spec, err := c.ec2Spec(ctx, client, rec.SKU); err == nil {
						rec.VCPU = spec.VCPU
						rec.MemoryMiB = spec.MemoryMiB
						if rec.Architecture == "" {
							rec.Architecture = spec.Architecture
						}
					}
					rec.TagsJSON = ec2TagsJSON(inst.Tags)
					rec.DetailsJSON = JSONMap(map[string]any{
						"private_ip":        aws.ToString(inst.PrivateIpAddress),
						"public_ip":         aws.ToString(inst.PublicIpAddress),
						"image_id":          aws.ToString(inst.ImageId),
						"iam_instance_role": ec2InstanceProfileARN(inst.IamInstanceProfile),
						"launch_time":       inst.LaunchTime,
					})
					records = append(records, rec)
				}
			}
		}
	}

	if c.enabled("vpc") {
		p := ec2.NewDescribeVpcsPaginator(client, &ec2.DescribeVpcsInput{})
		for p.HasMorePages() {
			page, err := p.NextPage(ctx)
			if err != nil {
				errs = append(errs, fmt.Errorf("ec2 describe vpcs %s: %w", region, err))
				break
			}
			for _, vpc := range page.Vpcs {
				id := aws.ToString(vpc.VpcId)
				rec := NewRecord(c.accountID, region, "vpc", "vpc", id)
				rec.Name = ec2Name(vpc.Tags)
				rec.ARN = arn(region, c.accountID, "ec2", "vpc/"+id)
				rec.State = string(vpc.State)
				rec.TagsJSON = ec2TagsJSON(vpc.Tags)
				rec.DetailsJSON = JSONMap(map[string]any{"cidr": aws.ToString(vpc.CidrBlock), "default": aws.ToBool(vpc.IsDefault)})
				records = append(records, rec)
			}
		}
	}

	if c.enabled("subnet") {
		p := ec2.NewDescribeSubnetsPaginator(client, &ec2.DescribeSubnetsInput{})
		for p.HasMorePages() {
			page, err := p.NextPage(ctx)
			if err != nil {
				errs = append(errs, fmt.Errorf("ec2 describe subnets %s: %w", region, err))
				break
			}
			for _, sn := range page.Subnets {
				id := aws.ToString(sn.SubnetId)
				rec := NewRecord(c.accountID, region, "vpc", "subnet", id)
				rec.Name = ec2Name(sn.Tags)
				rec.ARN = arn(region, c.accountID, "ec2", "subnet/"+id)
				rec.State = string(sn.State)
				rec.VPCID = aws.ToString(sn.VpcId)
				rec.PublicAccess = boolString(aws.ToBool(sn.MapPublicIpOnLaunch))
				rec.TagsJSON = ec2TagsJSON(sn.Tags)
				rec.DetailsJSON = JSONMap(map[string]any{
					"cidr":                    aws.ToString(sn.CidrBlock),
					"availability_zone":       aws.ToString(sn.AvailabilityZone),
					"available_ip_count":      sn.AvailableIpAddressCount,
					"map_public_ip_on_launch": aws.ToBool(sn.MapPublicIpOnLaunch),
				})
				records = append(records, rec)
			}
		}
	}

	if c.enabled("routetable") {
		p := ec2.NewDescribeRouteTablesPaginator(client, &ec2.DescribeRouteTablesInput{})
		for p.HasMorePages() {
			page, err := p.NextPage(ctx)
			if err != nil {
				errs = append(errs, fmt.Errorf("ec2 describe route tables %s: %w", region, err))
				break
			}
			for _, rt := range page.RouteTables {
				id := aws.ToString(rt.RouteTableId)
				rec := NewRecord(c.accountID, region, "vpc", "route_table", id)
				rec.Name = ec2Name(rt.Tags)
				rec.ARN = arn(region, c.accountID, "ec2", "route-table/"+id)
				rec.VPCID = aws.ToString(rt.VpcId)
				rec.SubnetIDs = routeTableSubnetIDs(rt.Associations)
				rec.PublicAccess = boolString(routeTableHasIGW(rt.Routes))
				rec.TagsJSON = ec2TagsJSON(rt.Tags)
				rec.DetailsJSON = JSONMap(map[string]any{"routes": routeSummaries(rt.Routes), "main": routeTableMain(rt.Associations)})
				records = append(records, rec)
			}
		}
	}

	if c.enabled("sg") {
		p := ec2.NewDescribeSecurityGroupsPaginator(client, &ec2.DescribeSecurityGroupsInput{})
		for p.HasMorePages() {
			page, err := p.NextPage(ctx)
			if err != nil {
				errs = append(errs, fmt.Errorf("ec2 describe security groups %s: %w", region, err))
				break
			}
			for _, sg := range page.SecurityGroups {
				id := aws.ToString(sg.GroupId)
				rec := NewRecord(c.accountID, region, "vpc", "security_group", id)
				rec.Name = aws.ToString(sg.GroupName)
				rec.ARN = arn(region, c.accountID, "ec2", "security-group/"+id)
				rec.VPCID = aws.ToString(sg.VpcId)
				rec.PublicAccess = boolString(securityGroupPublicIngress(sg.IpPermissions))
				rec.TagsJSON = ec2TagsJSON(sg.Tags)
				rec.DetailsJSON = JSONMap(map[string]any{"description": aws.ToString(sg.Description), "ingress": ipPermissions(sg.IpPermissions), "egress": ipPermissions(sg.IpPermissionsEgress)})
				records = append(records, rec)
			}
		}
	}

	if c.enabled("vpn") {
		page, err := client.DescribeVpnConnections(ctx, &ec2.DescribeVpnConnectionsInput{})
		if err != nil {
			errs = append(errs, fmt.Errorf("ec2 describe vpn connections %s: %w", region, err))
		} else {
			for _, vpn := range page.VpnConnections {
				id := aws.ToString(vpn.VpnConnectionId)
				rec := NewRecord(c.accountID, region, "vpc", "site_to_site_vpn", id)
				rec.Name = ec2Name(vpn.Tags)
				rec.ARN = arn(region, c.accountID, "ec2", "vpn-connection/"+id)
				rec.State = string(vpn.State)
				rec.TagsJSON = ec2TagsJSON(vpn.Tags)
				rec.DetailsJSON = JSONMap(map[string]any{"customer_gateway_id": aws.ToString(vpn.CustomerGatewayId), "vpn_gateway_id": aws.ToString(vpn.VpnGatewayId), "transit_gateway_id": aws.ToString(vpn.TransitGatewayId), "type": string(vpn.Type), "routes": vpnRoutes(vpn.Routes), "tunnels": vpnTunnels(vpn.VgwTelemetry)})
				records = append(records, rec)
			}
		}
	}

	return records, errs
}

func (c *Collector) collectEKS(ctx context.Context, cfg aws.Config, region string) ([]AssetRecord, []error) {
	client := eks.NewFromConfig(cfg)
	ec2Client := ec2.NewFromConfig(cfg)
	var records []AssetRecord
	var errs []error
	p := eks.NewListClustersPaginator(client, &eks.ListClustersInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("eks list clusters %s: %w", region, err))
			break
		}
		for _, clusterName := range page.Clusters {
			out, err := client.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String(clusterName)})
			if err != nil {
				errs = append(errs, fmt.Errorf("eks describe cluster %s/%s: %w", region, clusterName, err))
				continue
			}
			cl := out.Cluster
			if cl != nil {
				rec := NewRecord(c.accountID, region, "eks", "cluster", aws.ToString(cl.Name))
				rec.Name = aws.ToString(cl.Name)
				rec.ARN = aws.ToString(cl.Arn)
				rec.State = string(cl.Status)
				vpc := eksClusterVPC(cl.ResourcesVpcConfig)
				rec.VPCID = vpc.vpcID
				rec.SubnetIDs = Join(vpc.subnetIDs)
				rec.SecurityGroupIDs = Join(vpc.securityGroupIDs)
				rec.PublicAccess = boolString(vpc.endpointPublicAccess)
				rec.TagsJSON = JSONStringMap(cl.Tags)
				rec.DetailsJSON = JSONMap(map[string]any{"version": aws.ToString(cl.Version), "endpoint_private_access": vpc.endpointPrivateAccess, "role_arn": aws.ToString(cl.RoleArn), "logging": cl.Logging})
				records = append(records, rec)
			}

			np := eks.NewListNodegroupsPaginator(client, &eks.ListNodegroupsInput{ClusterName: aws.String(clusterName)})
			for np.HasMorePages() {
				npage, err := np.NextPage(ctx)
				if err != nil {
					errs = append(errs, fmt.Errorf("eks list nodegroups %s/%s: %w", region, clusterName, err))
					break
				}
				for _, ngName := range npage.Nodegroups {
					ngOut, err := client.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{ClusterName: aws.String(clusterName), NodegroupName: aws.String(ngName)})
					if err != nil {
						errs = append(errs, fmt.Errorf("eks describe nodegroup %s/%s/%s: %w", region, clusterName, ngName, err))
						continue
					}
					ng := ngOut.Nodegroup
					if ng == nil {
						continue
					}
					rec := NewRecord(c.accountID, region, "eks", "nodegroup", aws.ToString(ng.NodegroupName))
					rec.Name = aws.ToString(ng.NodegroupName)
					rec.ARN = aws.ToString(ng.NodegroupArn)
					rec.State = string(ng.Status)
					rec.SKU = Join(ng.InstanceTypes)
					rec.SubnetIDs = Join(ng.Subnets)
					rec.TagsJSON = JSONStringMap(ng.Tags)
					if len(ng.InstanceTypes) == 1 {
						if spec, err := c.ec2Spec(ctx, ec2Client, ng.InstanceTypes[0]); err == nil {
							rec.VCPU = spec.VCPU
							rec.MemoryMiB = spec.MemoryMiB
							rec.Architecture = spec.Architecture
						}
					}
					rec.DetailsJSON = JSONMap(map[string]any{"cluster": clusterName, "ami_type": string(ng.AmiType), "capacity_type": string(ng.CapacityType), "disk_size_gib": ng.DiskSize, "scaling": ng.ScalingConfig, "remote_access": ng.RemoteAccess})
					records = append(records, rec)
				}
			}
		}
	}
	return records, errs
}

func (c *Collector) collectRDS(ctx context.Context, cfg aws.Config, region string) ([]AssetRecord, []error) {
	client := rds.NewFromConfig(cfg)
	var records []AssetRecord
	var errs []error
	p := rds.NewDescribeDBInstancesPaginator(client, &rds.DescribeDBInstancesInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("rds describe db instances %s: %w", region, err))
			break
		}
		for _, db := range page.DBInstances {
			id := aws.ToString(db.DBInstanceIdentifier)
			rec := NewRecord(c.accountID, region, "rds", "db_instance", id)
			rec.Name = id
			rec.ARN = aws.ToString(db.DBInstanceArn)
			rec.State = aws.ToString(db.DBInstanceStatus)
			rec.SKU = aws.ToString(db.DBInstanceClass)
			rec.VPCID = rdsVPCID(db.DBSubnetGroup)
			rec.SecurityGroupIDs = rdsVPCSGIDs(db.VpcSecurityGroups)
			rec.PublicAccess = boolString(aws.ToBool(db.PubliclyAccessible))
			rec.Encrypted = boolString(aws.ToBool(db.StorageEncrypted))
			if spec, err := c.rdsSpec(ctx, region, rec.SKU); err == nil {
				rec.VCPU = spec.VCPU
				rec.MemoryMiB = spec.MemoryMiB
			}
			rec.TagsJSON = "{}"
			rec.DetailsJSON = JSONMap(map[string]any{"engine": aws.ToString(db.Engine), "engine_version": aws.ToString(db.EngineVersion), "endpoint": rdsEndpoint(db.Endpoint), "storage_type": aws.ToString(db.StorageType), "allocated_storage_gib": db.AllocatedStorage, "multi_az": aws.ToBool(db.MultiAZ), "backup_retention_days": db.BackupRetentionPeriod, "deletion_protection": aws.ToBool(db.DeletionProtection)})
			records = append(records, rec)
		}
	}
	return records, errs
}

func (c *Collector) collectS3(ctx context.Context, cfg aws.Config) ([]AssetRecord, []error) {
	client := s3.NewFromConfig(cfg)
	out, err := client.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		return nil, []error{fmt.Errorf("s3 list buckets: %w", err)}
	}
	var records []AssetRecord
	var errs []error
	for _, b := range out.Buckets {
		name := aws.ToString(b.Name)
		bucketRegion := c.bucketRegion(ctx, client, name)
		rec := NewRecord(c.accountID, bucketRegion, "s3", "bucket", name)
		rec.Name = name
		rec.ARN = "arn:aws:s3:::" + name
		rec.PublicAccess = c.s3PublicAccess(ctx, client, name)
		rec.Encrypted = c.s3Encrypted(ctx, client, name)
		rec.DetailsJSON = JSONMap(map[string]any{"creation_date": b.CreationDate, "versioning": c.s3Versioning(ctx, client, name)})
		rec.TagsJSON = "{}"
		records = append(records, rec)
	}
	return records, errs
}

func (c *Collector) collectELBv2(ctx context.Context, cfg aws.Config, region string) ([]AssetRecord, []error) {
	client := elasticloadbalancingv2.NewFromConfig(cfg)
	var records []AssetRecord
	var errs []error
	p := elasticloadbalancingv2.NewDescribeLoadBalancersPaginator(client, &elasticloadbalancingv2.DescribeLoadBalancersInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("elbv2 describe load balancers %s: %w", region, err))
			break
		}
		for _, lb := range page.LoadBalancers {
			name := aws.ToString(lb.LoadBalancerName)
			rec := NewRecord(c.accountID, region, "lb", "load_balancer", name)
			rec.Name = name
			rec.ARN = aws.ToString(lb.LoadBalancerArn)
			rec.State = lbState(lb.State)
			rec.SKU = string(lb.Type)
			rec.VPCID = aws.ToString(lb.VpcId)
			rec.SubnetIDs = lbSubnetIDs(lb.AvailabilityZones)
			rec.SecurityGroupIDs = Join(lb.SecurityGroups)
			rec.PublicAccess = boolString(lb.Scheme == elbtypes.LoadBalancerSchemeEnumInternetFacing)
			rec.DetailsJSON = JSONMap(map[string]any{"dns_name": aws.ToString(lb.DNSName), "ip_address_type": string(lb.IpAddressType), "created_time": lb.CreatedTime})
			rec.TagsJSON = "{}"
			records = append(records, rec)
		}
	}
	return records, errs
}

func (c *Collector) collectRoute53(ctx context.Context, cfg aws.Config) ([]AssetRecord, []error) {
	client := route53.NewFromConfig(cfg)
	var records []AssetRecord
	var errs []error
	p := route53.NewListHostedZonesPaginator(client, &route53.ListHostedZonesInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("route53 list hosted zones: %w", err))
			break
		}
		for _, z := range page.HostedZones {
			zoneID := strings.TrimPrefix(aws.ToString(z.Id), "/hostedzone/")
			rec := NewRecord(c.accountID, "global", "route53", "hosted_zone", zoneID)
			rec.Name = strings.TrimSuffix(aws.ToString(z.Name), ".")
			rec.ARN = "arn:aws:route53:::hostedzone/" + zoneID
			zoneCfg := hostedZoneConfig(z.Config)
			rec.PublicAccess = boolString(!zoneCfg.privateZone)
			rec.DetailsJSON = JSONMap(map[string]any{"private_zone": zoneCfg.privateZone, "record_count": aws.ToInt64(z.ResourceRecordSetCount), "comment": zoneCfg.comment})
			rec.TagsJSON = "{}"
			records = append(records, rec)

			rp := route53.NewListResourceRecordSetsPaginator(client, &route53.ListResourceRecordSetsInput{HostedZoneId: z.Id})
			for rp.HasMorePages() {
				rpage, err := rp.NextPage(ctx)
				if err != nil {
					errs = append(errs, fmt.Errorf("route53 list records %s: %w", zoneID, err))
					break
				}
				for _, rr := range rpage.ResourceRecordSets {
					rid := zoneID + ":" + aws.ToString(rr.Name) + ":" + string(rr.Type)
					rec := NewRecord(c.accountID, "global", "route53", "record", rid)
					rec.Name = strings.TrimSuffix(aws.ToString(rr.Name), ".")
					rec.SKU = string(rr.Type)
					rec.DetailsJSON = JSONMap(map[string]any{"hosted_zone_id": zoneID, "ttl": rr.TTL, "alias_target": rr.AliasTarget, "records": rr.ResourceRecords})
					rec.TagsJSON = "{}"
					records = append(records, rec)
				}
			}
		}
	}
	return records, errs
}

func (c *Collector) collectWAF(ctx context.Context, cfg aws.Config, region string, scope waftypes.Scope) ([]AssetRecord, []error) {
	client := wafv2.NewFromConfig(cfg)
	var records []AssetRecord
	var errs []error
	var nextMarker *string
	for {
		page, err := client.ListWebACLs(ctx, &wafv2.ListWebACLsInput{Scope: scope, NextMarker: nextMarker, Limit: aws.Int32(100)})
		if err != nil {
			errs = append(errs, fmt.Errorf("waf list web acls %s/%s: %w", region, scope, err))
			break
		}
		for _, acl := range page.WebACLs {
			rec := NewRecord(c.accountID, region, "waf", "web_acl", aws.ToString(acl.Id))
			rec.Name = aws.ToString(acl.Name)
			rec.ARN = aws.ToString(acl.ARN)
			rec.SKU = string(scope)
			get, err := client.GetWebACL(ctx, &wafv2.GetWebACLInput{Id: acl.Id, Name: acl.Name, Scope: scope})
			if err == nil && get.WebACL != nil {
				rec.DetailsJSON = JSONMap(map[string]any{"default_action": get.WebACL.DefaultAction, "rules": get.WebACL.Rules, "capacity": get.WebACL.Capacity})
			} else {
				rec.DetailsJSON = JSONMap(map[string]any{"description": aws.ToString(acl.Description), "lock_token": aws.ToString(acl.LockToken)})
			}
			rec.TagsJSON = "{}"
			records = append(records, rec)
		}
		if page.NextMarker == nil || aws.ToString(page.NextMarker) == "" {
			break
		}
		nextMarker = page.NextMarker
	}
	return records, errs
}

func (c *Collector) collectLambda(ctx context.Context, cfg aws.Config, region string) ([]AssetRecord, []error) {
	client := lambda.NewFromConfig(cfg)
	var records []AssetRecord
	var errs []error
	p := lambda.NewListFunctionsPaginator(client, &lambda.ListFunctionsInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("lambda list functions %s: %w", region, err))
			break
		}
		for _, fn := range page.Functions {
			name := aws.ToString(fn.FunctionName)
			rec := NewRecord(c.accountID, region, "lambda", "function", name)
			rec.Name = name
			rec.ARN = aws.ToString(fn.FunctionArn)
			rec.State = string(fn.State)
			rec.SKU = string(fn.Runtime)
			rec.MemoryMiB = strconv.Itoa(int(aws.ToInt32(fn.MemorySize)))
			rec.Architecture = lambdaArchitectures(fn.Architectures)
			vpc := lambdaVPC(fn.VpcConfig)
			rec.VPCID = vpc.vpcID
			rec.SubnetIDs = Join(vpc.subnetIDs)
			rec.SecurityGroupIDs = Join(vpc.securityGroupIDs)
			rec.DetailsJSON = JSONMap(map[string]any{"timeout_seconds": fn.Timeout, "package_type": string(fn.PackageType), "role": aws.ToString(fn.Role), "last_modified": aws.ToString(fn.LastModified), "ephemeral_storage_mib": lambdaEphemeralStorageMiB(fn.EphemeralStorage), "cpu_note": "Lambda CPU is allocated proportionally to configured memory; AWS does not expose a fixed vCPU SKU."})
			rec.TagsJSON = "{}"
			records = append(records, rec)
		}
	}
	return records, errs
}

func (c *Collector) ec2Spec(ctx context.Context, client *ec2.Client, instanceType string) (InstanceSpec, error) {
	if instanceType == "" {
		return InstanceSpec{}, fmt.Errorf("empty instance type")
	}
	if spec, ok := c.ec2Specs[instanceType]; ok {
		return spec, nil
	}
	out, err := client.DescribeInstanceTypes(ctx, &ec2.DescribeInstanceTypesInput{InstanceTypes: []ec2types.InstanceType{ec2types.InstanceType(instanceType)}})
	if err != nil || len(out.InstanceTypes) == 0 {
		return InstanceSpec{}, err
	}
	it := out.InstanceTypes[0]
	spec := ec2InstanceTypeSpec(it)
	c.ec2Specs[instanceType] = spec
	return spec, nil
}

func (c *Collector) rdsSpec(ctx context.Context, region, instanceClass string) (InstanceSpec, error) {
	if !c.opts.RDSPricing || c.pricingCfg.Region == "" || instanceClass == "" {
		return InstanceSpec{}, fmt.Errorf("rds pricing disabled")
	}
	if spec, ok := c.rdsSpecs[instanceClass]; ok {
		return spec, nil
	}
	client := pricing.NewFromConfig(c.pricingCfg)
	out, err := client.GetProducts(ctx, &pricing.GetProductsInput{
		ServiceCode: aws.String("AmazonRDS"),
		MaxResults:  aws.Int32(10),
		Filters: []pricingtypes.Filter{
			{Type: pricingtypes.FilterTypeTermMatch, Field: aws.String("instanceType"), Value: aws.String(instanceClass)},
			{Type: pricingtypes.FilterTypeTermMatch, Field: aws.String("location"), Value: aws.String(pricingLocation(region))},
		},
	})
	if err != nil {
		return InstanceSpec{}, err
	}
	for _, raw := range out.PriceList {
		var product struct {
			Product struct {
				Attributes map[string]string `json:"attributes"`
			} `json:"product"`
		}
		if json.Unmarshal([]byte(raw), &product) != nil {
			continue
		}
		attrs := product.Product.Attributes
		if attrs["instanceType"] != instanceClass {
			continue
		}
		spec := InstanceSpec{VCPU: attrs["vcpu"], MemoryMiB: memoryToMiB(attrs["memory"])}
		c.rdsSpecs[instanceClass] = spec
		return spec, nil
	}
	return InstanceSpec{}, fmt.Errorf("rds class not found in pricing: %s", instanceClass)
}

func pricingLocation(region string) string {
	switch region {
	case "ap-northeast-2":
		return "Asia Pacific (Seoul)"
	default:
		return region
	}
}

func memoryToMiB(memory string) string {
	fields := strings.Fields(strings.ReplaceAll(memory, ",", ""))
	if len(fields) == 0 {
		return ""
	}
	gb, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return ""
	}
	return strconv.Itoa(int(gb * 1024))
}

func arn(region, accountID, service, resource string) string {
	return fmt.Sprintf("arn:aws:%s:%s:%s:%s", service, region, accountID, resource)
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func ec2Name(tags []ec2types.Tag) string {
	for _, t := range tags {
		if aws.ToString(t.Key) == "Name" {
			return aws.ToString(t.Value)
		}
	}
	return ""
}

func ec2TagsJSON(tags []ec2types.Tag) string {
	m := map[string]string{}
	for _, t := range tags {
		m[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	return JSONStringMap(m)
}

func ec2SGIDs(groups []ec2types.GroupIdentifier) string {
	ids := make([]string, 0, len(groups))
	for _, g := range groups {
		ids = append(ids, aws.ToString(g.GroupId))
	}
	return Join(ids)
}

func routeTableSubnetIDs(assocs []ec2types.RouteTableAssociation) string {
	ids := make([]string, 0, len(assocs))
	for _, a := range assocs {
		ids = append(ids, aws.ToString(a.SubnetId))
	}
	return Join(ids)
}

func routeTableMain(assocs []ec2types.RouteTableAssociation) bool {
	for _, a := range assocs {
		if aws.ToBool(a.Main) {
			return true
		}
	}
	return false
}

func routeTableHasIGW(routes []ec2types.Route) bool {
	for _, r := range routes {
		if aws.ToString(r.DestinationCidrBlock) == "0.0.0.0/0" && strings.HasPrefix(aws.ToString(r.GatewayId), "igw-") {
			return true
		}
	}
	return false
}

func routeSummaries(routes []ec2types.Route) []map[string]string {
	items := make([]map[string]string, 0, len(routes))
	for _, r := range routes {
		items = append(items, map[string]string{"destination": firstNonEmpty(aws.ToString(r.DestinationCidrBlock), aws.ToString(r.DestinationIpv6CidrBlock), aws.ToString(r.DestinationPrefixListId)), "target": firstNonEmpty(aws.ToString(r.GatewayId), aws.ToString(r.NatGatewayId), aws.ToString(r.TransitGatewayId), aws.ToString(r.VpcPeeringConnectionId), aws.ToString(r.NetworkInterfaceId)), "state": string(r.State)})
	}
	return items
}

func securityGroupPublicIngress(perms []ec2types.IpPermission) bool {
	for _, p := range perms {
		for _, r := range p.IpRanges {
			if aws.ToString(r.CidrIp) == "0.0.0.0/0" {
				return true
			}
		}
		for _, r := range p.Ipv6Ranges {
			if aws.ToString(r.CidrIpv6) == "::/0" {
				return true
			}
		}
	}
	return false
}

func ipPermissions(perms []ec2types.IpPermission) []map[string]any {
	items := make([]map[string]any, 0, len(perms))
	for _, p := range perms {
		items = append(items, map[string]any{"protocol": aws.ToString(p.IpProtocol), "from_port": p.FromPort, "to_port": p.ToPort, "ipv4": p.IpRanges, "ipv6": p.Ipv6Ranges, "sg_refs": p.UserIdGroupPairs})
	}
	return items
}

func vpnRoutes(routes []ec2types.VpnStaticRoute) []map[string]string {
	items := make([]map[string]string, 0, len(routes))
	for _, r := range routes {
		items = append(items, map[string]string{"destination": aws.ToString(r.DestinationCidrBlock), "state": string(r.State)})
	}
	return items
}

func vpnTunnels(tunnels []ec2types.VgwTelemetry) []map[string]string {
	items := make([]map[string]string, 0, len(tunnels))
	for _, t := range tunnels {
		items = append(items, map[string]string{"outside_ip": aws.ToString(t.OutsideIpAddress), "status": string(t.Status), "message": aws.ToString(t.StatusMessage)})
	}
	return items
}

func ec2InstanceTypeSpec(it ec2types.InstanceTypeInfo) InstanceSpec {
	spec := InstanceSpec{}
	if it.VCpuInfo != nil {
		spec.VCPU = strconv.Itoa(int(aws.ToInt32(it.VCpuInfo.DefaultVCpus)))
	}
	if it.MemoryInfo != nil {
		spec.MemoryMiB = strconv.FormatInt(aws.ToInt64(it.MemoryInfo.SizeInMiB), 10)
	}
	if it.ProcessorInfo != nil && len(it.ProcessorInfo.SupportedArchitectures) > 0 {
		spec.Architecture = string(it.ProcessorInfo.SupportedArchitectures[0])
	}
	return spec
}

func ec2InstanceProfileARN(profile *ec2types.IamInstanceProfile) string {
	if profile == nil {
		return ""
	}
	return aws.ToString(profile.Arn)
}

type eksVPCInfo struct {
	vpcID                 string
	subnetIDs             []string
	securityGroupIDs      []string
	endpointPublicAccess  bool
	endpointPrivateAccess bool
}

func eksClusterVPC(vpc *ekstypes.VpcConfigResponse) eksVPCInfo {
	if vpc == nil {
		return eksVPCInfo{}
	}
	securityGroups := append([]string{}, vpc.SecurityGroupIds...)
	if aws.ToString(vpc.ClusterSecurityGroupId) != "" {
		securityGroups = append(securityGroups, aws.ToString(vpc.ClusterSecurityGroupId))
	}
	return eksVPCInfo{
		vpcID:                 aws.ToString(vpc.VpcId),
		subnetIDs:             vpc.SubnetIds,
		securityGroupIDs:      securityGroups,
		endpointPublicAccess:  vpc.EndpointPublicAccess,
		endpointPrivateAccess: vpc.EndpointPrivateAccess,
	}
}

func rdsVPCID(group *rdstypes.DBSubnetGroup) string {
	if group == nil {
		return ""
	}
	return aws.ToString(group.VpcId)
}

func rdsVPCSGIDs(groups []rdstypes.VpcSecurityGroupMembership) string {
	ids := make([]string, 0, len(groups))
	for _, g := range groups {
		ids = append(ids, aws.ToString(g.VpcSecurityGroupId))
	}
	return Join(ids)
}

func rdsEndpoint(ep *rdstypes.Endpoint) map[string]any {
	if ep == nil {
		return map[string]any{}
	}
	return map[string]any{"address": aws.ToString(ep.Address), "port": ep.Port, "hosted_zone_id": aws.ToString(ep.HostedZoneId)}
}

type lambdaVPCInfo struct {
	vpcID            string
	subnetIDs        []string
	securityGroupIDs []string
}

func lambdaVPC(vpc *lambdatypes.VpcConfigResponse) lambdaVPCInfo {
	if vpc == nil {
		return lambdaVPCInfo{}
	}
	return lambdaVPCInfo{vpcID: aws.ToString(vpc.VpcId), subnetIDs: vpc.SubnetIds, securityGroupIDs: vpc.SecurityGroupIds}
}

type hostedZoneConfigInfo struct {
	privateZone bool
	comment     string
}

func lbState(state *elbtypes.LoadBalancerState) string {
	if state == nil {
		return ""
	}
	return string(state.Code)
}

func hostedZoneConfig(cfg *route53types.HostedZoneConfig) hostedZoneConfigInfo {
	if cfg == nil {
		return hostedZoneConfigInfo{}
	}
	return hostedZoneConfigInfo{privateZone: cfg.PrivateZone, comment: aws.ToString(cfg.Comment)}
}

func lbSubnetIDs(zones []elbtypes.AvailabilityZone) string {
	ids := make([]string, 0, len(zones))
	for _, z := range zones {
		ids = append(ids, aws.ToString(z.SubnetId))
	}
	return Join(ids)
}

func lambdaEphemeralStorageMiB(storage *lambdatypes.EphemeralStorage) string {
	if storage == nil {
		return ""
	}
	return strconv.Itoa(int(aws.ToInt32(storage.Size)))
}

func lambdaArchitectures(archs []lambdatypes.Architecture) string {
	values := make([]string, 0, len(archs))
	for _, a := range archs {
		values = append(values, string(a))
	}
	return Join(values)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (c *Collector) bucketRegion(ctx context.Context, client *s3.Client, bucket string) string {
	out, err := client.GetBucketLocation(ctx, &s3.GetBucketLocationInput{Bucket: aws.String(bucket)})
	if err != nil {
		return "unknown"
	}
	if out.LocationConstraint == "" {
		return "us-east-1"
	}
	if out.LocationConstraint == s3types.BucketLocationConstraint("EU") {
		return "eu-west-1"
	}
	return string(out.LocationConstraint)
}

func (c *Collector) s3Encrypted(ctx context.Context, client *s3.Client, bucket string) string {
	_, err := client.GetBucketEncryption(ctx, &s3.GetBucketEncryptionInput{Bucket: aws.String(bucket)})
	return boolString(err == nil)
}

func (c *Collector) s3Versioning(ctx context.Context, client *s3.Client, bucket string) string {
	out, err := client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(bucket)})
	if err != nil {
		return "unknown"
	}
	return string(out.Status)
}

func (c *Collector) s3PublicAccess(ctx context.Context, client *s3.Client, bucket string) string {
	out, err := client.GetPublicAccessBlock(ctx, &s3.GetPublicAccessBlockInput{Bucket: aws.String(bucket)})
	if err != nil || out.PublicAccessBlockConfiguration == nil {
		return "unknown"
	}
	cfg := out.PublicAccessBlockConfiguration
	blocked := aws.ToBool(cfg.BlockPublicAcls) && aws.ToBool(cfg.BlockPublicPolicy) && aws.ToBool(cfg.IgnorePublicAcls) && aws.ToBool(cfg.RestrictPublicBuckets)
	return boolString(!blocked)
}
