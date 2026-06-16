# 2026-06-16 AWS Asset Inventory Workbook 작업 이력

## 목적

AWS 한국 리전(`ap-northeast-2`) 중심으로 여러 SSO 계정/profile의 AWS 자산관리대장을 생성하는 CLI를 구현했다. 초기 요구는 CSV였지만, 이후 사용자가 보안그룹 룰/SSO 권한을 별도 탭으로 요구하여 XLSX 멀티시트 출력까지 확장했다. 추가로 기존 엑셀 출력 기능을 유지한 채 웹에서 테이블 조회와 XLSX 다운로드를 할 수 있는 Go 웹 모드와 SQLite snapshot 저장소를 추가했다.

## 현재 Git 상태

마지막 확인 시점 기준 작업 내용은 GitHub `main`에 push 완료되어 있고 로컬 working tree는 clean이었다.

최근 커밋:

```text
937b690 Add XLSX workbook and extended AWS inventory
fb329ee Support collecting multiple AWS profiles
0b48c39 Add AWS asset CSV collector
896d782 Initial commit
```

원격:

```text
origin https://github.com/seongtaekkim/aws-asset-view.git
branch main
```

## 주요 구현 내용

### 1. Go CLI 기본 구조

추가/수정 파일:

- `cmd/aws-asset-view/main.go`
- `internal/inventory/collector.go`
- `internal/inventory/model.go`
- `internal/inventory/writer.go`
- `internal/inventory/sso.go`
- `internal/inventory/report.go`
- `internal/inventory/xlsx.go`
- `internal/inventory/extra_collectors.go`
- `internal/store/sqlite.go`
- `internal/web/server.go`
- `go.mod`
- `go.sum`
- `README.md`

기본 빌드:

```bash
go build -o aws-asset-view ./cmd/aws-asset-view
```

### 2. 출력 방식

#### CSV

`--output assets.csv` 또는 기본값 사용 시 단일 CSV만 생성한다. CSV는 탭을 가질 수 없다.

```bash
./aws-asset-view --profiles dev,prod --regions ap-northeast-2 --output assets.csv
```

#### XLSX

`--output assets.xlsx`처럼 `.xlsx` 확장자를 사용해야 멀티시트가 생성된다.

```bash
./aws-asset-view \
  --profiles dev,staging,prod,security \
  --regions ap-northeast-2 \
  --output assets.xlsx
```

생성 시트:

1. `assets`
2. `security_group_rules`
3. `sso_permissions`

#### Web UI

`--serve` 옵션으로 웹 UI를 실행한다. 웹 모드는 로컬 SQLite DB에 수집 snapshot을 저장하고 브라우저에서 테이블 조회, 필터링, XLSX 다운로드를 제공한다. 기존 CSV/XLSX 출력 기능은 유지된다.

```bash
./aws-asset-view \
  --serve \
  --profiles dev,staging,prod,security \
  --sso-admin-profile management \
  --regions ap-northeast-2 \
  --db asset-view.db \
  --addr 127.0.0.1:8080
```

웹 메뉴:

- `/assets`
- `/security-groups`
- `/sso-permissions`
- `/download.xlsx`
- `/refresh` POST

DB만 열어 기존 snapshot을 조회하려면 profile 옵션 없이 실행한다.

```bash
./aws-asset-view --serve --db asset-view.db --addr 127.0.0.1:8080
```

중요: 사용자가 “탭이 안 나뉜다”고 지적했다. 가장 가능성 높은 원인은 `assets.csv`로 실행했거나, `git pull` 후 기존 바이너리를 다시 빌드하지 않은 경우다. 반드시 최신 commit 확인 후 재빌드하고 `.xlsx`로 실행해야 한다.

```bash
git pull origin main
git log -1 --oneline   # 937b690 이어야 함
rm -f aws-asset-view
go build -o aws-asset-view ./cmd/aws-asset-view
./aws-asset-view -h    # xlsx, efs, backup, cloudwatch 옵션 문구 확인
```

### 3. 멀티 profile / 계정별 role 대응

사용자가 “계정마다 profile/role이 다르다”고 지적하여 `--profiles` 모드를 추가했다.

```bash
./aws-asset-view \
  --profiles dev,staging,prod,security \
  --regions ap-northeast-2 \
  --output assets.xlsx
```

이 모드는 `~/.aws/config`의 각 profile에 있는 다음 값을 그대로 사용한다.

- `sso_session`
- `sso_account_id`
- `sso_role_name`
- `region`

즉 계정별 role / permission set 이름이 달라도 각 profile이 정상 설정되어 있으면 동작해야 한다.

### 4. SSO 전체 계정 순회 모드

동일 SSO Permission Set/role 이름을 모든 계정에서 쓸 수 있는 경우를 위해 `--sso-all-accounts` 모드도 구현되어 있다.

```bash
./aws-asset-view \
  --profile your-sso-profile \
  --sso-all-accounts \
  --sso-region ap-northeast-2 \
  --sso-role-name ReadOnlyAccess \
  --regions ap-northeast-2 \
  --output all-accounts-assets.xlsx
```

동작:

1. `~/.aws/sso/cache/*.json`에서 유효한 SSO access token 탐색
2. `sso:ListAccounts`
3. `sso:ListAccountRoles`
4. `sso:GetRoleCredentials`
5. 계정별 임시 credential로 자산 조회

단, 사용자 환경처럼 계정별 role 이름이 다르면 `--profiles` 모드가 더 적합하다.

## 수집 대상 / 컬럼

### assets 시트

대표 컬럼:

- `collected_at`
- `account_id`
- `account_name`
- `profile`
- `region`
- `service`
- `resource_type`
- `resource_id`
- `name`
- `arn`
- `state`
- `product_name`
- `version`
- `sku`
- `vcpu`
- `memory_mib`
- `architecture`
- `vpc_id`
- `subnet_ids`
- `security_group_ids`
- `public_access`
- `encrypted`
- `worm_enabled`
- `retention`
- `backup_retention`
- `details_json`
- `tags_json`

추가 수집 구현:

- EC2 Instance
- EC2 Instance Type spec(vCPU/Memory)
- VPC
- Subnet
- Route Table
- Security Group
- Site-to-Site VPN
- VPC Flow Logs
- EKS Cluster / version
- EKS Managed Node Group
- RDS DB Instance / engine / version / backup retention
- RDS DB Cluster / engine / version / backup retention
- S3 Bucket
- S3 Versioning
- S3 Encryption
- S3 Public Access Block
- S3 Object Lock / WORM / retention
- S3 Lifecycle
- EFS File System
- EFS Lifecycle
- EFS Backup Policy
- EFS Mount Targets
- AWS Backup Vault / Vault Lock / retention range
- AWS Backup Plan
- AWS Backup Protected Resource
- CloudWatch Metric Alarm
- CloudWatch Log Group / retention
- ALB/NLB/ELBv2
- Route53 Hosted Zone / Record
- WAFv2 Web ACL
- Lambda Function

### security_group_rules 시트

사용자가 요구한 보안그룹 상세 탭이다. `assets`의 Security Group `details_json`에 들어있는 ingress/egress를 펼쳐서 생성한다.

컬럼:

- `account_id`
- `account_name`
- `profile`
- `region`
- `security_group_id`
- `security_group_name`
- `direction` (`inbound` / `outbound`)
- `priority` — AWS Security Group에는 priority 개념이 없어 빈 값
- `rule_name` — rule description 사용
- `port`
- `protocol`
- `source`
- `destination`
- `access` — Security Group은 allow rule만 있으므로 `allow`
- `note`

주의: AWS Security Group Rule ID를 직접 수집하는 `DescribeSecurityGroupRules`는 아직 별도로 구현하지 않았고, 현재는 `DescribeSecurityGroups`의 `IpPermissions` 기반으로 펼친다. 사용자가 “룰 이름”을 정확히 AWS Security Group Rule ID/tag까지 원하면 다음 작업에서 `DescribeSecurityGroupRules`를 추가하는 것이 좋다.

### sso_permissions 시트

SSO Admin / Identity Store API로 Permission Set 할당 현황을 수집한다.

컬럼:

- `account_id`
- `account_name`
- `permission_set`
- `principal_type`
- `principal_id`
- `username`
- `display_name`
- `email`
- `group_name`
- `permission_set_arn`
- `profile`
- `identity_store_id`
- `instance_arn`
- `note`

사용 예:

```bash
./aws-asset-view \
  --profiles dev,staging,prod \
  --sso-admin-profile management \
  --regions ap-northeast-2 \
  --output assets.xlsx
```

SSO Admin 권한이 없으면 자산 수집은 계속하고 `sso_permissions` 시트에 오류 note를 남기도록 수정했다. 단, 실제 AWS 환경에서는 아직 검증하지 못했다.

## 읽기 전용 여부 확인

사용자가 “변경/삭제 없이 조회만 했는지” 확인 요청했다. 코드에서 AWS SDK operation 검색 결과 AWS 리소스에 대해서는 `List*`, `Describe*`, `Get*` 계열만 사용한다.

발견된 `Create`는 AWS API가 아니라 로컬 파일 생성:

```go
os.Create(output)
```

즉 AWS 리소스는 생성/수정/삭제하지 않고, 로컬에 `assets.csv` 또는 `assets.xlsx` 파일만 생성한다.

주의: SSO `GetRoleCredentials`는 임시 자격증명 발급 API이며 AWS 리소스 변경 API는 아니다.

## 검증 내역

로컬 환경에 Go가 없어 Docker Go 1.22 컨테이너로 검증했다.

```bash
docker run --rm -v "$PWD":/src -w /src golang:1.22-bookworm \
  sh -lc 'export PATH=/usr/local/go/bin:$PATH; gofmt -w cmd/aws-asset-view/main.go internal/inventory/*.go && go test ./...'
```

결과:

```text
?   	aws-asset-view/cmd/aws-asset-view	[no test files]
?   	aws-asset-view/internal/inventory	[no test files]
```

CLI help도 확인했다.

```bash
go run ./cmd/aws-asset-view -h
```

확인된 옵션:

- `--profiles`
- `--output` CSV/XLSX
- `--serve`
- `--db`
- `--addr`
- `--include-sso-permissions`
- `--sso-admin-profile`
- `--services`에 `efs`, `backup`, `cloudwatch`, `logs`, `flowlog` 포함

웹 smoke test도 수행했다. Docker Go 1.22에서 `go build -buildvcs=false` 후 `--serve --db /tmp/asset-view-test.db --addr 127.0.0.1:18080`로 서버를 띄우고 `/assets`를 curl 조회하여 `WEB_OK`를 확인했다.

## 아직 실제 AWS에서 확인하지 못한 점

이 개발 환경에는 AWS SSO 로그인 토큰/실제 권한이 없어서 실제 계정 수집은 실행하지 못했다. 컴파일과 CLI 구조만 검증했다.

실제 사용자가 테스트해야 할 명령:

```bash
git pull origin main
rm -f aws-asset-view
go build -o aws-asset-view ./cmd/aws-asset-view

aws sso login --profile dev

./aws-asset-view \
  --profiles dev,staging,prod,security \
  --sso-admin-profile management \
  --regions ap-northeast-2 \
  --output assets.xlsx
```

웹으로 조회하려면:

```bash
./aws-asset-view \
  --serve \
  --profiles dev,staging,prod,security \
  --sso-admin-profile management \
  --regions ap-northeast-2 \
  --db asset-view.db \
  --addr 127.0.0.1:8080
```

접속 URL:

```text
http://127.0.0.1:8080
```

만약 SSO Admin 권한이 없다면 일단 SSO 권한 시트 없이 자산/보안그룹 시트부터 확인:

```bash
./aws-asset-view \
  --profiles dev,staging,prod,security \
  --include-sso-permissions=false \
  --regions ap-northeast-2 \
  --output assets.xlsx
```

## 다음 세션에서 우선 확인할 것

1. 사용자가 실제 실행한 명령어와 출력 파일 확장자 확인 (`.csv`인지 `.xlsx`인지).
2. 사용자가 최신 binary를 재빌드했는지 확인.
3. `./aws-asset-view -h` 출력에 `CSV or XLSX`, `--serve`, `--db`, `--addr`, `efs`, `backup`, `cloudwatch`, `sso-admin-profile`이 보이는지 확인.
4. 웹 조회가 안 되면 `./aws-asset-view --serve --db asset-view.db --addr 127.0.0.1:8080`로 기존 DB만 열어 `/assets` 접근 확인.
5. `assets.xlsx`가 생성됐는데 시트가 없다면 `internal/inventory/xlsx.go`의 `WriteXLSX` 동작 확인.
6. Security Group rule sheet가 비어 있으면 `assets` 시트에 `security_group` row가 있는지, `details_json`에 ingress/egress가 있는지 확인.
7. SSO permissions sheet가 비면 `--sso-admin-profile` 권한 확인. 필요한 권한은 `sso-admin:ListInstances`, `sso-admin:ListPermissionSets`, `sso-admin:ListAccountsForProvisionedPermissionSet`, `sso-admin:ListAccountAssignments`, `identitystore:DescribeUser`, `identitystore:DescribeGroup`.
8. 사용자가 “룰 이름/우선순위”를 더 엄격하게 요구하면 `DescribeSecurityGroupRules` 추가 검토. SG에는 priority가 없으므로 priority는 비워두거나 NACL을 별도 수집해야 한다.

## 알려진 설계상 한계

- CSV는 단일 시트라 탭 분리가 불가능하다. 탭은 XLSX만 가능.
- 웹 모드는 로컬 SQLite(`asset-view.db`)에 snapshot을 저장한다. AWS 리소스 변경은 하지 않는다.
- Security Group에는 priority가 없다. NACL은 priority가 있으나 아직 별도 수집하지 않았다.
- SSO username/permission 조회는 일반 workload account read-only role이 아니라 IAM Identity Center 관리 권한이 있는 profile이 필요할 수 있다.
- RDS vCPU/Memory는 `--rds-pricing` 옵션을 켜야 Pricing API 기반으로 채운다.
- 실제 AWS 권한별 AccessDenied가 발생할 수 있으며, 현재는 가능한 만큼 수집하고 오류를 stderr 또는 SSO note에 남기는 방식이다.
