package nswpsi

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ParsedProperty represents a property extracted from a NSW PSI record
type ParsedProperty struct {
	GnafPID      string
	BaseGnafPID  string
	PropertyID   string
	DistrictCode string
	StreetNumber string
	StreetName   string
	StreetType   string
	Suburb       string
	State        string
	Postcode     string
	LotNumber    string
	PlanNumber   string
	Zoning       string
	PropertyType string
	LandArea     float64
}

// ParsedSale represents a sales record extracted from a NSW PSI record
type ParsedSale struct {
	SaleID         string
	GnafPID        string
	SettlementDate string
	ContractDate   string
	SalePrice      int64
	SourceType     string
	DealingNumber  string
	DistrictCode   string
}

// Common Australian street suffixes
var streetTypes = map[string]string{
	"ST":        "ST",
	"STREET":    "ST",
	"RD":        "RD",
	"ROAD":      "RD",
	"AVE":       "AVE",
	"AV":        "AVE",
	"AVENUE":    "AVE",
	"CL":        "CL",
	"CLOSE":     "CL",
	"CT":        "CT",
	"COURT":     "CT",
	"CRES":      "CRES",
	"CR":        "CRES",
	"CRESCENT":  "CRES",
	"DR":        "DR",
	"DRIVE":     "DR",
	"LANE":      "LANE",
	"LN":        "LANE",
	"PDE":       "PDE",
	"PARADE":    "PDE",
	"PL":        "PL",
	"PLACE":     "PL",
	"WAY":       "WAY",
	"CIR":       "CIR",
	"CCT":       "CCT",
	"CIRCUIT":   "CCT",
	"TCE":       "TCE",
	"TERRACE":   "TCE",
	"GR":        "GR",
	"GROVE":     "GR",
	"ESP":       "ESP",
	"ESPLANADE": "ESP",
	"BLVD":      "BLVD",
	"BVD":       "BLVD",
	"BOULEVARD": "BLVD",
	"HWY":       "HWY",
	"HIGHWAY":   "HWY",
	"ACCS":      "ACCS",
	"ACCESS":    "ACCS",
	"WK":        "WK",
	"WALK":      "WK",
	"SQ":        "SQ",
	"SQUARE":    "SQ",
	"TRK":       "TRK",
	"TRACK":     "TRK",
	"MEWS":      "MEWS",
	"PKWY":      "PKWY",
	"PARKWAY":   "PKWY",
	"RISE":      "RISE",
	"PASS":      "PASS",
	"VIEW":      "VIEW",
	"VWS":       "VIEW",
	"VIEWS":     "VIEW",
	"ROW":       "ROW",
	"LOOP":      "LOOP",
	"GLADE":     "GLD",
	"GLD":       "GLD",
	"COVE":      "COVE",
	"EST":       "EST",
	"ESTATE":    "EST",
	"RIDGE":     "RDG",
	"RDG":       "RDG",
}

// Regex for extracting lot and plan numbers from strings like "56/270293" or "LOT 12 DP 38192" or "12/SP10445"
var (
	slashLotPlanRegex = regexp.MustCompile(`(?i)^([A-Z0-9]+)/([A-Z0-9]+)$`)
	dpLotRegex        = regexp.MustCompile(`(?i)LOT\s*([A-Z0-9]+).*?(?:DP|SP|LP|RP|FP)\s*([0-9]+)`)
	lotOnlyRegex      = regexp.MustCompile(`(?i)LOT\s*([A-Z0-9]+)`)
	dpOnlyRegex       = regexp.MustCompile(`(?i)(?:DP|SP|LP|RP|FP)\s*([0-9]+)`)
)

// ParseDATStream parses an incoming stream of .DAT records (1990-2026 format)
func ParseDATStream(r io.Reader) ([]ParsedProperty, []ParsedSale, error) {
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	var properties []ParsedProperty
	var sales []ParsedSale

	propIndexMap := make(map[string]int)
	recordIndex := make(map[string]int)
	lastIndexByBase := make(map[string]int)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		parts := strings.Split(line, ";")
		if len(parts) < 2 {
			continue
		}

		recType := strings.ToUpper(strings.TrimSpace(parts[0]))

		switch recType {
		case "B":
			prop, sale, ok := parseRecordB(parts)
			if ok {
				idx, exists := propIndexMap[prop.GnafPID]
				if exists {
					if prop.LotNumber != "" && properties[idx].LotNumber == "" {
						properties[idx].LotNumber = prop.LotNumber
					}
					if prop.PlanNumber != "" && properties[idx].PlanNumber == "" {
						properties[idx].PlanNumber = prop.PlanNumber
					}
				} else {
					idx = len(properties)
					propIndexMap[prop.GnafPID] = idx
					properties = append(properties, prop)
				}
				recordIndex[datRecordKey(parts)] = idx
				lastIndexByBase[prop.BaseGnafPID] = idx
				sales = append(sales, sale)
			}

		case "C":
			// Record C carries the legal lot/plan for the immediately preceding
			// B record. Matching by record sequence/timestamp is essential for
			// strata buildings where many units share one PSI property ID.
			if len(parts) >= 6 {
				cadastral := strings.TrimSpace(parts[5])
				idx, exists := recordIndex[datRecordKey(parts)]
				if !exists {
					district := strings.TrimSpace(parts[1])
					propID := strings.TrimSpace(parts[2])
					idx, exists = lastIndexByBase[makeBaseGnafPID(district, propID)]
				}
				if exists && cadastral != "" {
					lot, plan := parseCadastral(cadastral)
					if lot != "" {
						properties[idx].LotNumber = lot
					}
					if plan != "" {
						properties[idx].PlanNumber = plan
					}
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return properties, sales, err
	}

	return properties, sales, nil
}

// parseRecordB parses modern (2001+) and historical (1990-2000) Record B lines
func parseRecordB(parts []string) (ParsedProperty, ParsedSale, bool) {
	// Detect historical format: parts[2] is ARCHIVE / VALNET or parts[10] contains "/"
	if len(parts) >= 12 && (strings.EqualFold(parts[2], "ARCHIVE") || strings.EqualFold(parts[2], "VALNET1") || strings.Contains(parts[10], "/")) {
		return parseHistoricalRecordB(parts)
	}

	// Modern format (2001 - 2026)
	if len(parts) >= 16 {
		return parseModernRecordB(parts)
	}

	return ParsedProperty{}, ParsedSale{}, false
}

func parseModernRecordB(parts []string) (ParsedProperty, ParsedSale, bool) {
	district := strings.TrimSpace(parts[1])
	propID := strings.TrimSpace(parts[2])
	if propID == "" {
		return ParsedProperty{}, ParsedSale{}, false
	}

	unitPrefix := strings.TrimSpace(parts[5])
	unitNo := strings.TrimSpace(parts[6])
	houseNo := strings.TrimSpace(parts[7])
	streetRaw := strings.TrimSpace(parts[8])
	suburb := strings.ToUpper(strings.TrimSpace(parts[9]))
	postcode := strings.TrimSpace(parts[10])

	areaStr := strings.TrimSpace(parts[11])
	contractDateStr := strings.TrimSpace(parts[13])
	settleDateStr := strings.TrimSpace(parts[14])
	priceStr := strings.TrimSpace(parts[15])
	zoning := ""
	if len(parts) > 16 {
		zoning = strings.TrimSpace(parts[16])
	}
	purpose := ""
	if len(parts) > 18 {
		purpose = strings.TrimSpace(parts[18])
	}
	dealingNo := ""
	if len(parts) > 23 {
		dealingNo = strings.TrimSpace(parts[23])
	}

	price, err := strconv.ParseInt(priceStr, 10, 64)
	if err != nil || price <= 10000 {
		return ParsedProperty{}, ParsedSale{}, false
	}

	settleDate := normalizeDate(settleDateStr)
	contractDate := normalizeDate(contractDateStr)
	if settleDate == "" {
		if contractDate != "" {
			settleDate = contractDate
		} else {
			return ParsedProperty{}, ParsedSale{}, false
		}
	}
	if !validSaleDate(settleDate) {
		return ParsedProperty{}, ParsedSale{}, false
	}

	postcode = normalizePostcode(postcode)
	if len(postcode) != 4 {
		return ParsedProperty{}, ParsedSale{}, false
	}

	var fullUnit string
	if unitPrefix != "" && unitNo != "" {
		fullUnit = unitPrefix + " " + unitNo
	} else if unitNo != "" {
		fullUnit = unitNo
	} else if unitPrefix != "" {
		fullUnit = unitPrefix
	}

	streetNo := houseNo
	if fullUnit != "" {
		if streetNo != "" {
			streetNo = fullUnit + "/" + streetNo
		} else {
			streetNo = fullUnit
		}
	}

	stName, stType := splitStreetNameAndType(streetRaw)
	if stName == "" {
		stName = "UNKNOWN"
	}
	if stType == "" {
		stType = "ST"
	}

	var landArea float64
	if areaStr != "" {
		if a, err := strconv.ParseFloat(areaStr, 64); err == nil {
			landArea = a
		}
	}
	areaUnit := ""
	if len(parts) > 12 {
		areaUnit = strings.ToUpper(strings.TrimSpace(parts[12]))
	}
	switch areaUnit {
	case "H":
		landArea *= 10000
	case "A":
		landArea *= 4046.8564224
	}

	gnafPID := makeGnafPID(district, propID, fullUnit)
	baseGnafPID := makeBaseGnafPID(district, propID)
	saleID := makeSaleID(district, propID, settleDate, dealingNo)

	prop := ParsedProperty{
		GnafPID:      gnafPID,
		BaseGnafPID:  baseGnafPID,
		PropertyID:   propID,
		DistrictCode: district,
		StreetNumber: streetNo,
		StreetName:   stName,
		StreetType:   stType,
		Suburb:       suburb,
		State:        "NSW",
		Postcode:     postcode,
		Zoning:       zoning,
		PropertyType: purpose,
		LandArea:     landArea,
	}

	sale := ParsedSale{
		SaleID:         saleID,
		GnafPID:        gnafPID,
		SettlementDate: settleDate,
		ContractDate:   contractDate,
		SalePrice:      price,
		SourceType:     "NSW Valuer General (PSI)",
		DealingNumber:  dealingNo,
		DistrictCode:   district,
	}

	return prop, sale, true
}

func parseHistoricalRecordB(parts []string) (ParsedProperty, ParsedSale, bool) {
	district := strings.TrimSpace(parts[1])
	propID := ""
	if len(parts) > 4 {
		propID = strings.TrimSpace(parts[4])
	}
	if propID == "" && len(parts) > 3 {
		propID = strings.TrimSpace(parts[3])
	}
	if propID == "" {
		propID = fmt.Sprintf("HIST_%d", time.Now().UnixNano()%10000000)
	}

	unitNo := ""
	if len(parts) > 5 {
		unitNo = strings.TrimSpace(parts[5])
	}
	streetNo := ""
	if len(parts) > 6 {
		streetNo = strings.TrimSpace(parts[6])
	}
	if unitNo != "" {
		if streetNo != "" {
			streetNo = unitNo + "/" + streetNo
		} else {
			streetNo = unitNo
		}
	}

	streetRaw := ""
	if len(parts) > 7 {
		streetRaw = strings.TrimSpace(parts[7])
	}
	suburb := ""
	if len(parts) > 8 {
		suburb = strings.ToUpper(strings.TrimSpace(parts[8]))
	}
	postcode := ""
	if len(parts) > 9 {
		postcode = normalizePostcode(strings.TrimSpace(parts[9]))
	}
	if len(postcode) != 4 || suburb == "" {
		return ParsedProperty{}, ParsedSale{}, false
	}

	contractDate := ""
	if len(parts) > 10 {
		contractDate = normalizeDate(strings.TrimSpace(parts[10]))
	}
	if contractDate == "" || !validSaleDate(contractDate) {
		return ParsedProperty{}, ParsedSale{}, false
	}

	priceStr := ""
	if len(parts) > 11 {
		priceStr = strings.TrimSpace(parts[11])
	}
	price, err := strconv.ParseInt(priceStr, 10, 64)
	if err != nil || price <= 10000 {
		return ParsedProperty{}, ParsedSale{}, false
	}

	legalDesc := ""
	if len(parts) > 12 {
		legalDesc = strings.TrimSpace(parts[12])
	}
	lot, plan := parseCadastral(legalDesc)

	landArea := 0.0
	if len(parts) > 13 {
		if a, err := strconv.ParseFloat(strings.TrimSpace(parts[13]), 64); err == nil {
			landArea = a
		}
	}
	if len(parts) > 14 {
		switch strings.ToUpper(strings.TrimSpace(parts[14])) {
		case "H":
			landArea *= 10000
		case "A":
			landArea *= 4046.8564224
		}
	}
	propertyType := ""
	if len(parts) > 16 {
		propertyType = strings.ToUpper(strings.TrimSpace(parts[16]))
	}
	zoning := ""
	if len(parts) > 17 {
		zoning = strings.ToUpper(strings.TrimSpace(parts[17]))
	}

	stName, stType := splitStreetNameAndType(streetRaw)
	if stName == "" {
		stName = "UNKNOWN"
	}
	if stType == "" {
		stType = "ST"
	}

	gnafPID := makeGnafPID(district, propID, unitNo)
	baseGnafPID := makeBaseGnafPID(district, propID)
	saleID := makeSaleID(district, propID, contractDate, "")

	prop := ParsedProperty{
		GnafPID:      gnafPID,
		BaseGnafPID:  baseGnafPID,
		PropertyID:   propID,
		DistrictCode: district,
		StreetNumber: streetNo,
		StreetName:   stName,
		StreetType:   stType,
		Suburb:       suburb,
		State:        "NSW",
		Postcode:     postcode,
		LotNumber:    lot,
		PlanNumber:   plan,
		Zoning:       zoning,
		PropertyType: propertyType,
		LandArea:     landArea,
	}

	sale := ParsedSale{
		SaleID:         saleID,
		GnafPID:        gnafPID,
		SettlementDate: contractDate,
		ContractDate:   contractDate,
		SalePrice:      price,
		SourceType:     "NSW Valuer General (PSI)",
		DistrictCode:   district,
	}

	return prop, sale, true
}

// validSaleDate rejects impossible or out-of-range PSI dates. The NSW PSI
// dataset starts in 1990 and real searches should never contain future
// settlement dates beyond tomorrow.
func validSaleDate(dateStr string) bool {
	t, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return false
	}
	min := time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC)
	max := time.Now().AddDate(0, 0, 1)
	return !t.Before(min) && !t.After(max)
}

func makeBaseGnafPID(district, propID string) string {
	cleanProp := strings.TrimSpace(propID)
	if strings.HasPrefix(cleanProp, "GANSW") {
		return cleanProp
	}
	return fmt.Sprintf("GANSW_PSI_%s_%s", district, cleanProp)
}

func makeGnafPID(district, propID, unit string) string {
	base := makeBaseGnafPID(district, propID)
	suffix := unitSuffix(unit)
	if suffix == "" {
		return base
	}
	return base + "_U" + suffix
}

func unitSuffix(unit string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(unit)) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func datRecordKey(parts []string) string {
	if len(parts) < 5 {
		return ""
	}
	return strings.Join([]string{
		strings.TrimSpace(parts[1]),
		strings.TrimSpace(parts[2]),
		strings.TrimSpace(parts[3]),
		strings.TrimSpace(parts[4]),
	}, "|")
}

func makeSaleID(district, propID, date, dealingNo string) string {
	if dealingNo != "" {
		return fmt.Sprintf("NSW-%s-%s", district, dealingNo)
	}
	return fmt.Sprintf("NSW-%s-%s-%s", district, propID, strings.ReplaceAll(date, "-", ""))
}

// splitStreetNameAndType splits "CROWN ST" into "CROWN" and "ST"
func splitStreetNameAndType(streetRaw string) (string, string) {
	streetRaw = strings.TrimSpace(strings.ToUpper(streetRaw))
	if streetRaw == "" {
		return "", ""
	}

	words := strings.Fields(streetRaw)
	if len(words) == 1 {
		return words[0], "ST"
	}

	lastWord := words[len(words)-1]
	if normType, exists := streetTypes[lastWord]; exists {
		stName := strings.Join(words[:len(words)-1], " ")
		return stName, normType
	}

	return streetRaw, "ST"
}

// parseCadastral extracts lot and plan numbers from legal descriptors
func parseCadastral(desc string) (string, string) {
	desc = strings.TrimSpace(strings.ToUpper(desc))
	if desc == "" {
		return "", ""
	}

	if m := slashLotPlanRegex.FindStringSubmatch(desc); len(m) == 3 {
		lot := m[1]
		plan := m[2]
		if !strings.HasPrefix(plan, "DP") && !strings.HasPrefix(plan, "SP") && !strings.HasPrefix(plan, "LP") {
			plan = "DP" + plan
		}
		return lot, plan
	}

	if m := dpLotRegex.FindStringSubmatch(desc); len(m) == 3 {
		lot := m[1]
		planNum := m[2]
		plan := "DP" + planNum
		if strings.Contains(desc, "SP") {
			plan = "SP" + planNum
		}
		return lot, plan
	}

	var lot, plan string
	if m := lotOnlyRegex.FindStringSubmatch(desc); len(m) == 2 {
		lot = m[1]
	}
	if m := dpOnlyRegex.FindStringSubmatch(desc); len(m) == 2 {
		plan = "DP" + m[1]
		if strings.Contains(desc, "SP") {
			plan = "SP" + m[1]
		}
	}

	return lot, plan
}

// normalizeDate converts YYYYMMDD or DD/MM/YYYY to YYYY-MM-DD
func normalizeDate(dateStr string) string {
	dateStr = strings.TrimSpace(dateStr)
	if dateStr == "" {
		return ""
	}

	if len(dateStr) == 8 && !strings.Contains(dateStr, "/") && !strings.Contains(dateStr, "-") {
		if t, err := time.Parse("20060102", dateStr); err == nil {
			return t.Format("2006-01-02")
		}
	}

	if strings.Contains(dateStr, "/") {
		if t, err := time.Parse("02/01/2006", dateStr); err == nil {
			return t.Format("2006-01-02")
		}
	}

	if strings.Contains(dateStr, "-") && len(dateStr) == 10 {
		if t, err := time.Parse("2006-01-02", dateStr); err == nil {
			return t.Format("2006-01-02")
		}
	}

	return ""
}

// normalizePostcode ensures 4-digit Australian postcode
func normalizePostcode(pc string) string {
	pc = strings.TrimSpace(pc)
	var digits strings.Builder
	for _, ch := range pc {
		if ch >= '0' && ch <= '9' {
			digits.WriteRune(ch)
		}
	}
	res := digits.String()
	if len(res) == 3 {
		return "0" + res
	}
	if len(res) == 4 {
		return res
	}
	return ""
}
