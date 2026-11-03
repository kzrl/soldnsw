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

	lastImportMu      sync.Mutex
	lastImportDate    string
	lastImportChecked time.Time

	suggestLimiter *ipRateLimiter
	suggestCacheMu sync.Mutex
	suggestCache   map[string]suggestionCacheEntry

	statsMu      sync.Mutex
	statsStatus  string
	statsUpdated time.Time
	statsError   string
	statsTimeout time.Duration
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

// QueryParam is a rendered query-string parameter for filter forms and pager
// links.
type QueryParam struct {
	Name  string
	Value string
}

// PageData represents common and view-specific template data.
type PageData struct {
	Title              string
	ActiveNav          string
	Flash              *FlashMessage
	QueryAddress       string
	QuerySuburb        string
	QueryStreet        string
	QueryStreetType    string
	QueryNumber        string
	QueryState         string
	QueryPostcode      string
	QueryDwellingType  string
	SearchMode         string
	SearchError        string
	HasSearched        bool
	Properties         []database.GnafProperty
	StreetMatches      []database.SearchStreetGroupsRow
	StreetSummary      *database.GetStreetSummaryRow
	PostcodeSummary    *database.GetPostcodeSummaryRow
	PostcodeSuburbs    []database.GetPostcodeSuburbsRow
	ResultTotal        int64
	QuerySort          string
	QueryOnlyWithSales bool
	AllowRelevanceSort bool
	PagerBaseURL       string
	FilterAction       string
	FilterParams       []QueryParam
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
	LastImportDate     string
	StatsStatus        string
	StatsUpdated       string
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
		"int64":     priceToInt,
		"streetURL": streetSearchURL,
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
	statsTimeoutSec := flag.Int("stats-timeout", 120, "Timeout in seconds for each materialised stats rebuild attempt")
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
	canonicalSalesRebuilt := false
	if linksRebuilt {
		log.Printf("Canonical property links rebuilt; refreshing canonical sales, analytics and address search...")
		if err := database.RebuildCanonicalSales(context.Background(), db); err != nil {
			log.Printf("Warning: canonical sales rebuild failed: %v", err)
		} else {
			canonicalSalesRebuilt = true
		}
		if err := database.RebuildAddressSearch(context.Background(), db); err != nil {
			log.Printf("Warning: address search rebuild failed: %v", err)
		}
	}
	canonicalSalesEnsured, err := database.EnsureCanonicalSales(context.Background(), db)
	if err != nil {
		log.Fatalf("Failed to build canonical sales: %v", err)
	}
	if canonicalSalesEnsured {
		canonicalSalesRebuilt = true
	}
	if canonicalSalesRebuilt {
		log.Printf("Canonical sales rebuilt; refreshing materialised statistics...")
		if err := database.RefreshSuburbStats(context.Background(), db); err != nil {
			log.Printf("Warning: suburb statistics refresh failed: %v", err)
		}
		if err := database.RefreshStreetStats(context.Background(), db); err != nil {
			log.Printf("Warning: street statistics refresh failed: %v", err)
		}
		if err := database.RefreshPostcodeStats(context.Background(), db); err != nil {
			log.Printf("Warning: postcode statistics refresh failed: %v", err)
		}
	}
	if err := database.EnsureAddressSearch(context.Background(), db); err != nil {
		log.Fatalf("Failed to build address search index: %v", err)
	}
	if missing, err := database.MissingSearchIndexes(context.Background(), db); err != nil {
		log.Printf("Warning: could not check search indexes: %v", err)
	} else if len(missing) > 0 {
		log.Printf("Warning: %d search indexes are missing (%s). Run bin/migrate -db %s before serving production traffic.",
			len(missing), database.DescribeMissingIndexes(missing), *dbPath)
	}

	tmpls, err := parseTemplates(*templateDir)
	if err != nil {
		log.Fatalf("Failed to parse templates: %v", err)
	}

	app, err := newApp(db, tmpls)
	if err != nil {
		log.Fatalf("Failed to initialise app: %v", err)
	}
	if *statsTimeoutSec > 0 {
		app.statsTimeout = time.Duration(*statsTimeoutSec) * time.Second
	}

	mux := http.NewServeMux()
	fs := http.FileServer(http.Dir(*staticDir))
	mux.Handle("GET /static/", http.StripPrefix("/static/", fs))

	mux.HandleFunc("GET /", app.handleHome)
	mux.HandleFunc("GET /properties", app.handlePropertiesSearch)
	mux.HandleFunc("GET /readyz", app.handleReadyz)
	mux.HandleFunc("GET /api/address/suggest", app.handleAddressSuggest)
	mux.HandleFunc("GET /api/search/suggest", app.handleSearchSuggest)
	mux.HandleFunc("GET /property/{pid}", app.handlePropertyDetail)
	mux.HandleFunc("GET /building/{base_pid}", app.handleBuilding)
	mux.HandleFunc("GET /suburbs", app.handleSuburbAnalytics)
	mux.HandleFunc("GET /suburbs/losses", app.handleSuburbLosses)
	mux.HandleFunc("GET /postcode/{code}", app.handlePostcodePage)
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
	go app.ensureMaterializedStats()

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
	pages := []string{"index.html", "postcode.html", "property.html", "suburb.html", "submit.html", "losses.html", "building.html", "404.html"}
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
		db:             db,
		queries:        database.New(db),
		templates:      tmpls,
		flashKey:       key,
		limiter:        newIPRateLimiter(5, time.Minute),
		suggestLimiter: newIPRateLimiter(60, time.Minute),
		suggestCache:   make(map[string]suggestionCacheEntry),
		statsStatus:    "unknown",
		statsTimeout:   2 * time.Minute,
	}, nil
}

// setStatsStatus records the materialised analytics build state.
func (app *App) setStatsStatus(status string, buildErr error) {
	app.statsMu.Lock()
	defer app.statsMu.Unlock()
	app.statsStatus = status
	if buildErr != nil {
		app.statsError = buildErr.Error()
	} else {
		app.statsError = ""
	}
	if status == "ready" {
		app.statsUpdated = time.Now()
	}
}

func (app *App) statsSnapshot() (status, updated, lastErr string) {
	app.statsMu.Lock()
	defer app.statsMu.Unlock()
	status = app.statsStatus
	lastErr = app.statsError
	if !app.statsUpdated.IsZero() {
		updated = app.statsUpdated.Format("2 Jan 2006 15:04")
	}
	return status, updated, lastErr
}

// ensureMaterializedStats builds the materialised analytics and suggestion
// tables in the background. Each attempt has a configurable timeout, and the
// build is retried with backoff before the status is marked failed.
func (app *App) ensureMaterializedStats() {
	ready, err := app.materializedStatsReady()
	if err != nil {
		log.Printf("Materialised stats check failed: %v", err)
		app.setStatsStatus("failed", err)
		return
	}
	if ready {
		app.setStatsStatus("ready", nil)
		return
	}

	app.setStatsStatus("building", nil)
	const attempts = 3
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		timeout := app.statsTimeout
		if timeout <= 0 {
			timeout = 2 * time.Minute
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		lastErr = app.rebuildMissingStats(ctx)
		cancel()
		if lastErr == nil {
			log.Printf("Materialised analytics stats ready")
			app.setStatsStatus("ready", nil)
			return
		}
		log.Printf("Materialised stats build attempt %d/%d failed: %v", attempt, attempts, lastErr)
		if attempt < attempts {
			time.Sleep(time.Duration(attempt) * 30 * time.Second)
		}
	}
	app.setStatsStatus("failed", lastErr)
}

func (app *App) materializedStatsReady() (bool, error) {
	for _, table := range []string{"suburb_stats", "street_stats", "postcode_stats"} {
		var count int64
		if err := app.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			return false, fmt.Errorf("count %s: %w", table, err)
		}
		if count == 0 {
			return false, nil
		}
	}
	return true, nil
}

func (app *App) rebuildMissingStats(ctx context.Context) error {
	var suburbCount, streetCount, postcodeCount int64
	if err := app.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM suburb_stats").Scan(&suburbCount); err != nil {
		return fmt.Errorf("count suburb stats: %w", err)
	}
	if err := app.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM street_stats").Scan(&streetCount); err != nil {
		return fmt.Errorf("count street stats: %w", err)
	}
	if err := app.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM postcode_stats").Scan(&postcodeCount); err != nil {
		return fmt.Errorf("count postcode stats: %w", err)
	}
	if suburbCount == 0 {
		log.Printf("Building suburb statistics for the first time; this can take a moment on a large dataset...")
		if err := database.RefreshSuburbStats(ctx, app.db); err != nil {
			return err
		}
	}
	if streetCount == 0 {
		log.Printf("Building street suggestion statistics...")
		if err := database.RefreshStreetStats(ctx, app.db); err != nil {
			return err
		}
	}
	if postcodeCount == 0 {
		log.Printf("Building postcode suggestion statistics...")
		if err := database.RefreshPostcodeStats(ctx, app.db); err != nil {
			return err
		}
	}
	return nil
}

// handleReadyz reports database and materialised-stats readiness.
func (app *App) handleReadyz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	pingErr := app.db.PingContext(r.Context())
	status, updated, lastErr := app.statsSnapshot()
	response := map[string]any{
		"database":      pingErr == nil,
		"stats_status":  status,
		"stats_updated": updated,
	}
	if lastErr != "" {
		response["stats_error"] = lastErr
	}
	if pingErr != nil || status == "failed" {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(response)
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

	data := app.homePageData(r.Context())
	data.Title = "Australian Property Sales & Suburb Trends"
	data.Flash = app.readFlash(w, r)
	app.renderTemplate(w, "index.html", data)
}

// homePageData loads the statistics and recent sales shared by the search
// views and the landing page.
func (app *App) homePageData(ctx context.Context) PageData {
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

	return PageData{
		ActiveNav:       "search",
		RecentSales:     recentSales,
		TotalProperties: totalProps,
		TotalSales:      totalSales,
		TotalSuburbs:    int(totalSuburbs),
		LatestSaleDate:  latestSaleDate,
		TopSuburbs:      topSuburbs,
	}
}

// searchPageData is the shared result-page shell for address, street and
// postcode searches.
func (app *App) searchPageData(ctx context.Context) PageData {
	data := app.homePageData(ctx)
	data.Title = "Search Results - SoldNSW"
	data.ActiveNav = "search"
	data.HasSearched = true
	return data
}

const maxSearchQueryLength = 120
const suggestionCacheTTL = time.Minute

type suggestionCacheEntry struct {
	suggestions []searchSuggestion
	expires     time.Time
}

func (app *App) cachedSuggestions(q string) ([]searchSuggestion, bool) {
	key := strings.ToLower(strings.TrimSpace(q))
	app.suggestCacheMu.Lock()
	defer app.suggestCacheMu.Unlock()
	entry, ok := app.suggestCache[key]
	if !ok || time.Now().After(entry.expires) {
		if ok {
			delete(app.suggestCache, key)
		}
		return nil, false
	}
	return entry.suggestions, true
}

func (app *App) cacheSuggestions(q string, suggestions []searchSuggestion) {
	key := strings.ToLower(strings.TrimSpace(q))
	app.suggestCacheMu.Lock()
	defer app.suggestCacheMu.Unlock()
	if len(app.suggestCache) > 5000 {
		app.suggestCache = make(map[string]suggestionCacheEntry)
	}
	app.suggestCache[key] = suggestionCacheEntry{
		suggestions: suggestions,
		expires:     time.Now().Add(suggestionCacheTTL),
	}
}

// handleAddressSuggest returns autocomplete matches for the homepage search box.
func (app *App) handleAddressSuggest(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if len([]rune(q)) < 2 || len([]rune(q)) > maxSearchQueryLength {
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

// searchSuggestion is a typed autocomplete entry for the homepage search box.
type searchSuggestion struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	Value string `json:"value"`
	PID   string `json:"pid,omitempty"`
}

// handleSearchSuggest returns addresses, streets and postcodes that match the
// current query. The legacy address-only endpoint remains available.
func (app *App) handleSearchSuggest(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if len([]rune(q)) < 2 || len([]rune(q)) > maxSearchQueryLength {
		_, _ = w.Write([]byte("[]"))
		return
	}
	if app.suggestLimiter != nil && !app.suggestLimiter.allow(clientIP(r)) {
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode([]searchSuggestion{})
		return
	}
	if cached, ok := app.cachedSuggestions(q); ok {
		_ = json.NewEncoder(w).Encode(cached)
		return
	}

	ctx := r.Context()
	parsed := database.ParseSearchQuery(q)
	suggestions := make([]searchSuggestion, 0, 10)
	postcodeSuggestions := make([]searchSuggestion, 0, 3)
	streetSuggestions := make([]searchSuggestion, 0, 5)
	suburbSuggestions := make([]searchSuggestion, 0, 3)
	addressSuggestions := make([]searchSuggestion, 0, 5)
	propertySuggestions := make([]searchSuggestion, 0, 1)

	if parsed.Kind == database.SearchKindPropertyID {
		prop, err := app.queries.GetPropertyByPID(ctx, parsed.PropertyID)
		if err == nil {
			propertySuggestions = append(propertySuggestions, searchSuggestion{
				Kind:  "property",
				Label: fmt.Sprintf("%s, %s %s", propertyAddress(prop), prop.Suburb, prop.State),
				Value: "/property/" + url.PathEscape(prop.GnafPid),
				PID:   prop.GnafPid,
			})
		} else if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("Property suggestion error: %v", err)
		}
		app.cacheSuggestions(q, propertySuggestions)
		_ = json.NewEncoder(w).Encode(propertySuggestions)
		return
	}

	numericPrefix := len(q) >= 3 && len(q) <= 4 && allDigits(q)
	if numericPrefix {
		rows, err := app.queries.SuggestPostcodeStats(ctx, database.SuggestPostcodeStatsParams{
			PostcodePattern: q + "%",
			ExactPostcode:   q,
			RowLimit:        3,
		})
		if err != nil {
			log.Printf("Postcode suggestion error: %v", err)
		}
		for _, row := range rows {
			label := row.Postcode
			if row.SampleSuburb != "" {
				label += " — " + row.SampleSuburb
			}
			label += fmt.Sprintf(" (%s)", propertyCountLabel(row.PropertyCount))
			postcodeSuggestions = append(postcodeSuggestions, searchSuggestion{
				Kind:  "postcode",
				Label: label,
				Value: "/postcode/" + row.Postcode,
			})
		}
		if len(rows) == 0 {
			fallback, fallbackErr := app.queries.SuggestPostcodes(ctx, database.SuggestPostcodesParams{
				PostcodePattern: q + "%",
				RowLimit:        3,
			})
			if fallbackErr != nil {
				log.Printf("Postcode suggestion fallback error: %v", fallbackErr)
			}
			for _, row := range fallback {
				suburb, _ := row.SampleSuburb.(string)
				label := row.Postcode
				if suburb != "" {
					label += " — " + suburb
				}
				label += fmt.Sprintf(" (%s)", propertyCountLabel(row.PropertyCount))
				postcodeSuggestions = append(postcodeSuggestions, searchSuggestion{
					Kind:  "postcode",
					Label: label,
					Value: "/postcode/" + row.Postcode,
				})
			}
		}
	}

	pattern := parsed.StreetPattern
	if pattern == "" && parsed.StreetName != "" {
		pattern = parsed.StreetName + "%"
	}
	if pattern != "" && pattern != "%" && len(parsed.StreetName) >= 3 {
		rows, err := app.queries.SuggestStreetStats(ctx, database.SuggestStreetStatsParams{
			StreetPattern: pattern,
			ExactStreet:   parsed.StreetName,
			RowLimit:      5,
		})
		if err != nil {
			log.Printf("Street suggestion error: %v", err)
		}
		for _, row := range rows {
			streetSuggestions = append(streetSuggestions, searchSuggestion{
				Kind:  "street",
				Label: fmt.Sprintf("%s %s, %s %s %s (%s)", row.StreetName, row.StreetType, row.Suburb, row.State, row.Postcode, propertyCountLabel(row.PropertyCount)),
				Value: buildStreetURL(row.StreetName, row.StreetType, row.Suburb, row.State, row.Postcode),
			})
		}
		if len(rows) == 0 {
			fallback, fallbackErr := app.queries.SuggestStreetGroups(ctx, database.SuggestStreetGroupsParams{
				StreetPattern: pattern,
				RowLimit:      5,
			})
			if fallbackErr != nil {
				log.Printf("Street suggestion fallback error: %v", fallbackErr)
			}
			for _, row := range fallback {
				streetSuggestions = append(streetSuggestions, searchSuggestion{
					Kind:  "street",
					Label: fmt.Sprintf("%s %s, %s %s %s (%s)", row.StreetName, row.StreetType, row.Suburb, row.State, row.Postcode, propertyCountLabel(row.PropertyCount)),
					Value: buildStreetURL(row.StreetName, row.StreetType, row.Suburb, row.State, row.Postcode),
				})
			}
		}
	}

	// Suburb suggestions help with names that also look like street prefixes.
	if !numericPrefix {
		fields := strings.Fields(strings.ToUpper(q))
		if len(fields) > 0 && !allDigits(fields[0]) && len(fields[0]) >= 3 {
			rows, err := app.queries.SuggestSuburbs(ctx, database.SuggestSuburbsParams{
				SuburbPattern: fields[0] + "%",
				ExactSuburb:   fields[0],
				RowLimit:      3,
			})
			if err != nil {
				log.Printf("Suburb suggestion error: %v", err)
			}
			for _, row := range rows {
				suburbSuggestions = append(suburbSuggestions, searchSuggestion{
					Kind:  "suburb",
					Label: fmt.Sprintf("%s, %s (%s, %d sales)", row.Suburb, row.State, propertyCountLabel(row.PropertyCount), row.SalesCount),
					Value: suburbSearchURL(row.Suburb, row.State),
				})
			}
		}
	}

	// Pure postcode searches do not need address rows.
	if !numericPrefix && (parsed.Kind != database.SearchKindPostcode || parsed.StreetName != "") {
		matches, err := database.SearchAddressFTS(ctx, app.db, q, 5)
		if err != nil {
			log.Printf("Address suggestion error: %v", err)
		}
		for _, match := range matches {
			addressSuggestions = append(addressSuggestions, searchSuggestion{
				Kind:  "address",
				Label: match.Address,
				Value: "/property/" + url.PathEscape(match.GnafPID),
				PID:   match.GnafPID,
			})
		}
	}

	if numericPrefix {
		suggestions = append(suggestions, postcodeSuggestions...)
		suggestions = append(suggestions, streetSuggestions...)
	} else {
		switch parsed.Kind {
		case database.SearchKindPostcode:
			suggestions = append(suggestions, postcodeSuggestions...)
			suggestions = append(suggestions, suburbSuggestions...)
			suggestions = append(suggestions, streetSuggestions...)
		case database.SearchKindAddress:
			suggestions = append(suggestions, addressSuggestions...)
			suggestions = append(suggestions, streetSuggestions...)
			suggestions = append(suggestions, suburbSuggestions...)
			suggestions = append(suggestions, postcodeSuggestions...)
		default:
			suggestions = append(suggestions, streetSuggestions...)
			suggestions = append(suggestions, suburbSuggestions...)
			suggestions = append(suggestions, addressSuggestions...)
			suggestions = append(suggestions, postcodeSuggestions...)
		}
	}
	if len(suggestions) > 10 {
		suggestions = suggestions[:10]
	}
	app.cacheSuggestions(q, suggestions)
	_ = json.NewEncoder(w).Encode(suggestions)
}

// streetSearchURL builds the canonical link for the properties on one street.
func streetSearchURL(row database.SearchStreetGroupsRow) string {
	return buildStreetURL(row.StreetName, row.StreetType, row.Suburb, row.State, row.Postcode)
}

func suburbSearchURL(suburb, state string) string {
	values := url.Values{}
	values.Set("suburb", suburb)
	values.Set("state", state)
	return "/suburbs?" + values.Encode()
}

func buildStreetURL(name, streetType, suburb, state, postcode string) string {
	values := url.Values{}
	values.Set("street", name)
	values.Set("street_type", streetType)
	values.Set("suburb", suburb)
	values.Set("state", state)
	values.Set("postcode", postcode)
	values.Set("view", "street")
	return "/properties?" + values.Encode()
}

func propertyCountLabel(count int64) string {
	if count == 1 {
		return "1 property"
	}
	return fmt.Sprintf("%d properties", count)
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// queryParams returns sorted, rendered query parameters excluding the named
// keys. It is used for filter forms and pager links.
func queryParams(values url.Values, exclude ...string) []QueryParam {
	skip := make(map[string]bool, len(exclude))
	for _, name := range exclude {
		skip[name] = true
	}
	params := make([]QueryParam, 0, len(values))
	for name, items := range values {
		if skip[name] {
			continue
		}
		for _, value := range items {
			if value == "" {
				continue
			}
			params = append(params, QueryParam{Name: name, Value: value})
		}
	}
	sort.Slice(params, func(i, j int) bool {
		if params[i].Name == params[j].Name {
			return params[i].Value < params[j].Value
		}
		return params[i].Name < params[j].Name
	})
	return params
}

// pagerBaseURL returns a URL ending in '?' or '&amp;' so templates can append
// "page=N".
func pagerBaseURL(path string, values url.Values) string {
	encoded := values.Encode()
	if encoded == "" {
		return path + "?"
	}
	return path + "?" + encoded + "&"
}

func resultSort(value, fallback string, allowRelevance bool) string {
	switch value {
	case "street", "latest", "price":
		return value
	case "relevance":
		if allowRelevance {
			return value
		}
	}
	return fallback
}

func onlyWithSalesParam(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "on", "yes":
		return true
	default:
		return false
	}
}

func normalizeDwellingType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "house", "unit", "townhouse":
		return strings.ToLower(strings.TrimSpace(value))
	case "other":
		return "other"
	default:
		return ""
	}
}

// renderSearchError renders a search page with a service-unavailable notice.
func (app *App) renderSearchError(w http.ResponseWriter, r *http.Request, templateName, title, message string) {
	app.renderSearchErrorStatus(w, r, templateName, title, message, http.StatusServiceUnavailable)
}

func (app *App) renderSearchErrorStatus(w http.ResponseWriter, r *http.Request, templateName, title, message string, status int) {
	data := app.searchPageData(r.Context())
	data.Title = title
	data.SearchMode = "error"
	data.SearchError = message
	data.Flash = app.readFlash(w, r)
	app.renderTemplateStatus(w, templateName, data, status)
}

// setSearchPagination populates the shared pager fields for a result set.
func setSearchPagination(data *PageData, page int, total int64, pageSize int) {
	totalPages := int((total + int64(pageSize) - 1) / int64(pageSize))
	if totalPages < 1 {
		totalPages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}
	data.Page = page
	data.TotalPages = totalPages
	data.ResultTotal = total
	data.HasPrev = page > 1
	data.HasNext = page < totalPages
	data.PrevPage = page - 1
	data.NextPage = page + 1
	if page > 1 {
		data.PrevPage = page - 1
	}
	if total == 0 {
		data.ShowingFrom = 0
		data.ShowingTo = 0
		return
	}
	data.ShowingFrom = (page-1)*pageSize + 1
	data.ShowingTo = page * pageSize
	if int64(data.ShowingTo) > total {
		data.ShowingTo = int(total)
	}
}

// findStreetGroupsPage resolves a parsed street query to one page of matching
// streets and the total group count.
func (app *App) findStreetGroupsPage(ctx context.Context, parsed database.ParsedSearch, page, pageSize int) ([]database.SearchStreetGroupsRow, int64, error) {
	pattern := parsed.StreetPattern
	if pattern == "" && parsed.StreetName != "" {
		pattern = parsed.StreetName + "%"
	}
	if len(parsed.StreetName) < 2 || pattern == "" || pattern == "%" {
		return nil, 0, nil
	}
	params := database.SearchStreetGroupsParams{
		StreetPattern: pattern,
		ExactStreet:   parsed.StreetName,
		StreetType:    parsed.StreetType,
		Suburb:        parsed.Suburb,
		State:         parsed.State,
		Postcode:      parsed.Postcode,
	}
	total, err := app.queries.CountStreetGroups(ctx, database.CountStreetGroupsParams{
		StreetPattern: params.StreetPattern,
		StreetType:    params.StreetType,
		Suburb:        params.Suburb,
		State:         params.State,
		Postcode:      params.Postcode,
	})
	if err != nil {
		return nil, 0, err
	}
	totalPages := int((total + int64(pageSize) - 1) / int64(pageSize))
	if totalPages < 1 {
		totalPages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}
	params.RowOffset = int64((page - 1) * pageSize)
	params.RowLimit = int64(pageSize)
	groups, err := app.queries.SearchStreetGroups(ctx, params)
	if err != nil {
		return nil, 0, err
	}
	return groups, total, nil
}

// streetGroupsMatchName prevents a single-token suburb such as KILLARA from
// being treated as a street just because a different street (KILLARNEY) shares
// the prefix. Prefix-only matches still work through the advanced form.
func streetGroupsMatchName(groups []database.SearchStreetGroupsRow, streetName string) bool {
	for _, group := range groups {
		if strings.EqualFold(group.StreetName, streetName) {
			return true
		}
	}
	return false
}

// renderStreetGroups displays one page of matching streets for a street query.
func (app *App) renderStreetGroups(w http.ResponseWriter, r *http.Request, address string, groups []database.SearchStreetGroupsRow, total int64, page int, flash *FlashMessage) {
	data := app.searchPageData(r.Context())
	data.Title = "Street Search Results - SoldNSW"
	data.SearchMode = "street"
	data.QueryAddress = address
	data.StreetMatches = groups
	data.Flash = flash
	setSearchPagination(&data, page, total, suburbSalesPageSize)
	values := r.URL.Query()
	values.Del("page")
	data.PagerBaseURL = pagerBaseURL("/properties", values)
	app.renderTemplate(w, "index.html", data)
}

// renderStreetProperties displays every property on one selected street.
func (app *App) renderStreetProperties(w http.ResponseWriter, r *http.Request, group database.SearchStreetGroupsRow, page int, flash *FlashMessage) {
	ctx := r.Context()
	query := r.URL.Query()
	dwellingType := normalizeDwellingType(query.Get("dwelling"))
	sortValue := resultSort(strings.ToLower(strings.TrimSpace(query.Get("sort"))), "street", false)
	onlyWithSales := onlyWithSalesParam(query.Get("only_with_sales"))

	data := app.searchPageData(ctx)
	data.Title = fmt.Sprintf("%s %s, %s - SoldNSW", group.StreetName, group.StreetType, group.Suburb)
	data.SearchMode = "street-properties"
	data.QueryStreet = group.StreetName
	data.QueryStreetType = group.StreetType
	data.QuerySuburb = group.Suburb
	data.QueryState = group.State
	data.QueryPostcode = group.Postcode
	data.QueryDwellingType = dwellingType
	data.QuerySort = sortValue
	data.QueryOnlyWithSales = onlyWithSales
	data.Flash = flash

	summary, err := app.queries.GetStreetSummary(ctx, database.GetStreetSummaryParams{
		StreetName: group.StreetName,
		StreetType: group.StreetType,
		Suburb:     group.Suburb,
		State:      group.State,
		Postcode:   group.Postcode,
	})
	if err != nil {
		log.Printf("Street summary error: %v", err)
	} else {
		data.StreetSummary = &summary
	}

	total, err := app.queries.CountStreetProperties(ctx, database.CountStreetPropertiesParams{
		StreetName:    group.StreetName,
		StreetType:    group.StreetType,
		Suburb:        group.Suburb,
		State:         group.State,
		Postcode:      group.Postcode,
		DwellingType:  dwellingType,
		OnlyWithSales: boolToInt64(onlyWithSales),
	})
	if err != nil {
		log.Printf("Street property count error: %v", err)
		app.renderSearchError(w, r, "index.html", "Search unavailable - SoldNSW", "Street search is temporarily unavailable. Please try again.")
		return
	}
	setSearchPagination(&data, page, total, suburbSalesPageSize)
	props, err := app.queries.ListStreetProperties(ctx, database.ListStreetPropertiesParams{
		StreetName:    group.StreetName,
		StreetType:    group.StreetType,
		Suburb:        group.Suburb,
		State:         group.State,
		Postcode:      group.Postcode,
		DwellingType:  dwellingType,
		OnlyWithSales: boolToInt64(onlyWithSales),
		Sort:          sortValue,
		RowOffset:     int64((data.Page - 1) * suburbSalesPageSize),
		RowLimit:      suburbSalesPageSize,
	})
	if err != nil {
		log.Printf("Street property list error: %v", err)
		app.renderSearchError(w, r, "index.html", "Search unavailable - SoldNSW", "Street search is temporarily unavailable. Please try again.")
		return
	}
	data.Properties = props

	pagerValues := url.Values{}
	pagerValues.Set("street", group.StreetName)
	pagerValues.Set("street_type", group.StreetType)
	pagerValues.Set("suburb", group.Suburb)
	pagerValues.Set("state", group.State)
	pagerValues.Set("postcode", group.Postcode)
	pagerValues.Set("view", "street")
	if dwellingType != "" {
		pagerValues.Set("dwelling", dwellingType)
	}
	if sortValue != "street" {
		pagerValues.Set("sort", sortValue)
	}
	if onlyWithSales {
		pagerValues.Set("only_with_sales", "1")
	}
	data.PagerBaseURL = pagerBaseURL("/properties", pagerValues)
	data.FilterAction = "/properties"
	data.FilterParams = queryParams(pagerValues, "dwelling", "sort", "only_with_sales")
	app.renderTemplate(w, "index.html", data)
}

func boolToInt64(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

// handlePostcodePage renders the postcode overview page.
func (app *App) handlePostcodePage(w http.ResponseWriter, r *http.Request) {
	postcode := strings.TrimSpace(r.PathValue("code"))
	if !validPostcode(postcode) {
		app.renderNotFound(w, r)
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	app.renderPostcodePage(w, r, postcode, page, app.readFlash(w, r))
}

// renderPostcodePage renders the postcode summary, suburb breakdown and its
// paginated property records.
func (app *App) renderPostcodePage(w http.ResponseWriter, r *http.Request, postcode string, page int, flash *FlashMessage) {
	ctx := r.Context()
	query := r.URL.Query()
	dwellingType := normalizeDwellingType(query.Get("dwelling"))
	sortValue := resultSort(strings.ToLower(strings.TrimSpace(query.Get("sort"))), "street", false)
	onlyWithSales := onlyWithSalesParam(query.Get("only_with_sales"))

	data := app.searchPageData(ctx)
	data.Title = "Postcode " + postcode + " - SoldNSW"
	data.SearchMode = "postcode"
	data.QueryPostcode = postcode
	data.QueryDwellingType = dwellingType
	data.QuerySort = sortValue
	data.QueryOnlyWithSales = onlyWithSales
	data.Flash = flash

	summary, err := app.queries.GetPostcodeSummary(ctx, postcode)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		log.Printf("Postcode summary error for %s: %v", postcode, err)
		app.renderPostcodeError(w, r, postcode, "Postcode unavailable - SoldNSW", "Postcode data is temporarily unavailable. Please try again.")
		return
	}
	data.PostcodeSummary = &summary
	suburbs, err := app.queries.GetPostcodeSuburbs(ctx, postcode)
	if err != nil {
		log.Printf("Postcode suburb list error for %s: %v", postcode, err)
		app.renderPostcodeError(w, r, postcode, "Postcode unavailable - SoldNSW", "Postcode data is temporarily unavailable. Please try again.")
		return
	}
	data.PostcodeSuburbs = suburbs

	total, err := app.queries.CountPostcodeProperties(ctx, database.CountPostcodePropertiesParams{
		Postcode:      postcode,
		DwellingType:  dwellingType,
		OnlyWithSales: boolToInt64(onlyWithSales),
	})
	if err != nil {
		log.Printf("Postcode property count error for %s: %v", postcode, err)
		app.renderPostcodeError(w, r, postcode, "Postcode unavailable - SoldNSW", "Postcode data is temporarily unavailable. Please try again.")
		return
	}
	setSearchPagination(&data, page, total, suburbSalesPageSize)
	props, err := app.queries.ListPostcodeProperties(ctx, database.ListPostcodePropertiesParams{
		Postcode:      postcode,
		DwellingType:  dwellingType,
		OnlyWithSales: boolToInt64(onlyWithSales),
		Sort:          sortValue,
		RowOffset:     int64((data.Page - 1) * suburbSalesPageSize),
		RowLimit:      suburbSalesPageSize,
	})
	if err != nil {
		log.Printf("Postcode property list error for %s: %v", postcode, err)
		app.renderPostcodeError(w, r, postcode, "Postcode unavailable - SoldNSW", "Postcode data is temporarily unavailable. Please try again.")
		return
	}
	data.Properties = props

	pagerValues := url.Values{}
	if dwellingType != "" {
		pagerValues.Set("dwelling", dwellingType)
	}
	if sortValue != "street" {
		pagerValues.Set("sort", sortValue)
	}
	if onlyWithSales {
		pagerValues.Set("only_with_sales", "1")
	}
	data.PagerBaseURL = pagerBaseURL("/postcode/"+postcode, pagerValues)
	data.FilterAction = "/postcode/" + postcode
	data.FilterParams = queryParams(pagerValues, "dwelling", "sort", "only_with_sales")
	app.renderTemplate(w, "postcode.html", data)
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
	if len([]rune(address)) > maxSearchQueryLength {
		app.renderSearchErrorStatus(w, r, "index.html", "Search query too long - SoldNSW", "Search queries must be 120 characters or fewer.", http.StatusBadRequest)
		return
	}
	suburb := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("suburb")))
	street := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("street")))
	streetType := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("street_type")))
	number := normalizedStreetNumber(r.URL.Query().Get("number"))
	state := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("state")))
	postcode := strings.TrimSpace(r.URL.Query().Get("postcode"))
	view := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("view")))
	dwellingType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("dwelling")))
	switch dwellingType {
	case "house", "unit", "townhouse", "other":
	default:
		dwellingType = ""
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	flash := app.readFlash(w, r)

	if address != "" {
		parsed := database.ParseSearchQuery(address)

		if parsed.Kind == database.SearchKindPropertyID {
			prop, err := app.queries.GetPropertyByPID(ctx, parsed.PropertyID)
			if err == nil {
				http.Redirect(w, r, "/property/"+prop.GnafPid, http.StatusSeeOther)
				return
			}
			if !errors.Is(err, sql.ErrNoRows) {
				log.Printf("Property ID search error for %q: %v", parsed.PropertyID, err)
				app.renderSearchError(w, r, "index.html", "Search unavailable - SoldNSW", "Property ID lookup is temporarily unavailable. Please try again.")
				return
			}
			data := app.searchPageData(ctx)
			data.Title = "Property ID not found - SoldNSW"
			data.SearchMode = "address"
			data.QueryAddress = address
			data.QuerySort = "relevance"
			data.SearchError = fmt.Sprintf("Property ID %s was not found.", parsed.PropertyID)
			data.Flash = flash
			app.renderTemplateStatus(w, "index.html", data, http.StatusNotFound)
			return
		}

		if parsed.Kind == database.SearchKindPostcode {
			app.renderPostcodePage(w, r, parsed.Postcode, page, flash)
			return
		}

		if parsed.Kind == database.SearchKindStreet {
			groups, total, err := app.findStreetGroupsPage(ctx, parsed, page, suburbSalesPageSize)
			if err != nil {
				log.Printf("Street search error for %q: %v", address, err)
				app.renderSearchError(w, r, "index.html", "Search unavailable - SoldNSW", "Street search is temporarily unavailable. Please try again.")
				return
			}
			if total > 0 && (page > 1 || streetGroupsMatchName(groups, parsed.StreetName)) {
				app.renderStreetGroups(w, r, address, groups, total, page, flash)
				return
			}
		}

		// Fall back to the address FTS index when the query is a full address
		// or a street prefix did not resolve.
		addressSort := resultSort(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("sort"))), "relevance", true)
		onlyWithSales := onlyWithSalesParam(r.URL.Query().Get("only_with_sales"))
		opts := database.AddressPropertySearchOptions{
			Query:         address,
			DwellingType:  dwellingType,
			OnlyWithSales: onlyWithSales,
			Sort:          addressSort,
		}
		total, err := database.CountAddressProperties(ctx, app.db, opts)
		if err != nil {
			log.Printf("Address search count error for %q: %v", address, err)
			app.renderSearchError(w, r, "index.html", "Search unavailable - SoldNSW", "Address search is temporarily unavailable. Please try again.")
			return
		}

		// Preserve the direct-to-property behaviour for exact addresses and
		// single results.
		if dwellingType == "" && !onlyWithSales {
			matches, ftsErr := database.SearchAddressFTS(ctx, app.db, address, 50)
			if ftsErr != nil {
				log.Printf("Address search error for %q: %v", address, ftsErr)
				app.renderSearchError(w, r, "index.html", "Search unavailable - SoldNSW", "Address search is temporarily unavailable. Please try again.")
				return
			}
			normalizedAddress := strings.Join(strings.Fields(strings.ToUpper(address)), " ")
			for _, match := range matches {
				if strings.Join(strings.Fields(match.Address), " ") == normalizedAddress {
					http.Redirect(w, r, "/property/"+match.GnafPID, http.StatusSeeOther)
					return
				}
			}
		}
		if total == 1 {
			props, err := database.SearchAddressProperties(ctx, app.db, database.AddressPropertySearchOptions{
				Query:         address,
				DwellingType:  dwellingType,
				OnlyWithSales: onlyWithSales,
				Sort:          addressSort,
				Limit:         1,
			})
			if err == nil && len(props) == 1 {
				http.Redirect(w, r, "/property/"+props[0].GnafPid, http.StatusSeeOther)
				return
			}
		}

		data := app.searchPageData(ctx)
		data.Title = "Address Search Results - SoldNSW"
		data.SearchMode = "address"
		data.QueryAddress = address
		data.QueryDwellingType = dwellingType
		data.QuerySort = addressSort
		data.QueryOnlyWithSales = onlyWithSales
		data.AllowRelevanceSort = true
		data.Flash = flash
		setSearchPagination(&data, page, total, suburbSalesPageSize)
		props, err := database.SearchAddressProperties(ctx, app.db, database.AddressPropertySearchOptions{
			Query:         address,
			DwellingType:  dwellingType,
			OnlyWithSales: onlyWithSales,
			Sort:          addressSort,
			Limit:         suburbSalesPageSize,
			Offset:        (data.Page - 1) * suburbSalesPageSize,
		})
		if err != nil {
			log.Printf("Address search error for %q: %v", address, err)
			app.renderSearchError(w, r, "index.html", "Search unavailable - SoldNSW", "Address search is temporarily unavailable. Please try again.")
			return
		}
		data.Properties = props

		pagerValues := url.Values{}
		pagerValues.Set("address", address)
		if dwellingType != "" {
			pagerValues.Set("dwelling", dwellingType)
		}
		if addressSort != "relevance" {
			pagerValues.Set("sort", addressSort)
		}
		if onlyWithSales {
			pagerValues.Set("only_with_sales", "1")
		}
		data.PagerBaseURL = pagerBaseURL("/properties", pagerValues)
		data.FilterAction = "/properties"
		data.FilterParams = queryParams(pagerValues, "dwelling", "sort", "only_with_sales")
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

	if postcode != "" && street == "" && suburb == "" && state == "" && number == "" {
		app.renderPostcodePage(w, r, postcode, page, flash)
		return
	}

	if suburb == "" && street == "" && state == "" && postcode == "" {
		if flash != nil {
			data := app.homePageData(ctx)
			data.Title = "NSW Property Sales & Suburb Trends - SoldNSW"
			data.Flash = flash
			app.renderTemplate(w, "index.html", data)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	parsed := database.ParseStructuredSearch(street, streetType, suburb, state, postcode, number)

	// A street selected from the grouped results carries the full tuple, so we
	// can list every property on it rather than showing the street again.
	exactStreetSelection := view == "street" && streetType != "" && suburb != "" && state != "" && postcode != ""
	if exactStreetSelection {
		app.renderStreetProperties(w, r, database.SearchStreetGroupsRow{
			StreetName: street,
			StreetType: streetType,
			Suburb:     suburb,
			State:      state,
			Postcode:   postcode,
		}, page, flash)
		return
	}

	if street != "" && number == "" {
		groups, total, err := app.findStreetGroupsPage(ctx, parsed, page, suburbSalesPageSize)
		if err != nil {
			log.Printf("Street search error for street %q: %v", street, err)
			app.renderSearchError(w, r, "index.html", "Search unavailable - SoldNSW", "Street search is temporarily unavailable. Please try again.")
			return
		}
		if total > 0 {
			if total == 1 && groups[0].PropertyCount == 1 {
				props, err := app.queries.ListStreetProperties(ctx, database.ListStreetPropertiesParams{
					StreetName: groups[0].StreetName,
					StreetType: groups[0].StreetType,
					Suburb:     groups[0].Suburb,
					State:      groups[0].State,
					Postcode:   groups[0].Postcode,
					RowOffset:  0,
					RowLimit:   1,
				})
				if err == nil && len(props) == 1 {
					http.Redirect(w, r, "/property/"+props[0].GnafPid, http.StatusSeeOther)
					return
				}
			}
			app.renderStreetGroups(w, r, "", groups, total, page, flash)
			return
		}
	}

	// SearchPropertiesFlexible handles the remaining structured combinations,
	// including number + street lookups.
	streetPattern := "%"
	if parsed.StreetPattern != "" {
		streetPattern = parsed.StreetPattern
	}
	numberPattern := "%"
	numberExact := ""
	if number != "" {
		numberPattern = number + "%"
		numberExact = number
	}
	structuredSort := resultSort(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("sort"))), "street", false)
	onlyWithSales := onlyWithSalesParam(r.URL.Query().Get("only_with_sales"))
	props, err := app.queries.SearchPropertiesFlexible(ctx, database.SearchPropertiesFlexibleParams{
		Suburb:        suburb,
		StreetPattern: streetPattern,
		State:         state,
		Postcode:      postcode,
		NumberPattern: numberPattern,
		NumberExact:   numberExact,
		DwellingType:  dwellingType,
		OnlyWithSales: boolToInt64(onlyWithSales),
		Sort:          structuredSort,
	})
	if err != nil {
		log.Printf("Search error for suburb '%s', street '%s', number '%s', postcode '%s': %v", suburb, street, number, postcode, err)
		app.renderSearchError(w, r, "index.html", "Search unavailable - SoldNSW", "Property search is temporarily unavailable. Please try again.")
		return
	}

	exactNumberMatch := len(props) > 0 && number != "" && (suburb != "" || postcode != "") &&
		normalizedStreetNumber(props[0].StreetNumber.String) == number
	if len(props) == 1 || exactNumberMatch {
		http.Redirect(w, r, "/property/"+props[0].GnafPid, http.StatusSeeOther)
		return
	}

	data := app.searchPageData(ctx)
	data.SearchMode = "address"
	data.QuerySuburb = suburb
	data.QueryStreet = street
	data.QueryStreetType = streetType
	data.QueryNumber = number
	data.QueryState = state
	data.QueryPostcode = postcode
	data.QueryDwellingType = dwellingType
	data.QuerySort = structuredSort
	data.QueryOnlyWithSales = onlyWithSales
	data.Properties = props
	data.ResultTotal = int64(len(props))
	data.Flash = flash
	filterValues := url.Values{}
	for _, name := range []string{"street", "street_type", "suburb", "state", "postcode", "number"} {
		if value := r.URL.Query().Get(name); value != "" {
			filterValues.Set(name, value)
		}
	}
	if dwellingType != "" {
		filterValues.Set("dwelling", dwellingType)
	}
	if structuredSort != "street" {
		filterValues.Set("sort", structuredSort)
	}
	if onlyWithSales {
		filterValues.Set("only_with_sales", "1")
	}
	data.FilterAction = "/properties"
	data.FilterParams = queryParams(filterValues, "dwelling", "sort", "only_with_sales")
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
	data.StatsStatus, data.StatsUpdated, _ = app.statsSnapshot()

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

	dwellingFilter := lossType
	if dwellingFilter == "all" {
		dwellingFilter = ""
	}
	total, err := app.queries.CountSuburbLosses(ctx, database.CountSuburbLossesParams{
		Cutoff:       lossCutoff(rangeValue),
		DwellingType: dwellingFilter,
		Suburb:       suburb,
		State:        state,
	})
	if err != nil {
		log.Printf("Error counting losses for %s: %v", suburb, err)
	}
	data.LossTotal = total
	setSearchPagination(&data, page, total, suburbSalesPageSize)

	losses, err := app.queries.GetSuburbLosses(ctx, database.GetSuburbLossesParams{
		Cutoff:       lossCutoff(rangeValue),
		Suburb:       suburb,
		State:        state,
		DwellingType: dwellingFilter,
		Sort:         sortValue,
		RowOffset:    int64((data.Page - 1) * suburbSalesPageSize),
		RowLimit:     suburbSalesPageSize,
	})
	if err != nil {
		log.Printf("Error fetching losses for %s: %v", suburb, err)
	}
	data.LossSales = losses

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

	// Record the sale against the canonical property so duplicate PSI rows do
	// not fragment the sale history or the analytics.
	ctx := r.Context()
	canonicalPID, err := database.GetCanonicalPID(ctx, app.db, gnafPID)
	if err != nil {
		log.Printf("Could not resolve canonical PID for %s: %v", gnafPID, err)
		canonicalPID = gnafPID
	}
	if canonicalPID == "" {
		canonicalPID = gnafPID
	}

	// Verify that the property exists in the database.
	prop, err := app.queries.GetPropertyByPID(ctx, canonicalPID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			app.renderSubmitError(w, r, http.StatusNotFound, gnafPID, settleDate, "That property ID was not found. Search for the address first, then add the sale from its property page.")
			return
		}
		log.Printf("Database error looking up property '%s': %v", canonicalPID, err)
		app.renderSubmitError(w, r, http.StatusInternalServerError, gnafPID, settleDate, "We could not check that property right now. Please try again.")
		return
	}

	saleID := "SALE-" + time.Now().Format("2006") + "-" + strings.ToUpper(uuid.New().String()[:8])

	insertParams := database.InsertPropertySaleParams{
		SaleID:         saleID,
		GnafPid:        canonicalPID,
		SettlementDate: settleDate,
		SalePrice:      price,
		SourceType:     sourceType,
	}

	if err := app.queries.InsertPropertySale(ctx, insertParams); err != nil {
		log.Printf("Failed to insert property sale: %v", err)
		app.renderSubmitError(w, r, http.StatusInternalServerError, gnafPID, settleDate, "The sale could not be saved. Please check the values and try again.")
		return
	}

	if err := database.RefreshCanonicalSale(ctx, app.db, saleID); err != nil {
		log.Printf("Could not refresh canonical sale %s: %v", saleID, err)
		if rebuildErr := database.RebuildCanonicalSales(ctx, app.db); rebuildErr != nil {
			log.Printf("Could not rebuild canonical sales after refresh failure: %v", rebuildErr)
		}
	}
	if err := database.RefreshSuburbStat(ctx, app.db, prop.Suburb, prop.State); err != nil {
		log.Printf("Could not refresh suburb statistics: %v", err)
	}
	if err := database.RefreshStreetStat(ctx, app.db, prop.StreetName, prop.StreetType, prop.Suburb, prop.State, prop.Postcode); err != nil {
		log.Printf("Could not refresh street statistics: %v", err)
	}
	if err := database.RefreshPostcodeStat(ctx, app.db, prop.Postcode); err != nil {
		log.Printf("Could not refresh postcode statistics: %v", err)
	}
	log.Printf("Successfully recorded new sale %s for %s at $%d", saleID, canonicalPID, price)
	app.setFlash(w, fmt.Sprintf("Sale of $%s recorded for %s, %s.", formatPriceInt(price), propertyAddress(prop), prop.Suburb))
	http.Redirect(w, r, "/property/"+canonicalPID, http.StatusSeeOther)
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

// renderPostcodeError renders a postcode page with a service-unavailable
// notice while keeping the requested postcode in the title/context.
func (app *App) renderPostcodeError(w http.ResponseWriter, r *http.Request, postcode, title, message string) {
	data := app.searchPageData(r.Context())
	data.Title = title
	data.SearchMode = "postcode"
	data.QueryPostcode = postcode
	data.SearchError = message
	data.Flash = app.readFlash(w, r)
	app.renderTemplateStatus(w, "postcode.html", data, http.StatusServiceUnavailable)
}

// lastImportDate returns the most recent successful or partial import date.
// The value is cached briefly because it appears in the shared footer.
func (app *App) getLastImportDate() string {
	if app.db == nil || app.queries == nil {
		return ""
	}
	app.lastImportMu.Lock()
	defer app.lastImportMu.Unlock()
	if !app.lastImportChecked.IsZero() && time.Since(app.lastImportChecked) < time.Minute {
		return app.lastImportDate
	}
	date, err := app.queries.GetLastSuccessfulImport(context.Background())
	if err == nil {
		app.lastImportDate = date
	} else if errors.Is(err, sql.ErrNoRows) {
		app.lastImportDate = ""
	} else {
		log.Printf("Could not read last import date: %v", err)
	}
	app.lastImportChecked = time.Now()
	return app.lastImportDate
}

// enrichPageData fills shared template fields that are not set by individual
// handlers.
func (app *App) enrichPageData(data any) any {
	switch page := data.(type) {
	case PageData:
		if page.LastImportDate == "" {
			page.LastImportDate = app.getLastImportDate()
		}
		return page
	case *PageData:
		if page != nil && page.LastImportDate == "" {
			page.LastImportDate = app.getLastImportDate()
		}
		return page
	default:
		return data
	}
}

func (app *App) renderTemplate(w http.ResponseWriter, name string, data any) {
	app.renderTemplateStatus(w, name, data, http.StatusOK)
}

func (app *App) renderTemplateStatus(w http.ResponseWriter, name string, data any, status int) {
	data = app.enrichPageData(data)
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
