package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"

	_ "github.com/mattn/go-sqlite3"
	"soldproperty/internal/database"
)

// Embedded sample dataset for immediate out-of-the-box seeding
const defaultGnafCSV = `gnaf_pid,street_number,street_name,street_type,suburb,state,postcode,latitude,longitude,lot_number,plan_number
GANSW718293801,120,CROWN,ST,SURRY HILLS,NSW,2010,-33.8821,151.2144,12,DP54921
GANSW718293802,245,BOURKE,ST,SURRY HILLS,NSW,2010,-33.8805,151.2162,5,DP89214
GANSW718293803,42,FOVEAUX,ST,SURRY HILLS,NSW,2010,-33.8839,151.2115,8,DP10293
GANSW718293804,88,CAMPBELL,ST,SURRY HILLS,NSW,2010,-33.8812,151.2128,19,DP38192
GANSW718293805,15,DEVONSHIRE,ST,SURRY HILLS,NSW,2010,-33.8864,151.2098,3,DP72819
GANSW718293806,304,OXFORD,ST,PADDINGTON,NSW,2021,-33.8872,151.2265,14,DP49281
GANSW718293807,78,GLENMORE,RD,PADDINGTON,NSW,2021,-33.8845,151.2291,22,DP68192
GANSW718293808,112,JERSEY,RD,PADDINGTON,NSW,2021,-33.8891,151.2314,7,DP91823
GANSW718293809,45,UNDERWOOD,ST,PADDINGTON,NSW,2021,-33.8860,151.2248,11,DP58271
GANSW718293810,18,BELGRAVE,ST,MANLY,NSW,2095,-33.7994,151.2856,4,DP39182
GANSW718293811,64,DARLEY,RD,MANLY,NSW,2095,-33.8041,151.2892,16,DP82914
GANSW718293812,22,THE CORSO,,MANLY,NSW,2095,-33.7981,151.2869,9,DP47281
GANSW718293813,105,CAMPBELL,PDE,BONDI,NSW,2026,-33.8912,151.2774,25,DP94821
GANSW718293814,54,GLENAYR,AVE,BONDI,NSW,2026,-33.8895,151.2721,18,DP63912
GANSW718293815,19,HALL,ST,BONDI,NSW,2026,-33.8884,151.2738,10,DP84920
GAVIC819203901,84,SWAN,ST,RICHMOND,VIC,3121,-37.8252,144.9945,6,LP48291
GAVIC819203902,152,BRIDGE,RD,RICHMOND,VIC,3121,-37.8189,145.0012,15,LP73918
GAVIC819203903,210,CHURCH,ST,RICHMOND,VIC,3121,-37.8214,145.0034,2,LP84921
GAVIC819203904,45,VICTORIA,ST,RICHMOND,VIC,3121,-37.8112,145.0089,31,LP91824
GAVIC819203905,128,GERTRUDE,ST,FITZROY,VIC,3065,-37.8055,144.9782,12,LP58291
GAVIC819203906,290,BRUNSWICK,ST,FITZROY,VIC,3065,-37.7989,144.9789,8,LP38192
GAVIC819203907,74,SMITH,ST,FITZROY,VIC,3065,-37.8031,144.9834,19,LP62918
GAVIC819203908,51,NAPIER,ST,FITZROY,VIC,3065,-37.8012,144.9801,4,LP82910
GAVIC819203909,92,TOORAK,RD,SOUTH YARRA,VIC,3141,-37.8395,144.9882,23,LP74821
GAVIC819203910,188,CHAPEL,ST,SOUTH YARRA,VIC,3141,-37.8421,144.9928,17,LP92831
GAQLD928301801,650,BRUNSWICK,ST,NEW FARM,QLD,4005,-27.4645,153.0452,14,RP83912
GAQLD928301802,88,JAMES,ST,NEW FARM,QLD,4005,-27.4589,153.0421,5,RP49281
GAQLD928301803,142,MERTHYR,RD,NEW FARM,QLD,4005,-27.4682,153.0512,21,RP67291
GAQLD928301804,33,MORAY,ST,NEW FARM,QLD,4005,-27.4715,153.0489,9,RP89213
GAQLD928301805,110,MACQUARIE,ST,TENERIFFE,QLD,4005,-27.4561,153.0545,16,RP94821
GAQLD928301806,52,VERNON,TCE,TENERIFFE,QLD,4005,-27.4532,153.0528,7,RP58291
GAQLD928301807,24,FLORENCE,ST,TENERIFFE,QLD,4005,-27.4549,153.0498,11,RP73910
GAWA192830101,45,ROKEBY,RD,SUBIACO,WA,6008,-31.9472,115.8234,3,DP48291
GAWA192830102,180,HAY,ST,SUBIACO,WA,6008,-31.9445,115.8261,15,DP73912
GAWA192830103,72,BAGOT,RD,SUBIACO,WA,6008,-31.9512,115.8219,8,DP84920
GAWA192830104,14,MARINE,PDE,COTTESLOE,WA,6011,-31.9945,115.7512,2,DP39182
GAWA192830105,88,BROOME,ST,COTTESLOE,WA,6011,-31.9912,115.7564,28,DP92814
GASA293840101,160,THE PARADE,,NORWOOD,SA,5067,-34.9212,138.6345,10,FP48291
GASA293840102,42,OSMOND,TCE,NORWOOD,SA,5067,-34.9234,138.6312,19,FP67281
GASA293840103,77,WILLIAM,ST,NORWOOD,SA,5067,-34.9198,138.6378,6,FP83912
GASA293840104,215,UNLEY,RD,UNLEY,SA,5061,-34.9489,138.6056,14,FP58291
GASA293840105,82,KING WILLIAM,RD,UNLEY,SA,5061,-34.9456,138.6012,25,FP91823
GATAS394820101,36,HAMPDEN,RD,BATTERY POINT,TAS,7004,-42.8891,147.3345,4,SP48291
GATAS394820102,19,KELLY,ST,BATTERY POINT,TAS,7004,-42.8872,147.3321,11,SP63912
GATAS394820103,55,COLVILLE,ST,BATTERY POINT,TAS,7004,-42.8912,147.3368,8,SP82914
GAACT492810101,28,GROSE,ST,KINGSTON,ACT,2604,-35.3189,149.1412,17,DP48291
GAACT492810102,62,GILES,ST,KINGSTON,ACT,2604,-35.3164,149.1445,9,DP73910
GAACT492810103,40,LONSDALE,ST,BRADDON,ACT,2612,-35.2745,149.1321,12,DP83912
GANT592810101,58,MITCHELL,ST,DARWIN CITY,NT,0800,-12.4634,130.8412,5,NT48291
GANT592810102,124,SMITH,ST,DARWIN CITY,NT,0800,-12.4612,130.8445,18,NT73912
`

const defaultSalesCSV = `sale_id,gnaf_pid,settlement_date,sale_price,source_type
SALE-2026-001,GANSW718293801,2026-02-14,2450000,Auction
SALE-2023-002,GANSW718293801,2023-05-18,1920000,Public Registry
SALE-2025-003,GANSW718293802,2025-11-20,3100000,Private Treaty
SALE-2021-004,GANSW718293802,2021-03-10,2300000,Public Registry
SALE-2026-005,GANSW718293803,2026-01-22,1850000,Auction
SALE-2024-006,GANSW718293804,2024-09-15,2250000,Private Treaty
SALE-2025-007,GANSW718293805,2025-07-30,1650000,Public Registry
SALE-2026-008,GANSW718293806,2026-03-01,4200000,Auction
SALE-2022-009,GANSW718293806,2022-06-14,3400000,Public Registry
SALE-2025-010,GANSW718293807,2025-10-12,3850000,Private Treaty
SALE-2024-011,GANSW718293808,2024-12-05,2950000,Auction
SALE-2025-012,GANSW718293809,2025-04-18,2700000,Public Registry
SALE-2026-013,GANSW718293810,2026-02-28,3600000,Auction
SALE-2023-014,GANSW718293810,2023-08-22,2900000,Public Registry
SALE-2025-015,GANSW718293811,2025-11-10,4800000,Private Treaty
SALE-2024-016,GANSW718293812,2024-05-19,3250000,Public Registry
SALE-2026-017,GANSW718293813,2026-01-15,5100000,Auction
SALE-2022-018,GANSW718293813,2022-11-04,4100000,Public Registry
SALE-2025-019,GANSW718293814,2025-08-21,3350000,Private Treaty
SALE-2024-020,GANSW718293815,2024-04-14,2900000,Public Registry
SALE-2026-021,GAVIC819203901,2026-02-10,1750000,Auction
SALE-2023-022,GAVIC819203901,2023-09-17,1420000,Public Registry
SALE-2025-023,GAVIC819203902,2025-12-01,1620000,Private Treaty
SALE-2025-024,GAVIC819203903,2025-06-18,1950000,Auction
SALE-2024-025,GAVIC819203904,2024-10-25,1380000,Public Registry
SALE-2026-026,GAVIC819203905,2026-01-30,2150000,Auction
SALE-2022-027,GAVIC819203905,2022-04-12,1780000,Public Registry
SALE-2025-028,GAVIC819203906,2025-09-14,2600000,Private Treaty
SALE-2024-029,GAVIC819203907,2024-11-20,1850000,Auction
SALE-2025-030,GAVIC819203908,2025-03-08,1920000,Public Registry
SALE-2026-031,GAVIC819203909,2026-02-05,2850000,Auction
SALE-2023-032,GAVIC819203909,2023-07-29,2300000,Public Registry
SALE-2025-033,GAVIC819203910,2025-10-04,2100000,Private Treaty
SALE-2026-034,GAQLD928301801,2026-02-18,2950000,Auction
SALE-2023-035,GAQLD928301801,2023-10-15,2250000,Public Registry
SALE-2025-036,GAQLD928301802,2025-08-30,3400000,Private Treaty
SALE-2025-037,GAQLD928301803,2025-05-12,2750000,Auction
SALE-2024-038,GAQLD928301804,2024-11-18,3100000,Public Registry
SALE-2026-039,GAQLD928301805,2026-01-20,3800000,Auction
SALE-2023-040,GAQLD928301805,2023-03-24,2950000,Public Registry
SALE-2025-041,GAQLD928301806,2025-09-05,2450000,Private Treaty
SALE-2024-042,GAQLD928301807,2024-07-14,2150000,Public Registry
SALE-2026-043,GAWA192830101,2026-01-28,1850000,Private Treaty
SALE-2023-044,GAWA192830101,2023-04-19,1450000,Public Registry
SALE-2025-045,GAWA192830102,2025-11-12,1650000,Auction
SALE-2024-046,GAWA192830103,2024-08-22,1750000,Public Registry
SALE-2026-047,GAWA192830104,2026-02-12,4600000,Private Treaty
SALE-2023-048,GAWA192830104,2023-11-05,3750000,Public Registry
SALE-2025-049,GAWA192830105,2025-06-30,3100000,Auction
SALE-2026-050,GASA293840101,2026-02-02,1550000,Auction
SALE-2023-051,GASA293840101,2023-06-11,1200000,Public Registry
SALE-2025-052,GASA293840102,2025-10-19,1750000,Private Treaty
SALE-2024-053,GASA293840103,2024-05-14,1400000,Public Registry
SALE-2026-054,GASA293840104,2026-01-18,1950000,Auction
SALE-2025-055,GASA293840105,2025-09-22,1850000,Private Treaty
SALE-2026-056,GATAS394820101,2026-02-15,2250000,Private Treaty
SALE-2023-057,GATAS394820101,2023-08-10,1800000,Public Registry
SALE-2025-058,GATAS394820102,2025-07-25,1950000,Auction
SALE-2024-059,GATAS394820103,2024-12-10,1650000,Public Registry
SALE-2026-060,GAACT492810101,2026-01-25,1350000,Auction
SALE-2025-061,GAACT492810102,2025-11-14,1450000,Private Treaty
SALE-2025-062,GAACT492810103,2025-08-08,1150000,Public Registry
SALE-2026-063,GANT592810101,2026-02-10,850000,Private Treaty
SALE-2025-064,GANT592810102,2025-09-18,920000,Public Registry
`

func main() {
	dbPath := flag.String("db", "properties.db", "Path to SQLite database file")
	gnafCSVPath := flag.String("gnaf", "", "Path to custom G-NAF CSV (optional)")
	salesCSVPath := flag.String("sales", "", "Path to custom Sales CSV (optional)")
	flag.Parse()

	log.Printf("Connecting to SQLite database: %s", *dbPath)
	db, err := sql.Open("sqlite3", *dbPath)
	if err != nil {
		log.Fatalf("Failed to open SQLite database: %v", err)
	}
	defer db.Close()

	db.SetMaxOpenConns(1)

	// Apply SQLite PRAGMAs for high-performance WAL mode and strict integrity
	pragmas := []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA synchronous=NORMAL;",
		"PRAGMA foreign_keys=ON;",
		"PRAGMA busy_timeout=5000;",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			log.Fatalf("Failed to execute PRAGMA '%s': %v", p, err)
		}
	}
	log.Println("Applied SQLite PRAGMAs (WAL mode, foreign_keys=ON, synchronous=NORMAL)")

	// Apply the embedded schema so seeding works on a fresh database from any
	// working directory.
	if _, err := db.Exec(database.SchemaSQL); err != nil {
		log.Fatalf("Failed to apply schema: %v", err)
	}
	if err := database.EnsurePropertyColumns(context.Background(), db); err != nil {
		log.Fatalf("Failed to migrate property columns: %v", err)
	}
	if err := database.EnsureSuburbStatsSchema(context.Background(), db); err != nil {
		log.Fatalf("Failed to migrate suburb stats: %v", err)
	}
	log.Println("Applied embedded SQLite STRICT schema")

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		log.Fatalf("Failed to begin transaction: %v", err)
	}
	defer tx.Rollback()

	queries := database.New(tx)

	// Ingest G-NAF Properties
	var gnafReader *csv.Reader
	if *gnafCSVPath != "" {
		f, err := os.Open(*gnafCSVPath)
		if err != nil {
			log.Fatalf("Failed to open G-NAF CSV '%s': %v", *gnafCSVPath, err)
		}
		defer f.Close()
		gnafReader = csv.NewReader(f)
	} else {
		log.Println("Using embedded sample G-NAF dataset (Australian Capital & Regional Suburbs)...")
		gnafReader = csv.NewReader(strings.NewReader(defaultGnafCSV))
	}

	// Read G-NAF header
	header, err := gnafReader.Read()
	if err != nil {
		log.Fatalf("Failed to read G-NAF CSV header: %v", err)
	}
	log.Printf("G-NAF header: %v", header)

	gnafCount := 0
	for {
		record, err := gnafReader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Printf("Warning: error reading G-NAF CSV row: %v", err)
			continue
		}
		if len(record) < 11 {
			continue
		}

		pid := strings.TrimSpace(record[0])
		stNum := strings.TrimSpace(record[1])
		stName := strings.TrimSpace(record[2])
		stType := strings.TrimSpace(record[3])
		suburb := strings.ToUpper(strings.TrimSpace(record[4]))
		state := strings.ToUpper(strings.TrimSpace(record[5]))
		postcode := strings.TrimSpace(record[6])
		latStr := strings.TrimSpace(record[7])
		lngStr := strings.TrimSpace(record[8])
		lotNum := strings.TrimSpace(record[9])
		planNum := strings.TrimSpace(record[10])
		propertyType := ""
		if len(record) > 11 {
			propertyType = strings.ToUpper(strings.TrimSpace(record[11]))
		}
		var landAreaVal sql.NullFloat64
		if len(record) > 12 {
			if f, err := strconv.ParseFloat(strings.TrimSpace(record[12]), 64); err == nil && f > 0 {
				landAreaVal = sql.NullFloat64{Float64: f, Valid: true}
			}
		}

		var latVal sql.NullFloat64
		if latStr != "" {
			if f, err := strconv.ParseFloat(latStr, 64); err == nil {
				latVal = sql.NullFloat64{Float64: f, Valid: true}
			}
		}

		var lngVal sql.NullFloat64
		if lngStr != "" {
			if f, err := strconv.ParseFloat(lngStr, 64); err == nil {
				lngVal = sql.NullFloat64{Float64: f, Valid: true}
			}
		}

		params := database.InsertGnafPropertyParams{
			GnafPid:      pid,
			StreetNumber: sql.NullString{String: stNum, Valid: stNum != ""},
			StreetName:   stName,
			StreetType:   stType,
			Suburb:       suburb,
			State:        state,
			Postcode:     postcode,
			Latitude:     latVal,
			Longitude:    lngVal,
			LotNumber:    sql.NullString{String: lotNum, Valid: lotNum != ""},
			PlanNumber:   sql.NullString{String: planNum, Valid: planNum != ""},
			PropertyType: sql.NullString{String: propertyType, Valid: propertyType != ""},
			LandArea:     landAreaVal,
			BasePid:      sql.NullString{String: pid, Valid: true},
			DwellingType: database.ClassifyDwellingType(propertyType, stNum, planNum),
		}

		if err := queries.InsertGnafProperty(ctx, params); err != nil {
			log.Fatalf("Failed to insert G-NAF property %s: %v", pid, err)
		}
		gnafCount++
	}
	log.Printf("Successfully inserted/updated %d G-NAF properties", gnafCount)

	// Ingest Sales Records
	var salesReader *csv.Reader
	if *salesCSVPath != "" {
		f, err := os.Open(*salesCSVPath)
		if err != nil {
			log.Fatalf("Failed to open Sales CSV '%s': %v", *salesCSVPath, err)
		}
		defer f.Close()
		salesReader = csv.NewReader(f)
	} else {
		log.Println("Using embedded sample Sales dataset (historical Australian settlements)...")
		salesReader = csv.NewReader(strings.NewReader(defaultSalesCSV))
	}

	salesHeader, err := salesReader.Read()
	if err != nil {
		log.Fatalf("Failed to read Sales CSV header: %v", err)
	}
	log.Printf("Sales header: %v", salesHeader)

	salesCount := 0
	for {
		record, err := salesReader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Printf("Warning: error reading Sales CSV row: %v", err)
			continue
		}
		if len(record) < 5 {
			continue
		}

		saleID := strings.TrimSpace(record[0])
		gnafPID := strings.TrimSpace(record[1])
		settleDate := strings.TrimSpace(record[2])
		priceStr := strings.TrimSpace(record[3])
		sourceType := strings.TrimSpace(record[4])

		price, err := strconv.ParseInt(priceStr, 10, 64)
		if err != nil {
			log.Printf("Warning: invalid price '%s' for sale %s: %v", priceStr, saleID, err)
			continue
		}

		params := database.InsertPropertySaleParams{
			SaleID:         saleID,
			GnafPid:        gnafPID,
			SettlementDate: settleDate,
			SalePrice:      price,
			SourceType:     sourceType,
		}

		if err := queries.InsertPropertySale(ctx, params); err != nil {
			// If already inserted in a prior run, ignore duplicate or log
			log.Printf("Note on sale %s insert: %v", saleID, err)
			continue
		}
		salesCount++
	}
	log.Printf("Successfully inserted %d Property Sales records", salesCount)

	if err := tx.Commit(); err != nil {
		log.Fatalf("Failed to commit transaction: %v", err)
	}

	if err := database.RebuildPropertyLinks(ctx, db); err != nil {
		log.Printf("Warning: could not rebuild property links: %v", err)
	}
	if err := database.RebuildCanonicalSales(ctx, db); err != nil {
		log.Printf("Warning: could not rebuild canonical sales: %v", err)
	}
	if err := database.RefreshSuburbStats(ctx, db); err != nil {
		log.Printf("Warning: could not rebuild suburb statistics: %v", err)
	}
	if err := database.RefreshStreetStats(ctx, db); err != nil {
		log.Printf("Warning: could not rebuild street statistics: %v", err)
	}
	if err := database.RefreshPostcodeStats(ctx, db); err != nil {
		log.Printf("Warning: could not rebuild postcode statistics: %v", err)
	}
	if err := database.RefreshSuburbLossStats(ctx, db); err != nil {
		log.Printf("Warning: could not rebuild suburb loss statistics: %v", err)
	}
	if err := database.RebuildAddressSearch(ctx, db); err != nil {
		log.Printf("Warning: could not rebuild address search index: %v", err)
	}

	fmt.Println("\n========================================================")
	fmt.Printf(" [SUCCESS] Ingest complete!\n")
	fmt.Printf(" - Database: %s (SQLite STRICT + WAL)\n", *dbPath)
	fmt.Printf(" - G-NAF Properties: %d\n", gnafCount)
	fmt.Printf(" - Settled Sales Records: %d\n", salesCount)
	fmt.Println("========================================================")
}
