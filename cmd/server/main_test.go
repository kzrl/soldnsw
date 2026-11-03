package main

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"soldproperty/internal/database"
)

func newTestApp(t *testing.T) (*App, func()) {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(database.SchemaSQL); err != nil {
		t.Fatalf("schema: %v", err)
	}
	tmpls, err := parseTemplates("../../templates")
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}
	app, err := newApp(db, tmpls)
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	return app, func() { _ = db.Close() }
}

func seedProperty(t *testing.T, app *App, pid, street, suburb, state string) {
	seedPropertyWithNumber(t, app, pid, "1", street, suburb, state, "2000")
}

func seedPropertyWithNumber(t *testing.T, app *App, pid, number, street, suburb, state, postcode string) {
	t.Helper()
	err := app.queries.InsertGnafProperty(context.Background(), database.InsertGnafPropertyParams{
		GnafPid:      pid,
		StreetNumber: sql.NullString{String: number, Valid: number != ""},
		StreetName:   street,
		StreetType:   "ST",
		Suburb:       suburb,
		State:        state,
		Postcode:     postcode,
		LotNumber:    sql.NullString{},
		PlanNumber:   sql.NullString{},
	})
	if err != nil {
		t.Fatalf("seed property: %v", err)
	}
}

// TestTemplatesRenderCorrectPage guards against the regression where every
// view shared one template namespace and therefore rendered submit.html.
func TestTemplatesRenderCorrectPage(t *testing.T) {
	tmpls, err := parseTemplates("../../templates")
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}

	cases := []struct {
		page  string
		data  PageData
		title string
		body  string
	}{
		{"index.html", PageData{}, "Suburb Trends", "Find NSW property sales"},
		{"postcode.html", PageData{QueryPostcode: "2071", PostcodeSummary: &database.GetPostcodeSummaryRow{PropertyCount: 2, SalesCount: 1, MedianPrice: 600000}}, "Postcode 2071", "Properties in 2071"},
		{"property.html", PageData{Property: database.GnafProperty{GnafPid: "PID", StreetName: "TEST", StreetType: "ST", Suburb: "SUBURB", State: "NSW", Postcode: "2000"}}, "TEST ST", "Sales history"},
		{"suburb.html", PageData{}, "Suburb Market Trends", "Suburb market trends"},
		{"submit.html", PageData{CSRFToken: "token", TodayDate: "2026-01-01"}, "Add a Sale", "Submit sale record"},
		{"losses.html", PageData{SelectedSuburb: "TEST", SelectedState: "NSW"}, "Sold at a Loss", "Properties sold at a loss"},
		{"building.html", PageData{BuildingAddress: "TEST BUILDING", BuildingBasePID: "BASE"}, "TEST BUILDING", "Units and properties"},
		{"404.html", PageData{}, "Page Not Found", "We couldn't find that page"},
	}

	for _, tc := range cases {
		rec := httptest.NewRecorder()
		app := &App{templates: tmpls}
		app.renderTemplate(rec, tc.page, tc.data)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", tc.page, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, tc.title) {
			t.Errorf("%s: expected title containing %q", tc.page, tc.title)
		}
		if !strings.Contains(body, tc.body) {
			t.Errorf("%s: expected body containing %q", tc.page, tc.body)
		}
		// The submit form must never appear on non-submit pages.
		if tc.page != "submit.html" && strings.Contains(body, "Submit sale record") {
			t.Errorf("%s: submit form leaked into page", tc.page)
		}
	}
}

func seedSale(t *testing.T, app *App, saleID, pid, date string, price int64, source string) {
	t.Helper()
	err := app.queries.InsertPropertySale(context.Background(), database.InsertPropertySaleParams{
		SaleID:         saleID,
		GnafPid:        pid,
		SettlementDate: date,
		SalePrice:      price,
		SourceType:     source,
	})
	if err != nil {
		t.Fatalf("seed sale: %v", err)
	}
	if err := database.RefreshCanonicalSale(context.Background(), app.db, saleID); err != nil {
		t.Fatalf("refresh canonical sale: %v", err)
	}
}

func TestStreetOnlySearch(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	seedProperty(t, app, "PID-1", "BARNHILL", "ROTHBURY", "NSW")
	seedProperty(t, app, "PID-2", "MITCHELL", "KURRI KURRI", "NSW")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/properties?street=BARN", nil)
	app.handlePropertiesSearch(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected direct redirect to the only match, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/property/PID-1" {
		t.Errorf("unexpected redirect location: %s", loc)
	}
}

func TestSuburbSearchRespectsState(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	seedProperty(t, app, "PID-NSW", "CROWN", "RICHMOND", "NSW")
	seedProperty(t, app, "PID-VIC", "SWAN", "RICHMOND", "VIC")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/properties?suburb=RICHMOND&state=VIC", nil)
	app.handlePropertiesSearch(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected direct redirect to the only VIC match, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/property/PID-VIC" {
		t.Errorf("state filter was ignored; redirect location was %s", loc)
	}
}

func TestSubmitRequiresCSRF(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()
	seedProperty(t, app, "PID-1", "BARNHILL", "ROTHBURY", "NSW")

	form := url.Values{
		"gnaf_pid":        {"PID-1"},
		"settlement_date": {"2026-01-01"},
		"sale_price":      {"500000"},
		"source_type":     {"Auction"},
	}
	req := httptest.NewRequest(http.MethodPost, "/submit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	app.handleSubmitAction(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without CSRF token, got %d", rec.Code)
	}
}

func TestSubmitRecordsSaleWithCSRF(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()
	seedProperty(t, app, "PID-1", "BARNHILL", "ROTHBURY", "NSW")

	// Fetch the form to obtain a CSRF cookie.
	formRec := httptest.NewRecorder()
	app.handleSubmitForm(formRec, httptest.NewRequest(http.MethodGet, "/submit", nil))
	var csrf string
	for _, c := range formRec.Result().Cookies() {
		if c.Name == "csrf_token" {
			csrf = c.Value
		}
	}
	if csrf == "" {
		t.Fatalf("form did not set a CSRF cookie")
	}

	form := url.Values{
		"csrf_token":      {csrf},
		"gnaf_pid":        {"PID-1"},
		"settlement_date": {"2026-01-01"},
		"sale_price":      {"500000"},
		"source_type":     {"Auction"},
	}
	req := httptest.NewRequest(http.MethodPost, "/submit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "csrf_token", Value: csrf})
	rec := httptest.NewRecorder()
	app.handleSubmitAction(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after valid submission, got %d: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/property/PID-1" {
		t.Errorf("unexpected redirect location: %s", loc)
	}
	count, err := app.queries.GetTotalSalesCount(context.Background())
	if err != nil {
		t.Fatalf("count sales: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 sale, got %d", count)
	}
}

func TestFlashCookieTamperingRejected(t *testing.T) {
	app := &App{flashKey: []byte("01234567890123456789012345678901")}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "flash_msg", Value: "Zm9yZ2Vk.deadbeef"})
	rec := httptest.NewRecorder()

	if got := app.readFlash(rec, req); got != nil {
		t.Errorf("expected forged flash message to be rejected, got %+v", got)
	}
}

func TestSuburbLosses(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	oldDate := time.Now().AddDate(-5, 0, 0).Format("2006-01-02")
	recentDate := time.Now().AddDate(-2, 0, 0).Format("2006-01-02")

	seedProperty(t, app, "PID-LOSS", "LOSS", "ROTHBURY", "NSW")
	seedSale(t, app, "S-LOSS-1", "PID-LOSS", oldDate, 500000, "Auction")
	seedSale(t, app, "S-LOSS-2", "PID-LOSS", recentDate, 400000, "Private Treaty")

	seedProperty(t, app, "PID-GAIN", "GAIN", "ROTHBURY", "NSW")
	seedSale(t, app, "S-GAIN-1", "PID-GAIN", oldDate, 500000, "Auction")
	seedSale(t, app, "S-GAIN-2", "PID-GAIN", recentDate, 600000, "Private Treaty")

	seedProperty(t, app, "PID-SINGLE", "SINGLE", "ROTHBURY", "NSW")
	seedSale(t, app, "S-SINGLE-1", "PID-SINGLE", recentDate, 500000, "Auction")

	seedProperty(t, app, "PID-SAMEDAY", "SAMEDAY", "ROTHBURY", "NSW")
	seedSale(t, app, "S-SAME-1", "PID-SAMEDAY", recentDate, 500000, "Auction")
	seedSale(t, app, "S-SAME-2", "PID-SAMEDAY", recentDate, 400000, "Private Treaty")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/suburbs/losses?suburb=ROTHBURY&state=NSW&range=10&sort=amount", nil)
	app.handleSuburbLosses(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "LOSS ST") {
		t.Errorf("expected loss-making property in results")
	}
	if strings.Contains(body, "GAIN ST") {
		t.Errorf("gain-making property leaked into loss results")
	}
	if strings.Contains(body, "SINGLE ST") {
		t.Errorf("single-sale property leaked into loss results")
	}
	if strings.Contains(body, "SAMEDAY ST") {
		t.Errorf("same-day resale leaked into loss results")
	}
	if !strings.Contains(body, "of 2 resales") {
		t.Errorf("expected resale count to exclude same-day/single-sale properties")
	}

	for _, sortKey := range []string{"percent", "date"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/suburbs/losses?suburb=ROTHBURY&state=NSW&range=10&sort="+sortKey, nil)
		app.handleSuburbLosses(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("sort %s: status %d", sortKey, rec.Code)
		}
	}
}

func TestSuburbLossesLinksByCanonicalProperty(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	oldDate := time.Now().AddDate(-5, 0, 0).Format("2006-01-02")
	recentDate := time.Now().AddDate(-2, 0, 0).Format("2006-01-02")

	// Same address and lot/plan under old/new PSI districts.
	seedProperty(t, app, "GANSW_PSI_003_123", "LINKED", "ROTHBURY", "NSW")
	seedProperty(t, app, "GANSW_PSI_259_123", "LINKED", "ROTHBURY", "NSW")
	if _, err := app.db.Exec(`UPDATE gnaf_properties SET lot_number='29', plan_number='DP1045502' WHERE gnaf_pid IN ('GANSW_PSI_003_123','GANSW_PSI_259_123')`); err != nil {
		t.Fatalf("set lot/plan: %v", err)
	}
	seedSale(t, app, "S-LINK-1", "GANSW_PSI_003_123", oldDate, 500000, "Public Registry")
	seedSale(t, app, "S-LINK-2", "GANSW_PSI_259_123", recentDate, 400000, "Auction")
	if err := database.RebuildPropertyLinks(context.Background(), app.db); err != nil {
		t.Fatalf("rebuild links: %v", err)
	}
	if err := database.RebuildCanonicalSales(context.Background(), app.db); err != nil {
		t.Fatalf("rebuild canonical sales: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/suburbs/losses?suburb=ROTHBURY&state=NSW&range=10", nil)
	app.handleSuburbLosses(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "LINKED ST") {
		t.Errorf("expected linked address in loss results")
	}
	if !strings.Contains(body, "/property/GANSW_PSI_003_123") {
		t.Errorf("expected canonical property ID to be linked")
	}
	if !strings.Contains(body, "of 1 resales") {
		t.Errorf("expected canonical resale to be counted once")
	}
}

func TestStructuredNumberSearchRedirectsExactMatch(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	seedPropertyWithNumber(t, app, "PID-17", "17", "ARNOLD", "KILLARA", "NSW", "2071")
	seedPropertyWithNumber(t, app, "PID-17A", "17A", "ARNOLD", "KILLARA", "NSW", "2071")
	seedPropertyWithNumber(t, app, "PID-19", "19", "ARNOLD", "KILLARA", "NSW", "2071")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/properties?number=17&street=ARNOLD&suburb=KILLARA&state=NSW", nil)
	app.handlePropertiesSearch(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect to exact number match, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/property/PID-17" {
		t.Errorf("unexpected redirect location: %s", loc)
	}
}

func TestAddressSearchRedirectsExactMatch(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	seedPropertyWithNumber(t, app, "PID-17", "17", "ARNOLD", "KILLARA", "NSW", "2071")
	seedPropertyWithNumber(t, app, "PID-17A", "17A", "ARNOLD", "KILLARA", "NSW", "2071")
	if err := database.RebuildAddressSearch(context.Background(), app.db); err != nil {
		t.Fatalf("rebuild address search: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/properties?address=17+Arnold+St+Killara+NSW+2071", nil)
	app.handlePropertiesSearch(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect for exact address, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/property/PID-17" {
		t.Errorf("unexpected redirect location: %s", loc)
	}
}

func TestAddressSuggestEndpoint(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	seedPropertyWithNumber(t, app, "PID-17", "17", "ARNOLD", "KILLARA", "NSW", "2071")
	if err := database.RebuildAddressSearch(context.Background(), app.db); err != nil {
		t.Fatalf("rebuild address search: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/address/suggest?q=17+arnold", nil)
	app.handleAddressSuggest(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "17 ARNOLD ST KILLARA NSW 2071") {
		t.Errorf("suggestion missing address: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "PID-17") {
		t.Errorf("suggestion missing PID: %s", rec.Body.String())
	}
}

func TestNumberRequiresStreet(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/properties?number=17", nil)
	app.handlePropertiesSearch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Add a street name when searching by street number.") {
		t.Errorf("expected number/street warning")
	}
}

func TestPostcodeSearchAndValidation(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	seedPropertyWithNumber(t, app, "PID-17", "17", "ARNOLD", "KILLARA", "NSW", "2071")
	seedPropertyWithNumber(t, app, "PID-18", "18", "ARNOLD", "KILLARA", "NSW", "2071")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/properties?postcode=2071", nil)
	app.handlePropertiesSearch(rec, req)
	if rec.Code != http.StatusOK || (!strings.Contains(rec.Body.String(), "PID-17") && !strings.Contains(rec.Body.String(), "17 ARNOLD")) {
		t.Errorf("postcode search did not return matching properties")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/properties?suburb=KILLARA&postcode=abc", nil)
	app.handlePropertiesSearch(rec, req)
	if !strings.Contains(rec.Body.String(), "Postcode must be four digits.") {
		t.Errorf("expected postcode validation warning")
	}
}

func TestPropertyRedirectsToCanonicalAndMergesSales(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	seedPropertyWithNumber(t, app, "GANSW_PSI_003_2981264", "14", "BELLBOWER", "GREEN POINT", "NSW", "2251")
	seedPropertyWithNumber(t, app, "GANSW_PSI_259_2981264", "14", "BELLBOWER", "GREEN POINT", "NSW", "2251")
	if _, err := app.db.Exec(`UPDATE gnaf_properties SET lot_number='29', plan_number='DP1045502' WHERE gnaf_pid IN ('GANSW_PSI_003_2981264','GANSW_PSI_259_2981264')`); err != nil {
		t.Fatalf("set lot/plan: %v", err)
	}
	seedSale(t, app, "S-OLD", "GANSW_PSI_003_2981264", "2013-06-06", 680000, "NSW Valuer General (PSI)")
	seedSale(t, app, "S-NEW", "GANSW_PSI_259_2981264", "2021-04-26", 1225000, "NSW Valuer General (PSI)")
	if err := database.RebuildPropertyLinks(context.Background(), app.db); err != nil {
		t.Fatalf("rebuild links: %v", err)
	}
	if err := database.RebuildCanonicalSales(context.Background(), app.db); err != nil {
		t.Fatalf("rebuild canonical sales: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/property/GANSW_PSI_003_2981264", nil)
	req.SetPathValue("pid", "GANSW_PSI_003_2981264")
	rec := httptest.NewRecorder()
	app.handlePropertyDetail(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected deterministic canonical page, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "6 Jun 2013") || !strings.Contains(body, "26 Apr 2021") {
		t.Errorf("merged sale history missing dates")
	}

	req = httptest.NewRequest(http.MethodGet, "/property/GANSW_PSI_259_2981264", nil)
	req.SetPathValue("pid", "GANSW_PSI_259_2981264")
	rec = httptest.NewRecorder()
	app.handlePropertyDetail(rec, req)
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("expected non-canonical PID to redirect, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/property/GANSW_PSI_003_2981264" {
		t.Errorf("redirect location = %q", loc)
	}
}

func TestSuburbPriceStatsFallsBackToFiveYears(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	seedProperty(t, app, "PID-1", "TEST", "ROTHBURY", "NSW")
	recent := time.Now().AddDate(0, -3, 0).Format("2006-01-02")
	older := time.Now().AddDate(-2, 0, 0).Format("2006-01-02")
	for i := 0; i < 19; i++ {
		seedSale(t, app, "S-RECENT-"+strconv.Itoa(i), "PID-1", recent, int64(400000+i*1000), "Public Registry")
	}
	for i := 0; i < 10; i++ {
		seedSale(t, app, "S-OLDER-"+strconv.Itoa(i), "PID-1", older, int64(300000+i*1000), "Public Registry")
	}

	row, actual, requestedCount, err := app.suburbPriceStats(context.Background(), "ROTHBURY", "NSW", "1")
	if err != nil {
		t.Fatalf("suburbPriceStats: %v", err)
	}
	if requestedCount != 19 {
		t.Errorf("requested count = %d, want 19", requestedCount)
	}
	if actual != "5" {
		t.Errorf("actual range = %q, want 5", actual)
	}
	if row.TotalSales != 29 || row.MedianPrice == 0 {
		t.Errorf("unexpected fallback stats: %+v", row)
	}
}

func TestBuildingPageAndOldBasePIDRedirect(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	seedPropertyWithNumber(t, app, "BASE", "1", "BRUCE", "KILLARA", "NSW", "2071")
	seedPropertyWithNumber(t, app, "BASE_U1", "1/1", "BRUCE", "KILLARA", "NSW", "2071")
	seedPropertyWithNumber(t, app, "BASE_U2", "2/1", "BRUCE", "KILLARA", "NSW", "2071")
	if _, err := app.db.Exec(`
		UPDATE gnaf_properties SET base_pid = CASE gnaf_pid
			WHEN 'BASE' THEN 'BASE' WHEN 'BASE_U1' THEN 'BASE' WHEN 'BASE_U2' THEN 'BASE' END,
			lot_number = CASE gnaf_pid WHEN 'BASE' THEN '40' WHEN 'BASE_U1' THEN '1' WHEN 'BASE_U2' THEN '2' END,
			plan_number = 'SP83181'
		WHERE gnaf_pid IN ('BASE','BASE_U1','BASE_U2')
	`); err != nil {
		t.Fatalf("set building data: %v", err)
	}
	seedSale(t, app, "S-U1", "BASE_U1", "2020-01-01", 500000, "Public Registry")
	seedSale(t, app, "S-U2", "BASE_U2", "2021-01-01", 600000, "Public Registry")

	req := httptest.NewRequest(http.MethodGet, "/property/BASE", nil)
	req.SetPathValue("pid", "BASE")
	rec := httptest.NewRecorder()
	app.handlePropertyDetail(rec, req)
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("base PID status = %d, want 301", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/building/BASE" {
		t.Errorf("base PID redirect = %q", loc)
	}

	req = httptest.NewRequest(http.MethodGet, "/building/BASE", nil)
	req.SetPathValue("base_pid", "BASE")
	rec = httptest.NewRecorder()
	app.handleBuilding(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("building page status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "1/1 BRUCE") || !strings.Contains(body, "2/1 BRUCE") {
		t.Errorf("building page missing unit addresses")
	}
	if !strings.Contains(body, "3 property records") {
		t.Errorf("building page missing unit count")
	}
}

func TestSuburbLossTypeFilters(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	oldDate := time.Now().AddDate(-5, 0, 0).Format("2006-01-02")
	recentDate := time.Now().AddDate(-2, 0, 0).Format("2006-01-02")

	properties := []struct {
		pid, number, street, dwelling string
	}{
		{"PID-HOUSE", "17", "HOUSELANE", "house"},
		{"PID-UNIT", "3/1", "UNITLANE", "unit"},
		{"PID-TOWN", "5", "TOWNLANE", "townhouse"},
		{"PID-OTHER", "9", "OTHERLANE", "other"},
	}
	for _, p := range properties {
		seedPropertyWithNumber(t, app, p.pid, p.number, p.street, "ROTHBURY", "NSW", "2000")
		if _, err := app.db.Exec(`UPDATE gnaf_properties SET dwelling_type=? WHERE gnaf_pid=?`, p.dwelling, p.pid); err != nil {
			t.Fatalf("set dwelling type: %v", err)
		}
		seedSale(t, app, "S-"+p.pid+"-1", p.pid, oldDate, 500000, "Public Registry")
		seedSale(t, app, "S-"+p.pid+"-2", p.pid, recentDate, 400000, "Public Registry")
	}

	check := func(typeValue, want, reject string) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/suburbs/losses?suburb=ROTHBURY&state=NSW&range=10&type="+typeValue, nil)
		app.handleSuburbLosses(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("type %s: status %d", typeValue, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, want) {
			t.Errorf("type %s: missing %s", typeValue, want)
		}
		if reject != "" && strings.Contains(body, reject) {
			t.Errorf("type %s: unexpectedly contains %s", typeValue, reject)
		}
	}

	check("house", "HOUSELANE", "UNITLANE")
	check("unit", "UNITLANE", "HOUSELANE")
	check("townhouse", "TOWNLANE", "UNITLANE")
	check("other", "OTHERLANE", "HOUSELANE")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/suburbs/losses?suburb=ROTHBURY&state=NSW&range=10", nil)
	app.handleSuburbLosses(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, "Houses (1)") || !strings.Contains(body, "Units (1)") || !strings.Contains(body, "Townhouses (1)") {
		t.Errorf("type tabs missing counts")
	}
}

func TestSearchDwellingTypeFilter(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	seedPropertyWithNumber(t, app, "PID-HOUSE", "17", "HOUSELANE", "ROTHBURY", "NSW", "2000")
	seedPropertyWithNumber(t, app, "PID-UNIT", "3/1", "UNITLANE", "ROTHBURY", "NSW", "2000")
	if _, err := app.db.Exec(`
		UPDATE gnaf_properties SET dwelling_type = CASE gnaf_pid
			WHEN 'PID-HOUSE' THEN 'house' WHEN 'PID-UNIT' THEN 'unit' END
	`); err != nil {
		t.Fatalf("set dwelling types: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/properties?suburb=ROTHBURY&state=NSW&dwelling=house", nil)
	app.handlePropertiesSearch(rec, req)
	if rec.Code != http.StatusSeeOther {
		// One house result should redirect directly.
		t.Fatalf("status = %d, want redirect", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/property/PID-HOUSE" {
		t.Errorf("redirect = %q", loc)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/properties?suburb=ROTHBURY&state=NSW&dwelling=unit", nil)
	app.handlePropertiesSearch(rec, req)
	if loc := rec.Header().Get("Location"); loc != "/property/PID-UNIT" {
		t.Errorf("unit redirect = %q", loc)
	}
}

func TestStreetSearchShowsGroupedResults(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	seedPropertyWithNumber(t, app, "PID-1", "17", "ARNOLD", "KILLARA", "NSW", "2071")
	seedPropertyWithNumber(t, app, "PID-2", "19", "ARNOLD", "KILLARA", "NSW", "2071")
	seedPropertyWithNumber(t, app, "PID-3", "3", "ARNOLD", "ROTHBURY", "NSW", "2000")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/properties?address=Arnold+St+Killara", nil)
	app.handlePropertiesSearch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Streets matching") {
		t.Errorf("expected grouped street results")
	}
	if !strings.Contains(body, "ARNOLD ST") || !strings.Contains(body, "KILLARA") {
		t.Errorf("expected ARNOLD ST in KILLARA, got body")
	}
	if !strings.Contains(body, "2 properties") {
		t.Errorf("expected grouped property count")
	}
	if !strings.Contains(body, "street_type=ST") {
		t.Errorf("expected street selection link with street type")
	}
}

func TestStreetSelectionListsAllProperties(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	seedPropertyWithNumber(t, app, "PID-1", "17", "ARNOLD", "KILLARA", "NSW", "2071")
	seedPropertyWithNumber(t, app, "PID-2", "19", "ARNOLD", "KILLARA", "NSW", "2071")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/properties?street=ARNOLD&street_type=ST&suburb=KILLARA&state=NSW&postcode=2071&view=street", nil)
	app.handlePropertiesSearch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "PID-1") || !strings.Contains(body, "PID-2") {
		t.Errorf("expected both properties on the street")
	}
	if !strings.Contains(body, "Showing 1–2 of 2 properties") {
		t.Errorf("expected street pager summary")
	}
}

func TestPostcodePageSummarisesSales(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	seedPropertyWithNumber(t, app, "PID-1", "17", "ARNOLD", "KILLARA", "NSW", "2071")
	seedPropertyWithNumber(t, app, "PID-2", "19", "ARNOLD", "KILLARA", "NSW", "2071")
	seedSale(t, app, "SALE-1", "PID-1", "2025-01-10", 500000, "Auction")
	seedSale(t, app, "SALE-2", "PID-2", "2025-02-10", 700000, "Auction")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/postcode/2071", nil)
	req.SetPathValue("code", "2071")
	app.handlePostcodePage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Postcode 2071") {
		t.Errorf("expected postcode title")
	}
	if !strings.Contains(body, "Median sale price") || !strings.Contains(body, "$600,000") {
		t.Errorf("expected median sale price in postcode summary")
	}
	if !strings.Contains(body, "PID-1") || !strings.Contains(body, "PID-2") {
		t.Errorf("expected postcode properties")
	}
	if !strings.Contains(body, "KILLARA") {
		t.Errorf("expected suburb breakdown")
	}
}

func TestPostcodePageRejectsInvalidCode(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/postcode/abc", nil)
	req.SetPathValue("code", "abc")
	app.handlePostcodePage(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestSearchSuggestReturnsTypedEntries(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	seedPropertyWithNumber(t, app, "PID-1", "17", "ARNOLD", "KILLARA", "NSW", "2071")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/search/suggest?q=arnold", nil)
	app.handleSearchSuggest(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("street suggestion status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"kind":"street"`) {
		t.Errorf("expected a street suggestion, got %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/search/suggest?q=207", nil)
	app.handleSearchSuggest(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `"kind":"postcode"`) || !strings.Contains(body, "/postcode/2071") {
		t.Errorf("expected a postcode suggestion, got %s", body)
	}
}

func TestSuburbNameFallsBackToAddressSearch(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	// KILLARNEY is a street prefix match for KILLARA, but it is not the same
	// street. A suburb lookup must not be mistaken for that street.
	seedPropertyWithNumber(t, app, "PID-1", "1", "KILLARNEY", "KILLARA", "NSW", "2071")
	seedPropertyWithNumber(t, app, "PID-2", "2", "KILLARNEY", "KILLARA", "NSW", "2071")
	if err := database.RebuildAddressSearch(context.Background(), app.db); err != nil {
		t.Fatalf("rebuild address search: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/properties?address=Killara", nil)
	app.handlePropertiesSearch(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "Streets matching") {
		t.Errorf("suburb query was treated as a street search")
	}
	if !strings.Contains(body, "Search results") {
		t.Errorf("expected address results for the suburb query")
	}
}

func TestStreetPropertiesPagination(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	for i := 1; i <= 51; i++ {
		number := strconv.Itoa(i)
		seedPropertyWithNumber(t, app, "PID-"+number, number, "ARNOLD", "KILLARA", "NSW", "2071")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/properties?street=ARNOLD&street_type=ST&suburb=KILLARA&state=NSW&postcode=2071&view=street&page=2", nil)
	app.handlePropertiesSearch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Showing 51–51 of 51 properties") {
		t.Errorf("expected second-page summary, got body")
	}
	if !strings.Contains(body, "/property/PID-51") {
		t.Errorf("expected the 51st property on page 2")
	}
	if strings.Contains(body, "/property/PID-1\"") {
		t.Errorf("page 2 unexpectedly contains the first property")
	}
}

func TestStreetResultsIncludePropertiesWithoutNumbers(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	seedPropertyWithNumber(t, app, "PID-1", "17", "ARNOLD", "KILLARA", "NSW", "2071")
	seedPropertyWithNumber(t, app, "PID-NO-NUMBER", "", "ARNOLD", "KILLARA", "NSW", "2071")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/properties?street=ARNOLD&street_type=ST&suburb=KILLARA&state=NSW&postcode=2071&view=street", nil)
	app.handlePropertiesSearch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Showing 1–2 of 2 properties") {
		t.Errorf("expected both numbered and unnumbered properties")
	}
	if !strings.Contains(body, "/property/PID-NO-NUMBER") {
		t.Errorf("expected unnumbered property in street results")
	}
}

func TestSubmitRecordsAgainstCanonicalProperty(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	seedPropertyWithNumber(t, app, "GANSW_PSI_003_123", "14", "BELLBOWER", "GREEN POINT", "NSW", "2251")
	seedPropertyWithNumber(t, app, "GANSW_PSI_259_123", "14", "BELLBOWER", "GREEN POINT", "NSW", "2251")
	if _, err := app.db.Exec(`UPDATE gnaf_properties SET lot_number='29', plan_number='DP1045502' WHERE gnaf_pid IN ('GANSW_PSI_003_123','GANSW_PSI_259_123')`); err != nil {
		t.Fatalf("set lot/plan: %v", err)
	}
	seedSale(t, app, "S-OLD", "GANSW_PSI_259_123", "2020-01-01", 500000, "Public Registry")
	if err := database.RebuildPropertyLinks(context.Background(), app.db); err != nil {
		t.Fatalf("rebuild links: %v", err)
	}
	if err := database.RebuildCanonicalSales(context.Background(), app.db); err != nil {
		t.Fatalf("rebuild canonical sales: %v", err)
	}
	if err := database.RefreshStreetStats(context.Background(), app.db); err != nil {
		t.Fatalf("refresh street stats: %v", err)
	}
	if err := database.RefreshPostcodeStats(context.Background(), app.db); err != nil {
		t.Fatalf("refresh postcode stats: %v", err)
	}

	formRec := httptest.NewRecorder()
	app.handleSubmitForm(formRec, httptest.NewRequest(http.MethodGet, "/submit?gnaf_pid=GANSW_PSI_259_123", nil))
	var csrf string
	for _, c := range formRec.Result().Cookies() {
		if c.Name == "csrf_token" {
			csrf = c.Value
		}
	}
	if csrf == "" {
		t.Fatalf("form did not set a CSRF cookie")
	}

	form := url.Values{
		"csrf_token":      {csrf},
		"gnaf_pid":        {"GANSW_PSI_259_123"},
		"settlement_date": {"2026-01-01"},
		"sale_price":      {"700000"},
		"source_type":     {"Auction"},
	}
	req := httptest.NewRequest(http.MethodPost, "/submit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "csrf_token", Value: csrf})
	rec := httptest.NewRecorder()
	app.handleSubmitAction(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after submission, got %d: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/property/GANSW_PSI_003_123" {
		t.Errorf("redirect location = %q, want canonical property", loc)
	}

	var insertedPID string
	if err := app.db.QueryRow(`
		SELECT gnaf_pid FROM property_sales
		WHERE sale_price = 700000 AND settlement_date = '2026-01-01'
		ORDER BY sale_id DESC LIMIT 1`).Scan(&insertedPID); err != nil {
		t.Fatalf("query inserted sale: %v", err)
	}
	if insertedPID != "GANSW_PSI_003_123" {
		t.Errorf("inserted sale PID = %q, want canonical PID", insertedPID)
	}

	var canonicalCount int64
	if err := app.db.QueryRow("SELECT COUNT(*) FROM canonical_sales").Scan(&canonicalCount); err != nil {
		t.Fatalf("count canonical sales: %v", err)
	}
	if canonicalCount != 2 {
		t.Errorf("canonical sales = %d, want 2", canonicalCount)
	}

	var statsSales int64
	if err := app.db.QueryRow("SELECT sales_count FROM suburb_stats WHERE suburb = 'GREEN POINT' AND state = 'NSW'").Scan(&statsSales); err != nil {
		t.Fatalf("query suburb stats: %v", err)
	}
	if statsSales != 2 {
		t.Errorf("suburb_stats sales_count = %d, want 2", statsSales)
	}
	var streetSales, postcodeSales int64
	if err := app.db.QueryRow("SELECT sales_count FROM street_stats WHERE street_name = 'BELLBOWER' AND street_type = 'ST' AND suburb = 'GREEN POINT' AND state = 'NSW' AND postcode = '2251'").Scan(&streetSales); err != nil {
		t.Fatalf("query street stats: %v", err)
	}
	if streetSales != 2 {
		t.Errorf("street_stats sales_count = %d, want 2", streetSales)
	}
	if err := app.db.QueryRow("SELECT sales_count FROM postcode_stats WHERE postcode = '2251'").Scan(&postcodeSales); err != nil {
		t.Fatalf("query postcode stats: %v", err)
	}
	if postcodeSales != 2 {
		t.Errorf("postcode_stats sales_count = %d, want 2", postcodeSales)
	}
}

func TestFooterShowsLastSuccessfulImport(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	if _, err := app.db.Exec(`
		INSERT INTO import_runs (
			run_id, file_name, started_at, finished_at, status,
			properties_parsed, properties_written, sales_parsed, sales_written
		) VALUES ('RUN-1', '20260101.zip', '2026-01-01 10:00:00', '2026-01-01 10:05:00', 'success', 10, 10, 5, 5)
	`); err != nil {
		t.Fatalf("seed import run: %v", err)
	}

	rec := httptest.NewRecorder()
	app.handleHome(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Last successful data import: 1 Jan 2026") {
		t.Errorf("expected last import date in footer")
	}
}

func resultsSection(body string) string {
	start := strings.Index(body, `id="search-results"`)
	if start == -1 {
		return body
	}
	end := strings.Index(body[start:], `id="recent-sales"`)
	if end == -1 {
		return body[start:]
	}
	return body[start : start+end]
}

func TestAddressSearchPagination(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	for i := 1; i <= 60; i++ {
		seedPropertyWithNumber(t, app, "ADDR-"+strconv.Itoa(i), strconv.Itoa(i), "TEST", "TESTVILLE", "NSW", "2000")
	}
	if err := database.RebuildAddressSearch(context.Background(), app.db); err != nil {
		t.Fatalf("rebuild address search: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/properties?address=TESTVILLE&page=1", nil)
	app.handlePropertiesSearch(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("page 1 status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "60 address records") || !strings.Contains(body, "page 1 of 2") {
		t.Errorf("expected first page totals, got page")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/properties?address=TESTVILLE&page=2", nil)
	app.handlePropertiesSearch(rec, req)
	body = rec.Body.String()
	if !strings.Contains(body, "page 2 of 2") {
		t.Errorf("expected second page indicator")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/properties?address=TESTVILLE&page=99", nil)
	app.handlePropertiesSearch(rec, req)
	body = rec.Body.String()
	if !strings.Contains(body, "page 2 of 2") {
		t.Errorf("out-of-range page should clamp to the last page")
	}
}

func TestStreetGroupPagination(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	for i := 1; i <= 55; i++ {
		street := "PAGEST" + strconv.Itoa(i)
		seedPropertyWithNumber(t, app, "GROUP-"+strconv.Itoa(i), "1", street, "PAGETOWN", "NSW", "2000")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/properties?street=PAGEST&page=2", nil)
	app.handlePropertiesSearch(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "55 streets found") || !strings.Contains(body, "page 2 of 2") {
		t.Errorf("expected street group page 2 totals")
	}
	if !strings.Contains(body, "PAGEST55") {
		t.Errorf("expected the last group on page 2")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/properties?street=PAGEST&page=99", nil)
	app.handlePropertiesSearch(rec, req)
	if body := rec.Body.String(); !strings.Contains(body, "page 2 of 2") {
		t.Errorf("out-of-range street page should clamp")
	}
}

func TestSearchSuggestIncludesSuburbs(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	seedPropertyWithNumber(t, app, "PID-1", "1", "TEST", "KILLARA", "NSW", "2071")
	if err := database.RefreshSuburbStats(context.Background(), app.db); err != nil {
		t.Fatalf("refresh stats: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/search/suggest?q=kill", nil)
	app.handleSearchSuggest(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `"kind":"suburb"`) || !strings.Contains(body, "KILLARA") {
		t.Errorf("expected suburb suggestion, got %s", body)
	}
}

func TestStreetSummaryAndDuplicatePIDs(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	seedPropertyWithNumber(t, app, "GANSW_PSI_003_900", "1", "SUMMARY", "SUMMARYVILLE", "NSW", "2000")
	seedPropertyWithNumber(t, app, "GANSW_PSI_259_900", "1", "SUMMARY", "SUMMARYVILLE", "NSW", "2000")
	if _, err := app.db.Exec(`UPDATE gnaf_properties SET lot_number='1', plan_number='DP1' WHERE gnaf_pid IN ('GANSW_PSI_003_900','GANSW_PSI_259_900')`); err != nil {
		t.Fatalf("set lot/plan: %v", err)
	}
	seedPropertyWithNumber(t, app, "PID-3", "3", "SUMMARY", "SUMMARYVILLE", "NSW", "2000")
	seedSale(t, app, "S-1", "GANSW_PSI_259_900", "2025-01-01", 600000, "Auction")
	if err := database.RebuildPropertyLinks(context.Background(), app.db); err != nil {
		t.Fatalf("rebuild links: %v", err)
	}
	if err := database.RebuildCanonicalSales(context.Background(), app.db); err != nil {
		t.Fatalf("rebuild canonical sales: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/properties?street=SUMMARY&street_type=ST&suburb=SUMMARYVILLE&state=NSW&postcode=2000&view=street", nil)
	app.handlePropertiesSearch(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Recorded sales") || !strings.Contains(body, "$600,000") {
		t.Errorf("expected street summary KPI cards")
	}
	if !strings.Contains(body, "Showing 1–2 of 2 properties") {
		t.Errorf("expected duplicate PIDs to collapse to one canonical property")
	}
}

func TestStreetAndPostcodeFilters(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	seedPropertyWithNumber(t, app, "PID-HOUSE", "1", "FILTER", "FILTERVILLE", "NSW", "2000")
	seedPropertyWithNumber(t, app, "PID-UNIT", "2", "FILTER", "FILTERVILLE", "NSW", "2000")
	seedPropertyWithNumber(t, app, "PID-NOSALE", "3", "FILTER", "FILTERVILLE", "NSW", "2000")
	if _, err := app.db.Exec(`UPDATE gnaf_properties SET dwelling_type='house' WHERE gnaf_pid='PID-HOUSE'`); err != nil {
		t.Fatalf("set house type: %v", err)
	}
	if _, err := app.db.Exec(`UPDATE gnaf_properties SET dwelling_type='unit' WHERE gnaf_pid IN ('PID-UNIT','PID-NOSALE')`); err != nil {
		t.Fatalf("set unit type: %v", err)
	}
	seedSale(t, app, "S-HOUSE", "PID-HOUSE", "2024-01-01", 500000, "Auction")
	seedSale(t, app, "S-UNIT", "PID-UNIT", "2025-01-01", 700000, "Auction")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/properties?street=FILTER&street_type=ST&suburb=FILTERVILLE&state=NSW&postcode=2000&view=street&dwelling=unit&only_with_sales=1&sort=price", nil)
	app.handlePropertiesSearch(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("street filter status: %d", rec.Code)
	}
	body := resultsSection(rec.Body.String())
	if !strings.Contains(body, "/property/PID-UNIT") {
		t.Errorf("expected the matching unit with a sale")
	}
	if strings.Contains(body, "/property/PID-NOSALE") || strings.Contains(body, "/property/PID-HOUSE") {
		t.Errorf("street filters leaked non-matching properties")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/postcode/2000?dwelling=unit&only_with_sales=1&sort=price", nil)
	req.SetPathValue("code", "2000")
	app.handlePostcodePage(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("postcode filter status: %d", rec.Code)
	}
	body = rec.Body.String()
	if !strings.Contains(body, "/property/PID-UNIT") {
		t.Errorf("expected postcode unit result")
	}
	if strings.Contains(body, "/property/PID-NOSALE") || strings.Contains(body, "/property/PID-HOUSE") {
		t.Errorf("postcode filters leaked non-matching properties (unexpected result row)")
	}
}

func TestAddressSearchFiltersAndPagination(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	seedPropertyWithNumber(t, app, "PID-HOUSE", "1", "TEST", "TESTVILLE", "NSW", "2000")
	seedPropertyWithNumber(t, app, "PID-UNIT", "2", "TEST", "TESTVILLE", "NSW", "2000")
	seedPropertyWithNumber(t, app, "PID-NOSALE", "3", "TEST", "TESTVILLE", "NSW", "2000")
	seedPropertyWithNumber(t, app, "PID-UNIT-2", "4", "TEST", "TESTVILLE", "NSW", "2000")
	if _, err := app.db.Exec(`UPDATE gnaf_properties SET dwelling_type='house' WHERE gnaf_pid='PID-HOUSE'`); err != nil {
		t.Fatalf("set house type: %v", err)
	}
	if _, err := app.db.Exec(`UPDATE gnaf_properties SET dwelling_type='unit' WHERE gnaf_pid IN ('PID-UNIT','PID-UNIT-2','PID-NOSALE')`); err != nil {
		t.Fatalf("set unit type: %v", err)
	}
	seedSale(t, app, "S-HOUSE", "PID-HOUSE", "2024-01-01", 500000, "Auction")
	seedSale(t, app, "S-UNIT", "PID-UNIT", "2025-01-01", 700000, "Auction")
	seedSale(t, app, "S-UNIT-2", "PID-UNIT-2", "2025-02-01", 650000, "Auction")
	if err := database.RebuildAddressSearch(context.Background(), app.db); err != nil {
		t.Fatalf("rebuild address search: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/properties?address=TESTVILLE&dwelling=unit&only_with_sales=1&sort=price", nil)
	app.handlePropertiesSearch(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := resultsSection(rec.Body.String())
	if !strings.Contains(body, "/property/PID-UNIT") {
		t.Errorf("expected matching unit in address results")
	}
	if strings.Contains(body, "/property/PID-NOSALE") || strings.Contains(body, "/property/PID-HOUSE") {
		t.Errorf("address filters leaked non-matching properties")
	}
}

func TestPropertyIDSearchRedirects(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	seedPropertyWithNumber(t, app, "GANSW12345678", "1", "TEST", "TESTVILLE", "NSW", "2000")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/properties?address=GANSW12345678", nil)
	app.handlePropertiesSearch(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected property ID redirect, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/property/GANSW12345678" {
		t.Errorf("redirect location = %q", loc)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/search/suggest?q=GANSW12345678", nil)
	app.handleSearchSuggest(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `"kind":"property"`) || !strings.Contains(body, "GANSW12345678") {
		t.Errorf("expected property suggestion, got %s", body)
	}
}

func TestSearchErrorsRenderServiceUnavailable(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	if err := app.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/properties?address=TESTVILLE", nil)
	app.handlePropertiesSearch(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Search unavailable") {
		t.Errorf("expected search error notice")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/postcode/2000", nil)
	req.SetPathValue("code", "2000")
	app.handlePostcodePage(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("postcode status = %d, want 503", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Postcode 2000") || !strings.Contains(body, "Postcode unavailable") {
		t.Errorf("expected postcode error context")
	}
}

func TestLossPaginationAndSort(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	oldDate := time.Now().AddDate(-5, 0, 0)
	recentDate := time.Now().AddDate(-2, 0, 0)
	for i := 1; i <= 55; i++ {
		pid := "LOSS-" + strconv.Itoa(i)
		street := "LOSS" + strconv.Itoa(i)
		seedPropertyWithNumber(t, app, pid, strconv.Itoa(i), street, "LOSSTOWN", "NSW", "2000")
		seedSale(t, app, "S-OLD-"+strconv.Itoa(i), pid, oldDate.AddDate(0, 0, -i).Format("2006-01-02"), 500000, "Public Registry")
		seedSale(t, app, "S-NEW-"+strconv.Itoa(i), pid, recentDate.AddDate(0, 0, -i).Format("2006-01-02"), 400000, "Public Registry")
	}
	if _, err := app.db.Exec(`UPDATE gnaf_properties SET dwelling_type='house' WHERE suburb='LOSSTOWN'`); err != nil {
		t.Fatalf("set dwelling type: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/suburbs/losses?suburb=LOSSTOWN&state=NSW&range=10&sort=date&type=house&page=2", nil)
	app.handleSuburbLosses(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Showing 51–55 of 55 properties") {
		t.Errorf("expected paginated loss results, got page")
	}
	if !strings.Contains(body, "Page 2") && !strings.Contains(body, "LOSS") {
		t.Errorf("expected page 2 loss rows")
	}
}

func TestSuggestionStatsAndCache(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	seedPropertyWithNumber(t, app, "PID-1", "1", "ARNOLD", "KILLARA", "NSW", "2071")
	seedSale(t, app, "S-1", "PID-1", "2025-01-01", 500000, "Auction")
	if err := database.RefreshStreetStats(context.Background(), app.db); err != nil {
		t.Fatalf("refresh street stats: %v", err)
	}
	if err := database.RefreshPostcodeStats(context.Background(), app.db); err != nil {
		t.Fatalf("refresh postcode stats: %v", err)
	}

	first := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/search/suggest?q=arnold", nil)
	app.handleSearchSuggest(first, req)
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"kind":"street"`) {
		t.Fatalf("expected street suggestion, got %d: %s", first.Code, first.Body.String())
	}

	// The second identical request should be served from the in-memory cache,
	// even after the database becomes unavailable.
	if err := app.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	second := httptest.NewRecorder()
	app.handleSearchSuggest(second, req)
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), `"kind":"street"`) {
		t.Errorf("cached suggestion failed after DB close: %d: %s", second.Code, second.Body.String())
	}
}

func TestSuggestionLengthAndRateLimit(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	longQuery := strings.Repeat("a", maxSearchQueryLength+1)
	rec := httptest.NewRecorder()
	app.handleSearchSuggest(rec, httptest.NewRequest(http.MethodGet, "/api/search/suggest?q="+longQuery, nil))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("long query was not rejected: %d %s", rec.Code, rec.Body.String())
	}

	app.suggestLimiter = newIPRateLimiter(1, time.Minute)
	rec = httptest.NewRecorder()
	app.handleSearchSuggest(rec, httptest.NewRequest(http.MethodGet, "/api/search/suggest?q=ab", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("first suggestion status: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	app.handleSearchSuggest(rec, httptest.NewRequest(http.MethodGet, "/api/search/suggest?q=bc", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("second suggestion status = %d, want 429", rec.Code)
	}
}

func TestMaterializedStatsLifecycleAndReadiness(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	app.ensureMaterializedStats()
	status, updated, lastErr := app.statsSnapshot()
	if status != "ready" {
		t.Fatalf("stats status = %q (%s), want ready", status, lastErr)
	}
	if updated == "" {
		t.Errorf("expected stats update timestamp")
	}

	rec := httptest.NewRecorder()
	app.handleReadyz(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("readyz status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"stats_status":"ready"`) {
		t.Errorf("readyz did not expose stats status: %s", rec.Body.String())
	}
}
