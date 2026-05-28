package shadow

import (
	"container/list"
	"fmt"
	"sync"
	"strings"
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
}

// PriceLevel represents a single price level in the order book containing multiple orders (FIFO)
type PriceLevel struct {
	Price  int64
	Orders *list.List // List of *Order
}


// Validator verifies that the C++ engine matched orders correctly
type Validator struct {
	mu sync.Mutex
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

	missedFills       int64
	phantomFills      int64
	priorityViolations int64
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

	cleanStatus := strings.ToLower(status)
	if cleanStatus != "accepted" {
		return
	}

	order, ok := v.pendingOrders[orderID]
	if !ok {
		// If this happens, Kafka ordering fundamentally failed (Ack arrived before Sent)
		// But because they share a Partition Key, this should never happen.
		return 
	}

	// Now that it is officially the "next" order in the C++ engine's timeline, match it!
	// 2. Force lowercase check
	cleanType := strings.ToLower(order.Type)
	if cleanType == "limit" {
		v.matchLimitOrder(order)
	}
}
func (v *Validator) matchLimitOrder(order *Order) {
	var remainingQty = order.Quantity
	cleanSide := strings.ToLower(order.Side)

	if cleanSide == "buy" {
		// Match against asks
		for remainingQty > 0 {
			bestAskNode := v.asks.Left()
			if bestAskNode == nil {
				break
			}
			
			bestAskPrice := bestAskNode.Key.(int64)
			if order.Price < bestAskPrice {
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

				v.recordExpectedFill(order.ID, tradeQty, bestAskPrice)
				v.recordExpectedFill(restingOrder.ID, tradeQty, bestAskPrice)

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

		if remainingQty > 0 {
			order.Quantity = remainingQty
			v.addOrderToBook(order, v.bids)
		}

	} else if order.Side == "sell" {
		// Match against bids
		for remainingQty > 0 {
			bestBidNode := v.bids.Left()
			if bestBidNode == nil {
				break
			}

			bestBidPrice := bestBidNode.Key.(int64)
			if order.Price > bestBidPrice {
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

				v.recordExpectedFill(order.ID, tradeQty, bestBidPrice)
				v.recordExpectedFill(restingOrder.ID, tradeQty, bestBidPrice)

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

		if remainingQty > 0 {
			order.Quantity = remainingQty
			v.addOrderToBook(order, v.asks)
		}
	}
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

func (v *Validator) recordExpectedFill(orderID int64, qty int64, price int64) {
	v.expectedFills[orderID] = append(v.expectedFills[orderID], Fill{OrderID: orderID, FilledQty: qty, FilledPrice: price})
	v.totalExpectedFills++
}

// ProcessFill processes a fill event from the sandbox
func (v *Validator) ProcessFill(orderID int64, filledQty int64, filledPrice int64) {
	v.mu.Lock()
	defer v.mu.Unlock()

	actualFill := Fill{OrderID: orderID, FilledQty: filledQty, FilledPrice: filledPrice}
	v.actualFills[orderID] = append(v.actualFills[orderID], actualFill)
	v.totalActualFills++
}

// GetCorrectnessScore calculates final correctness score
func (v *Validator) GetCorrectnessScore() float64 {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.totalExpectedFills == 0 && v.totalActualFills == 0 {
		return 100.0
	}

	var correctOrders int64
	var totalOrdersEvaluated int64
	
	v.missedFills = 0
	v.phantomFills = 0

	for orderID, expectedList := range v.expectedFills {
		totalOrdersEvaluated++
		actualList, actOk := v.actualFills[orderID]

		if !actOk {
			v.missedFills++
			continue 
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

		if totalExpQty == totalActQty && expValue == actValue {
			correctOrders++
		} else {
			if totalActQty < totalExpQty {
				v.missedFills++ // Partially missed
			} else if totalActQty > totalExpQty {
				v.phantomFills++
			}
			// Value mismatch could indicate price violation
			fmt.Printf("[Validator] Mismatch Order %d: Expected Qty=%d Value=%d, Actual Qty=%d Value=%d\n",
				orderID, totalExpQty, expValue, totalActQty, actValue)
		}
	}

	// Unaccounted fills (completely phantom)
	for orderID := range v.actualFills {
		if _, expOk := v.expectedFills[orderID]; !expOk {
			v.phantomFills++
		}
	}

	if totalOrdersEvaluated == 0 {
		if v.phantomFills > 0 {
			return 0.0
		}
		return 100.0
	}

	// Deduct heavily for missed and phantom fills
	score := 100.0 - (float64(v.missedFills)*5.0 + float64(v.phantomFills)*10.0)
	
	// Basic correctness percentage if score wasn't heavily penalized
	baseScore := (float64(correctOrders) / float64(totalOrdersEvaluated)) * 100.0
	
	if score < 0 {
		score = 0
	}
	
	// Return the lower of the two to be strict
	if score > baseScore {
		return baseScore
	}
	
	return score
}
