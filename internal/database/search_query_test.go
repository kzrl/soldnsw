package database

import "testing"

func TestParseSearchQuery(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		kind       SearchKind
		street     string
		streetType string
		suburb     string
		state      string
		postcode   string
		number     string
	}{
		{
			name:       "full address",
			input:      "17 Arnold St Killara NSW 2071",
			kind:       SearchKindAddress,
			street:     "ARNOLD",
			streetType: "ST",
			suburb:     "KILLARA",
			state:      "NSW",
			postcode:   "2071",
			number:     "17",
		},
		{
			name:       "street and suburb",
			input:      "arnold street killara",
			kind:       SearchKindStreet,
			street:     "ARNOLD",
			streetType: "ST",
			suburb:     "KILLARA",
		},
		{
			name:   "street only",
			input:  "Arnold",
			kind:   SearchKindStreet,
			street: "ARNOLD",
		},
		{
			name:     "postcode only",
			input:    "2071",
			kind:     SearchKindPostcode,
			postcode: "2071",
		},
		{
			name:     "suburb with postcode",
			input:    "Killara 2071",
			kind:     SearchKindPostcode,
			street:   "KILLARA",
			postcode: "2071",
		},
		{
			name:       "multiword street",
			input:      "Sun Valley Rd Green Point 2251",
			kind:       SearchKindStreet,
			street:     "SUN VALLEY",
			streetType: "RD",
			suburb:     "GREEN POINT",
			postcode:   "2251",
		},
		{
			name:       "unit address",
			input:      "1/7 Arnold St Killara",
			kind:       SearchKindAddress,
			street:     "ARNOLD",
			streetType: "ST",
			suburb:     "KILLARA",
			number:     "1/7",
		},
		{
			name:       "apostrophe in street name",
			input:      "O'Connell St",
			kind:       SearchKindStreet,
			street:     "O'CONNELL",
			streetType: "ST",
		},
		{
			name:       "street and postcode",
			input:      "Arnold St 2071",
			kind:       SearchKindStreet,
			street:     "ARNOLD",
			streetType: "ST",
			postcode:   "2071",
		},
		{
			name:       "wildcard characters are removed",
			input:      "Arn%old_ St",
			kind:       SearchKindStreet,
			street:     "ARNOLD",
			streetType: "ST",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseSearchQuery(tc.input)
			if got.Kind != tc.kind {
				t.Errorf("kind = %q, want %q", got.Kind, tc.kind)
			}
			if got.StreetName != tc.street {
				t.Errorf("street = %q, want %q", got.StreetName, tc.street)
			}
			if got.StreetType != tc.streetType {
				t.Errorf("street type = %q, want %q", got.StreetType, tc.streetType)
			}
			if got.Suburb != tc.suburb {
				t.Errorf("suburb = %q, want %q", got.Suburb, tc.suburb)
			}
			if got.State != tc.state {
				t.Errorf("state = %q, want %q", got.State, tc.state)
			}
			if got.Postcode != tc.postcode {
				t.Errorf("postcode = %q, want %q", got.Postcode, tc.postcode)
			}
			if got.Number != tc.number {
				t.Errorf("number = %q, want %q", got.Number, tc.number)
			}
		})
	}
}

func TestParseSearchQueryPropertyID(t *testing.T) {
	for _, id := range []string{"GANSW12345678", "GANSW_PSI_003_2981264", "GATAS394820101"} {
		parsed := ParseSearchQuery(id)
		if parsed.Kind != SearchKindPropertyID {
			t.Errorf("%s: kind = %q, want property_id", id, parsed.Kind)
		}
		if parsed.PropertyID != id {
			t.Errorf("%s: property ID = %q", id, parsed.PropertyID)
		}
	}
	for _, value := range []string{"Killara", "GABRIELLE", "GATEWAY1"} {
		if parsed := ParseSearchQuery(value); parsed.Kind == SearchKindPropertyID {
			t.Errorf("%s was misclassified as a property ID: %+v", value, parsed)
		}
	}
}

func TestParseSearchQueryStreetPattern(t *testing.T) {
	parsed := ParseSearchQuery("Arnold St Killara")
	if parsed.StreetPattern != "ARNOLD%" {
		t.Errorf("street pattern = %q, want ARNOLD%%", parsed.StreetPattern)
	}
}

func TestParseStructuredSearchSplitsStreetType(t *testing.T) {
	parsed := ParseStructuredSearch("Arnold St", "", "", "", "", "")
	if parsed.StreetName != "ARNOLD" || parsed.StreetType != "ST" {
		t.Fatalf("unexpected street parts: %+v", parsed)
	}
	if parsed.StreetPattern != "ARNOLD%" {
		t.Errorf("street pattern = %q, want ARNOLD%%", parsed.StreetPattern)
	}

	parsed = ParseStructuredSearch("Arnold St Killara", "", "", "", "", "")
	if parsed.StreetName != "ARNOLD" || parsed.StreetType != "ST" || parsed.Suburb != "KILLARA" {
		t.Fatalf("unexpected structured parse: %+v", parsed)
	}
}

func TestParseSearchQueryStreetTypeAtStartOfName(t *testing.T) {
	parsed := ParseSearchQuery("Park St Killara")
	if parsed.StreetName != "PARK" || parsed.StreetType != "ST" || parsed.Suburb != "KILLARA" {
		t.Fatalf("unexpected parse: %+v", parsed)
	}
}
