package taskrefactor

// ShippingCost returns the cost of shipping a parcel given its weight
// (kg), distance (km), and the customer's tier ("bronze" | "silver" |
// "gold"). Higher tiers get cheaper shipping.
func ShippingCost(weight, distance float64, tier string) float64 {
	base := weight*0.5 + distance*0.02
	var mult float64
	switch tier {
	case "gold":
		mult = 0.7
	case "silver":
		mult = 0.85
	case "bronze":
		mult = 1.0
	default:
		mult = 1.0
	}
	return base * mult
}

// Discount returns the discount fraction (0..1) applied to the
// invoice for a customer of the given tier. Tier multiplier here is
// the COMPLEMENT of the shipping multiplier (gold gets 30% off, etc).
func Discount(tier string) float64 {
	switch tier {
	case "gold":
		return 0.3
	case "silver":
		return 0.15
	case "bronze":
		return 0.0
	default:
		return 0.0
	}
}
