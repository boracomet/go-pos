package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func basketTestItem(id, price string, quantity ...int) BasketBuilderItem {
	item := BasketBuilderItem{
		ID:        id,
		Name:      "Item " + id,
		Category1: "Category",
		ItemType:  string(BasketItemPhysical),
		Price:     price,
	}
	if len(quantity) > 0 {
		item.Quantity = coreTestPtr(quantity[0])
	}
	return item
}

func basketTestPrices(items []BasketItem) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Price)
	}
	return out
}

func basketTestWantPrices(t *testing.T, got []BasketItem, want []string) {
	t.Helper()
	if !reflect.DeepEqual(basketTestPrices(got), want) {
		t.Fatalf("prices = %v, want %v", basketTestPrices(got), want)
	}
}

func basketTestBuild(t *testing.T, options BuildBasketOptions) *BuiltBasket {
	t.Helper()
	result, err := BuildBasket(options)
	if err != nil {
		t.Fatalf("BuildBasket: %v", err)
	}
	return result
}

func basketTestIssue(t *testing.T, options BuildBasketOptions, path string) {
	t.Helper()
	_, err := BuildBasket(options)
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %v (%T), want *ValidationError", err, err)
	}
	if validation.Field() != path {
		t.Errorf("field = %q, want %q (message %q)", validation.Field(), path, validation.Message)
	}
}

func TestBuildBasket(t *testing.T) {
	t.Run("returns the items unchanged without discount or shipping", func(t *testing.T) {
		result := basketTestBuild(t, BuildBasketOptions{Items: []BasketBuilderItem{
			basketTestItem("A", "49.9"), basketTestItem("B", "35"),
		}})
		basketTestWantPrices(t, result.BasketItems, []string{"49.90", "35.00"})
		if result.Price != "84.90" {
			t.Errorf("price = %q, want 84.90", result.Price)
		}
		if result.Discount != "0.00" {
			t.Errorf("discount = %q, want 0.00", result.Discount)
		}
		want := BasketItem{ID: "A", Name: "Item A", Category1: "Category", ItemType: "PHYSICAL", Price: "49.90"}
		if !reflect.DeepEqual(result.BasketItems[0], want) {
			t.Errorf("item = %+v, want %+v", result.BasketItems[0], want)
		}
	})

	t.Run("spreads a percentage discount and gives the leftover kurus to the largest remainder", func(t *testing.T) {
		result := basketTestBuild(t, BuildBasketOptions{
			Items: []BasketBuilderItem{
				basketTestItem("A", "33.33"), basketTestItem("B", "33.33"), basketTestItem("C", "33.34"),
			},
			Discount: &BasketDiscount{Percent: coreTestPtr(10.0)},
		})
		if result.Discount != "10.00" {
			t.Errorf("discount = %q, want 10.00", result.Discount)
		}
		if result.Price != "90.00" {
			t.Errorf("price = %q, want 90.00", result.Price)
		}
		if got := coreTestSumMinor(t, result.BasketItems); got != 9000 {
			t.Errorf("item total = %d, want 9000", got)
		}
		basketTestWantPrices(t, result.BasketItems, []string{"30.00", "30.00", "30.00"})
	})

	t.Run("breaks remainder ties towards the larger item, then the earlier one", func(t *testing.T) {
		equal := basketTestBuild(t, BuildBasketOptions{
			Items:    []BasketBuilderItem{basketTestItem("A", "10"), basketTestItem("B", "10")},
			Discount: &BasketDiscount{Amount: "0.01"},
		})
		basketTestWantPrices(t, equal.BasketItems, []string{"9.99", "10.00"})

		unequal := basketTestBuild(t, BuildBasketOptions{
			Items:    []BasketBuilderItem{basketTestItem("A", "10"), basketTestItem("B", "20")},
			Discount: &BasketDiscount{Amount: "0.02"},
		})
		basketTestWantPrices(t, unequal.BasketItems, []string{"9.99", "19.99"})
	})

	t.Run("spreads a fixed discount in proportion to item totals", func(t *testing.T) {
		result := basketTestBuild(t, BuildBasketOptions{
			Items:    []BasketBuilderItem{basketTestItem("A", "49.90", 2), basketTestItem("B", "35.00")},
			Discount: &BasketDiscount{Amount: "13.48"},
			Quantity: QuantityMultiply,
		})
		basketTestWantPrices(t, result.BasketItems, []string{"89.82", "31.50"})
		if result.Price != "121.32" {
			t.Errorf("price = %q, want 121.32", result.Price)
		}
	})

	t.Run("expands quantities into one item per unit by default", func(t *testing.T) {
		result := basketTestBuild(t, BuildBasketOptions{Items: []BasketBuilderItem{
			basketTestItem("A", "49.90", 2), basketTestItem("B", "35.00"),
		}})
		if len(result.BasketItems) != 3 {
			t.Fatalf("items = %d, want 3", len(result.BasketItems))
		}
		got := make([][2]string, 0, len(result.BasketItems))
		for _, item := range result.BasketItems {
			got = append(got, [2]string{item.ID, item.Price})
		}
		want := [][2]string{{"A-1", "49.90"}, {"A-2", "49.90"}, {"B", "35.00"}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("items = %v, want %v", got, want)
		}
		data, err := json.Marshal(result.BasketItems[0])
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(data), "quantity") {
			t.Error("an expanded item must not carry a quantity")
		}
		if _, has := reflect.TypeOf(BasketItem{}).FieldByName("Quantity"); has {
			t.Error("BasketItem must not expose a quantity field")
		}
	})

	t.Run("multiplies the price by the quantity when asked", func(t *testing.T) {
		result := basketTestBuild(t, BuildBasketOptions{
			Items:    []BasketBuilderItem{basketTestItem("A", "49.90", 3)},
			Quantity: QuantityMultiply,
		})
		if len(result.BasketItems) != 1 {
			t.Fatalf("items = %d, want 1", len(result.BasketItems))
		}
		if result.BasketItems[0].ID != "A" || result.BasketItems[0].Price != "149.70" {
			t.Errorf("item = %+v, want id A priced 149.70", result.BasketItems[0])
		}
	})

	t.Run("adds shipping as the last item, outside the discount", func(t *testing.T) {
		result := basketTestBuild(t, BuildBasketOptions{
			Items:    []BasketBuilderItem{basketTestItem("A", "100")},
			Discount: &BasketDiscount{Percent: coreTestPtr(10.0)},
			Shipping: &BasketShipping{Price: "29.90"},
		})
		want := []BasketItem{
			{ID: "A", Name: "Item A", Category1: "Category", ItemType: "PHYSICAL", Price: "90.00"},
			{ID: "shipping", Name: "Shipping", Category1: "Shipping", ItemType: "VIRTUAL", Price: "29.90"},
		}
		if !reflect.DeepEqual(result.BasketItems, want) {
			t.Errorf("items = %+v, want %+v", result.BasketItems, want)
		}
		if result.Price != "119.90" {
			t.Errorf("price = %q, want 119.90", result.Price)
		}
	})

	t.Run("accepts a custom shipping item and skips free shipping", func(t *testing.T) {
		custom := basketTestBuild(t, BuildBasketOptions{
			Items: []BasketBuilderItem{basketTestItem("A", "10")},
			Shipping: &BasketShipping{
				Price: "5", ID: "S1", Name: "Kargo", Category1: "Kargo", ItemType: "PHYSICAL",
			},
		})
		want := BasketItem{ID: "S1", Name: "Kargo", Category1: "Kargo", ItemType: "PHYSICAL", Price: "5.00"}
		if !reflect.DeepEqual(custom.BasketItems[1], want) {
			t.Errorf("shipping = %+v, want %+v", custom.BasketItems[1], want)
		}

		free := basketTestBuild(t, BuildBasketOptions{
			Items:    []BasketBuilderItem{basketTestItem("A", "10")},
			Shipping: &BasketShipping{Price: "0"},
		})
		if len(free.BasketItems) != 1 {
			t.Errorf("items = %d, want 1: free shipping adds no item", len(free.BasketItems))
		}
		if free.Price != "10.00" {
			t.Errorf("price = %q, want 10.00", free.Price)
		}
	})

	t.Run("rounds percentage discounts half up", func(t *testing.T) {
		half := basketTestBuild(t, BuildBasketOptions{
			Items:    []BasketBuilderItem{basketTestItem("A", "0.20")},
			Discount: &BasketDiscount{Percent: coreTestPtr(12.5)},
		})
		if half.Discount != "0.03" {
			t.Errorf("discount = %q, want 0.03", half.Discount)
		}
		down := basketTestBuild(t, BuildBasketOptions{
			Items:    []BasketBuilderItem{basketTestItem("A", "1")},
			Discount: &BasketDiscount{Percent: coreTestPtr(33.333)},
		})
		if down.Discount != "0.33" {
			t.Errorf("discount = %q, want 0.33", down.Discount)
		}
	})
}

func TestBuildBasketValidation(t *testing.T) {
	t.Run("rejects an empty basket", func(t *testing.T) {
		basketTestIssue(t, BuildBasketOptions{Items: []BasketBuilderItem{}}, "items")
	})

	t.Run("rejects zero, negative and invalid prices", func(t *testing.T) {
		basketTestIssue(t, BuildBasketOptions{Items: []BasketBuilderItem{basketTestItem("A", "0")}}, "items[0].price")
		basketTestIssue(t, BuildBasketOptions{Items: []BasketBuilderItem{
			basketTestItem("A", "10"), basketTestItem("B", "-1"),
		}}, "items[1].price")
		basketTestIssue(t, BuildBasketOptions{Items: []BasketBuilderItem{basketTestItem("A", "abc")}}, "items[0].price")
		basketTestIssue(t, BuildBasketOptions{
			Items:    []BasketBuilderItem{basketTestItem("A", "10")},
			Shipping: &BasketShipping{Price: "-5"},
		}, "shipping.price")
	})

	t.Run("rejects an invalid quantity", func(t *testing.T) {
		basketTestIssue(t, BuildBasketOptions{Items: []BasketBuilderItem{basketTestItem("A", "10", 0)}}, "items[0].quantity")
		basketTestIssue(t, BuildBasketOptions{Items: []BasketBuilderItem{basketTestItem("A", "10", -2)}}, "items[0].quantity")
	})

	t.Run("rejects a discount equal to or larger than the item total", func(t *testing.T) {
		basketTestIssue(t, BuildBasketOptions{
			Items: []BasketBuilderItem{basketTestItem("A", "10")}, Discount: &BasketDiscount{Amount: "10"},
		}, "discount")
		basketTestIssue(t, BuildBasketOptions{
			Items: []BasketBuilderItem{basketTestItem("A", "10")}, Discount: &BasketDiscount{Amount: "12"},
		}, "discount")
		basketTestIssue(t, BuildBasketOptions{
			Items: []BasketBuilderItem{basketTestItem("A", "10")}, Discount: &BasketDiscount{Percent: coreTestPtr(100.0)},
		}, "discount")
		basketTestIssue(t, BuildBasketOptions{
			Items:    []BasketBuilderItem{basketTestItem("A", "10")},
			Discount: &BasketDiscount{Amount: "10"},
			Shipping: &BasketShipping{Price: "5"},
		}, "discount")
	})

	t.Run("rejects an invalid percent", func(t *testing.T) {
		for _, percent := range []float64{-1, 101, math.NaN()} {
			basketTestIssue(t, BuildBasketOptions{
				Items:    []BasketBuilderItem{basketTestItem("A", "10")},
				Discount: &BasketDiscount{Percent: coreTestPtr(percent)},
			}, "discount.percent")
		}
	})

	t.Run("rejects a discount that brings an item down to zero", func(t *testing.T) {
		basketTestIssue(t, BuildBasketOptions{
			Items:    []BasketBuilderItem{basketTestItem("A", "1.00"), basketTestItem("B", "0.01")},
			Discount: &BasketDiscount{Amount: "1.00"},
		}, "discount")
	})
}

var basketTestLinePrice = regexp.MustCompile(`^\d+\.\d{2}$`)

type basketTestRand struct{ seed uint32 }

func (r *basketTestRand) next() float64 {
	r.seed += 0x6d2b79f5
	t := (r.seed ^ (r.seed >> 15)) * (1 | r.seed)
	t = (t + (t^(t>>7))*(61|t)) ^ t
	return float64(t^(t>>14)) / 4294967296
}

func (r *basketTestRand) between(min, max int) int {
	return min + int(math.Floor(r.next()*float64(max-min+1)))
}

func basketTestAmount(minor int) string {
	return fmt.Sprintf("%d.%02d", minor/100, minor%100)
}

func TestBuildBasketAlwaysAddsUpToPrice(t *testing.T) {
	random := &basketTestRand{seed: 0x5eed}
	checked := 0

	for run := 0; run < 2000; run++ {
		count := random.between(1, 8)
		items := make([]BasketBuilderItem, 0, count)
		itemTotal := int64(0)
		for i := 0; i < count; i++ {
			price := random.between(1, 500000)
			quantity := random.between(1, 4)
			items = append(items, basketTestItem(fmt.Sprintf("I%d", i), basketTestAmount(price), quantity))
			itemTotal += int64(price) * int64(quantity)
		}
		mode := QuantityExpand
		if random.next() < 0.5 {
			mode = QuantityMultiply
		}
		options := BuildBasketOptions{Items: items, Quantity: mode}
		if random.next() < 0.5 {
			options.Discount = &BasketDiscount{Percent: coreTestPtr(float64(random.between(0, 9999)) / 100)}
		} else {
			options.Discount = &BasketDiscount{Amount: basketTestAmount(random.between(0, int(itemTotal)-1))}
		}
		shippingMinor := 0
		if random.next() < 0.5 {
			shippingMinor = random.between(0, 10000)
			options.Shipping = &BasketShipping{Price: basketTestAmount(shippingMinor)}
		}

		result, err := BuildBasket(options)
		if err != nil {
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("run %d: error = %v (%T), want *ValidationError", run, err, err)
			}
			if !strings.Contains(validation.Message, "down to zero") {
				t.Fatalf("run %d: message = %q, want a discount that zeroes out an item", run, validation.Message)
			}
			continue
		}
		checked++

		if got, want := coreTestSumMinor(t, result.BasketItems), coreTestMinor(t, result.Price); got != want {
			t.Fatalf("run %d: item total = %d, price = %d", run, got, want)
		}
		if got, want := coreTestMinor(t, result.Price), itemTotal-coreTestMinor(t, result.Discount)+int64(shippingMinor); got != want {
			t.Fatalf("run %d: price = %d, want %d", run, got, want)
		}
		for _, line := range result.BasketItems {
			if !basketTestLinePrice.MatchString(line.Price) {
				t.Fatalf("run %d: line price = %q, want two decimals", run, line.Price)
			}
			if coreTestMinor(t, line.Price) <= 0 {
				t.Fatalf("run %d: line %s priced %q, want a positive amount", run, line.ID, line.Price)
			}
		}
	}

	if checked <= 1000 {
		t.Errorf("checked = %d, want more than 1000 accepted baskets", checked)
	}
}
