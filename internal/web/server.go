package web

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"strings"
	"time"

	"aws-asset-view/internal/inventory"
	"aws-asset-view/internal/store"
)

type Server struct {
	store *store.Store
	opts  inventory.Options
}

type pageData struct {
	Title       string
	Active      string
	Stats       stats
	Assets      []inventory.AssetRecord
	Rules       []inventory.SecurityGroupRuleRecord
	Permissions []inventory.SSOPermissionRecord
	Query       string
	Service     string
	Account     string
	Region      string
	Message     string
	Error       string
}

type stats struct {
	Assets      int
	Rules       int
	Permissions int
	Accounts    int
	Regions     int
	Services    int
	Public      int
	Unencrypted int
	WORM        int
}

func New(st *store.Store, opts inventory.Options) *Server {
	return &Server{store: st, opts: opts}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.redirectRoot)
	mux.HandleFunc("/assets", s.assets)
	mux.HandleFunc("/security-groups", s.securityGroups)
	mux.HandleFunc("/sso-permissions", s.ssoPermissions)
	mux.HandleFunc("/download.xlsx", s.downloadXLSX)
	mux.HandleFunc("/refresh", s.refresh)
	return mux
}

func (s *Server) redirectRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/assets", http.StatusFound)
}

func (s *Server) assets(w http.ResponseWriter, r *http.Request) {
	report, err := s.store.LatestReport(r.Context())
	data := s.baseData(r, "Assets", "assets", report, err)
	data.Assets = filterAssets(report.Assets, r)
	render(w, data)
}

func (s *Server) securityGroups(w http.ResponseWriter, r *http.Request) {
	report, err := s.store.LatestReport(r.Context())
	data := s.baseData(r, "Security group rules", "security", report, err)
	data.Rules = filterRules(report.SecurityRules, r)
	render(w, data)
}

func (s *Server) ssoPermissions(w http.ResponseWriter, r *http.Request) {
	report, err := s.store.LatestReport(r.Context())
	data := s.baseData(r, "SSO permissions", "sso", report, err)
	data.Permissions = filterPermissions(report.SSOPermissions, r)
	render(w, data)
}

func (s *Server) downloadXLSX(w http.ResponseWriter, r *http.Request) {
	report, err := s.store.LatestReport(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	file, err := os.CreateTemp("", "aws-asset-view-*.xlsx")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	path := file.Name()
	_ = file.Close()
	defer os.Remove(path)
	if err := inventory.WriteXLSX(path, report); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="aws-assets.xlsx"`)
	http.ServeFile(w, r, path)
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	report, err := inventory.CollectReport(ctx, s.opts)
	if err != nil {
		http.Error(w, "collection failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := s.store.SaveReport(ctx, report, "web-refresh"); err != nil {
		http.Error(w, "save failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/assets?message=refresh-complete", http.StatusFound)
}

func (s *Server) baseData(r *http.Request, title, active string, report inventory.Report, err error) pageData {
	q := r.URL.Query()
	data := pageData{Title: title, Active: active, Query: q.Get("q"), Service: q.Get("service"), Account: q.Get("account"), Region: q.Get("region"), Message: q.Get("message")}
	if err != nil {
		data.Error = "No inventory snapshot is stored yet. Run a collection or press Refresh. " + err.Error()
		return data
	}
	data.Stats = buildStats(report)
	return data
}

func buildStats(report inventory.Report) stats {
	accounts, regions, services := map[string]bool{}, map[string]bool{}, map[string]bool{}
	st := stats{Assets: len(report.Assets), Rules: len(report.SecurityRules), Permissions: len(report.SSOPermissions)}
	for _, a := range report.Assets {
		accounts[a.AccountID] = true
		regions[a.Region] = true
		services[a.Service] = true
		if a.PublicAccess == "true" {
			st.Public++
		}
		if a.Encrypted == "false" {
			st.Unencrypted++
		}
		if a.WORMEnabled == "true" {
			st.WORM++
		}
	}
	st.Accounts, st.Regions, st.Services = len(accounts), len(regions), len(services)
	return st
}

func filterAssets(rows []inventory.AssetRecord, r *http.Request) []inventory.AssetRecord {
	q := strings.ToLower(r.URL.Query().Get("q"))
	service := strings.ToLower(r.URL.Query().Get("service"))
	account := strings.ToLower(r.URL.Query().Get("account"))
	region := strings.ToLower(r.URL.Query().Get("region"))
	out := rows[:0]
	for _, row := range rows {
		if service != "" && strings.ToLower(row.Service) != service {
			continue
		}
		if account != "" && !strings.Contains(strings.ToLower(row.AccountID+row.AccountName+row.SourceProfile), account) {
			continue
		}
		if region != "" && strings.ToLower(row.Region) != region {
			continue
		}
		blob := strings.ToLower(row.AccountID + " " + row.AccountName + " " + row.SourceProfile + " " + row.Region + " " + row.Service + " " + row.ResourceType + " " + row.ResourceID + " " + row.Name + " " + row.ProductName + " " + row.Version + " " + row.SKU)
		if q != "" && !strings.Contains(blob, q) {
			continue
		}
		out = append(out, row)
	}
	return out
}

func filterRules(rows []inventory.SecurityGroupRuleRecord, r *http.Request) []inventory.SecurityGroupRuleRecord {
	q := strings.ToLower(r.URL.Query().Get("q"))
	out := rows[:0]
	for _, row := range rows {
		blob := strings.ToLower(fmt.Sprint(row))
		if q != "" && !strings.Contains(blob, q) {
			continue
		}
		out = append(out, row)
	}
	return out
}

func filterPermissions(rows []inventory.SSOPermissionRecord, r *http.Request) []inventory.SSOPermissionRecord {
	q := strings.ToLower(r.URL.Query().Get("q"))
	out := rows[:0]
	for _, row := range rows {
		blob := strings.ToLower(fmt.Sprint(row))
		if q != "" && !strings.Contains(blob, q) {
			continue
		}
		out = append(out, row)
	}
	return out
}

func render(w http.ResponseWriter, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTemplate.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

var pageTemplate = template.Must(template.New("page").Funcs(template.FuncMap{
	"boolBadge": boolBadge,
	"first": func(values ...string) string {
		for _, v := range values {
			if v != "" {
				return v
			}
		}
		return ""
	},
}).Parse(`<!doctype html>
<html lang="ko">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · aws-asset-view</title>
<style>
:root { color-scheme: light; --bg:#f6f8fb; --panel:#ffffff; --line:#d9e1ec; --ink:#172033; --muted:#5b667a; --accent:#2563eb; --accent-ink:#fff; --danger:#b42318; --ok:#067647; --warn:#b54708; }
* { box-sizing: border-box; }
body { margin:0; background:var(--bg); color:var(--ink); font:14px/1.5 system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif; }
a { color:inherit; }
.shell { min-height:100vh; display:flex; }
.sidebar { width:240px; padding:24px 16px; background:#111827; color:#e5e7eb; position:sticky; top:0; height:100vh; }
.brand { font-weight:750; font-size:17px; letter-spacing:-.01em; margin-bottom:22px; }
.nav { display:grid; gap:6px; }
.nav a { text-decoration:none; color:#cbd5e1; padding:10px 12px; border-radius:10px; }
.nav a.active, .nav a:hover { background:#1f2937; color:#fff; }
.main { flex:1; min-width:0; padding:24px; }
.top { display:flex; justify-content:space-between; gap:16px; align-items:flex-start; margin-bottom:18px; }
h1 { margin:0 0 4px; font-size:24px; letter-spacing:-.02em; }
.sub { color:var(--muted); }
.actions { display:flex; gap:8px; flex-wrap:wrap; justify-content:flex-end; }
button,.button { border:1px solid var(--line); background:var(--panel); color:var(--ink); padding:9px 12px; border-radius:10px; text-decoration:none; font-weight:650; cursor:pointer; }
.primary { background:var(--accent); color:var(--accent-ink); border-color:var(--accent); }
.stats { display:grid; grid-template-columns:repeat(auto-fit,minmax(140px,1fr)); gap:10px; margin-bottom:16px; }
.stat { background:var(--panel); border:1px solid var(--line); border-radius:14px; padding:14px; }
.stat b { display:block; font-size:22px; line-height:1.1; }
.stat span { color:var(--muted); font-size:12px; }
.filters { background:var(--panel); border:1px solid var(--line); border-radius:14px; padding:12px; display:flex; gap:10px; flex-wrap:wrap; margin-bottom:14px; }
.filters input { border:1px solid var(--line); border-radius:10px; padding:9px 10px; min-width:180px; color:var(--ink); }
.notice { border:1px solid var(--line); background:var(--panel); border-radius:14px; padding:14px; margin-bottom:14px; }
.error { color:var(--danger); }
.table-wrap { background:var(--panel); border:1px solid var(--line); border-radius:14px; overflow:auto; max-height:70vh; }
table { border-collapse:separate; border-spacing:0; width:100%; min-width:1200px; }
th,td { padding:9px 10px; border-bottom:1px solid #edf1f7; text-align:left; vertical-align:top; white-space:nowrap; }
th { position:sticky; top:0; background:#f8fafc; z-index:1; font-size:12px; color:#334155; }
td.muted { color:var(--muted); }
.badge { display:inline-flex; border-radius:999px; padding:2px 8px; font-size:12px; font-weight:650; background:#eef2ff; color:#3730a3; }
.badge.ok { background:#ecfdf3; color:var(--ok); }
.badge.warn { background:#fff7ed; color:var(--warn); }
.badge.danger { background:#fef3f2; color:var(--danger); }
@media (max-width: 860px) { .shell { display:block; } .sidebar { width:auto; height:auto; position:static; } .main { padding:16px; } .top { display:block; } .actions { justify-content:flex-start; margin-top:12px; } }
</style>
</head>
<body><div class="shell"><aside class="sidebar"><div class="brand">aws-asset-view</div><nav class="nav">
<a class="{{if eq .Active "assets"}}active{{end}}" href="/assets">Assets</a>
<a class="{{if eq .Active "security"}}active{{end}}" href="/security-groups">Security groups</a>
<a class="{{if eq .Active "sso"}}active{{end}}" href="/sso-permissions">SSO permissions</a>
</nav></aside><main class="main">
<div class="top"><div><h1>{{.Title}}</h1><div class="sub">Stored AWS inventory. Use Refresh to collect a new snapshot, Download XLSX to export the current snapshot.</div></div><div class="actions"><form method="post" action="/refresh"><button class="primary" type="submit">Refresh inventory</button></form><a class="button" href="/download.xlsx">Download XLSX</a></div></div>
{{if .Error}}<div class="notice error">{{.Error}}</div>{{end}}{{if .Message}}<div class="notice">{{.Message}}</div>{{end}}
<div class="stats"><div class="stat"><b>{{.Stats.Assets}}</b><span>Assets</span></div><div class="stat"><b>{{.Stats.Accounts}}</b><span>Accounts</span></div><div class="stat"><b>{{.Stats.Regions}}</b><span>Regions</span></div><div class="stat"><b>{{.Stats.Services}}</b><span>Services</span></div><div class="stat"><b>{{.Stats.Public}}</b><span>Public exposure</span></div><div class="stat"><b>{{.Stats.Unencrypted}}</b><span>Unencrypted</span></div><div class="stat"><b>{{.Stats.WORM}}</b><span>WORM enabled</span></div></div>
<form class="filters" method="get"><input name="q" value="{{.Query}}" placeholder="Search"><input name="account" value="{{.Account}}" placeholder="Account/profile"><input name="region" value="{{.Region}}" placeholder="Region"><input name="service" value="{{.Service}}" placeholder="Service"><button type="submit">Apply filters</button><a class="button" href="?">Clear</a></form>
{{if eq .Active "assets"}}{{template "assets" .}}{{else if eq .Active "security"}}{{template "rules" .}}{{else}}{{template "sso" .}}{{end}}
</main></div></body></html>
{{define "assets"}}<div class="table-wrap"><table><thead><tr><th>Account</th><th>Profile</th><th>Region</th><th>Service</th><th>Type</th><th>Name</th><th>State</th><th>Product</th><th>Version</th><th>SKU</th><th>vCPU</th><th>Memory</th><th>Public</th><th>Encrypted</th><th>WORM</th><th>Retention</th><th>ARN</th></tr></thead><tbody>{{range .Assets}}<tr><td>{{.AccountID}}<br><span class="muted">{{.AccountName}}</span></td><td>{{.SourceProfile}}</td><td>{{.Region}}</td><td><span class="badge">{{.Service}}</span></td><td>{{.ResourceType}}</td><td>{{.Name}}</td><td>{{.State}}</td><td>{{.ProductName}}</td><td>{{.Version}}</td><td>{{.SKU}}</td><td>{{.VCPU}}</td><td>{{.MemoryMiB}}</td><td>{{boolBadge .PublicAccess}}</td><td>{{boolBadge .Encrypted}}</td><td>{{boolBadge .WORMEnabled}}</td><td>{{first .Retention .BackupRetention}}</td><td class="muted">{{.ARN}}</td></tr>{{end}}</tbody></table></div>{{end}}
{{define "rules"}}<div class="table-wrap"><table><thead><tr><th>Account</th><th>Profile</th><th>Region</th><th>Group</th><th>Direction</th><th>Priority</th><th>Rule name</th><th>Port</th><th>Protocol</th><th>Source</th><th>Destination</th><th>Access</th><th>Note</th></tr></thead><tbody>{{range .Rules}}<tr><td>{{.AccountID}}<br><span class="muted">{{.AccountName}}</span></td><td>{{.SourceProfile}}</td><td>{{.Region}}</td><td>{{.GroupName}}<br><span class="muted">{{.GroupID}}</span></td><td>{{.Direction}}</td><td>{{.Priority}}</td><td>{{.RuleName}}</td><td>{{.Port}}</td><td>{{.Protocol}}</td><td>{{.Source}}</td><td>{{.Destination}}</td><td>{{.Access}}</td><td>{{.Note}}</td></tr>{{end}}</tbody></table></div>{{end}}
{{define "sso"}}<div class="table-wrap"><table><thead><tr><th>Account</th><th>Permission set</th><th>Principal</th><th>Username</th><th>Display name</th><th>Email</th><th>Group</th><th>Profile</th><th>Note</th></tr></thead><tbody>{{range .Permissions}}<tr><td>{{.AccountID}}<br><span class="muted">{{.AccountName}}</span></td><td>{{.PermissionSet}}</td><td>{{.PrincipalType}}<br><span class="muted">{{.PrincipalID}}</span></td><td>{{.UserName}}</td><td>{{.DisplayName}}</td><td>{{.Email}}</td><td>{{.GroupName}}</td><td>{{.SourceProfile}}</td><td>{{.Note}}</td></tr>{{end}}</tbody></table></div>{{end}}`))

func boolBadge(v string) template.HTML {
	switch v {
	case "true":
		return template.HTML(`<span class="badge ok">true</span>`)
	case "false":
		return template.HTML(`<span class="badge warn">false</span>`)
	case "unknown":
		return template.HTML(`<span class="badge warn">unknown</span>`)
	default:
		return template.HTML(template.HTMLEscapeString(v))
	}
}
