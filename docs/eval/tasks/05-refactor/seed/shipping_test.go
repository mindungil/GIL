package taskrefactor

import (
	"math"
	"testing"
)

func approxEq(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestShippingCost_Bronze(t *testing.T) {
	got := ShippingCost(10, 100, "bronze")
	want := 7.0 // (10*0.5 + 100*0.02) * 1.0
	if !approxEq(got, want) {
		t.Errorf("bronze got %v want %v", got, want)
	}
}

func TestShippingCost_Silver(t *testing.T) {
	got := ShippingCost(10, 100, "silver")
	want := 7.0 * 0.85
	if !approxEq(got, want) {
		t.Errorf("silver got %v want %v", got, want)
	}
}

func TestShippingCost_Gold(t *testing.T) {
	got := ShippingCost(10, 100, "gold")
	want := 7.0 * 0.7
	if !approxEq(got, want) {
		t.Errorf("gold got %v want %v", got, want)
	}
}

func TestShippingCost_Unknown(t *testing.T) {
	got := ShippingCost(10, 100, "platinum")
	want := 7.0 // unknown tier defaults to bronze multiplier
	if !approxEq(got, want) {
		t.Errorf("unknown got %v want %v", got, want)
	}
}

func TestDiscount(t *testing.T) {
	cases := []struct {
		tier string
		want float64
	}{
		{"bronze", 0.0},
		{"silver", 0.15},
		{"gold", 0.3},
		{"platinum", 0.0},
	}
	for _, c := range cases {
		if got := Discount(c.tier); !approxEq(got, c.want) {
			t.Errorf("Discount(%q): got %v want %v", c.tier, got, c.want)
		}
	}
}
