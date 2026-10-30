package database

import "testing"

func TestClassifyDwellingType(t *testing.T) {
	cases := []struct {
		name         string
		propertyType string
		streetNumber string
		planNumber   string
		want         string
	}{
		{"unit by address", "RESIDENCE", "3/1", "SP83181", DwellingUnit},
		{"unit by plan", "RESIDENCE", "1", "SP83181", DwellingUnit},
		{"house", "RESIDENCE", "17", "DP340114", DwellingHouse},
		{"house explicit", "HOUSE", "17", "DP340114", DwellingHouse},
		{"townhouse", "TOWNHOUSE", "1/10", "SP12345", DwellingTownhouse},
		{"vacant land", "VACANT LAND", "LOT 3/1364", "DP12345", DwellingOther},
		{"commercial", "COMMERCIAL", "100", "DP12345", DwellingOther},
		{"unknown", "", "", "", DwellingUnknown},
	}
	for _, tc := range cases {
		if got := ClassifyDwellingType(tc.propertyType, tc.streetNumber, tc.planNumber); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
