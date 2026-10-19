# gen-eval-samples.ps1 generates the v1.6 evaluation dataset expansion:
# 15 hand-written minimal samples (diff + source_files + ground_truth).
# Each pr.diff is derived from the file content itself, so ground-truth
# line numbers always match. Output is UTF-8 WITHOUT BOM.
$ErrorActionPreference = "Stop"
$enc = New-Object System.Text.UTF8Encoding($false)
$root = Join-Path $PSScriptRoot "dataset"
$rulesSrc = Join-Path $PSScriptRoot "..\rules\semgrep"

function Write-UTF8NoBom($path, $content) {
	$dir = Split-Path $path -Parent
	if (-not (Test-Path $dir)) { New-Item -ItemType Directory -Force -Path $dir | Out-Null }
	[System.IO.File]::WriteAllText($path, $content, $enc)
}

function New-Diff($filePath, $content) {
	$lines = $content -split "`r?`n"
	if ($lines[-1] -eq "") { $lines = $lines[0..($lines.Count - 2)] }  # drop trailing empty from final newline
	$n = $lines.Count
	$sb = New-Object System.Text.StringBuilder
	[void]$sb.AppendLine("diff --git a/$filePath b/$filePath")
	[void]$sb.AppendLine("new file mode 100644")
	[void]$sb.AppendLine("index 0000000..83db48f")
	[void]$sb.AppendLine("--- /dev/null")
	[void]$sb.AppendLine("+++ b/$filePath")
	[void]$sb.AppendLine("@@ -0,0 +1,$n @@")
	foreach ($l in $lines) { [void]$sb.AppendLine("+$l") }
	return $sb.ToString()
}

function New-Sample($name, $filePath, $content, $gtItems) {
	$dir = Join-Path $root $name
	$srcDir = Join-Path $dir "source_files"

	Write-UTF8NoBom (Join-Path $srcDir $filePath) $content
	Write-UTF8NoBom (Join-Path $srcDir "go.mod") "module evalsample`n`ngo 1.26`n"
	Write-UTF8NoBom (Join-Path $dir "pr.diff") (New-Diff $filePath $content)

	$gt = "[" + (($gtItems | ForEach-Object {
		$seg = "`n    {`"FilePath`": `"$($_.file)`", `"Line`": $($_.line), `"Severity`": `"$($_.severity)`", `"Category`": `"$($_.category)`", `"Message`": `"$($_.message)`"}"
		$seg
	}) -join ",") + "`n  ]`n"
	Write-UTF8NoBom (Join-Path $dir "ground_truth.json") $gt

	# Semgrep custom rules for the sample workspace.
	$rulesDir = Join-Path $srcDir "rules\semgrep"
	if (-not (Test-Path $rulesDir)) { New-Item -ItemType Directory -Force -Path $rulesDir | Out-Null }
	Copy-Item (Join-Path $rulesSrc "*.yml") $rulesDir -Force

	Write-Output "sample: $name ($($gtItems.Count) gt items)"
}

# ---------- 1. SQL injection via Sprintf ----------
New-Sample "sample-sql-sprintf" "internal/store/order.go" @'
package store

import (
	"database/sql"
	"fmt"
)

// Order is one order row.
type Order struct {
	ID     string
	UserID string
}

// GetOrder returns the order for the user.
func GetOrder(db *sql.DB, userID, orderID string) (*Order, error) {
	q := fmt.Sprintf("SELECT * FROM orders WHERE id = '%s' AND user_id = '%s'", orderID, userID)
	row := db.QueryRow(q)
	var o Order
	if err := row.Scan(&o.ID, &o.UserID); err != nil {
		return nil, err
	}
	return &o, nil
}
'@ @(
	@{ file = "internal/store/order.go"; line = 16; severity = "critical"; category = "security"; message = "SQL injection: query built with fmt.Sprintf from user-controlled orderID/userID" }
)

# ---------- 2. Command injection ----------
New-Sample "sample-cmd-injection" "internal/ops/resize.go" @'
package ops

import (
	"fmt"
	"os/exec"
)

// ResizeImage scales the uploaded image to the requested size.
func ResizeImage(path, size string) error {
	out, err := exec.Command("convert", path, "-resize", size, path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("resize: %w", err)
	}
	_ = out
	return nil
}
'@ @(
	@{ file = "internal/ops/resize.go"; line = 10; severity = "critical"; category = "security"; message = "command injection: user-controlled path/size flow into exec.Command without validation" }
)

# ---------- 3. Hardcoded secret ----------
New-Sample "sample-hardcoded-secret" "internal/billingclient/client.go" @'
package billingclient

// Client talks to the billing service.
type Client struct {
	apiKey string
}

// NewClient creates a billing client with the embedded key.
func NewClient() *Client {
	return &Client{apiKey: "sk-live-9d8f7a6b5c4d3e2f1a0b"}
}
'@ @(
	@{ file = "internal/billingclient/client.go"; line = 10; severity = "critical"; category = "security"; message = "hardcoded API key in source; load from configuration or a secret manager" }
)

# ---------- 4. Weak crypto ----------
New-Sample "sample-weak-crypto" "internal/auth/hash.go" @'
package auth

import (
	"crypto/md5"
	"encoding/hex"
)

// HashPassword hashes the password for storage.
func HashPassword(password string) string {
	sum := md5.Sum([]byte(password))
	return hex.EncodeToString(sum[:])
}
'@ @(
	@{ file = "internal/auth/hash.go"; line = 10; severity = "critical"; category = "security"; message = "MD5 used for password hashing; use bcrypt/argon2 with a per-user salt" }
)

# ---------- 5. Path traversal ----------
New-Sample "sample-path-traversal" "internal/files/serve.go" @'
package files

import (
	"net/http"
	"os"
	"path/filepath"
)

// Serve writes the requested upload to the response.
func Serve(w http.ResponseWriter, uploadsDir, name string) {
	p := filepath.Join(uploadsDir, name)
	data, err := os.ReadFile(p)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	_, _ = w.Write(data)
}
'@ @(
	@{ file = "internal/files/serve.go"; line = 11; severity = "warning"; category = "security"; message = "path traversal: name is joined without filepath.Clean/contains check, '..' escapes the uploads dir" }
)

# ---------- 6. Unhandled json error ----------
New-Sample "sample-unhandled-json" "internal/api/decode.go" @'
package api

import "encoding/json"

// DecodeConfig parses the raw config payload.
func DecodeConfig(data []byte) map[string]string {
	var cfg map[string]string
	json.Unmarshal(data, &cfg)
	return cfg
}
'@ @(
	@{ file = "internal/api/decode.go"; line = 8; severity = "warning"; category = "logic"; message = "unhandled json.Unmarshal error: invalid input silently yields an empty config" }
)

# ---------- 7. Nil-deref guard bug ----------
New-Sample "sample-nil-deref" "internal/user/profile.go" @'
package user

// Email returns the user's email.
func Email(u *User) string {
	if u == nil && u.Name == "" {
		return ""
	}
	return u.Email
}
'@ @(
	@{ file = "internal/user/profile.go"; line = 5; severity = "warning"; category = "logic"; message = "nil guard uses && instead of ||: a nil u dereferences u.Name and panics" }
)

# ---------- 8. Off-by-one ----------
New-Sample "sample-off-by-one" "internal/pager/page.go" @'
package pager

// LastPage returns the last 1-based page index.
func LastPage(total, perPage int) int {
	if perPage <= 0 {
		return 1
	}
	pages := total / perPage
	return pages
}
'@ @(
	@{ file = "internal/pager/page.go"; line = 8; severity = "warning"; category = "logic"; message = "off-by-one: integer division drops the partial page (total=101, perPage=100 reports 1 page, want 2)" }
)

# ---------- 9. Goroutine leak ----------
New-Sample "sample-goroutine-leak" "internal/worker/loop.go" @'
package worker

import "time"

// Start launches the periodic job.
func Start(fn func()) {
	go func() {
		for {
			fn()
			time.Sleep(time.Minute)
		}
	}()
}
'@ @(
	@{ file = "internal/worker/loop.go"; line = 7; severity = "warning"; category = "logic"; message = "goroutine leak: no stop channel or context, the loop runs for the process lifetime" }
)

# ---------- 10. N+1 queries ----------
New-Sample "sample-n-plus-one" "internal/store/emails.go" @'
package store

import "database/sql"

// EmailsOf returns the email of every given user id.
func EmailsOf(db *sql.DB, ids []int) ([]string, error) {
	var out []string
	for _, id := range ids {
		row := db.QueryRow("SELECT email FROM users WHERE id = $1", id)
		var email string
		if err := row.Scan(&email); err != nil {
			return nil, err
		}
		out = append(out, email)
	}
	return out, nil
}
'@ @(
	@{ file = "internal/store/emails.go"; line = 9; severity = "warning"; category = "performance"; message = "N+1 queries: one round-trip per id inside the loop; fetch all ids with a single IN query" }
)

# ---------- 11. String concat in loop ----------
New-Sample "sample-string-concat" "internal/render/table.go" @'
package render

// Table renders the rows as an HTML table.
func Table(rows []string) string {
	var html string
	for _, r := range rows {
		html += "<tr><td>" + r + "</td></tr>"
	}
	return html
}
'@ @(
	@{ file = "internal/render/table.go"; line = 7; severity = "warning"; category = "performance"; message = "quadratic string concatenation in the loop; use strings.Builder" }
)

# ---------- 12. Missing preallocation ----------
New-Sample "sample-prealloc" "internal/queue/take.go" @'
package queue

// Take returns at most n pending ids.
func Take(pending []int, n int) []int {
	var out []int
	for _, id := range pending {
		if len(out) >= n {
			break
		}
		out = append(out, id)
	}
	return out
}
'@ @(
	@{ file = "internal/queue/take.go"; line = 5; severity = "suggestion"; category = "performance"; message = "preallocate out with make([]int, 0, n) to avoid repeated growth" }
)

# ---------- 13. Naming convention ----------
New-Sample "sample-naming" "internal/api/user_handler.go" @'
package api

import "net/http"

// Get_User_Data writes the user payload.
func Get_User_Data(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("ok"))
}
'@ @(
	@{ file = "internal/api/user_handler.go"; line = 6; severity = "suggestion"; category = "style"; message = "naming convention: underscores in Get_User_Data; use GetUserData" }
)

# ---------- 14. Magic numbers ----------
New-Sample "sample-magic-numbers" "internal/billing/fee.go" @'
package billing

// Fee returns the transaction fee in cents.
func Fee(amountCents int) int {
	return amountCents * 143 / 10000
}
'@ @(
	@{ file = "internal/billing/fee.go"; line = 5; severity = "suggestion"; category = "style"; message = "magic numbers 143/10000 without named constants explaining the 1.43 percent rate" }
)

# ---------- 15. Swallowed error ----------
New-Sample "sample-swallowed-error" "internal/cache/warm.go" @'
package cache

// Warm loads the entry and ignores any failure.
func Warm(load func() ([]byte, error)) {
	data, _ := load()
	_ = data
}
'@ @(
	@{ file = "internal/cache/warm.go"; line = 5; severity = "warning"; category = "logic"; message = "error from load silently discarded with _; warm failures are invisible" }
)

Write-Output "done: 15 samples generated"
