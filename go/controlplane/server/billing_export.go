package server

// billing_export.go — XLSX and PDF renderers for GET /api/v1/billing/report.
//
// writeBillingXLSX and writeBillingPDF are called from handleBillingReport
// (billing.go) when the caller supplies ?format=xlsx or ?format=pdf.
// Both functions write directly to http.ResponseWriter after setting the
// appropriate Content-Type and Content-Disposition headers.
//
// XLSX uses github.com/xuri/excelize/v2 (pure Go, no CGO).
// PDF  uses github.com/jung-kurt/gofpdf (pure Go, no CGO).

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jung-kurt/gofpdf"
	"github.com/purser/purser/go/controlplane/registry"
	"github.com/xuri/excelize/v2"
)

// billingPeriodLabel returns a "YYYY-MM" label for the billing period, used in
// file names and sheet content.
func billingPeriodLabel(r *registry.BillingReport) string {
	return r.PeriodStart.UTC().Format("2006-01")
}

// splitTenantForExport splits a tenant_id of the form "orgId/teamSlug" into its
// two parts. When no slash is present the whole ID is treated as the org and
// team is empty.
func splitTenantForExport(tenantID string) (org, team string) {
	if idx := strings.IndexByte(tenantID, '/'); idx >= 0 {
		return tenantID[:idx], tenantID[idx+1:]
	}
	return tenantID, ""
}

// xlsxApplyHeader writes a row of bold white-on-green column headers.
func xlsxApplyHeader(f *excelize.File, sheet string, headers []string, styleID int) {
	for col, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(col+1, 1)
		_ = f.SetCellValue(sheet, cell, h)
		_ = f.SetCellStyle(sheet, cell, cell, styleID)
	}
}

// writeBillingXLSX renders report as a multi-sheet Excel workbook and streams
// it to w. It sets Content-Type and Content-Disposition before writing bytes.
func writeBillingXLSX(w http.ResponseWriter, report *registry.BillingReport) {
	f := excelize.NewFile()
	defer f.Close() //nolint:errcheck

	// Brand-green header style: white bold text on #2D6A4F background.
	hdrStyle, _ := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true, Color: "FFFFFF"},
		Fill: excelize.Fill{Type: "pattern", Color: []string{"2D6A4F"}, Pattern: 1},
	})

	period := billingPeriodLabel(report)

	// Build a TenantID→SLAComplianceRate lookup for the Summary sheet.
	slaByTenant := make(map[string]float64, len(report.SLAStats))
	for _, s := range report.SLAStats {
		slaByTenant[s.TenantID] = s.SLAComplianceRate
	}

	// ------------------------------------------------------------------
	// Sheet 1 — Summary (aggregate per tenant)
	// ------------------------------------------------------------------
	const sheetSummary = "Summary"
	f.SetSheetName("Sheet1", sheetSummary) //nolint:errcheck

	type tenantRow struct {
		orgID, teamID string
		requests      int64
		inputTokens   int64
		outputTokens  int64
		slaRate       float64
		hasSLA        bool
	}
	tenantRows := make(map[string]*tenantRow)
	var tenantOrder []string

	for _, tu := range report.Tenants {
		if _, seen := tenantRows[tu.TenantID]; !seen {
			org, team := splitTenantForExport(tu.TenantID)
			tenantRows[tu.TenantID] = &tenantRow{orgID: org, teamID: team}
			tenantOrder = append(tenantOrder, tu.TenantID)
		}
		tr := tenantRows[tu.TenantID]
		tr.requests += tu.RequestCount
		tr.inputTokens += tu.PromptTokens
		tr.outputTokens += tu.CompletionTokens
	}
	for tid, tr := range tenantRows {
		if rate, ok := slaByTenant[tid]; ok {
			tr.slaRate = rate
			tr.hasSLA = true
		}
	}

	summaryHeaders := []string{
		"org_id", "team_id", "period",
		"request_count", "input_tokens", "output_tokens",
		"cost_usd", "sla_compliance_rate",
	}
	xlsxApplyHeader(f, sheetSummary, summaryHeaders, hdrStyle)

	for i, tid := range tenantOrder {
		row := i + 2
		tr := tenantRows[tid]
		slaCell := ""
		if tr.hasSLA {
			slaCell = fmt.Sprintf("%.4f", tr.slaRate)
		}
		vals := []any{
			tr.orgID, tr.teamID, period,
			tr.requests, tr.inputTokens, tr.outputTokens,
			0.0, slaCell,
		}
		for col, v := range vals {
			cell, _ := excelize.CoordinatesToCellName(col+1, row)
			_ = f.SetCellValue(sheetSummary, cell, v)
		}
	}

	// ------------------------------------------------------------------
	// Sheet 2 — By Model
	// ------------------------------------------------------------------
	const sheetByModel = "By Model"
	_, _ = f.NewSheet(sheetByModel)

	type modelRow struct {
		requests  int64
		tokensIn  int64
		tokensOut int64
	}
	modelRows := make(map[string]*modelRow)
	var modelOrder []string

	for _, tu := range report.Tenants {
		if _, seen := modelRows[tu.ModelID]; !seen {
			modelRows[tu.ModelID] = &modelRow{}
			modelOrder = append(modelOrder, tu.ModelID)
		}
		mr := modelRows[tu.ModelID]
		mr.requests += tu.RequestCount
		mr.tokensIn += tu.PromptTokens
		mr.tokensOut += tu.CompletionTokens
	}

	byModelHeaders := []string{"model_id", "request_count", "tokens_in", "tokens_out", "cost_usd"}
	xlsxApplyHeader(f, sheetByModel, byModelHeaders, hdrStyle)

	for i, mid := range modelOrder {
		row := i + 2
		mr := modelRows[mid]
		vals := []any{mid, mr.requests, mr.tokensIn, mr.tokensOut, 0.0}
		for col, v := range vals {
			cell, _ := excelize.CoordinatesToCellName(col+1, row)
			_ = f.SetCellValue(sheetByModel, cell, v)
		}
	}

	// ------------------------------------------------------------------
	// Sheet 3 — Key Usage (proxy: per-tenant aggregates)
	// Note: the billing report endpoint does not expose per-API-key data;
	// this sheet uses tenant-level aggregates as a proxy. Operators can
	// obtain per-key detail via GET /api/v1/apikeys/{id}/usage.
	// ------------------------------------------------------------------
	const sheetKeyUsage = "Key Usage"
	_, _ = f.NewSheet(sheetKeyUsage)

	keyUsageHeaders := []string{"key_id", "name", "tenant", "request_count", "total_tokens", "last_used_at"}
	xlsxApplyHeader(f, sheetKeyUsage, keyUsageHeaders, hdrStyle)

	lastUsed := report.PeriodEnd.UTC().Format(time.RFC3339)
	for i, tid := range tenantOrder {
		row := i + 2
		tr := tenantRows[tid]
		vals := []any{
			tid, tr.orgID, tid,
			tr.requests, tr.inputTokens + tr.outputTokens, lastUsed,
		}
		for col, v := range vals {
			cell, _ := excelize.CoordinatesToCellName(col+1, row)
			_ = f.SetCellValue(sheetKeyUsage, cell, v)
		}
	}

	// Stream workbook to the response.
	filename := fmt.Sprintf("purser-billing-%s.xlsx", period)
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	_ = f.Write(w)
}

// writeBillingPDF renders a single-page billing summary as a PDF and streams
// it to w. It sets Content-Type and Content-Disposition before writing bytes.
func writeBillingPDF(w http.ResponseWriter, report *registry.BillingReport) {
	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(15, 15, 15)
	pdf.AddPage()

	period := billingPeriodLabel(report)
	generated := time.Now().UTC().Format("2006-01-02 15:04:05 UTC")

	pageWidth, _ := pdf.GetPageSize()
	contentWidth := pageWidth - 30 // left + right margin = 30mm

	// Title
	pdf.SetFont("Helvetica", "B", 16)
	pdf.SetTextColor(45, 106, 79) // Purser brand green #2D6A4F
	pdf.CellFormat(contentWidth, 10, "Purser Billing Report — "+period, "", 1, "C", false, 0, "")

	// Timestamp
	pdf.SetFont("Helvetica", "", 9)
	pdf.SetTextColor(100, 100, 100)
	pdf.CellFormat(contentWidth, 6, "Generated: "+generated, "", 1, "C", false, 0, "")
	pdf.Ln(5)

	// Table header
	colWidths := []float64{45, 22, 28, 28, 28, 28}
	headers := []string{"Tenant", "Requests", "Input Tokens", "Output Tokens", "Total Tokens", "Avg Latency ms"}

	pdf.SetFont("Helvetica", "B", 9)
	pdf.SetFillColor(45, 106, 79)   // #2D6A4F
	pdf.SetTextColor(255, 255, 255) // white
	pdf.SetDrawColor(200, 200, 200)
	for i, h := range headers {
		pdf.CellFormat(colWidths[i], 8, h, "1", 0, "C", true, 0, "")
	}
	pdf.Ln(-1)

	// Data rows with alternating fill
	pdf.SetFont("Helvetica", "", 8)
	pdf.SetTextColor(30, 30, 30)
	even := false
	for _, tu := range report.Tenants {
		if even {
			pdf.SetFillColor(237, 247, 243)
		} else {
			pdf.SetFillColor(255, 255, 255)
		}
		even = !even

		tenantLabel := tu.TenantID
		if len([]rune(tenantLabel)) > 24 {
			tenantLabel = string([]rune(tenantLabel)[:23]) + "…"
		}
		vals := []string{
			tenantLabel,
			fmt.Sprintf("%d", tu.RequestCount),
			fmt.Sprintf("%d", tu.PromptTokens),
			fmt.Sprintf("%d", tu.CompletionTokens),
			fmt.Sprintf("%d", tu.TotalTokens),
			fmt.Sprintf("%.1f", tu.AvgLatencyMs),
		}
		aligns := []string{"L", "R", "R", "R", "R", "R"}
		for i, v := range vals {
			pdf.CellFormat(colWidths[i], 7, v, "1", 0, aligns[i], true, 0, "")
		}
		pdf.Ln(-1)
	}

	// Totals row
	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetFillColor(200, 230, 215)
	pdf.SetTextColor(30, 30, 30)
	totals := []string{
		"TOTAL",
		fmt.Sprintf("%d", report.TotalRequests),
		"", "",
		fmt.Sprintf("%d", report.TotalTokens),
		"",
	}
	aligns := []string{"L", "R", "R", "R", "R", "R"}
	for i, v := range totals {
		pdf.CellFormat(colWidths[i], 7, v, "1", 0, aligns[i], true, 0, "")
	}
	pdf.Ln(-1)

	// Footer
	pdf.SetY(-20)
	pdf.SetFont("Helvetica", "I", 8)
	pdf.SetTextColor(130, 130, 130)
	pdf.CellFormat(contentWidth, 5, "Generated by Purser Control Plane v0.6", "", 0, "C", false, 0, "")

	filename := fmt.Sprintf("purser-billing-%s.pdf", period)
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	_ = pdf.Output(w)
}
