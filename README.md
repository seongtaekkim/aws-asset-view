# aws-asset-view

AWS Config를 사용하지 않고 AWS SDK API를 직접 호출해 자산관리대장용 CSV를 생성하는 가벼운 CLI입니다.

기본 대상 리전은 한국 서울 리전(`ap-northeast-2`)입니다.

## 왜 저장소가 없어도 되는가?

일회성 또는 주기적 스냅샷 CSV 산출만 목적이면 별도 DB가 필요 없습니다.

```text
AWS API 직접 조회 → 정규화된 row 생성 → CSV 파일 출력
```

SQLite/PostgreSQL 같은 저장소가 필요한 경우는 다음 단계입니다.

- 전일/전월 대비 변경 이력 추적
- 웹 UI 검색/필터 제공
- 대량 멀티 계정 수집 결과 누적
- 감사 증적 보관
- 비용/보안 finding 추세 분석

그래서 이 MVP는 저장소 없이 CSV만 출력합니다. 나중에 이력이 필요해지면 SQLite를 선택 옵션으로 붙이는 방식이 가장 가볍습니다.

## 설치/빌드

Go 1.22+가 필요합니다.

```bash
go mod download
go build -o aws-asset-view ./cmd/aws-asset-view
```

## 사용 예시

현재 AWS 인증 컨텍스트로 서울 리전 자산을 수집합니다.

```bash
./aws-asset-view --regions ap-northeast-2 --output assets.csv
```

여러 탭이 있는 Excel 대장으로 출력하려면 `.xlsx` 확장자를 사용합니다.

```bash
./aws-asset-view --regions ap-northeast-2 --output assets.xlsx
```

XLSX 출력 시 시트는 다음처럼 생성됩니다.

1. `assets`: 전체 자산 대장
2. `security_group_rules`: 보안그룹 inbound/outbound 룰 상세
3. `sso_permissions`: SSO 사용자/그룹별 Permission Set 할당 현황

## 웹 조회 모드

기존 CSV/XLSX 출력 기능은 그대로 유지하면서 웹 UI로도 조회할 수 있습니다. 웹 모드는 로컬 SQLite 파일에 수집 snapshot을 저장하고 브라우저에서 테이블 조회, 필터링, XLSX 다운로드를 제공합니다.

```bash
./aws-asset-view \
  --serve \
  --profiles dev,staging,prod,security \
  --sso-admin-profile management \
  --regions ap-northeast-2 \
  --db asset-view.db \
  --addr 127.0.0.1:8080
```

접속:

```text
http://127.0.0.1:8080
```

웹 메뉴:

- `Assets`: 전체 자산 테이블 조회
- `Security groups`: 보안그룹 inbound/outbound 룰 조회
- `SSO permissions`: SSO 사용자/그룹 권한 조회
- `Refresh inventory`: 현재 실행 옵션으로 새 snapshot 수집
- `Download XLSX`: 현재 최신 snapshot을 Excel로 다운로드

이미 저장된 DB만 보고 싶고 시작 시 AWS 수집을 하지 않으려면 `--profile`, `--profiles`, `--sso-all-accounts` 없이 실행합니다.

```bash
./aws-asset-view --serve --db asset-view.db --addr 127.0.0.1:8080
```

특정 프로파일 사용:

```bash
./aws-asset-view --profile prod --regions ap-northeast-2 --output prod-assets.csv
```

계정마다 SSO role / permission set이 달라서 AWS CLI profile을 여러 개 만들어 둔 경우:

```bash
./aws-asset-view \
  --profiles dev,staging,prod,security \
  --regions ap-northeast-2 \
  --output assets.xlsx
```

이 모드는 각 profile의 `sso_account_id`, `sso_role_name`, `sso_session` 설정을 그대로 사용합니다. 즉 계정별 role 이름이 달라도 `~/.aws/config`에 profile만 정확히 잡혀 있으면 됩니다.

일부 서비스만 수집:

```bash
./aws-asset-view \
  --profile prod \
  --regions ap-northeast-2 \
  --services ec2,eks,rds,s3,efs,backup,cloudwatch,lb,vpc,subnet,routetable,sg,vpn,flowlog,waf,lambda,route53 \
  --output assets.xlsx
```

표준출력으로 출력:

```bash
./aws-asset-view --output -
```

RDS vCPU/Memory까지 AWS Pricing API로 채우기:

```bash
./aws-asset-view --rds-pricing --output assets.csv
```

> `--rds-pricing`은 `pricing:GetProducts` 권한이 필요하고 수집 시간이 늘어날 수 있습니다. EC2/EKS 노드그룹의 vCPU/Memory는 EC2 `DescribeInstanceTypes`로 조회합니다.

## AWS SSO 전체 계정 수집

먼저 AWS CLI로 SSO 로그인을 수행합니다.

```bash
aws sso login --profile your-sso-profile
```

그 다음 SSO에서 접근 가능한 모든 계정을 순회해 CSV 또는 XLSX를 생성합니다.

```bash
./aws-asset-view \
  --profile your-sso-profile \
  --sso-all-accounts \
  --sso-region ap-northeast-2 \
  --sso-role-name ReadOnlyAccess \
  --regions ap-northeast-2 \
  --output all-accounts-assets.xlsx
```

특정 계정만 포함하려면:

```bash
./aws-asset-view \
  --profile your-sso-profile \
  --sso-all-accounts \
  --sso-region ap-northeast-2 \
  --sso-role-name ReadOnlyAccess \
  --sso-account-ids 111122223333,444455556666 \
  --output selected-accounts-assets.csv
```

동작 방식:

1. `~/.aws/sso/cache/*.json`에서 유효한 SSO access token을 찾습니다.
2. `sso:ListAccounts`로 접근 가능한 계정 목록을 가져옵니다.
3. 계정별로 `sso:ListAccountRoles` / `sso:GetRoleCredentials`를 호출합니다.
4. 받은 임시 자격증명으로 각 계정의 AWS API를 직접 조회합니다.
5. 결과를 하나의 CSV 또는 XLSX에 합칩니다.

`--sso-role-name`을 생략하면 계정별 사용 가능한 role 이름 중 정렬상 첫 번째 role을 사용합니다. 운영에서는 Permission Set 이름이 계정마다 동일하도록 맞추고 `--sso-role-name`을 지정하는 것을 권장합니다.

## 자산 시트 / CSV 컬럼

| 컬럼 | 설명 |
|---|---|
| collected_at | 수집 시각 UTC |
| account_id | AWS Account ID |
| account_name | AWS SSO 계정 이름. profile 순회 모드에서는 profile 이름 |
| profile | 수집에 사용한 AWS CLI profile |
| region | 리전. Route53 등 글로벌 리소스는 `global` |
| service | ec2, eks, rds, s3, lb, route53, vpc, waf, lambda 등 |
| resource_type | instance, cluster, nodegroup, db_instance, bucket 등 |
| resource_id | 서비스별 식별자 |
| name | Name 태그 또는 리소스 이름 |
| arn | ARN |
| state | 리소스 상태 |
| product_name | 제품명/엔진명. 예: Amazon EKS, mysql, aurora-postgresql |
| version | DB engine version, EKS version, backup plan version 등 |
| sku | EC2 instance type, RDS class, Lambda runtime, LB type 등 |
| vcpu | 조회 가능한 컴퓨팅 자원의 vCPU |
| memory_mib | 조회 가능한 컴퓨팅 자원의 메모리 MiB |
| architecture | x86_64, arm64 등 |
| vpc_id | 연결 VPC |
| subnet_ids | 세미콜론 구분 subnet 목록 |
| security_group_ids | 세미콜론 구분 SG 목록 |
| public_access | 인터넷 노출 또는 public 설정 추정 |
| encrypted | 암호화 여부 |
| worm_enabled | S3 Object Lock 또는 AWS Backup Vault Lock 등 WORM성 설정 여부 |
| retention | Object Lock / Backup Vault / CloudWatch Logs 보관 기간 |
| backup_retention | RDS backup retention, EFS backup policy 등 백업 보관/활성 정보 |
| details_json | 서비스별 상세 정보 JSON |
| tags_json | 태그 JSON |

## 현재 수집 대상

- EC2 Instance
- EC2 Instance Type 스펙(vCPU/Memory)
- VPC
- Subnet
- Route Table
- Security Group
- Site-to-Site VPN
- VPC Flow Logs
- EKS Cluster
- EKS Managed Node Group
- RDS DB Instance
- RDS DB Cluster
- S3 Bucket / Object Lock(WORM) / Lifecycle
- EFS File System / Lifecycle / Backup Policy
- AWS Backup Vault / Plan / Protected Resource
- CloudWatch Alarm / Log Group
- ALB/NLB/ELBv2 Load Balancer
- Route53 Hosted Zone / Record
- WAFv2 Web ACL, Regional/CloudFront scope
- Lambda Function

## Security Group Rules 시트

XLSX 출력에서는 `security_group_rules` 시트가 추가됩니다. 보안그룹 룰을 다음 컬럼으로 펼쳐서 보여줍니다.

| 컬럼 | 설명 |
|---|---|
| security_group_id | 보안그룹 ID |
| security_group_name | 보안그룹명 |
| direction | inbound / outbound |
| priority | AWS Security Group은 우선순위 개념이 없어 비워둠 |
| rule_name | 룰 description을 룰 이름처럼 표시 |
| port | 포트 또는 포트 범위 |
| protocol | tcp/udp/icmp/all 등 |
| source | inbound source CIDR/SG |
| destination | outbound destination CIDR/SG |
| access | Security Group은 allow rule만 있으므로 `allow` |
| note | description 등 비고 |

## SSO Permissions 시트

XLSX 출력에서는 기본적으로 `sso_permissions` 시트 생성을 시도합니다. 이 시트는 SSO Admin / Identity Store API 권한이 있는 profile에서만 채워집니다.

필요 권한이 별도 관리 계정 profile에 있다면:

```bash
./aws-asset-view \
  --profiles dev,staging,prod \
  --sso-admin-profile management \
  --include-sso-permissions \
  --regions ap-northeast-2 \
  --output assets.xlsx
```

권한이 없으면 자산 수집은 계속 진행되고, SSO 권한 시트만 비거나 오류가 stderr에 표시될 수 있습니다.

## 필요한 대표 권한

읽기 전용 권한 기준으로 다음 액션이 필요합니다. 실제 운영에서는 서비스별 `Describe*`, `List*`, `Get*` 권한을 최소 권한으로 조정하세요.

- `sts:GetCallerIdentity`
- `sso:ListAccounts`, `sso:ListAccountRoles`, `sso:GetRoleCredentials` (`--sso-all-accounts` 사용 시)
- `ec2:Describe*`
- `eks:List*`, `eks:Describe*`
- `rds:Describe*`, `rds:ListTagsForResource` 선택
- `s3:ListAllMyBuckets`, `s3:GetBucketLocation`, `s3:GetBucketEncryption`, `s3:GetBucketVersioning`, `s3:GetBucketPublicAccessBlock`, `s3:GetObjectLockConfiguration`, `s3:GetLifecycleConfiguration`
- `efs:Describe*`
- `backup:List*`, `backup:Describe*`
- `cloudwatch:DescribeAlarms`, `logs:DescribeLogGroups`
- `elasticloadbalancing:Describe*`
- `route53:List*`, `route53:Get*`
- `wafv2:List*`, `wafv2:Get*`
- `lambda:ListFunctions`, `lambda:GetFunctionConfiguration`
- `sso-admin:ListInstances`, `sso-admin:ListPermissionSets`, `sso-admin:DescribePermissionSet`, `sso-admin:ListAccountsForProvisionedPermissionSet`, `sso-admin:ListAccountAssignments` (`sso_permissions` 시트 사용 시)
- `identitystore:DescribeUser`, `identitystore:DescribeGroup` (`sso_permissions` 시트 사용 시)
- `pricing:GetProducts` 선택, `--rds-pricing` 사용 시

## 저장소 구조

CLI CSV/XLSX 출력만 사용할 때는 DB가 필요 없습니다. `--serve` 웹 모드를 사용할 때만 SQLite를 사용합니다.

기본 DB 파일:

```text
asset-view.db
```

저장 개념:

```text
snapshots(id, collected_at, source)
assets(snapshot_id, indexed columns..., payload)
security_group_rules(snapshot_id, indexed columns..., payload)
sso_permissions(snapshot_id, indexed columns..., payload)
```

AWS 리소스는 계속 조회 전용 API(`List*`, `Describe*`, `Get*`)만 사용하고, SQLite에는 로컬 조회용 복사본만 저장합니다.
