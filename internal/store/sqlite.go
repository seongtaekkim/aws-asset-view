package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"aws-asset-view/internal/inventory"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	stmts := []string{
		`PRAGMA journal_mode=WAL`,
		`CREATE TABLE IF NOT EXISTS snapshots (id INTEGER PRIMARY KEY AUTOINCREMENT, collected_at TEXT NOT NULL, source TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS assets (snapshot_id INTEGER NOT NULL, account_id TEXT, account_name TEXT, profile TEXT, region TEXT, service TEXT, resource_type TEXT, resource_id TEXT, name TEXT, state TEXT, product_name TEXT, version TEXT, public_access TEXT, encrypted TEXT, worm_enabled TEXT, backup_retention TEXT, payload TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS security_group_rules (snapshot_id INTEGER NOT NULL, account_id TEXT, account_name TEXT, profile TEXT, region TEXT, group_id TEXT, group_name TEXT, direction TEXT, protocol TEXT, port TEXT, source TEXT, destination TEXT, access TEXT, payload TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS sso_permissions (snapshot_id INTEGER NOT NULL, account_id TEXT, account_name TEXT, permission_set TEXT, principal_type TEXT, username TEXT, display_name TEXT, email TEXT, group_name TEXT, profile TEXT, payload TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS idx_assets_snapshot ON assets(snapshot_id)`,
		`CREATE INDEX IF NOT EXISTS idx_assets_filter ON assets(snapshot_id, account_id, region, service, resource_type)`,
		`CREATE INDEX IF NOT EXISTS idx_sg_snapshot ON security_group_rules(snapshot_id)`,
		`CREATE INDEX IF NOT EXISTS idx_sso_snapshot ON sso_permissions(snapshot_id)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SaveReport(ctx context.Context, report inventory.Report, source string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO snapshots(collected_at, source) VALUES (?, ?)`, time.Now().UTC().Format(time.RFC3339), source)
	if err != nil {
		return 0, err
	}
	snapshotID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, r := range report.Assets {
		payload, _ := json.Marshal(r)
		_, err := tx.ExecContext(ctx, `INSERT INTO assets(snapshot_id, account_id, account_name, profile, region, service, resource_type, resource_id, name, state, product_name, version, public_access, encrypted, worm_enabled, backup_retention, payload) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, snapshotID, r.AccountID, r.AccountName, r.SourceProfile, r.Region, r.Service, r.ResourceType, r.ResourceID, r.Name, r.State, r.ProductName, r.Version, r.PublicAccess, r.Encrypted, r.WORMEnabled, r.BackupRetention, string(payload))
		if err != nil {
			return 0, err
		}
	}
	for _, r := range report.SecurityRules {
		payload, _ := json.Marshal(r)
		_, err := tx.ExecContext(ctx, `INSERT INTO security_group_rules(snapshot_id, account_id, account_name, profile, region, group_id, group_name, direction, protocol, port, source, destination, access, payload) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, snapshotID, r.AccountID, r.AccountName, r.SourceProfile, r.Region, r.GroupID, r.GroupName, r.Direction, r.Protocol, r.Port, r.Source, r.Destination, r.Access, string(payload))
		if err != nil {
			return 0, err
		}
	}
	for _, r := range report.SSOPermissions {
		payload, _ := json.Marshal(r)
		_, err := tx.ExecContext(ctx, `INSERT INTO sso_permissions(snapshot_id, account_id, account_name, permission_set, principal_type, username, display_name, email, group_name, profile, payload) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, snapshotID, r.AccountID, r.AccountName, r.PermissionSet, r.PrincipalType, r.UserName, r.DisplayName, r.Email, r.GroupName, r.SourceProfile, string(payload))
		if err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return snapshotID, nil
}

func (s *Store) LatestSnapshotID(ctx context.Context) (int64, error) {
	var id sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT max(id) FROM snapshots`).Scan(&id); err != nil {
		return 0, err
	}
	if !id.Valid {
		return 0, sql.ErrNoRows
	}
	return id.Int64, nil
}

func (s *Store) LatestReport(ctx context.Context) (inventory.Report, error) {
	id, err := s.LatestSnapshotID(ctx)
	if err != nil {
		return inventory.Report{}, err
	}
	return s.Report(ctx, id)
}

func (s *Store) Report(ctx context.Context, snapshotID int64) (inventory.Report, error) {
	var report inventory.Report
	assets, err := queryPayloads[inventory.AssetRecord](ctx, s.db, `SELECT payload FROM assets WHERE snapshot_id = ? ORDER BY account_id, region, service, resource_type, name`, snapshotID)
	if err != nil {
		return report, err
	}
	rules, err := queryPayloads[inventory.SecurityGroupRuleRecord](ctx, s.db, `SELECT payload FROM security_group_rules WHERE snapshot_id = ? ORDER BY account_id, region, group_name, direction, protocol, port`, snapshotID)
	if err != nil {
		return report, err
	}
	perms, err := queryPayloads[inventory.SSOPermissionRecord](ctx, s.db, `SELECT payload FROM sso_permissions WHERE snapshot_id = ? ORDER BY account_id, permission_set, principal_type, username, group_name`, snapshotID)
	if err != nil {
		return report, err
	}
	report.Assets = assets
	report.SecurityRules = rules
	report.SSOPermissions = perms
	return report, nil
}

func queryPayloads[T any](ctx context.Context, db *sql.DB, query string, snapshotID int64) ([]T, error) {
	rows, err := db.QueryContext(ctx, query, snapshotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var item T
		if err := json.Unmarshal([]byte(payload), &item); err != nil {
			return nil, fmt.Errorf("decode stored payload: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
