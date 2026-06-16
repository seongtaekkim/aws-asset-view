package inventory

import (
	"fmt"

	"github.com/xuri/excelize/v2"
)

func WriteXLSX(path string, report Report) error {
	f := excelize.NewFile()
	assetsSheet := "assets"
	f.SetSheetName("Sheet1", assetsSheet)
	if err := writeRows(f, assetsSheet, append([][]string{csvHeader()}, assetRows(report.Assets)...)); err != nil {
		return err
	}
	if err := writeRows(f, "security_group_rules", append([][]string{securityRuleHeader()}, securityRuleRows(report.SecurityRules)...)); err != nil {
		return err
	}
	if err := writeRows(f, "sso_permissions", append([][]string{ssoPermissionHeader()}, ssoPermissionRows(report.SSOPermissions)...)); err != nil {
		return err
	}
	for _, sheet := range []string{assetsSheet, "security_group_rules", "sso_permissions"} {
		_ = f.SetPanes(sheet, &excelize.Panes{Freeze: true, Split: false, XSplit: 0, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"})
		_ = f.AutoFilter(sheet, "A1:"+columnName(sheetColumnCount(sheet))+"1", nil)
	}
	return f.SaveAs(path)
}

func writeRows(f *excelize.File, sheet string, rows [][]string) error {
	idx, err := f.GetSheetIndex(sheet)
	if err != nil {
		return err
	}
	if idx == -1 {
		if _, err := f.NewSheet(sheet); err != nil {
			return err
		}
	}
	for r, row := range rows {
		for c, v := range row {
			cell, err := excelize.CoordinatesToCellName(c+1, r+1)
			if err != nil {
				return err
			}
			if err := f.SetCellStr(sheet, cell, v); err != nil {
				return err
			}
		}
	}
	return nil
}

func assetRows(records []AssetRecord) [][]string {
	rows := make([][]string, 0, len(records))
	for _, r := range records {
		rows = append(rows, r.csvRow())
	}
	return rows
}

func securityRuleHeader() []string {
	return []string{"account_id", "account_name", "profile", "region", "security_group_id", "security_group_name", "direction", "priority", "rule_name", "port", "protocol", "source", "destination", "access", "note"}
}

func securityRuleRows(records []SecurityGroupRuleRecord) [][]string {
	rows := make([][]string, 0, len(records))
	for _, r := range records {
		rows = append(rows, []string{r.AccountID, r.AccountName, r.SourceProfile, r.Region, r.GroupID, r.GroupName, r.Direction, r.Priority, r.RuleName, r.Port, r.Protocol, r.Source, r.Destination, r.Access, r.Note})
	}
	return rows
}

func ssoPermissionHeader() []string {
	return []string{"account_id", "account_name", "permission_set", "principal_type", "principal_id", "username", "display_name", "email", "group_name", "permission_set_arn", "profile", "identity_store_id", "instance_arn", "note"}
}

func ssoPermissionRows(records []SSOPermissionRecord) [][]string {
	rows := make([][]string, 0, len(records))
	for _, r := range records {
		rows = append(rows, []string{r.AccountID, r.AccountName, r.PermissionSet, r.PrincipalType, r.PrincipalID, r.UserName, r.DisplayName, r.Email, r.GroupName, r.PermissionARN, r.SourceProfile, r.IdentityStore, r.InstanceARN, r.Note})
	}
	return rows
}

func sheetColumnCount(sheet string) int {
	switch sheet {
	case "security_group_rules":
		return len(securityRuleHeader())
	case "sso_permissions":
		return len(ssoPermissionHeader())
	default:
		return len(csvHeader())
	}
}

func columnName(n int) string {
	name, err := excelize.ColumnNumberToName(n)
	if err != nil {
		return fmt.Sprintf("A")
	}
	return name
}
