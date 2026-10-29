package slo

import "testing"

func TestOccurrenceCalculation(t *testing.T) {
	result, err := CalculateOccurrences("999", "1100", "0.99", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Attainment != "0.908181818182" || result.State != "violated" {
		t.Fatalf("unexpected result: %+v", result)
	}
	result, err = CalculateOccurrences("9850", "10000", "0.99", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Allowed != "100.000000000000" || result.Remaining != "-50.000000000000" || result.Burn != "1.500000000000" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestOccurrenceProvisionalAndZeroTraffic(t *testing.T) {
	result, err := CalculateOccurrences("99", "100", "0.99", true)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "unknown" || result.ObservedState != "met" || !result.Provisional {
		t.Fatalf("unexpected result: %+v", result)
	}
	_, err = CalculateOccurrences("0", "0", "0.99", false)
	if err != ErrNoTraffic {
		t.Fatalf("expected no traffic, got %v", err)
	}
}

func TestTargetAndExactComparison(t *testing.T) {
	if _, err := CalculateOccurrences("1", "1", "1", false); err == nil {
		t.Fatal("accepted target 1")
	}
	result, err := CalculateOccurrences("999999999999", "1000000000000", "0.999999999999", false)
	if err != nil || result.State != "met" {
		t.Fatalf("boundary: %+v %v", result, err)
	}
	result, err = CalculateOccurrences("0", "1", "0", false)
	if err != nil || result.State != "met" || result.Burn != "1.000000000000" {
		t.Fatalf("zero target: %+v %v", result, err)
	}
}

func TestExactComparisonAndHalfEvenRounding(t *testing.T) {
	cases := []struct{ good, total, target, attainment, state, remaining, burn string }{
		{"9899999999996", "10000000000000", "0.99", "0.990000000000", "violated", "-4.000000000000", "1.000000000040"},
		{"1", "2000000000000", "0", "0.000000000000", "met", "1.000000000000", "1.000000000000"},
		{"3", "2000000000000", "0", "0.000000000002", "met", "3.000000000000", "0.999999999998"},
		{"0.3", "0.5", "0.5", "0.600000000000", "met", "0.050000000000", "0.800000000000"},
	}
	for _, tc := range cases {
		result, err := CalculateOccurrences(tc.good, tc.total, tc.target, false)
		if err != nil || result.Attainment != tc.attainment || result.State != tc.state || result.Remaining != tc.remaining || result.Burn != tc.burn {
			t.Errorf("%s/%s target %s: %+v, %v", tc.good, tc.total, tc.target, result, err)
		}
	}
}
