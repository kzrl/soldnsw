package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"soldproperty/internal/database"
)

const (
	suburbSalesPageSize = 50
	minSaleDate         = "1990-01-01"
	maxFutureSettlement = 30 * 24 * time.Hour
	maxRequestBodyBytes = 64 << 10
)

// App holds application state, database connection, templates, and queries.
type App struct {
	db        *sql.DB
	queries   *database.Queries
	templates map[string]*template.Template
	flashKey  []byte
	limiter   *ipRateLimiter
}

// FlashMessage structure for user feedback.
type FlashMessage struct {
	Type    string // success, danger, warning, info
	Message string
}

// SaleWithChange is a sale plus its movement from the previous recorded sale.
type SaleWithChange struct {
	SaleID         string
	SettlementDate string
	SalePrice      int64
	SourceType     string
	ChangeAmount   int64
	ChangePercent  float64
	HasChange      bool
	IsLatest       bool
}

// LossTypeCount summarises loss-making resales for one dwelling type.
type LossTypeCount struct {
	Key     string
	Label   string
	Losses  int64
	Resales int64
	Rate    float64
}

// PageData represents common and view-specific template data.
type PageData struct {
	Title              string
	ActiveNav          string
	Flash              *FlashMessage
	QueryAddress       string
	QuerySuburb        string
	QueryStreet        string
	QueryNumber        string
	QueryState         string
	QueryPostcode      string
	QueryDwellingType  string
	HasSearched        bool
	Properties         []database.GnafProperty
	RecentSales        []database.ListRecentSalesWithAddressRow
	TotalProperties    int64
	TotalSales         int64
	TotalSuburbs       int
	Property           database.GnafProperty
	IsPSI              bool
	Sales              []database.PropertySale
	SelectedSuburb     string
	SelectedState      string
	Analytics          *database.GetSuburbAnalyticsRow
	MaxSale            *database.GetSuburbExtremeSalesRow
	MinSale            *database.GetSuburbExtremeSalesRow
	OverviewStats      []database.GetSuburbOverviewStatsRow
	OverviewTotal      int
	SuburbSales        []database.GetSuburbSalesBySuburbRow
	SuburbSalesTotal   int64
	ShowingFrom        int
	ShowingTo          int
	Page               int
	TotalPages         int
	HasPrev            bool
	HasNext            bool
	PrevPage           int
	NextPage           int
	ChartLabels        []string
	ChartValues        []int64
	ChartRange         string
	PriceRange         string
	PriceWindow        string
	PriceLabel         string
	PriceNote          string
	SalesWithChange    []SaleWithChange
	TotalChangeAmount  int64
	TotalChangePercent float64
	HasTotalChange     bool
	HasDataWarning     bool
	DataWarningPercent float64
	LatestSaleDate     string
	TopSuburbs         []database.GetTopSuburbsRow
	BuildingBasePID    string
	BuildingAddress    string
	BuildingSuburb     string
	BuildingState      string
	BuildingUnits      []database.GetBuildingUnitsByBasePIDRow
	BuildingUnitCount  int
	BuildingSalesCount int64
	LossSummary        *database.GetSuburbLossSummaryRow
	LossSales          []database.GetSuburbLossesRow
	LossTotal          int64
	LossRate           float64
	LossRange          string
	LossSort           string
	LossType           string
	LossTypeCounts     []LossTypeCount
	PreFillPID         string
	PrefillProperty    *database.GnafProperty
	TodayDate          string
	MaxSaleDate        string
	CSRFToken          string
}

// Custom template helper functions.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"formatPrice": func(price any) string {
			if s, ok := price.(string); ok {
				if _, err := strconv.ParseInt(s, 10, 64); err != nil {
					return s
				}
			}
			return formatPriceInt(priceToInt(price))
		},
		"formatPriceInt": formatPriceInt,
		"formatSignedPrice": func(v any) string {
			n := priceToInt(v)
			if n == 0 {
				return "$0"
			}
			sign := "+"
			if n < 0 {
				sign = "-"
				n = -n
			}
			return sign + "$" + formatPriceInt(n)
		},
		"toPriceInt":  priceToInt,
		"formatCount": formatCount,
		"formatDate":  formatDate,
		"formatArea":  formatArea,
		"dwellingLabel": func(value string) string {
			switch value {
			case "house":
				return "House"
			case "unit":
				return "Unit"
			case "townhouse":
				return "Townhouse"
			case "other":
				return "Other"
			default:
				return "Unknown"
			}
		},
		"absPrice": func(v any) string {
			n := priceToInt(v)
			if n < 0 {
				n = -n
			}
			return formatPriceInt(n)
		},
		"formatHolding": func(v any) string {
			days := priceToInt(v)
			if days < 0 {
				days = -days
			}
			if days < 30 {
				return fmt.Sprintf("%d days", days)
			}
			if days < 365 {
				return fmt.Sprintf("%d mo", days/30)
			}
			return fmt.Sprintf("%.1f yr", float64(days)/365.25)
		},
		"sub": func(a, b int) int {
			return a - b
		},
		"int64": priceToInt,
	}
}

// priceToInt normalises the various numeric types returned by SQLite/sqlc.
func priceToInt(val any) int64 {
	switch v := val.(type) {
	case nil:
		return 0
	case sql.NullFloat64:
		if v.Valid {
			return int64(v.Float64)
		}
		return 0
	case sql.NullInt64:
		if v.Valid {
			return v.Int64
		}
		return 0
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case []byte:
		p, _ := strconv.ParseInt(string(v), 10, 64)
		return p
	case string:
		p, _ := strconv.ParseInt(v, 10, 64)
		return p
	default:
		return 0
	}
}

// formatCount renders large counts compactly for stat cards.
func formatCount(v any) string {
	n := priceToInt(v)
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.2fM", float64(n)/1_000_000)
	case n >= 10_000:
		return fmt.Sprintf("%.0fK", float64(n)/1_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	default:
		return strconv.FormatInt(n, 10)
	}
}

// formatArea renders a square-metre land area, switching to hectares for
// large parcels.
func formatArea(v any) string {
	var area float64
	switch value := v.(type) {
	case sql.NullFloat64:
		if !value.Valid {
			return ""
		}
		area = value.Float64
	case float64:
		area = value
	case int64:
		area = float64(value)
	case int:
		area = float64(value)
	case string:
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return ""
		}
		area = parsed
	default:
		return ""
	}
	if area <= 0 {
		return ""
	}
	if area >= 10000 {
		return fmt.Sprintf("%.2f ha", area/10000)
	}
	if area == float64(int64(area)) {
		return formatPriceInt(int64(area)) + " m²"
	}
	return fmt.Sprintf("%.1f m²", area)
}

// formatDate turns an ISO settlement date into a human-readable date.
func formatDate(value any) string {
	s, ok := value.(string)
	if !ok || s == "" {
		return ""
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return s
	}
	return t.Format("2 Jan 2006")
}

// propertyAddress formats a property's street address without leading spaces.
func propertyAddress(prop database.GnafProperty) string {
	parts := make([]string, 0, 3)
	if prop.StreetNumber.Valid && strings.TrimSpace(prop.StreetNumber.String) != "" {
		parts = append(parts, strings.TrimSpace(prop.StreetNumber.String))
	}
	if strings.TrimSpace(prop.StreetName) != "" {
		parts = append(parts, strings.TrimSpace(prop.StreetName))
	}
	if strings.TrimSpace(prop.StreetType) != "" {
		parts = append(parts, strings.TrimSpace(prop.StreetType))
	}
	return strings.Join(parts, " ")
}

// formatPriceInt formats an int64 price with comma separators: 2450000 -> "2,450,000".
func formatPriceInt(price int64) string {
	if price < 0 {
		return "-" + formatPriceInt(-price)
	}
	str := strconv.FormatInt(price, 10)
	n := len(str)
	if n <= 3 {
		return str
	}
	var b strings.Builder
	remainder := n % 3
	if remainder > 0 {
		b.WriteString(str[:remainder])
		b.WriteString(",")
	}
	for i := remainder; i < n; i += 3 {
		b.WriteString(str[i : i+3])
		if i+3 < n {
			b.WriteString(",")
		}
	}
	return b.String()
}

func main() {
	port := flag.String("port", "8080", "HTTP server port")
	dbPath := flag.String("db", "properties.db", "Path to SQLite database")
	templateDir := flag.String("templates", "templates", "Directory containing SSR HTML templates")
	staticDir := flag.String("static", "static", "Directory containing static assets")
	flag.Parse()

	// Environment variable overrides.
	if envPort := os.Getenv("PORT"); envPort != "" {
		*port = envPort
	}
	if envDB := os.Getenv("DB_PATH"); envDB != "" {
		*dbPath = envDB
	}

	log.Printf("Connecting to SQLite database at: %s", *dbPath)
	db, err := sql.Open("sqlite3", sqliteDSN(*dbPath))
	if err != nil {
		log.Fatalf("Failed to open SQLite database: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(4)

	if err := db.Ping(); err != nil {
		log.Fatalf("Failed to connect to SQLite database: %v", err)
	}

	// Schema is embedded, so the binary works from any working directory.
	if _, err := db.Exec(database.SchemaSQL); err != nil {
		log.Fatalf("Failed to apply database schema: %v", err)
	}
	if err := database.EnsurePropertyColumns(context.Background(), db); err != nil {
		log.Fatalf("Failed to migrate property columns: %v", err)
	}
	if err := database.EnsureDwellingTypes(context.Background(), db); err != nil {
		log.Fatalf("Failed to classify dwelling types: %v", err)
	}
	if err := database.EnsureSuburbStatsSchema(context.Background(), db); err != nil {
		log.Fatalf("Failed to migrate suburb stats schema: %v", err)
	}
	linksRebuilt, err := database.EnsurePropertyLinks(context.Background(), db)
	if err != nil {
		log.Fatalf("Failed to build property links: %v", err)
	}
	if linksRebuilt {
		log.Printf("Canonical property links rebuilt; refreshing analytics and address search...")
		if err := database.RefreshSuburbStats(context.Background(), db); err != nil {
			log.Printf("Warning: suburb statistics refresh failed: %v", err)
		}
		if err := database.RebuildAddressSearch(context.Background(), db); err != nil {
			log.Printf("Warning: address search rebuild failed: %v", err)
		}
	}
	if err := database.EnsureAddressSearch(context.Background(), db); err != nil {
		log.Fatalf("Failed to build address search index: %v", err)
	}

	tmpls, err := parseTemplates(*templateDir)
	if err != nil {
		log.Fatalf("Failed to parse templates: %v", err)
	}

	app, err := newApp(db, tmpls)
	if err != nil {
		log.Fatalf("Failed to initialise app: %v", err)
	}

	mux := http.NewServeMux()
	fs := http.FileServer(http.Dir(*staticDir))
	mux.Handle("GET /static/", http.StripPrefix("/static/", fs))

	mux.HandleFunc("GET /", app.handleHome)
	mux.HandleFunc("GET /properties", app.handlePropertiesSearch)
	mux.HandleFunc("GET /api/address/suggest", app.handleAddressSuggest)
	mux.HandleFunc("GET /property/{pid}", app.handlePropertyDetail)
	mux.HandleFunc("GET /building/{base_pid}", app.handleBuilding)
	mux.HandleFunc("GET /suburbs", app.handleSuburbAnalytics)
	mux.HandleFunc("GET /suburbs/losses", app.handleSuburbLosses)
	mux.HandleFunc("GET /submit", app.handleSubmitForm)
	mux.HandleFunc("POST /submit", app.handleSubmitAction)

	handler := withSecurityHeaders(withRequestLogging(mux))
	srv := &http.Server{
		Addr:              ":" + *port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Warm expensive aggregate caches in the background so the first user
	// does not pay for the full 2.9M-property scan.
	go app.ensureSuburbStats()

	log.Printf("========================================================")
	log.Printf(" SoldNSW Server listening on http://localhost%s", srv.Addr)
	log.Printf("========================================================")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		log.Printf("Shutting down gracefully...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("Server failed: %v", err)
	}
}

// sqliteDSN configures every pooled connection with the required pragmas.
func sqliteDSN(path string) string {
	dsn := path
	if !strings.HasPrefix(dsn, "file:") {
		dsn = "file:" + dsn
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "_busy_timeout=5000&_foreign_keys=on&_journal_mode=WAL&_synchronous=NORMAL"
}

// parseTemplates builds one template set per page. Parsing all views into a
// single namespace makes their identically named "title"/"content" defines
// overwrite each other, so every route would render the last page parsed.
func parseTemplates(dir string) (map[string]*template.Template, error) {
	pages := []string{"index.html", "property.html", "suburb.html", "submit.html", "losses.html", "building.html", "404.html"}
	sets := make(map[string]*template.Template, len(pages))
	for _, page := range pages {
		t, err := template.New("base.html").Funcs(templateFuncs()).ParseFiles(
			filepath.Join(dir, "base.html"),
			filepath.Join(dir, page),
		)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", page, err)
		}
		sets[page] = t
	}
	return sets, nil
}

func newApp(db *sql.DB, tmpls map[string]*template.Template) (*App, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate flash key: %w", err)
	}
	return &App{
		db:        db,
		queries:   database.New(db),
		templates: tmpls,
		flashKey:  key,
		limiter:   newIPRateLimiter(5, time.Minute),
	}, nil
}

// ensureSuburbStats populates the materialised suburb summary on a fresh
// database. The /suburbs page shows a "calculating" notice while this runs.
func (app *App) ensureSuburbStats() {
	var count int
	if err := app.db.QueryRow("SELECT COUNT(*) FROM suburb_stats").Scan(&count); err != nil {
		log.Printf("Suburb stats check failed: %v", err)
		return
	}
	if count > 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	log.Printf("Building suburb statistics for the first time; this can take a moment on a large dataset...")
	if err := database.RefreshSuburbStats(ctx, app.db); err != nil {
		log.Printf("Suburb stats build failed: %v", err)
		return
	}
	log.Printf("Suburb statistics ready")
}

// renderNotFound renders the branded 404 page.
func (app *App) renderNotFound(w http.ResponseWriter, r *http.Request) {
	topSuburbs, _ := app.queries.GetTopSuburbs(r.Context(), 8)
	data := PageData{
		Title:      "Page not found - SoldNSW",
		TopSuburbs: topSuburbs,
		Flash:      app.readFlash(w, r),
	}
	app.renderTemplateStatus(w, "404.html", data, http.StatusNotFound)
}

// lossCutoff returns the earliest latest-sale date for a range selector.
func lossCutoff(rangeValue string) string {
	if rangeValue == "all" {
		return minSaleDate
	}
	years, err := strconv.Atoi(rangeValue)
	if err != nil || years <= 0 {
		years = 10
	}
	return time.Now().AddDate(-years, 0, 0).Format("2006-01-02")
}

// priceRangeLabel describes a KPI range selector value.
func priceRangeLabel(rangeValue string) string {
	switch rangeValue {
	case "1":
		return "Last 12 months"
	case "5":
		return "Last 5 years"
	case "10":
		return "Last 10 years"
	default:
		return "All time"
	}
}

func priceRangeCandidates(requested string) []string {
	switch requested {
	case "1":
		return []string{"1", "5", "all"}
	case "5":
		return []string{"5", "10", "all"}
	case "10":
		return []string{"10", "all"}
	default:
		return []string{"all"}
	}
}

// suburbPriceStats returns the requested median/average statistics, falling
// back to a longer window when the requested window has fewer than 20 sales.
func (app *App) suburbPriceStats(ctx context.Context, suburb, state, requested string) (database.GetSuburbAnalyticsRow, string, int64, error) {
	candidates := priceRangeCandidates(requested)
	var requestedCount int64
	for i, candidate := range candidates {
		rows, err := app.queries.GetSuburbAnalytics(ctx, database.GetSuburbAnalyticsParams{
			Suburb: suburb,
			State:  state,
			Cutoff: lossCutoff(candidate),
		})
		if err != nil {
			return database.GetSuburbAnalyticsRow{}, requested, requestedCount, err
		}
		if len(rows) == 0 {
			continue
		}
		if i == 0 {
			requestedCount = rows[0].TotalSales
		}
		if rows[0].TotalSales >= 20 || candidate == "all" || i == len(candidates)-1 {
			return rows[0], candidate, requestedCount, nil
		}
	}
	return database.GetSuburbAnalyticsRow{}, requested, requestedCount, nil
}

// handleHome renders the landing search and recent sales feed.
func (app *App) handleHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		app.renderNotFound(w, r)
		return
	}

	ctx := r.Context()
	recentSales, err := app.queries.ListRecentSalesWithAddress(ctx)
	if err != nil {
		log.Printf("Error fetching recent sales: %v", err)
	}

	totalProps, _ := app.queries.GetTotalPropertiesCount(ctx)
	totalSales, _ := app.queries.GetTotalSalesCount(ctx)
	totalSuburbs, _ := database.CountSuburbs(ctx, app.db)
	topSuburbs, _ := app.queries.GetTopSuburbs(ctx, 8)

	latestSaleDate := ""
	if len(recentSales) > 0 {
		latestSaleDate = recentSales[0].SettlementDate
	}

	data := PageData{
		Title:           "Australian Property Sales & Suburb Trends",
		ActiveNav:       "search",
		RecentSales:     recentSales,
		TotalProperties: totalProps,
		TotalSales:      totalSales,
		TotalSuburbs:    int(totalSuburbs),
		LatestSaleDate:  latestSaleDate,
		TopSuburbs:      topSuburbs,
		Flash:           app.readFlash(w, r),
	}

	app.renderTemplate(w, "index.html", data)
}

// handleAddressSuggest returns autocomplete matches for the homepage search box.
func (app *App) handleAddressSuggest(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if len([]rune(q)) < 2 {
		_, _ = w.Write([]byte("[]"))
		return
	}
	matches, err := database.SearchAddressFTS(r.Context(), app.db, q, 10)
	if err != nil {
		log.Printf("Address suggestion error: %v", err)
		http.Error(w, "Address suggestions unavailable", http.StatusInternalServerError)
		return
	}
	if matches == nil {
		matches = []database.AddressMatch{}
	}
	_ = json.NewEncoder(w).Encode(matches)
}

func validPostcode(value string) bool {
	if len(value) != 4 {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func normalizedStreetNumber(value string) string {
	return strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(value)), " ", "")
}

// handlePropertiesSearch handles both free-text address searches and the
// structured suburb/street/number/state/postcode form.
func (app *App) handlePropertiesSearch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	address := strings.TrimSpace(r.URL.Query().Get("address"))
	suburb := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("suburb")))
	street := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("street")))
	number := normalizedStreetNumber(r.URL.Query().Get("number"))
	state := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("state")))
	postcode := strings.TrimSpace(r.URL.Query().Get("postcode"))
	dwellingType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("dwelling")))
	switch dwellingType {
	case "house", "unit", "townhouse", "other":
	default:
		dwellingType = ""
	}
	flash := app.readFlash(w, r)

	if address != "" {
		matches, err := database.SearchAddressFTS(ctx, app.db, address, 50)
		if err != nil {
			log.Printf("Address search error for %q: %v", address, err)
		}
		normalizedAddress := strings.Join(strings.Fields(strings.ToUpper(address)), " ")
		for _, match := range matches {
			if strings.Join(strings.Fields(match.Address), " ") == normalizedAddress {
				http.Redirect(w, r, "/property/"+match.GnafPID, http.StatusSeeOther)
				return
			}
		}
		if len(matches) == 1 {
			http.Redirect(w, r, "/property/"+matches[0].GnafPID, http.StatusSeeOther)
			return
		}
		props := make([]database.GnafProperty, 0, len(matches))
		for _, match := range matches {
			prop, err := app.queries.GetPropertyByPID(ctx, match.GnafPID)
			if err != nil {
				continue
			}
			if dwellingType != "" {
				if dwellingType == "other" {
					if prop.DwellingType != "other" && prop.DwellingType != "unknown" {
						continue
					}
				} else if prop.DwellingType != dwellingType {
					continue
				}
			}
			props = append(props, prop)
		}
		recentSales, _ := app.queries.ListRecentSalesWithAddress(ctx)
		totalProps, _ := app.queries.GetTotalPropertiesCount(ctx)
		totalSales, _ := app.queries.GetTotalSalesCount(ctx)
		totalSuburbs, _ := database.CountSuburbs(ctx, app.db)
		latestSaleDate := ""
		if len(recentSales) > 0 {
			latestSaleDate = recentSales[0].SettlementDate
		}
		data := PageData{
			Title:             "Address Search Results - SoldNSW",
			ActiveNav:         "search",
			QueryAddress:      address,
			QueryDwellingType: dwellingType,
			HasSearched:       true,
			Properties:        props,
			RecentSales:       recentSales,
			TotalProperties:   totalProps,
			TotalSales:        totalSales,
			TotalSuburbs:      int(totalSuburbs),
			LatestSaleDate:    latestSaleDate,
			Flash:             flash,
		}
		app.renderTemplate(w, "index.html", data)
		return
	}

	if number != "" && street == "" {
		number = ""
		flash = &FlashMessage{Type: "warning", Message: "Add a street name when searching by street number."}
	}
	if postcode != "" && !validPostcode(postcode) {
		postcode = ""
		flash = &FlashMessage{Type: "warning", Message: "Postcode must be four digits."}
	}

	if suburb == "" && street == "" && state == "" && postcode == "" {
		if flash != nil {
			recentSales, _ := app.queries.ListRecentSalesWithAddress(ctx)
			totalProps, _ := app.queries.GetTotalPropertiesCount(ctx)
			totalSales, _ := app.queries.GetTotalSalesCount(ctx)
			totalSuburbs, _ := database.CountSuburbs(ctx, app.db)
			topSuburbs, _ := app.queries.GetTopSuburbs(ctx, 8)
			latestSaleDate := ""
			if len(recentSales) > 0 {
				latestSaleDate = recentSales[0].SettlementDate
			}
			app.renderTemplate(w, "index.html", PageData{
				Title:           "NSW Property Sales & Suburb Trends - SoldNSW",
				ActiveNav:       "search",
				RecentSales:     recentSales,
				TotalProperties: totalProps,
				TotalSales:      totalSales,
				TotalSuburbs:    int(totalSuburbs),
				LatestSaleDate:  latestSaleDate,
				TopSuburbs:      topSuburbs,
				Flash:           flash,
			})
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	// SearchPropertiesFlexible handles every combination of address fields.
	streetPattern := "%"
	if street != "" {
		streetPattern = street + "%"
	}
	numberPattern := "%"
	numberExact := ""
	if number != "" {
		numberPattern = number + "%"
		numberExact = number
	}
	props, err := app.queries.SearchPropertiesFlexible(ctx, database.SearchPropertiesFlexibleParams{
		Suburb:        suburb,
		StreetPattern: streetPattern,
		State:         state,
		Postcode:      postcode,
		NumberPattern: numberPattern,
		NumberExact:   numberExact,
		DwellingType:  dwellingType,
	})
	if err != nil {
		log.Printf("Search error for suburb '%s', street '%s', number '%s', postcode '%s': %v", suburb, street, number, postcode, err)
	}

	exactNumberMatch := len(props) > 0 && number != "" && (suburb != "" || postcode != "") &&
		normalizedStreetNumber(props[0].StreetNumber.String) == number
	if len(props) == 1 || exactNumberMatch {
		http.Redirect(w, r, "/property/"+props[0].GnafPid, http.StatusSeeOther)
		return
	}

	recentSales, _ := app.queries.ListRecentSalesWithAddress(ctx)
	totalProps, _ := app.queries.GetTotalPropertiesCount(ctx)
	totalSales, _ := app.queries.GetTotalSalesCount(ctx)
	totalSuburbs, _ := database.CountSuburbs(ctx, app.db)

	latestSaleDate := ""
	if len(recentSales) > 0 {
		latestSaleDate = recentSales[0].SettlementDate
	}

	data := PageData{
		Title:             "Search Results - SoldNSW",
		ActiveNav:         "search",
		QuerySuburb:       suburb,
		QueryStreet:       street,
		QueryNumber:       number,
		QueryState:        state,
		QueryPostcode:     postcode,
		QueryDwellingType: dwellingType,
		HasSearched:       true,
		Properties:        props,
		RecentSales:       recentSales,
		TotalProperties:   totalProps,
		TotalSales:        totalSales,
		TotalSuburbs:      int(totalSuburbs),
		LatestSaleDate:    latestSaleDate,
		Flash:             flash,
	}

	app.renderTemplate(w, "index.html", data)
}

// handlePropertyDetail displays property metadata, map (when geocoded) and sales history.
func (app *App) handlePropertyDetail(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	if pid == "" {
		app.renderNotFound(w, r)
		return
	}

	ctx := r.Context()
	canonicalPID, err := database.GetCanonicalPID(ctx, app.db, pid)
	if err != nil {
		log.Printf("Error resolving canonical PID for '%s': %v", pid, err)
		canonicalPID = pid
	}
	if canonicalPID != "" && canonicalPID != pid {
		http.Redirect(w, r, "/property/"+canonicalPID, http.StatusMovedPermanently)
		return
	}
	pid = canonicalPID

	prop, err := app.queries.GetPropertyByPID(ctx, pid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Old PSI building/strata IDs no longer map to a single unit.
			if baseProp, baseErr := app.queries.GetPropertyByBasePID(ctx, sql.NullString{String: pid, Valid: true}); baseErr == nil {
				query := url.Values{}
				query.Set("suburb", baseProp.Suburb)
				query.Set("street", baseProp.StreetName)
				query.Set("state", baseProp.State)
				http.Redirect(w, r, "/properties?"+query.Encode(), http.StatusFound)
				return
			}
			app.renderNotFound(w, r)
			return
		}
		log.Printf("Database error fetching PID '%s': %v", pid, err)
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	// A base PSI ID with multiple unit records is a building, not a single
	// property. Send its old URL to the building overview.
	if prop.BasePid.Valid && prop.BasePid.String == prop.GnafPid {
		count, countErr := app.queries.CountPropertiesByBasePID(ctx, sql.NullString{String: prop.GnafPid, Valid: true})
		if countErr == nil && count > 1 {
			http.Redirect(w, r, "/building/"+prop.GnafPid, http.StatusMovedPermanently)
			return
		}
	}

	sales, err := app.queries.GetSalesHistoryByCanonical(ctx, pid)
	if err != nil {
		log.Printf("Error fetching sales history for PID '%s': %v", pid, err)
	}
	if len(sales) == 0 {
		// Fall back for databases where the canonical mapping has not been built.
		sales, err = app.queries.GetSalesHistoryByPID(ctx, pid)
		if err != nil {
			log.Printf("Error fetching sales history for PID '%s': %v", pid, err)
		}
	}

	withChange := make([]SaleWithChange, 0, len(sales))
	for i, sale := range sales {
		item := SaleWithChange{
			SaleID:         sale.SaleID,
			SettlementDate: sale.SettlementDate,
			SalePrice:      sale.SalePrice,
			SourceType:     sale.SourceType,
			IsLatest:       i == 0,
		}
		if i+1 < len(sales) {
			previous := sales[i+1]
			if previous.SalePrice > 0 {
				item.HasChange = true
				item.ChangeAmount = sale.SalePrice - previous.SalePrice
				item.ChangePercent = float64(item.ChangeAmount) / float64(previous.SalePrice) * 100
			}
		}
		withChange = append(withChange, item)
	}

	data := PageData{
		Title:           propertyAddress(prop) + ", " + prop.Suburb + " - Sales History",
		ActiveNav:       "search",
		Property:        prop,
		IsPSI:           strings.HasPrefix(prop.GnafPid, "GANSW_PSI_"),
		Sales:           sales,
		SalesWithChange: withChange,
		Flash:           app.readFlash(w, r),
	}

	if len(sales) > 1 {
		oldest := sales[len(sales)-1]
		latest := sales[0]
		if oldest.SalePrice > 0 {
			data.HasTotalChange = true
			data.TotalChangeAmount = latest.SalePrice - oldest.SalePrice
			data.TotalChangePercent = float64(data.TotalChangeAmount) / float64(oldest.SalePrice) * 100
		}
	}
	if len(withChange) > 0 && withChange[0].HasChange && withChange[0].ChangePercent <= -80 {
		data.HasDataWarning = true
		data.DataWarningPercent = -withChange[0].ChangePercent
	}

	app.renderTemplate(w, "property.html", data)
}

// handleSuburbAnalytics renders suburb KPIs, monthly trends and paginated sales.
func (app *App) handleSuburbAnalytics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	suburb := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("suburb")))
	state := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("state")))

	// The dropdown submits "SUBURB|STATE" so same-named suburbs in different
	// states are unambiguous.
	if i := strings.LastIndex(suburb, "|"); i >= 0 {
		if s := strings.TrimSpace(suburb[i+1:]); s != "" {
			state = s
		}
		suburb = strings.TrimSpace(suburb[:i])
	}

	// Auto-detect state when a direct link supplies only a suburb.
	if suburb != "" && state == "" {
		detected, err := database.SuburbStateByName(ctx, app.db, suburb)
		if err == nil {
			state = detected
		} else if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("Error detecting state for %s: %v", suburb, err)
		}
	}

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}

	chartRange := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("range")))
	if chartRange != "5" && chartRange != "10" && chartRange != "all" {
		chartRange = "10"
	}
	priceRange := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("price_range")))
	if priceRange != "1" && priceRange != "5" && priceRange != "10" && priceRange != "all" {
		priceRange = "1"
	}

	data := PageData{
		Title:          "Suburb Market Analytics - SoldNSW",
		ActiveNav:      "analytics",
		SelectedSuburb: suburb,
		SelectedState:  state,
		Page:           page,
		HasPrev:        page > 1,
		PrevPage:       page - 1,
		NextPage:       page + 1,
		ChartRange:     chartRange,
		PriceRange:     priceRange,
		Flash:          app.readFlash(w, r),
	}

	if suburb != "" && state != "" {
		analyticsRow, actualPriceRange, requestedCount, err := app.suburbPriceStats(ctx, suburb, state, priceRange)
		if err != nil {
			log.Printf("Error fetching analytics for %s, %s: %v", suburb, state, err)
		} else if analyticsRow.TotalSales > 0 {
			data.Analytics = &analyticsRow
			data.PriceWindow = actualPriceRange
			data.PriceLabel = priceRangeLabel(actualPriceRange)
			if actualPriceRange != priceRange {
				if requestedCount > 0 {
					data.PriceNote = fmt.Sprintf("Only %d sales in the %s; showing the %s median.",
						requestedCount, strings.ToLower(priceRangeLabel(priceRange)), strings.ToLower(priceRangeLabel(actualPriceRange)))
				} else {
					data.PriceNote = fmt.Sprintf("No sales in the %s; showing the %s median.",
						strings.ToLower(priceRangeLabel(priceRange)), strings.ToLower(priceRangeLabel(actualPriceRange)))
				}
			} else if analyticsRow.TotalSales < 20 {
				data.PriceNote = fmt.Sprintf("Based on %d recorded sales.", analyticsRow.TotalSales)
			}
		}

		extremes, err := app.queries.GetSuburbExtremeSales(ctx, database.GetSuburbExtremeSalesParams{
			Suburb: suburb,
			State:  state,
			Cutoff: lossCutoff(actualPriceRange),
		})
		if err != nil {
			log.Printf("Error fetching extreme sales for %s, %s: %v", suburb, state, err)
		}
		for i := range extremes {
			switch extremes[i].Kind {
			case "max":
				data.MaxSale = &extremes[i]
			case "min":
				data.MinSale = &extremes[i]
			}
		}

		totalSales, err := app.queries.GetSuburbSalesCount(ctx, database.GetSuburbSalesCountParams{
			Suburb: suburb,
			State:  state,
		})
		if err != nil {
			log.Printf("Error counting suburb sales: %v", err)
		}
		data.SuburbSalesTotal = totalSales
		data.TotalPages = int((totalSales + suburbSalesPageSize - 1) / suburbSalesPageSize)
		if data.TotalPages < 1 {
			data.TotalPages = 1
		}
		if page > data.TotalPages {
			page = data.TotalPages
			data.Page = page
			data.HasPrev = page > 1
			data.PrevPage = page - 1
			data.NextPage = page + 1
		}
		data.HasNext = page < data.TotalPages
		if page >= data.TotalPages {
			data.HasNext = false
		}
		if totalSales > 0 {
			data.ShowingFrom = (page-1)*suburbSalesPageSize + 1
			data.ShowingTo = page * suburbSalesPageSize
			if int64(data.ShowingTo) > totalSales {
				data.ShowingTo = int(totalSales)
			}
		}

		suburbSales, err := app.queries.GetSuburbSalesBySuburb(ctx, database.GetSuburbSalesBySuburbParams{
			Suburb:     suburb,
			State:      state,
			PageLimit:  suburbSalesPageSize,
			PageOffset: int64((page - 1) * suburbSalesPageSize),
		})
		if err != nil {
			log.Printf("Error fetching suburb sales: %v", err)
		}
		data.SuburbSales = suburbSales

		monthly, err := app.queries.GetSuburbMonthlyAverages(ctx, database.GetSuburbMonthlyAveragesParams{
			Suburb: suburb,
			State:  state,
		})
		if err != nil {
			log.Printf("Error fetching suburb monthly averages: %v", err)
		}
		cutoff := ""
		if chartRange != "all" {
			if years, err := strconv.Atoi(chartRange); err == nil {
				cutoff = time.Now().AddDate(-years, 0, 0).Format("2006-01")
			}
		}
		for _, m := range monthly {
			if cutoff != "" && m.Month < cutoff {
				continue
			}
			data.ChartLabels = append(data.ChartLabels, m.Month)
			data.ChartValues = append(data.ChartValues, m.AvgPrice)
		}

		lossSummary, err := app.queries.GetSuburbLossSummary(ctx, database.GetSuburbLossSummaryParams{
			Cutoff: lossCutoff("2"),
			Suburb: suburb,
			State:  state,
		})
		if err != nil {
			log.Printf("Error fetching loss summary: %v", err)
		} else {
			data.LossSummary = &lossSummary
			data.LossRange = "2"
			if lossSummary.Resales > 0 {
				data.LossRate = float64(lossSummary.Losses) / float64(lossSummary.Resales) * 100
			}
		}
	} else if suburb == "" {
		// The whole-market overview is read from the materialised
		// suburb_stats table, so this is cheap even on a cold start.
		overview, err := app.queries.GetSuburbOverviewStats(ctx)
		if err != nil {
			log.Printf("Error fetching overview stats: %v", err)
		}
		data.OverviewTotal = len(overview)
		// Rendering every suburb produces a ~10MB page on the full NSW PSI
		// dataset. The query is ordered by sales volume, so show the leaders.
		const overviewLimit = 200
		if len(overview) > overviewLimit {
			overview = overview[:overviewLimit]
		}
		data.OverviewStats = overview
	}

	app.renderTemplate(w, "suburb.html", data)
}

// handleSuburbLosses lists properties whose latest recorded sale was below
// their immediately preceding recorded sale.
func (app *App) handleSuburbLosses(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	suburb := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("suburb")))
	state := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("state")))

	if i := strings.LastIndex(suburb, "|"); i >= 0 {
		if s := strings.TrimSpace(suburb[i+1:]); s != "" {
			state = s
		}
		suburb = strings.TrimSpace(suburb[:i])
	}
	if suburb == "" {
		http.Redirect(w, r, "/suburbs", http.StatusSeeOther)
		return
	}
	if state == "" {
		detected, err := database.SuburbStateByName(ctx, app.db, suburb)
		if err == nil {
			state = detected
		} else if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("Error detecting state for %s: %v", suburb, err)
		}
	}

	rangeValue := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("range")))
	if rangeValue != "2" && rangeValue != "5" && rangeValue != "10" && rangeValue != "all" {
		rangeValue = "10"
	}
	lossType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	if lossType != "house" && lossType != "unit" && lossType != "townhouse" && lossType != "other" {
		lossType = "all"
	}
	sortValue := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("sort")))
	if sortValue != "amount" && sortValue != "percent" && sortValue != "date" {
		sortValue = "amount"
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}

	data := PageData{
		Title:          "Properties sold at a loss - SoldNSW",
		ActiveNav:      "analytics",
		SelectedSuburb: suburb,
		SelectedState:  state,
		LossRange:      rangeValue,
		LossSort:       sortValue,
		LossType:       lossType,
		Page:           page,
		Flash:          app.readFlash(w, r),
	}

	if state == "" {
		app.renderTemplate(w, "losses.html", data)
		return
	}

	summary, err := app.queries.GetSuburbLossSummary(ctx, database.GetSuburbLossSummaryParams{
		Cutoff: lossCutoff(rangeValue),
		Suburb: suburb,
		State:  state,
	})
	if err != nil {
		log.Printf("Error fetching loss summary for %s: %v", suburb, err)
	} else {
		data.LossSummary = &summary
		if summary.Resales > 0 {
			data.LossRate = float64(summary.Losses) / float64(summary.Resales) * 100
		}
	}

	typeRows, err := app.queries.GetSuburbLossTypeSummary(ctx, database.GetSuburbLossTypeSummaryParams{
		Cutoff: lossCutoff(rangeValue),
		Suburb: suburb,
		State:  state,
	})
	if err != nil {
		log.Printf("Error fetching loss type summary for %s: %v", suburb, err)
	}
	typeCounts := map[string]*LossTypeCount{
		"all":       {Key: "all", Label: "All"},
		"house":     {Key: "house", Label: "Houses"},
		"unit":      {Key: "unit", Label: "Units"},
		"townhouse": {Key: "townhouse", Label: "Townhouses"},
		"other":     {Key: "other", Label: "Other / unknown"},
	}
	if summary.Losses >= 0 {
		typeCounts["all"].Losses = summary.Losses
		typeCounts["all"].Resales = summary.Resales
		if summary.Resales > 0 {
			typeCounts["all"].Rate = float64(summary.Losses) / float64(summary.Resales) * 100
		}
	}
	for _, row := range typeRows {
		key := row.DwellingType
		if key == "unknown" {
			key = "other"
		}
		count, ok := typeCounts[key]
		if !ok {
			continue
		}
		count.Losses += row.Losses
		count.Resales += row.Resales
	}
	for _, count := range typeCounts {
		if count.Resales > 0 {
			count.Rate = float64(count.Losses) / float64(count.Resales) * 100
		}
	}
	data.LossTypeCounts = []LossTypeCount{
		*typeCounts["all"], *typeCounts["house"], *typeCounts["unit"], *typeCounts["townhouse"], *typeCounts["other"],
	}

	losses, err := app.queries.GetSuburbLosses(ctx, database.GetSuburbLossesParams{
		Cutoff: lossCutoff(rangeValue),
		Suburb: suburb,
		State:  state,
	})
	if err != nil {
		log.Printf("Error fetching losses for %s: %v", suburb, err)
	}

	if lossType != "all" {
		filtered := losses[:0]
		for _, loss := range losses {
			matches := loss.DwellingType == lossType
			if lossType == "other" {
				matches = loss.DwellingType == "other" || loss.DwellingType == "unknown"
			}
			if matches {
				filtered = append(filtered, loss)
			}
		}
		losses = filtered
	}

	switch sortValue {
	case "percent":
		sort.SliceStable(losses, func(i, j int) bool {
			if losses[i].LossPercent == losses[j].LossPercent {
				return losses[i].SettlementDate > losses[j].SettlementDate
			}
			return losses[i].LossPercent < losses[j].LossPercent
		})
	case "date":
		sort.SliceStable(losses, func(i, j int) bool {
			if losses[i].SettlementDate == losses[j].SettlementDate {
				return losses[i].LossPercent < losses[j].LossPercent
			}
			return losses[i].SettlementDate > losses[j].SettlementDate
		})
	default:
		sort.SliceStable(losses, func(i, j int) bool {
			if losses[i].LossAmount == losses[j].LossAmount {
				return losses[i].LossPercent < losses[j].LossPercent
			}
			return losses[i].LossAmount < losses[j].LossAmount
		})
	}

	data.LossTotal = int64(len(losses))
	data.TotalPages = int((data.LossTotal + suburbSalesPageSize - 1) / suburbSalesPageSize)
	if data.TotalPages < 1 {
		data.TotalPages = 1
	}
	if page > data.TotalPages {
		page = data.TotalPages
		data.Page = page
	}
	data.HasPrev = page > 1
	data.PrevPage = page - 1
	data.NextPage = page + 1
	data.HasNext = page < data.TotalPages
	start := (page - 1) * suburbSalesPageSize
	end := start + suburbSalesPageSize
	if start > len(losses) {
		start = len(losses)
	}
	if end > len(losses) {
		end = len(losses)
	}
	data.LossSales = losses[start:end]
	if len(data.LossSales) > 0 {
		data.ShowingFrom = start + 1
		data.ShowingTo = end
	}

	app.renderTemplate(w, "losses.html", data)
}

// handleBuilding renders the units/properties that share a base PSI building ID.
func (app *App) handleBuilding(w http.ResponseWriter, r *http.Request) {
	basePID := r.PathValue("base_pid")
	if basePID == "" {
		app.renderNotFound(w, r)
		return
	}
	units, err := app.queries.GetBuildingUnitsByBasePID(r.Context(), sql.NullString{String: basePID, Valid: true})
	if err != nil {
		log.Printf("Error fetching building %s: %v", basePID, err)
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	if len(units) == 0 {
		app.renderNotFound(w, r)
		return
	}

	first := units[0]
	addressNumber := ""
	if first.StreetNumber.Valid && !strings.Contains(first.StreetNumber.String, "/") {
		addressNumber = strings.TrimSpace(first.StreetNumber.String) + " "
	}
	address := addressNumber + first.StreetName + " " + first.StreetType + ", " + first.Suburb + " " + first.State + " " + first.Postcode
	var sales int64
	for _, unit := range units {
		sales += unit.SalesCount
	}
	data := PageData{
		Title:              address + " - Units - SoldNSW",
		ActiveNav:          "search",
		BuildingBasePID:    basePID,
		BuildingAddress:    address,
		BuildingSuburb:     first.Suburb,
		BuildingState:      first.State,
		BuildingUnits:      units,
		BuildingUnitCount:  len(units),
		BuildingSalesCount: sales,
		Flash:              app.readFlash(w, r),
	}
	app.renderTemplate(w, "building.html", data)
}

// handleSubmitForm renders the sale submission form.
func (app *App) handleSubmitForm(w http.ResponseWriter, r *http.Request) {
	pid := strings.TrimSpace(r.URL.Query().Get("gnaf_pid"))
	data := app.submitPageData(r.Context(), w, r, pid, time.Now().Format("2006-01-02"), app.readFlash(w, r))
	app.renderTemplate(w, "submit.html", data)
}

// submitPageData builds the form state, including the selected property when
// the user arrived from a property page.
func (app *App) submitPageData(ctx context.Context, w http.ResponseWriter, r *http.Request, pid, date string, flash *FlashMessage) PageData {
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	data := PageData{
		Title:       "Add a sale - SoldNSW",
		ActiveNav:   "submit",
		PreFillPID:  pid,
		TodayDate:   date,
		MaxSaleDate: time.Now().Add(maxFutureSettlement).Format("2006-01-02"),
		CSRFToken:   app.ensureCSRFToken(w, r),
		Flash:       flash,
	}
	if pid != "" {
		if prop, err := app.queries.GetPropertyByPID(ctx, pid); err == nil {
			data.PrefillProperty = &prop
		}
	}
	return data
}

func (app *App) renderSubmitError(w http.ResponseWriter, r *http.Request, status int, pid, date, message string) {
	data := app.submitPageData(r.Context(), w, r, pid, date, &FlashMessage{Type: "danger", Message: message})
	app.renderTemplateStatus(w, "submit.html", data, status)
}

// handleSubmitAction processes the submitted sale price form.
func (app *App) handleSubmitAction(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	if err := r.ParseForm(); err != nil {
		app.renderSubmitError(w, r, http.StatusBadRequest, "", "", "The form could not be read. Please check the values and try again.")
		return
	}

	gnafPID := strings.TrimSpace(r.FormValue("gnaf_pid"))
	settleDate := strings.TrimSpace(r.FormValue("settlement_date"))

	if !app.validCSRF(r) {
		// Drop the stale token so the re-rendered form gets a fresh one.
		http.SetCookie(w, &http.Cookie{
			Name:     "csrf_token",
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
		app.renderSubmitError(w, r, http.StatusForbidden, gnafPID, settleDate, "Your form session expired. Please check the details and submit again.")
		return
	}

	if !app.limiter.allow(clientIP(r)) {
		app.renderSubmitError(w, r, http.StatusTooManyRequests, gnafPID, settleDate, "Too many submissions. Please wait a minute and try again.")
		return
	}

	priceStr := strings.TrimSpace(r.FormValue("sale_price"))
	sourceType := strings.TrimSpace(r.FormValue("source_type"))

	if gnafPID == "" || settleDate == "" || priceStr == "" || sourceType == "" {
		app.renderSubmitError(w, r, http.StatusBadRequest, gnafPID, settleDate, "Please complete all required fields.")
		return
	}

	price, err := strconv.ParseInt(priceStr, 10, 64)
	if err != nil || price <= 10000 {
		app.renderSubmitError(w, r, http.StatusBadRequest, gnafPID, settleDate, "Sale price must be a whole number greater than $10,000.")
		return
	}

	date, err := time.Parse("2006-01-02", settleDate)
	if err != nil {
		app.renderSubmitError(w, r, http.StatusBadRequest, gnafPID, settleDate, "Settlement date must use the YYYY-MM-DD format.")
		return
	}
	minDate, _ := time.Parse("2006-01-02", minSaleDate)
	if date.Before(minDate) || date.After(time.Now().Add(maxFutureSettlement)) {
		app.renderSubmitError(w, r, http.StatusBadRequest, gnafPID, settleDate, "Settlement date must be between 1990-01-01 and 30 days from today.")
		return
	}

	allowedSources := map[string]bool{
		"Auction":                true,
		"Private Treaty":         true,
		"Public Registry":        true,
		"Prior to Auction":       true,
		"Expression of Interest": true,
	}
	if !allowedSources[sourceType] {
		app.renderSubmitError(w, r, http.StatusBadRequest, gnafPID, settleDate, "Please choose a valid sale source.")
		return
	}

	// Verify that the property exists in the database.
	ctx := r.Context()
	prop, err := app.queries.GetPropertyByPID(ctx, gnafPID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			app.renderSubmitError(w, r, http.StatusNotFound, gnafPID, settleDate, "That property ID was not found. Search for the address first, then add the sale from its property page.")
			return
		}
		log.Printf("Database error looking up property '%s': %v", gnafPID, err)
		app.renderSubmitError(w, r, http.StatusInternalServerError, gnafPID, settleDate, "We could not check that property right now. Please try again.")
		return
	}

	saleID := "SALE-" + time.Now().Format("2006") + "-" + strings.ToUpper(uuid.New().String()[:8])

	insertParams := database.InsertPropertySaleParams{
		SaleID:         saleID,
		GnafPid:        gnafPID,
		SettlementDate: settleDate,
		SalePrice:      price,
		SourceType:     sourceType,
	}

	if err := app.queries.InsertPropertySale(ctx, insertParams); err != nil {
		log.Printf("Failed to insert property sale: %v", err)
		app.renderSubmitError(w, r, http.StatusInternalServerError, gnafPID, settleDate, "The sale could not be saved. Please check the values and try again.")
		return
	}

	if err := database.RefreshSuburbStat(ctx, app.db, prop.Suburb, prop.State); err != nil {
		log.Printf("Could not refresh suburb statistics: %v", err)
	}
	log.Printf("Successfully recorded new sale %s for %s at $%d", saleID, gnafPID, price)
	app.setFlash(w, fmt.Sprintf("Sale of $%s recorded for %s, %s.", formatPriceInt(price), propertyAddress(prop), prop.Suburb))
	http.Redirect(w, r, "/property/"+gnafPID, http.StatusSeeOther)
}

// ensureCSRFToken returns the existing token cookie or generates a new one.
func (app *App) ensureCSRFToken(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie("csrf_token"); err == nil && len(c.Value) >= 32 {
		return c.Value
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		log.Printf("CSRF token generation failed: %v", err)
		return ""
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	http.SetCookie(w, &http.Cookie{
		Name:     "csrf_token",
		Value:    token,
		Path:     "/",
		MaxAge:   3600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	return token
}

func (app *App) validCSRF(r *http.Request) bool {
	c, err := r.Cookie("csrf_token")
	if err != nil || c.Value == "" {
		return false
	}
	formToken := r.FormValue("csrf_token")
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(formToken)) == 1
}

// setFlash stores a signed flash message. The signature prevents clients from
// forging arbitrary banner content in the cookie.
func (app *App) setFlash(w http.ResponseWriter, message string) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(message))
	mac := hmac.New(sha256.New, app.flashKey)
	mac.Write([]byte(payload))
	value := payload + "." + hex.EncodeToString(mac.Sum(nil))
	http.SetCookie(w, &http.Cookie{
		Name:     "flash_msg",
		Value:    value,
		Path:     "/",
		MaxAge:   60,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (app *App) readFlash(w http.ResponseWriter, r *http.Request) *FlashMessage {
	c, err := r.Cookie("flash_msg")
	if err != nil {
		return nil
	}
	// Clear the cookie whether or not it validates.
	http.SetCookie(w, &http.Cookie{
		Name:     "flash_msg",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	parts := strings.Split(c.Value, ".")
	if len(parts) != 2 {
		return nil
	}
	mac := hmac.New(sha256.New, app.flashKey)
	mac.Write([]byte(parts[0]))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(parts[1])) {
		return nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil
	}
	return &FlashMessage{Type: "success", Message: string(decoded)}
}

func (app *App) renderTemplate(w http.ResponseWriter, name string, data any) {
	app.renderTemplateStatus(w, name, data, http.StatusOK)
}

func (app *App) renderTemplateStatus(w http.ResponseWriter, name string, data any, status int) {
	tmpl, ok := app.templates[name]
	if !ok {
		log.Printf("Unknown template: %s", name)
		http.Error(w, "Template not found", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "base.html", data); err != nil {
		log.Printf("Template render error for %s: %v", name, err)
		http.Error(w, "Render error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// ipRateLimiter is a small in-memory sliding-window limiter for form posts.
type ipRateLimiter struct {
	mu     sync.Mutex
	hits   map[string][]time.Time
	limit  int
	window time.Duration
}

func newIPRateLimiter(limit int, window time.Duration) *ipRateLimiter {
	return &ipRateLimiter{
		hits:   make(map[string][]time.Time),
		limit:  limit,
		window: window,
	}
}

func (l *ipRateLimiter) allow(ip string) bool {
	now := time.Now()
	cutoff := now.Add(-l.window)

	l.mu.Lock()
	defer l.mu.Unlock()

	// Bound memory if many unique clients connect.
	if len(l.hits) > 10000 {
		for key, times := range l.hits {
			if len(times) == 0 || times[len(times)-1].Before(cutoff) {
				delete(l.hits, key)
			}
		}
	}

	times := l.hits[ip]
	i := 0
	for i < len(times) && times[i].Before(cutoff) {
		i++
	}
	times = times[i:]
	if len(times) >= l.limit {
		l.hits[ip] = times
		return false
	}
	l.hits[ip] = append(times, now)
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

func withRequestLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.RequestURI(), time.Since(start).Round(time.Millisecond))
	})
}
