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
	if !strings.Contains(body, "/property/GANSW_PSI_259_123") {
		t.Errorf("expected latest sale's canonical property ID to be linked")
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

	req := httptest.NewRequest(http.MethodGet, "/property/GANSW_PSI_003_2981264", nil)
	req.SetPathValue("pid", "GANSW_PSI_003_2981264")
	rec := httptest.NewRecorder()
	app.handlePropertyDetail(rec, req)
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("expected old PID to redirect, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/property/GANSW_PSI_259_2981264" {
		t.Errorf("redirect location = %q", loc)
	}

	req = httptest.NewRequest(http.MethodGet, "/property/GANSW_PSI_259_2981264", nil)
	req.SetPathValue("pid", "GANSW_PSI_259_2981264")
	rec = httptest.NewRecorder()
	app.handlePropertyDetail(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("canonical page status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "6 Jun 2013") || !strings.Contains(body, "26 Apr 2021") {
		t.Errorf("merged sale history missing dates")
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
