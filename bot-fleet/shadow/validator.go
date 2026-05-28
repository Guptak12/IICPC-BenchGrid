package shadow

import (
	"container/list"
	"fmt"
	"strings"
	"sync"

	"github.com/emirpasic/gods/trees/redblacktree"
	"github.com/emirpasic/gods/utils"
)

// Order represents an order in the shadow order book
type Order struct {
	ID       int64
	Type     string
	Side     string
	Price    int64
	Quantity int64
}

// Fill represents a trade execution
type Fill struct {
	OrderID     int64
	FilledQty   int64
	FilledPrice int64
	MatchedWith int64
}

// PriceLevel represents a single price level in the order book containing multiple orders (FIFO)
type PriceLevel struct {
	Price  int64
	Orders *list.List // List of *Order
}

// Validator verifies that the C++ engine matched orders correctly
type Validator struct {
	mu            sync.Mutex
	pendingOrders map[int64]*Order // Cache orders until the C++ engine ACKs them
	// O(1) Order Map: orderID -> list.Element inside a PriceLevel's Orders list
	orderMap map[int64]*list.Element

	// Red-Black trees for Price Levels
	bids *redblacktree.Tree // Sorted descending (highest price first)
	asks *redblacktree.Tree // Sorted ascending (lowest price first)

	expectedFills map[int64][]Fill
	actualFills   map[int64][]Fill

	totalExpectedFills int64
	totalActualFills   int64

	missedFills        int64
	phantomFills       int64
	priorityViolations int64
	ackViolations      int64
	duplicateOrders    int64
	unknownAcks        int64
}

func int64DescComparator(a, b interface{}) int {
	return -utils.Int64Comparator(a, b)
}

// NewValidator creates a new shadow global Validator
func NewValidator() *Validator {
	return &Validator{
		pendingOrders: make(map[int64]*Order),
		orderMap:      make(map[int64]*list.Element),
		bids:          redblacktree.NewWith(int64DescComparator),
		asks:          redblacktree.NewWith(utils.Int64Comparator),
		expectedFills: make(map[int64][]Fill),
		actualFills:   make(map[int64][]Fill),
	}
}

// ProcessOrder processes an order sent to the sandbox by adding it to the shadow book and matching it.
func (v *Validator) ProcessOrder(orderID int64, orderType string, side string, price int64, quantity int64) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if _, exists := v.pendingOrders[orderID]; exists {
		v.duplicateOrders++
	}
	if _, exists := v.orderMap[orderID]; exists && strings.ToLower(orderType) != "cancel" {
		v.duplicateOrders++
	}

	v.pendingOrders[orderID] = &Order{
		ID:       orderID,
		Type:     orderType,
		Side:     side,
		Price:    price,
		Quantity: quantity,
	}
}

// ProcessAck ACTUALLY TRIGGERS THE MATCHING LOGIC
func (v *Validator) ProcessAck(orderID int64, status string) {
	v.mu.Lock()
	defer v.mu.Unlock()

	order, ok := v.pendingOrders[orderID]
	if !ok {
		v.unknownAcks++
		return
	}
	delete(v.pendingOrders, orderID)

	cleanStatus := strings.ToLower(status)
	cleanType := strings.ToLower(order.Type)

	switch cleanType {
	case "limit":
		if cleanStatus != "accepted" {
			v.ackViolations++
			return
		}
		v.matchLimitOrder(order)
	case "market":
		if cleanStatus != "accepted" {
			v.ackViolations++
			return
		}
		v.matchMarketOrder(order)
	case "cancel":
		found := v.hasRestingOrder(order.ID)
		expectedStatus := "rejected"
		if found {
			expectedStatus = "cancelled"
		}
		if cleanStatus != expectedStatus {
			v.ackViolations++
			return
		}
		if found {
			v.removeRestingOrder(order.ID)
		}
	default:
		if cleanStatus != "rejected" {
			v.ackViolations++
		}
	}
}

func (v *Validator) matchLimitOrder(order *Order) {
	remainingQty := v.matchIncoming(order, true)
	if remainingQty <= 0 {
		return
	}

	cleanSide := strings.ToLower(order.Side)
	order.Quantity = remainingQty

	if cleanSide == "buy" {
		v.addOrderToBook(order, v.bids)
	} else if cleanSide == "sell" {
		v.addOrderToBook(order, v.asks)
	}
}

func (v *Validator) matchMarketOrder(order *Order) {
	v.matchIncoming(order, false)
}

func (v *Validator) matchIncoming(order *Order, useLimitPrice bool) int64 {
	remainingQty := order.Quantity
	cleanSide := strings.ToLower(order.Side)

	if cleanSide == "buy" {
		// Match against asks
		for remainingQty > 0 {
			bestAskNode := v.asks.Left()
			if bestAskNode == nil {
				break
			}

			bestAskPrice := bestAskNode.Key.(int64)
			if useLimitPrice && order.Price < bestAskPrice {
				break // No cross
			}

			level := bestAskNode.Value.(*PriceLevel)
			for e := level.Orders.Front(); e != nil && remainingQty > 0; {
				restingOrder := e.Value.(*Order)
				next := e.Next()

				tradeQty := remainingQty
				if restingOrder.Quantity < tradeQty {
					tradeQty = restingOrder.Quantity
				}

				v.recordExpectedFill(order.ID, tradeQty, bestAskPrice, restingOrder.ID)
				v.recordExpectedFill(restingOrder.ID, tradeQty, bestAskPrice, order.ID)

				remainingQty -= tradeQty
				restingOrder.Quantity -= tradeQty

				if restingOrder.Quantity == 0 {
					level.Orders.Remove(e)
					delete(v.orderMap, restingOrder.ID)
				}
				e = next
			}

			if level.Orders.Len() == 0 {
				v.asks.Remove(bestAskPrice)
			}
		}

	} else if cleanSide == "sell" {
		// Match against bids
		for remainingQty > 0 {
			bestBidNode := v.bids.Left()
			if bestBidNode == nil {
				break
			}

			bestBidPrice := bestBidNode.Key.(int64)
			if useLimitPrice && order.Price > bestBidPrice {
				break // No cross
			}

			level := bestBidNode.Value.(*PriceLevel)
			for e := level.Orders.Front(); e != nil && remainingQty > 0; {
				restingOrder := e.Value.(*Order)
				next := e.Next()

				tradeQty := remainingQty
				if restingOrder.Quantity < tradeQty {
					tradeQty = restingOrder.Quantity
				}

				v.recordExpectedFill(order.ID, tradeQty, bestBidPrice, restingOrder.ID)
				v.recordExpectedFill(restingOrder.ID, tradeQty, bestBidPrice, order.ID)

				remainingQty -= tradeQty
				restingOrder.Quantity -= tradeQty

				if restingOrder.Quantity == 0 {
					level.Orders.Remove(e)
					delete(v.orderMap, restingOrder.ID)
				}
				e = next
			}

			if level.Orders.Len() == 0 {
				v.bids.Remove(bestBidPrice)
			}
		}
	}

	return remainingQty
}

func (v *Validator) addOrderToBook(order *Order, tree *redblacktree.Tree) {
	node, found := tree.Get(order.Price)
	var level *PriceLevel
	if found {
		level = node.(*PriceLevel)
	} else {
		level = &PriceLevel{Price: order.Price, Orders: list.New()}
		tree.Put(order.Price, level)
	}

	elem := level.Orders.PushBack(order)
	v.orderMap[order.ID] = elem
}

func (v *Validator) hasRestingOrder(orderID int64) bool {
	_, ok := v.orderMap[orderID]
	return ok
}

func (v *Validator) removeRestingOrder(orderID int64) bool {
	elem, ok := v.orderMap[orderID]
	if !ok {
		return false
	}

	order := elem.Value.(*Order)
	tree := v.asks
	if strings.ToLower(order.Side) == "buy" {
		tree = v.bids
	}

	node, found := tree.Get(order.Price)
	if !found {
		delete(v.orderMap, orderID)
		return false
	}

	level := node.(*PriceLevel)
	level.Orders.Remove(elem)
	if level.Orders.Len() == 0 {
		tree.Remove(order.Price)
	}
	delete(v.orderMap, orderID)
	return true
}

func (v *Validator) recordExpectedFill(orderID int64, qty int64, price int64, matchedWith int64) {
	v.expectedFills[orderID] = append(v.expectedFills[orderID], Fill{
		OrderID:     orderID,
		FilledQty:   qty,
		FilledPrice: price,
		MatchedWith: matchedWith,
	})
	v.totalExpectedFills++
}

// ProcessFill processes a fill event from the sandbox
func (v *Validator) ProcessFill(orderID int64, filledQty int64, filledPrice int64, matchedWith ...int64) {
	v.mu.Lock()
	defer v.mu.Unlock()

	var counterparty int64
	if len(matchedWith) > 0 {
		counterparty = matchedWith[0]
	}

	actualFill := Fill{
		OrderID:     orderID,
		FilledQty:   filledQty,
		FilledPrice: filledPrice,
		MatchedWith: counterparty,
	}
	v.actualFills[orderID] = append(v.actualFills[orderID], actualFill)
	v.totalActualFills++
}

// GetCorrectnessScore calculates final correctness score
func (v *Validator) GetCorrectnessScore() float64 {
	v.mu.Lock()
	defer v.mu.Unlock()

	var expectedQty int64
	var actualQty int64
	var priceCorrectQty int64
	var priorityCorrectQty int64
	var phantomQty int64
	var expectedValue int64
	var valueDiff int64

	v.missedFills = 0
	v.phantomFills = 0
	v.priorityViolations = 0

	for orderID, expectedList := range v.expectedFills {
		actualList, actOk := v.actualFills[orderID]

		if !actOk {
			v.missedFills++
		}

		var totalExpQty, totalActQty int64
		var expValue, actValue int64

		for _, f := range expectedList {
			totalExpQty += f.FilledQty
			expValue += f.FilledQty * f.FilledPrice
		}

		for _, f := range actualList {
			totalActQty += f.FilledQty
			actValue += f.FilledQty * f.FilledPrice
		}

		expectedQty += totalExpQty
		actualQty += totalActQty
		expectedValue += expValue
		valueDiff += absInt64(expValue - actValue)

		if totalActQty < totalExpQty {
			v.missedFills++
		} else if totalActQty > totalExpQty {
			v.phantomFills++
			phantomQty += totalActQty - totalExpQty
		}

		priceCorrectQty += matchingQtyByPrice(expectedList, actualList)
		priorityCorrectQty += v.priorityMatchedQty(expectedList, actualList)

		if totalExpQty != totalActQty || expValue != actValue {
			fmt.Printf("[Validator] Mismatch Order %d: Expected Qty=%d Value=%d, Actual Qty=%d Value=%d\n",
				orderID, totalExpQty, expValue, totalActQty, actValue)
		}
	}

	// Unaccounted fills (completely phantom)
	for orderID := range v.actualFills {
		if _, expOk := v.expectedFills[orderID]; !expOk {
			v.phantomFills++
			for _, f := range v.actualFills[orderID] {
				actualQty += f.FilledQty
				phantomQty += f.FilledQty
			}
		}
	}

	if expectedQty == 0 {
		if actualQty > 0 || v.ackViolations > 0 || v.duplicateOrders > 0 || v.unknownAcks > 0 {
			return 0.0
		}
		return 100.0
	}

	quantityScore := (float64(minInt64(priceCorrectQty, expectedQty)) / float64(expectedQty)) * 70.0
	priorityScore := (float64(minInt64(priorityCorrectQty, expectedQty)) / float64(expectedQty)) * 20.0
	valueScore := 10.0
	if expectedValue > 0 {
		valueScore = maxFloat64(0, 10.0*(1.0-(float64(valueDiff)/float64(expectedValue))))
	}

	score := quantityScore + priorityScore + valueScore

	if phantomQty > 0 {
		score -= minFloat64(25.0, 25.0*(float64(phantomQty)/float64(expectedQty)))
	}
	score -= float64(v.ackViolations) * 2.0
	score -= float64(v.duplicateOrders) * 2.0
	score -= float64(v.unknownAcks) * 2.0

	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}

	return score
}

func (v *Validator) priorityMatchedQty(expectedList []Fill, actualList []Fill) int64 {
	var matched int64
	for i, expected := range expectedList {
		if i >= len(actualList) {
			v.priorityViolations++
			continue
		}
		actual := actualList[i]
		if actual.FilledQty != expected.FilledQty || actual.FilledPrice != expected.FilledPrice {
			v.priorityViolations++
			continue
		}
		if actual.MatchedWith != 0 && actual.MatchedWith != expected.MatchedWith {
			v.priorityViolations++
			continue
		}
		matched += expected.FilledQty
	}
	if len(actualList) > len(expectedList) {
		v.priorityViolations += int64(len(actualList) - len(expectedList))
	}
	return matched
}

func matchingQtyByPrice(expectedList []Fill, actualList []Fill) int64 {
	expectedByPrice := make(map[int64]int64)
	actualByPrice := make(map[int64]int64)
	for _, f := range expectedList {
		expectedByPrice[f.FilledPrice] += f.FilledQty
	}
	for _, f := range actualList {
		actualByPrice[f.FilledPrice] += f.FilledQty
	}

	var matched int64
	for price, expQty := range expectedByPrice {
		matched += minInt64(expQty, actualByPrice[price])
	}
	return matched
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func minFloat64(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxFloat64(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
