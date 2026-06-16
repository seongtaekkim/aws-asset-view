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

특정 프로파일 사용:

```bash
./aws-asset-view --profile prod --regions ap-northeast-2 --output prod-assets.csv
```

일부 서비스만 수집:

```bash
./aws-asset-view \
  --profile prod \
  --regions ap-northeast-2 \
  --services ec2,eks,rds,s3,lb,vpc,subnet,routetable,sg,vpn,waf,lambda,route53 \
  --output assets.csv
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

그 다음 SSO에서 접근 가능한 모든 계정을 순회해 CSV를 생성합니다.

```bash
./aws-asset-view \
  --profile your-sso-profile \
  --sso-all-accounts \
  --sso-region ap-northeast-2 \
  --sso-role-name ReadOnlyAccess \
  --regions ap-northeast-2 \
  --output all-accounts-assets.csv
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
5. 결과를 하나의 CSV에 합칩니다.

`--sso-role-name`을 생략하면 계정별 사용 가능한 role 이름 중 정렬상 첫 번째 role을 사용합니다. 운영에서는 Permission Set 이름이 계정마다 동일하도록 맞추고 `--sso-role-name`을 지정하는 것을 권장합니다.

## CSV 컬럼

| 컬럼 | 설명 |
|---|---|
| collected_at | 수집 시각 UTC |
| account_id | AWS Account ID |
| account_name | AWS SSO 계정 이름. 단일 계정 수집에서는 비어 있을 수 있음 |
| region | 리전. Route53 등 글로벌 리소스는 `global` |
| service | ec2, eks, rds, s3, lb, route53, vpc, waf, lambda 등 |
| resource_type | instance, cluster, nodegroup, db_instance, bucket 등 |
| resource_id | 서비스별 식별자 |
| name | Name 태그 또는 리소스 이름 |
| arn | ARN |
| state | 리소스 상태 |
| sku | EC2 instance type, RDS class, Lambda runtime, LB type 등 |
| vcpu | 조회 가능한 컴퓨팅 자원의 vCPU |
| memory_mib | 조회 가능한 컴퓨팅 자원의 메모리 MiB |
| architecture | x86_64, arm64 등 |
| vpc_id | 연결 VPC |
| subnet_ids | 세미콜론 구분 subnet 목록 |
| security_group_ids | 세미콜론 구분 SG 목록 |
| public_access | 인터넷 노출 또는 public 설정 추정 |
| encrypted | 암호화 여부 |
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
- EKS Cluster
- EKS Managed Node Group
- RDS DB Instance
- S3 Bucket
- ALB/NLB/ELBv2 Load Balancer
- Route53 Hosted Zone / Record
- WAFv2 Web ACL, Regional/CloudFront scope
- Lambda Function

## 필요한 대표 권한

읽기 전용 권한 기준으로 다음 액션이 필요합니다. 실제 운영에서는 서비스별 `Describe*`, `List*`, `Get*` 권한을 최소 권한으로 조정하세요.

- `sts:GetCallerIdentity`
- `sso:ListAccounts`, `sso:ListAccountRoles`, `sso:GetRoleCredentials` (`--sso-all-accounts` 사용 시)
- `ec2:Describe*`
- `eks:List*`, `eks:Describe*`
- `rds:Describe*`, `rds:ListTagsForResource` 선택
- `s3:ListAllMyBuckets`, `s3:GetBucketLocation`, `s3:GetBucketEncryption`, `s3:GetBucketVersioning`, `s3:GetBucketPublicAccessBlock`
- `elasticloadbalancing:Describe*`
- `route53:List*`, `route53:Get*`
- `wafv2:List*`, `wafv2:Get*`
- `lambda:ListFunctions`, `lambda:GetFunctionConfiguration`
- `pricing:GetProducts` 선택, `--rds-pricing` 사용 시

## 저장소 확장 방향

CSV만으로 충분하면 현재 구조를 유지하면 됩니다. 이력/비교가 필요해지는 시점에만 SQLite를 붙이는 것을 권장합니다.

예상 구조:

```text
snapshots(id, collected_at, account_id, regions)
assets(snapshot_id, account_id, region, service, resource_type, resource_id, raw_json, normalized columns...)
findings(snapshot_id, asset_key, severity, finding_type, message)
```
