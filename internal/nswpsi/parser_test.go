package nswpsi

import (
	"strings"
	"testing"
)

func TestParseModernRecordB(t *testing.T) {
	sampleDAT := `A;RTSALEDATA;001;20260810 01:00;VALNET;
B;001;2962123;1;20260810 01:00;;;27;BARNHILL ACCS;ROTHBURY;2320;610.5;M;20260622;20260803;1650000;SP3;R;RESIDENCE;;SAV;;;AW345368;
C;001;2962123;1;20260810 01:00;56/270293;
B;001;6054756;5;20260810 01:00;;12;110;MITCHELL AVE;KURRI KURRI;2327;121;M;20260306;20260730;495000;;3;COMMERCIAL;12;;;;AW338024;
C;001;6054756;5;20260810 01:00;12/SP111222;
`
	props, sales, err := ParseDATStream(strings.NewReader(sampleDAT))
	if err != nil {
		t.Fatalf("ParseDATStream error: %v", err)
	}

	if len(props) != 2 {
		t.Fatalf("Expected 2 properties, got %d", len(props))
	}
	if len(sales) != 2 {
		t.Fatalf("Expected 2 sales, got %d", len(sales))
	}

	p1 := props[0]
	if p1.StreetNumber != "27" || p1.StreetName != "BARNHILL" || p1.StreetType != "ACCS" || p1.Suburb != "ROTHBURY" || p1.Postcode != "2320" {
		t.Errorf("Unexpected property 1 data: %+v", p1)
	}
	if p1.LotNumber != "56" || p1.PlanNumber != "DP270293" {
		t.Errorf("Unexpected lot/plan for p1: Lot=%s, Plan=%s", p1.LotNumber, p1.PlanNumber)
	}
	if p1.PropertyType != "RESIDENCE" || p1.LandArea != 610.5 {
		t.Errorf("Unexpected property type/area for p1: Type=%s, Area=%v", p1.PropertyType, p1.LandArea)
	}

	s1 := sales[0]
	if s1.SalePrice != 1650000 || s1.SettlementDate != "2026-08-03" || s1.DealingNumber != "AW345368" {
		t.Errorf("Unexpected sale 1 data: %+v", s1)
	}

	p2 := props[1]
	if p2.StreetNumber != "12/110" || p2.StreetName != "MITCHELL" || p2.StreetType != "AVE" || p2.Suburb != "KURRI KURRI" {
		t.Errorf("Unexpected property 2 data: %+v", p2)
	}
	if p2.LotNumber != "12" || p2.PlanNumber != "SP111222" {
		t.Errorf("Unexpected lot/plan for p2: Lot=%s, Plan=%s", p2.LotNumber, p2.PlanNumber)
	}
}

func TestParseHistoricalRecordB(t *testing.T) {
	sample1990 := `A;;VALNET1;20150909 11:33;;
B;011;VALNET1;0145900000000;292674;;;ELDON ST;ABERDEEN;2336;20/11/1990;14500;LOT 7 SEC 22 DP 758003, LOT 8 SEC 22 DP 758003.;2365;M;;;A;;;;
B;204;ARCHIVE;T000594;;10;8;ROKEBY RD;ABBOTSFORD;2046;19/10/1990;182000;LOT 10 SP 10445;0;M;;;;;;;
`
	props, sales, err := ParseDATStream(strings.NewReader(sample1990))
	if err != nil {
		t.Fatalf("ParseDATStream historical error: %v", err)
	}

	if len(props) != 2 {
		t.Fatalf("Expected 2 properties, got %d", len(props))
	}
	if len(sales) != 2 {
		t.Fatalf("Expected 2 sales, got %d", len(sales))
	}

	p1 := props[0]
	if p1.Suburb != "ABERDEEN" || p1.Postcode != "2336" || p1.StreetName != "ELDON" || p1.StreetType != "ST" {
		t.Errorf("Unexpected historical prop 1: %+v", p1)
	}
	if p1.LandArea != 2365 {
		t.Errorf("Unexpected historical land area: %v", p1.LandArea)
	}
	s1 := sales[0]
	if s1.SalePrice != 14500 || s1.SettlementDate != "1990-11-20" {
		t.Errorf("Unexpected historical sale 1: %+v", s1)
	}

	p2 := props[1]
	if p2.StreetNumber != "10/8" || p2.StreetName != "ROKEBY" || p2.StreetType != "RD" || p2.Suburb != "ABBOTSFORD" {
		t.Errorf("Unexpected historical prop 2: %+v", p2)
	}
	if p2.LotNumber != "10" || p2.PlanNumber != "SP10445" {
		t.Errorf("Unexpected lot/plan for p2: Lot=%s, Plan=%s", p2.LotNumber, p2.PlanNumber)
	}
}

func TestNormalizePostcode(t *testing.T) {
	if normalizePostcode("2010") != "2010" {
		t.Errorf("Failed to normalize 2010")
	}
	if normalizePostcode("800") != "0800" {
		t.Errorf("Failed to normalize 800 to 0800")
	}
	if normalizePostcode("invalid") != "" {
		t.Errorf("Should reject invalid postcode")
	}
}

func TestParseRejectsOutOfRangeDates(t *testing.T) {
	for _, date := range []string{"21021115", "19031217"} {
		sample := `A;RTSALEDATA;001;20260810 01:00;VALNET;
B;001;2962123;1;20260810 01:00;;;27;BARNHILL ACCS;ROTHBURY;2320;610.5;M;20260622;` + date + `;1650000;SP3;R;RESIDENCE;;SAV;;;AW345368;
`
		props, sales, err := ParseDATStream(strings.NewReader(sample))
		if err != nil {
			t.Fatalf("ParseDATStream(%s) error: %v", date, err)
		}
		if len(props) != 0 || len(sales) != 0 {
			t.Errorf("expected date %s to be rejected, got %d properties / %d sales", date, len(props), len(sales))
		}
	}
}

func TestParseHistoricalHectares(t *testing.T) {
	sample := `B;003;VALNET1;3283400000000;;;;SUN VALLEY RD;GREEN POINT;2251;16/07/1993;130000000;LOT 4522 DP 813089.;3.57;H;;TE;A;;;;
`
	props, sales, err := ParseDATStream(strings.NewReader(sample))
	if err != nil {
		t.Fatalf("ParseDATStream error: %v", err)
	}
	if len(props) != 1 || len(sales) != 1 {
		t.Fatalf("expected 1 property/sale, got %d/%d", len(props), len(sales))
	}
	if props[0].LandArea != 35700 {
		t.Errorf("land area = %v, want 35700", props[0].LandArea)
	}
	if props[0].PropertyType != "TE" {
		t.Errorf("property type = %q, want TE", props[0].PropertyType)
	}
}

func TestParseStrataUnitsSeparateProperties(t *testing.T) {
	sample := `B;084;3499933;10;20100405 04:05;;2;1;BRUCE AVE;KILLARA;2071;;;20100217;20100303;495000;;R;RESIDENCE;2;;;0;AF378403;
C;084;3499933;10;20100405 04:05;2/SP83181;
B;084;3499933;8;20100802 03:01;;305;1;BRUCE AVE;KILLARA;2071;;;20100526;20100707;935000;;R;RESIDENCE;34;;;0;AF622722;
C;084;3499933;8;20100802 03:01;34/SP83181;
`
	props, sales, err := ParseDATStream(strings.NewReader(sample))
	if err != nil {
		t.Fatalf("ParseDATStream error: %v", err)
	}
	if len(props) != 2 || len(sales) != 2 {
		t.Fatalf("expected 2 properties/sales, got %d/%d", len(props), len(sales))
	}
	if props[0].GnafPID != "GANSW_PSI_084_3499933_U2" {
		t.Errorf("unit 2 PID = %q", props[0].GnafPID)
	}
	if props[1].GnafPID != "GANSW_PSI_084_3499933_U305" {
		t.Errorf("unit 305 PID = %q", props[1].GnafPID)
	}
	if props[0].StreetNumber != "2/1" || props[1].StreetNumber != "305/1" {
		t.Errorf("unexpected street numbers: %q / %q", props[0].StreetNumber, props[1].StreetNumber)
	}
	if props[0].LotNumber != "2" || props[0].PlanNumber != "SP83181" {
		t.Errorf("unit 2 lot/plan = %s/%s", props[0].LotNumber, props[0].PlanNumber)
	}
	if props[1].LotNumber != "34" || props[1].PlanNumber != "SP83181" {
		t.Errorf("unit 305 lot/plan = %s/%s", props[1].LotNumber, props[1].PlanNumber)
	}
	if sales[0].GnafPID != props[0].GnafPID || sales[1].GnafPID != props[1].GnafPID {
		t.Errorf("sales not linked to their units")
	}
}
