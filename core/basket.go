package core

import (
	"fmt"
	"math"
	"math/big"
	"sort"
)

// BasketBuilderItem is a product line given to BuildBasket.
type BasketBuilderItem struct {
	ID        string
	Name      string
	Category1 string
	Category2 string
	ItemType  string
	// Price is the unit price, e.g. "49.90".
	Price string
	// Quantity is the number of units, a positive integer. Nil means 1.
	Quantity *int
}

// BasketShipping is a shipping fee, added to the basket as its own item. Empty
// fields take the defaults noted below.
type BasketShipping struct {
	Price string
	// ID defaults to "shipping".
	ID string
	// Name defaults to "Shipping".
	Name string
	// Category1 defaults to "Shipping".
	Category1 string
	Category2 string
	// ItemType defaults to "VIRTUAL".
	ItemType string
}

// BasketDiscount is a discount on the items (not on shipping): a fixed Amount
// such as "13.48", or a Percent between 0 and 100. Percent wins when both are set.
// It is spread over the items in proportion to their totals.
type BasketDiscount struct {
	Amount  string
	Percent *float64
}

// QuantityMode selects how BasketBuilderItem.Quantity is sent to the provider.
type QuantityMode string

const (
	// QuantityExpand adds one item per unit, with ids "A-1", "A-2", ... (default).
	QuantityExpand QuantityMode = "expand"
	// QuantityMultiply adds one item priced at unit price × quantity.
	QuantityMultiply QuantityMode = "multiply"
)

// BuildBasketOptions configures BuildBasket.
type BuildBasketOptions struct {
	Items    []BasketBuilderItem
	Discount *BasketDiscount
	// Shipping is added as the last item. A zero fee adds no item.
	Shipping *BasketShipping
	// Quantity defaults to QuantityExpand.
	Quantity QuantityMode
}

// BuiltBasket is a basket whose item prices add up exactly to Price.
type BuiltBasket struct {
	// BasketItems carries the discount; its prices add up exactly to Price.
	BasketItems []BasketItem `json:"basketItems"`
	// Price is the basket total after discount, shipping included. Use it as the
	// payment's price and paidPrice.
	Price string `json:"price"`
	// Discount is the discount actually applied, e.g. "4.99" for 10% of "49.90".
	Discount string `json:"discount"`
}

// BuildBasket builds a basket whose item prices add up to the payment total to
// the kuruş, which some providers require. The discount is spread over the items
// in proportion to their totals, rounding leftovers go to the items with the
// largest remainders (ties go to the larger item, then the earlier one), and
// shipping is added as an item. All arithmetic is done in minor units.
//
// It returns a ValidationError for non-positive prices, an invalid quantity or
// percent, a discount that is not smaller than the item total, or a discount
// that would bring an item down to zero.
func BuildBasket(options BuildBasketOptions) (*BuiltBasket, error) {
	issues := make([]ValidationIssue, 0, 4)
	fail := func(path, message string) error {
		issues = append(issues, ValidationIssue{Path: path, Message: message})
		return NewValidationError(message, "", issues)
	}

	if len(options.Items) == 0 {
		return nil, fail("items", "The basket needs at least one item")
	}

	lines := make([]BasketItem, 0, len(options.Items))
	minors := make([]int64, 0, len(options.Items))
	for index, item := range options.Items {
		path := fmt.Sprintf("items[%d]", index)
		unit, err := minorOrFail(item.Price, path+".price", fail)
		if err != nil {
			return nil, err
		}
		if unit <= 0 {
			return nil, fail(path+".price", path+".price must be greater than zero")
		}
		quantity := 1
		if item.Quantity != nil {
			quantity = *item.Quantity
		}
		if quantity < 1 {
			return nil, fail(path+".quantity", path+".quantity must be a positive integer")
		}
		line := BasketItem{
			ID:        item.ID,
			Name:      item.Name,
			Category1: item.Category1,
			Category2: item.Category2,
			ItemType:  item.ItemType,
		}
		if options.Quantity == QuantityMultiply || quantity == 1 {
			lines = append(lines, line)
			minors = append(minors, unit*int64(quantity))
		} else {
			for n := 1; n <= quantity; n++ {
				expanded := line
				expanded.ID = fmt.Sprintf("%s-%d", item.ID, n)
				lines = append(lines, expanded)
				minors = append(minors, unit)
			}
		}
	}

	var subtotal int64
	for _, value := range minors {
		subtotal += value
	}
	discountMinor, err := resolveBasketDiscount(options.Discount, subtotal, fail)
	if err != nil {
		return nil, err
	}
	if discountMinor >= subtotal {
		return nil, fail("discount", "The discount must be smaller than the item total")
	}

	shares := distributeMinorUnits(discountMinor, minors, subtotal)
	for i, value := range minors {
		discounted := value - shares[i]
		if discounted <= 0 {
			return nil, fail("discount", fmt.Sprintf("The discount brings item %q down to zero", lines[i].ID))
		}
		minors[i] = discounted
		lines[i].Price = formatMinorUnits(discounted)
	}

	if options.Shipping != nil {
		fee, err := minorOrFail(options.Shipping.Price, "shipping.price", fail)
		if err != nil {
			return nil, err
		}
		if fee > 0 {
			shipping := *options.Shipping
			line := BasketItem{
				ID:        orDefault(shipping.ID, "shipping"),
				Name:      orDefault(shipping.Name, "Shipping"),
				Category1: orDefault(shipping.Category1, "Shipping"),
				Category2: shipping.Category2,
				ItemType:  orDefault(shipping.ItemType, string(BasketItemVirtual)),
				Price:     formatMinorUnits(fee),
			}
			lines = append(lines, line)
			minors = append(minors, fee)
		}
	}

	var total int64
	for _, value := range minors {
		total += value
	}
	return &BuiltBasket{
		BasketItems: lines,
		Price:       formatMinorUnits(total),
		Discount:    formatMinorUnits(discountMinor),
	}, nil
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// formatMinorUnits renders whole minor units with exactly two decimals.
func formatMinorUnits(minor int64) string {
	return fmt.Sprintf("%d.%02d", minor/100, minor%100)
}

func minorOrFail(value, path string, fail func(string, string) error) (int64, error) {
	minor, err := ToMinorUnits(value, path)
	if err != nil {
		return 0, fail(path, fmt.Sprintf("Invalid %s: %s", path, value))
	}
	return minor, nil
}

func resolveBasketDiscount(discount *BasketDiscount, subtotal int64, fail func(string, string) error) (int64, error) {
	if discount == nil {
		return 0, nil
	}
	if discount.Percent != nil {
		percent := *discount.Percent
		if math.IsNaN(percent) || math.IsInf(percent, 0) || percent < 0 || percent > 100 {
			return 0, fail("discount.percent", "discount.percent must be between 0 and 100")
		}
		// Percent in millionths of a percent, so the rounding below stays integral.
		scaled := big.NewInt(int64(math.Round(percent * 1_000_000)))
		hundredMillion := big.NewInt(100_000_000)
		numerator := new(big.Int).Mul(big.NewInt(subtotal), scaled)
		numerator.Mul(numerator, big.NewInt(2))
		numerator.Add(numerator, hundredMillion)
		denominator := new(big.Int).Mul(hundredMillion, big.NewInt(2))
		// Round half up.
		return numerator.Div(numerator, denominator).Int64(), nil
	}
	return minorOrFail(discount.Amount, "discount", fail)
}

// distributeMinorUnits splits amount over weights in proportion, in whole minor
// units, with the largest remainder method. The result always adds up to amount.
func distributeMinorUnits(amount int64, weights []int64, total int64) []int64 {
	shares := make([]int64, len(weights))
	if amount == 0 {
		return shares
	}
	bigAmount, bigTotal := big.NewInt(amount), big.NewInt(total)
	type leftover struct {
		index     int
		remainder *big.Int
	}
	remainders := make([]leftover, 0, len(weights))
	var allocated int64
	for index, weight := range weights {
		product := new(big.Int).Mul(bigAmount, big.NewInt(weight))
		share := new(big.Int).Div(product, bigTotal).Int64()
		remainders = append(remainders, leftover{index: index, remainder: new(big.Int).Mod(product, bigTotal)})
		shares[index] = share
		allocated += share
	}
	sort.Slice(remainders, func(i, j int) bool {
		a, b := remainders[i], remainders[j]
		if cmp := a.remainder.Cmp(b.remainder); cmp != 0 {
			return cmp > 0
		}
		if weights[a.index] != weights[b.index] {
			return weights[a.index] > weights[b.index]
		}
		return a.index < b.index
	})
	for i := int64(0); i < amount-allocated; i++ {
		shares[remainders[i].index]++
	}
	return shares
}
