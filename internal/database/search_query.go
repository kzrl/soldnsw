package database

import (
	"strings"
	"unicode"
)

// SearchKind identifies how a free-text search should be resolved.
type SearchKind string

const (
	SearchKindAddress    SearchKind = "address"
	SearchKindStreet     SearchKind = "street"
	SearchKindPostcode   SearchKind = "postcode"
	SearchKindPropertyID SearchKind = "property_id"
)

// ParsedSearch is a free-text or structured search broken into its parts.
type ParsedSearch struct {
	Raw           string
	Kind          SearchKind
	Address       string
	StreetName    string
	StreetPattern string
	StreetType    string
	Suburb        string
	State         string
	Postcode      string
	Number        string
	PropertyID    string
}

var streetTypeAliases = map[string]string{
	"STREET":      "ST",
	"ROAD":        "RD",
	"AVENUE":      "AVE",
	"AV":          "AVE",
	"PLACE":       "PL",
	"DRIVE":       "DR",
	"CRESCENT":    "CR",
	"CRES":        "CR",
	"COURT":       "CT",
	"LANE":        "LN",
	"PARADE":      "PDE",
	"HIGHWAY":     "HWY",
	"BOULEVARD":   "BLVD",
	"BVD":         "BLVD",
	"TERRACE":     "TCE",
	"CLOSE":       "CL",
	"GROVE":       "GR",
	"SQUARE":      "SQ",
	"ESPLANADE":   "ESP",
	"CIRCUIT":     "CIR",
	"GARDENS":     "GDNS",
	"HEIGHTS":     "HTS",
	"PARK":        "PK",
	"TRACK":       "TRK",
	"RISE":        "RISE",
	"BEND":        "BEND",
	"CHASE":       "CH",
	"WALK":        "WALK",
	"MEWS":        "MEWS",
	"LOOP":        "LOOP",
	"GLEN":        "GLN",
	"GRANGE":      "GRN",
	"RIDGE":       "RDGE",
	"VIEW":        "VW",
	"WAY":         "WAY",
	"CIRCLE":      "CIR",
	"STRIP":       "STRP",
	"ALLEY":       "ALY",
	"APPROACH":    "APP",
	"ARCADE":      "ARC",
	"BRACE":       "BR",
	"BROADWAY":    "BWAY",
	"BYPASS":      "BYP",
	"CAUSEWAY":    "CWY",
	"CRUISEWAY":   "CSWY",
	"DIVIDE":      "DIV",
	"EXPRESSWAY":  "EXP",
	"FREEWAY":     "FWY",
	"INTERCHANGE": "INTG",
	"MOTORWAY":    "MWY",
	"OVERPASS":    "OVP",
	"PROMENADE":   "PROM",
	"QUAY":        "QY",
	"RAMBLE":      "RMBL",
	"ROTARY":      "RTY",
	"SUBWAY":      "SBWY",
	"TOLLWAY":     "TLWY",
	"UNDERPASS":   "UPAS",
	"VIADUCT":     "VDCT",
	"VISTA":       "VSTA",
	"WALKWAY":     "WKWY",
	"YARD":        "YD",
}

var streetTypeAbbreviations = func() map[string]struct{} {
	types := map[string]struct{}{
		"ST": {}, "RD": {}, "AVE": {}, "PL": {}, "DR": {}, "CR": {}, "CT": {},
		"LN": {}, "PDE": {}, "HWY": {}, "BLVD": {}, "TCE": {}, "CL": {}, "GR": {},
		"SQ": {}, "ESP": {}, "CIR": {}, "GDNS": {}, "HTS": {}, "PK": {}, "TRK": {},
		"RISE": {}, "BEND": {}, "CH": {}, "WALK": {}, "MEWS": {}, "LOOP": {},
		"GLN": {}, "GRN": {}, "RDGE": {}, "VW": {}, "WAY": {}, "STRP": {},
		"ALY": {}, "APP": {}, "ARC": {}, "BR": {}, "BWAY": {}, "BYP": {},
		"CWY": {}, "CSWY": {}, "DIV": {}, "EXP": {}, "FWY": {}, "INTG": {},
		"MWY": {}, "OVP": {}, "PROM": {}, "QY": {}, "RMBL": {}, "RTY": {},
		"SBWY": {}, "TLWY": {}, "UPAS": {}, "VDCT": {}, "VSTA": {}, "WKWY": {},
		"YD": {},
	}
	for alias, abbreviation := range streetTypeAliases {
		types[alias] = struct{}{}
		types[abbreviation] = struct{}{}
	}
	return types
}()

var stateAbbreviations = map[string]struct{}{
	"NSW": {}, "VIC": {}, "QLD": {}, "SA": {}, "WA": {}, "TAS": {}, "NT": {}, "ACT": {},
}

// ParseSearchQuery classifies free text as an address, street or postcode
// search. It deliberately prefers a structured interpretation and leaves the
// caller free to fall back to address FTS when a street prefix finds nothing.
func ParseSearchQuery(raw string) ParsedSearch {
	rawUpper := strings.ToUpper(strings.TrimSpace(raw))
	if looksLikePropertyID(rawUpper) {
		return ParsedSearch{
			Raw:        rawUpper,
			Address:    rawUpper,
			Kind:       SearchKindPropertyID,
			PropertyID: rawUpper,
		}
	}
	normalized := normalizeSearchText(raw)
	parsed := ParsedSearch{Raw: normalized, Address: normalized, Kind: SearchKindAddress}
	if normalized == "" {
		return parsed
	}

	tokens := strings.Fields(normalized)
	postcode := ""
	var remaining []string
	for _, token := range tokens {
		if len(token) == 4 && isDigits(token) {
			postcode = token
			continue
		}
		remaining = append(remaining, token)
	}

	state := ""
	var content []string
	for _, token := range remaining {
		if _, ok := stateAbbreviations[token]; ok {
			state = token
			continue
		}
		content = append(content, token)
	}
	parsed.State = state
	parsed.Postcode = postcode

	number := ""
	if len(content) > 0 && looksLikeStreetNumber(content[0]) {
		number = content[0]
		content = content[1:]
	}
	parsed.Number = number

	typeIndex := -1
	streetType := ""
	// Prefer the last type token: names such as PARK ST, GROVE ST and
	// RIDGE ST begin with a word that is also a valid street type.
	for i := len(content) - 1; i >= 0; i-- {
		if canonical, ok := canonicalStreetType(content[i]); ok {
			typeIndex = i
			streetType = canonical
			break
		}
	}
	parsed.StreetType = streetType

	var streetTokens, suburbTokens []string
	if typeIndex >= 0 {
		streetTokens = content[:typeIndex]
		suburbTokens = content[typeIndex+1:]
	} else {
		streetTokens = content
	}
	parsed.StreetName = strings.Join(streetTokens, " ")
	parsed.Suburb = strings.Join(suburbTokens, " ")

	if number != "" {
		parsed.Kind = SearchKindAddress
		parsed.Address = normalized
		return parsed
	}
	if postcode != "" && streetType == "" {
		parsed.Kind = SearchKindPostcode
		return parsed
	}
	if parsed.StreetName != "" {
		parsed.Kind = SearchKindStreet
		parsed.StreetPattern = parsed.StreetName + "%"
		return parsed
	}
	if postcode != "" {
		parsed.Kind = SearchKindPostcode
		return parsed
	}
	parsed.Kind = SearchKindAddress
	return parsed
}

// ParseStructuredSearch turns the advanced search fields into the same
// representation as a free-text search.
func ParseStructuredSearch(street, streetType, suburb, state, postcode, number string) ParsedSearch {
	street = normalizeSearchText(street)
	streetType = normalizeSearchText(streetType)
	suburb = normalizeSearchText(suburb)
	state = normalizeSearchText(state)
	postcode = normalizeSearchText(postcode)
	number = normalizeSearchText(number)

	parsed := ParsedSearch{
		StreetName: street,
		Suburb:     suburb,
		State:      state,
		Postcode:   postcode,
		Number:     number,
		StreetType: streetType,
	}
	if street != "" {
		// Accept "ARNOLD ST" in the street field even though G-NAF stores the
		// name and type in separate columns.
		streetParts := ParseSearchQuery(street)
		if streetParts.StreetName != "" {
			parsed.StreetName = streetParts.StreetName
			if parsed.StreetType == "" {
				parsed.StreetType = streetParts.StreetType
			}
			if parsed.Suburb == "" {
				parsed.Suburb = streetParts.Suburb
			}
			if parsed.State == "" {
				parsed.State = streetParts.State
			}
			if parsed.Postcode == "" {
				parsed.Postcode = streetParts.Postcode
			}
			if parsed.Number == "" {
				parsed.Number = streetParts.Number
			}
		}
	}
	if parsed.StreetName != "" {
		parsed.StreetPattern = parsed.StreetName + "%"
	}
	if number != "" {
		parsed.Kind = SearchKindAddress
	} else if street != "" {
		parsed.Kind = SearchKindStreet
	} else if postcode != "" {
		parsed.Kind = SearchKindPostcode
	} else {
		parsed.Kind = SearchKindAddress
	}
	return parsed
}

// normalizeSearchText uppercases and strips characters that are not useful for
// address matching. Removing '%' and '_' also keeps LIKE patterns safe without
// needing an ESCAPE clause, which the sqlc SQLite parser does not accept.
func normalizeSearchText(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	value = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsLetter(r), unicode.IsNumber(r):
			return r
		case r == ' ', r == '/', r == '-', r == '\'', r == '&', r == '.':
			return r
		case r == '%', r == '_':
			return -1
		default:
			return ' '
		}
	}, value)
	return strings.Join(strings.Fields(value), " ")
}

func looksLikePropertyID(value string) bool {
	if len(value) < 8 || !strings.HasPrefix(value, "GA") {
		return false
	}
	digits := 0
	for _, r := range value {
		switch {
		case r >= 'A' && r <= 'Z', r == '_':
			continue
		case r >= '0' && r <= '9':
			digits++
		default:
			return false
		}
	}
	return digits >= 4
}

func looksLikeStreetNumber(token string) bool {
	if token == "" || token[0] < '0' || token[0] > '9' {
		return false
	}
	// A four-digit token is handled as a postcode before this point.
	if len(token) == 4 && isDigits(token) {
		return false
	}
	return true
}

func isDigits(value string) bool {
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

func canonicalStreetType(token string) (string, bool) {
	token = strings.TrimSuffix(token, ".")
	if abbreviation, ok := streetTypeAliases[token]; ok {
		return abbreviation, true
	}
	if _, ok := streetTypeAbbreviations[token]; ok {
		return token, true
	}
	return "", false
}
