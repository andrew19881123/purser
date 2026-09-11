package server_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/purser/purser/go/controlplane/server"
)

// TestBillingExport_XLSX_ContentType verifies that ?format=xlsx returns the
// correct XLSX Content-Type header.
func TestBillingExport_XLSX_ContentType(t *testing.T) {
	reg := newReg(t)
	lic := newBillingLicense(t)
	srv := server.New(reg, server.Config{Addr: ":0", License: lic})
	h := srv.Handler()

	seedInferenceEvent(t, h, "acme/eng", "llama3-8b", 100, 50, 200.0)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/billing/report?format=xlsx", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	want := "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	if ct := rec.Header().Get("Content-Type"); ct != want {
		t.Errorf("Content-Type = %q, want %q", ct, want)
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment;") {
		t.Errorf("Content-Disposition = %q, want attachment prefix", cd)
	}
}

// TestBillingExport_XLSX_HasSummarySheet verifies that the returned workbook
// contains a sheet named "Summary".
func TestBillingExport_XLSX_HasSummarySheet(t *testing.T) {
	reg := newReg(t)
	lic := newBillingLicense(t)
	srv := server.New(reg, server.Config{Addr: ":0", License: lic})
	h := srv.Handler()

	seedInferenceEvent(t, h, "acme/eng", "llama3-8b", 200, 100, 150.0)
	seedInferenceEvent(t, h, "acme/fin", "mistral-7b", 300, 120, 300.0)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/billing/report?format=xlsx", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	f, err := excelize.OpenReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("parse xlsx: %v", err)
	}
	defer f.Close()

	sheets := f.GetSheetList()
	wantSheets := []string{"Summary", "By Model", "Key Usage"}
	sheetSet := make(map[string]bool, len(sheets))
	for _, s := range sheets {
		sheetSet[s] = true
	}
	for _, want := range wantSheets {
		if !sheetSet[want] {
			t.Errorf("workbook missing sheet %q; got %v", want, sheets)
		}
	}

	// Summary sheet must have a header row with org_id as the first column.
	cell, err := f.GetCellValue("Summary", "A1")
	if err != nil {
		t.Fatalf("read Summary A1: %v", err)
	}
	if cell != "org_id" {
		t.Errorf("Summary A1 = %q, want %q", cell, "org_id")
	}
}

// TestBillingExport_PDF_ContentType verifies that ?format=pdf returns
// application/pdf and an attachment Content-Disposition.
func TestBillingExport_PDF_ContentType(t *testing.T) {
	reg := newReg(t)
	lic := newBillingLicense(t)
	srv := server.New(reg, server.Config{Addr: ":0", License: lic})
	h := srv.Handler()

	seedInferenceEvent(t, h, "acme/eng", "llama3-8b", 100, 50, 200.0)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/billing/report?format=pdf", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Errorf("Content-Type = %q, want application/pdf", ct)
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment;") {
		t.Errorf("Content-Disposition = %q, want attachment prefix", cd)
	}
	// Verify the response starts with the PDF magic bytes.
	if !bytes.HasPrefix(rec.Body.Bytes(), []byte("%PDF")) {
		t.Errorf("response body does not start with PDF magic %%PDF; got %q", rec.Body.Bytes()[:min(10, rec.Body.Len())])
	}
}

// TestBillingExport_UnknownFormat verifies that an unsupported format value
// returns 400 Bad Request.
func TestBillingExport_UnknownFormat(t *testing.T) {
	reg := newReg(t)
	lic := newBillingLicense(t)
	srv := server.New(reg, server.Config{Addr: ":0", License: lic})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/billing/report?format=parquet", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// min is a local helper to avoid importing slices for a single use.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
