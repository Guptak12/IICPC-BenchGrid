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

// foldPartials merges consecutive fills that have the same price and counterparty.
func foldPartials(fills []Fill) []Fill {
	if len(fills) <= 1 {
		return fills
	}

	folded := make([]Fill, 0, len(fills))
	current := fills[0]
	for i := 1; i < len(fills); i++ {
		if fills[i].FilledPrice == current.FilledPrice && fills[i].MatchedWith == current.MatchedWith {
			current.FilledQty += fills[i].FilledQty
			continue
		}
		folded = append(folded, current)
		current = fills[i]
	}
	folded = append(folded, current)
	return folded
}

// PriceLevel represents a single price level in the order book containing multiple orders (FIFO)
type PriceLevel struct {
	Price  int64
	Orders *list.List // List of *Order
}

func int64DescComparator(a, b interface{}) int {
	return -utils.Int64Comparator(a, b)
}

// SymbolShard handles order book and validation logic for a single symbol
type SymbolShard struct {
	mu            sync.Mutex
	pendingOrders map[int64]*Order
	orderMap      map[int64]*list.Element

	// Red-Black trees for Price Levels
	bids *redblacktree.Tree // Sorted descending
	asks *redblacktree.Tree // Sorted ascending

	expectedFills map[int64][]Fill
	actualFills   map[int64][]Fill

	// Incremental counters accumulated for cleaned-up orders
	expectedQty        int64
	actualQty          int64
	priceCorrectQty    int64
	priorityCorrectQty int64
	expectedValue      int64
	valueDiff          int64
	phantomQty         int64

	missedFills        int64
	phantomFills       int64
	priorityViolations int64
	ackViolations      int64
	duplicateOrders    int64
	unknownAcks        int64
	printedMismatches  int
}

func NewSymbolShard() *SymbolShard {
	return &SymbolShard{
		pendingOrders: make(map[int64]*Order),
		orderMap:      make(map[int64]*list.Element),
		bids:          redblacktree.NewWith(int64DescComparator),
		asks:          redblacktree.NewWith(utils.Int64Comparator),
		expectedFills: make(map[int64][]Fill),
		actualFills:   make(map[int64][]Fill),
	}
}

func (s *SymbolShard) checkCleanup(orderID int64) {
	_, isPending := s.pendingOrders[orderID]
	if isPending {
		return
	}
	_, isResting := s.orderMap[orderID]
	if isResting {
		return
	}

	expList := s.expectedFills[orderID]
	actList := s.actualFills[orderID]

	var totalExpQty, totalActQty int64
	for _, f := range expList {
		totalExpQty += f.FilledQty
	}
	for _, f := range actList {
		totalActQty += f.FilledQty
	}

	if totalExpQty != totalActQty {
		return
	}

	foldedExp := foldPartials(expList)
	foldedAct := foldPartials(actList)

	if len(foldedExp) != len(foldedAct) {
		return
	}
	for i := range foldedExp {
		if foldedExp[i].FilledQty != foldedAct[i].FilledQty ||
			foldedExp[i].FilledPrice != foldedAct[i].FilledPrice ||
			foldedExp[i].MatchedWith != foldedAct[i].MatchedWith {
			return
		}
	}

	// Perfectly matched!
	s.expectedQty += totalExpQty
	s.actualQty += totalActQty
	s.priceCorrectQty += totalExpQty
	s.priorityCorrectQty += totalExpQty

	var expValue int64
	for _, f := range foldedExp {
		expValue += f.FilledQty * f.FilledPrice
	}
	s.expectedValue += expValue

	delete(s.expectedFills, orderID)
	delete(s.actualFills, orderID)
}

func (s *SymbolShard) matchLimitOrder(order *Order) {
	remainingQty := s.matchIncoming(order, true)
	if remainingQty <= 0 {
		return
	}

	cleanSide := strings.ToLower(order.Side)
	order.Quantity = remainingQty

	if cleanSide == "buy" {
		s.addOrderToBook(order, s.bids)
	} else if cleanSide == "sell" {
		s.addOrderToBook(order, s.asks)
	}
}

func (s *SymbolShard) matchMarketOrder(order *Order) {
	s.matchIncoming(order, false)
}

func (s *SymbolShard) matchIncoming(order *Order, useLimitPrice bool) int64 {
	remainingQty := order.Quantity
	cleanSide := strings.ToLower(order.Side)
	var emptyLevels []int64

	if cleanSide == "buy" {
		iter := s.asks.Iterator()
		for iter.Next() && remainingQty > 0 {
			askPrice := iter.Key().(int64)

			if useLimitPrice && order.Price < askPrice {
				break
			}

			level := iter.Value().(*PriceLevel)

			for e := level.Orders.Front(); e != nil && remainingQty > 0; {
				restingOrder := e.Value.(*Order)
				next := e.Next()

				if isSelfCross(order.ID, restingOrder.ID) {
					e = next
					continue
				}

				tradeQty := remainingQty
				if restingOrder.Quantity < tradeQty {
					tradeQty = restingOrder.Quantity
				}

				s.recordExpectedFill(order.ID, tradeQty, askPrice, restingOrder.ID)
				s.recordExpectedFill(restingOrder.ID, tradeQty, askPrice, order.ID)

				remainingQty -= tradeQty
				restingOrder.Quantity -= tradeQty

				if restingOrder.Quantity == 0 {
					level.Orders.Remove(e)
					delete(s.orderMap, restingOrder.ID)
					s.checkCleanup(restingOrder.ID)
				}
				e = next
			}

			if level.Orders.Len() == 0 {
				emptyLevels = append(emptyLevels, askPrice)
			}
		}

		for _, price := range emptyLevels {
			s.asks.Remove(price)
		}

	} else if cleanSide == "sell" {
		iter := s.bids.Iterator()
		for iter.Next() && remainingQty > 0 {
			bidPrice := iter.Key().(int64)

			if useLimitPrice && order.Price > bidPrice {
				break
			}

			level := iter.Value().(*PriceLevel)

			for e := level.Orders.Front(); e != nil && remainingQty > 0; {
				restingOrder := e.Value.(*Order)
				next := e.Next()

				if isSelfCross(order.ID, restingOrder.ID) {
					e = next
					continue
				}

				tradeQty := remainingQty
				if restingOrder.Quantity < tradeQty {
					tradeQty = restingOrder.Quantity
				}

				s.recordExpectedFill(order.ID, tradeQty, bidPrice, restingOrder.ID)
				s.recordExpectedFill(restingOrder.ID, tradeQty, bidPrice, order.ID)

				remainingQty -= tradeQty
				restingOrder.Quantity -= tradeQty

				if restingOrder.Quantity == 0 {
					level.Orders.Remove(e)
					delete(s.orderMap, restingOrder.ID)
					s.checkCleanup(restingOrder.ID)
				}
				e = next
			}

			if level.Orders.Len() == 0 {
				emptyLevels = append(emptyLevels, bidPrice)
			}
		}

		for _, price := range emptyLevels {
			s.bids.Remove(price)
		}
	}

	return remainingQty
}

func (s *SymbolShard) addOrderToBook(order *Order, tree *redblacktree.Tree) {
	node, found := tree.Get(order.Price)
	var level *PriceLevel
	if found {
		level = node.(*PriceLevel)
	} else {
		level = &PriceLevel{Price: order.Price, Orders: list.New()}
		tree.Put(order.Price, level)
	}

	elem := level.Orders.PushBack(order)
	s.orderMap[order.ID] = elem
}

func (s *SymbolShard) hasRestingOrder(orderID int64) bool {
	_, ok := s.orderMap[orderID]
	return ok
}

func (s *SymbolShard) removeRestingOrder(orderID int64) bool {
	elem, ok := s.orderMap[orderID]
	if !ok {
		return false
	}

	order := elem.Value.(*Order)
	tree := s.asks
	if strings.ToLower(order.Side) == "buy" {
		tree = s.bids
	}

	node, found := tree.Get(order.Price)
	if !found {
		delete(s.orderMap, orderID)
		return false
	}

	level := node.(*PriceLevel)
	level.Orders.Remove(elem)
	if level.Orders.Len() == 0 {
		tree.Remove(order.Price)
	}
	delete(s.orderMap, orderID)
	return true
}

func (s *SymbolShard) recordExpectedFill(orderID int64, qty int64, price int64, matchedWith int64) {
	s.expectedFills[orderID] = append(s.expectedFills[orderID], Fill{
		OrderID:     orderID,
		FilledQty:   qty,
		FilledPrice: price,
		MatchedWith: matchedWith,
	})
}

func (s *SymbolShard) priorityMatchedQty(expectedList []Fill, actualList []Fill) int64 {
	var matched int64
	for i, expected := range expectedList {
		if i >= len(actualList) {
			s.priorityViolations++
			continue
		}
		actual := actualList[i]
		if actual.FilledQty != expected.FilledQty || actual.FilledPrice != expected.FilledPrice {
			s.priorityViolations++
			continue
		}
		if actual.MatchedWith != expected.MatchedWith {
			s.priorityViolations++
			continue
		}
		matched += expected.FilledQty
	}
	if len(actualList) > len(expectedList) {
		s.priorityViolations += int64(len(actualList) - len(expectedList))
	}
	return matched
}

// Validator routes validation updates to symbol shards to prevent contention
type Validator struct {
	mu           sync.RWMutex
	shards       map[string]*SymbolShard
	orderToShard map[int64]*SymbolShard
}

// NewValidator creates a new sharded Validator
func NewValidator() *Validator {
	return &Validator{
		shards:       make(map[string]*SymbolShard),
		orderToShard: make(map[int64]*SymbolShard),
	}
}

func (v *Validator) getShard(symbol string) *SymbolShard {
	v.mu.Lock()
	defer v.mu.Unlock()
	shard, ok := v.shards[symbol]
	if !ok {
		shard = NewSymbolShard()
		v.shards[symbol] = shard
	}
	return shard
}

func (v *Validator) getShardForOrder(orderID int64) *SymbolShard {
	v.mu.RLock()
	shard, ok := v.orderToShard[orderID]
	v.mu.RUnlock()
	if ok {
		return shard
	}
	return v.getShard("BTCUSD")
}

func (v *Validator) ProcessOrder(orderID int64, orderType string, side string, price int64, quantity int64) {
	shard := v.getShard("BTCUSD")
	v.mu.Lock()
	v.orderToShard[orderID] = shard
	v.mu.Unlock()

	shard.mu.Lock()
	defer shard.mu.Unlock()

	if strings.ToLower(orderType) == "cancel" {
		return
	}

	if _, exists := shard.pendingOrders[orderID]; exists {
		shard.duplicateOrders++
	}
	if _, exists := shard.orderMap[orderID]; exists {
		shard.duplicateOrders++
	}

	shard.pendingOrders[orderID] = &Order{
		ID:       orderID,
		Type:     orderType,
		Side:     side,
		Price:    price,
		Quantity: quantity,
	}
	shard.checkCleanup(orderID)
}

func (v *Validator) ProcessAck(orderID int64, status string) {
	shard := v.getShardForOrder(orderID)
	shard.mu.Lock()
	defer shard.mu.Unlock()

	cleanStatus := strings.ToLower(status)

	if cleanStatus == "cancelled" {
		found := shard.hasRestingOrder(orderID)
		if found {
			shard.removeRestingOrder(orderID)
		}
		shard.checkCleanup(orderID)
		return
	}

	order, ok := shard.pendingOrders[orderID]
	if !ok {
		if cleanStatus == "rejected" {
			shard.checkCleanup(orderID)
			return
		}
		shard.unknownAcks++
		shard.checkCleanup(orderID)
		return
	}
	delete(shard.pendingOrders, orderID)

	cleanType := strings.ToLower(order.Type)

	switch cleanType {
	case "limit":
		if cleanStatus != "accepted" {
			shard.ackViolations++
			shard.checkCleanup(orderID)
			return
		}
		shard.matchLimitOrder(order)
	case "market":
		if cleanStatus != "accepted" {
			shard.ackViolations++
			shard.checkCleanup(orderID)
			return
		}
		shard.matchMarketOrder(order)
	default:
		if cleanStatus != "rejected" {
			shard.ackViolations++
		}
	}
	shard.checkCleanup(orderID)
}

func (v *Validator) ProcessFill(orderID int64, filledQty int64, filledPrice int64, matchedWith ...int64) {
	shard := v.getShardForOrder(orderID)
	shard.mu.Lock()
	defer shard.mu.Unlock()

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
	shard.actualFills[orderID] = append(shard.actualFills[orderID], actualFill)
	shard.checkCleanup(orderID)
}

// GetCorrectnessScore calculates final correctness score across all shards
func (v *Validator) GetCorrectnessScore() float64 {
	v.mu.Lock()
	defer v.mu.Unlock()

	var expectedQty int64
	var actualQty int64
	var priceCorrectQty int64
	var priorityCorrectQty int64
	var expectedValue int64
	var valueDiff int64
	var phantomQty int64
	var ackViolations int64
	var duplicateOrders int64
	var unknownAcks int64

	for _, s := range v.shards {
		s.mu.Lock()
		for orderID := range s.expectedFills {
			s.checkCleanup(orderID)
		}

		expectedQty += s.expectedQty
		actualQty += s.actualQty
		priceCorrectQty += s.priceCorrectQty
		priorityCorrectQty += s.priorityCorrectQty
		expectedValue += s.expectedValue
		valueDiff += s.valueDiff
		phantomQty += s.phantomQty
		ackViolations += s.ackViolations
		duplicateOrders += s.duplicateOrders
		unknownAcks += s.unknownAcks

		for orderID, expectedList := range s.expectedFills {
			actualList, actOk := s.actualFills[orderID]
			expectedList = foldPartials(expectedList)
			if actOk {
				actualList = foldPartials(actualList)
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

			if totalActQty > totalExpQty {
				phantomQty += totalActQty - totalExpQty
			}

			priceCorrectQty += matchingQtyByPrice(expectedList, actualList)
			priorityCorrectQty += s.priorityMatchedQty(expectedList, actualList)

			if totalExpQty != totalActQty || expValue != actValue {
				if s.printedMismatches < 50 {
					s.printedMismatches++
					fmt.Printf("[Validator] Mismatch Order %d (Bot %d): Expected Qty=%d Value=%d, Actual Qty=%d Value=%d\n",
						orderID, botID(orderID), totalExpQty, expValue, totalActQty, actValue)
					for _, f := range expectedList {
						fmt.Printf("   -> Expected Fill: Qty=%d Price=%d MatchedWith=%d\n", f.FilledQty, f.FilledPrice, f.MatchedWith)
					}
					for _, f := range actualList {
						fmt.Printf("   -> Actual Fill: Qty=%d Price=%d MatchedWith=%d\n", f.FilledQty, f.FilledPrice, f.MatchedWith)
					}
					if s.printedMismatches == 50 {
						fmt.Println("[Validator] Maximum mismatch logs reached (50). Suppressing further logging.")
					}
				}
			}
		}

		for orderID := range s.actualFills {
			if _, expOk := s.expectedFills[orderID]; !expOk {
				for _, f := range s.actualFills[orderID] {
					actualQty += f.FilledQty
					phantomQty += f.FilledQty
				}
			}
		}
		s.mu.Unlock()
	}

	if expectedQty == 0 {
		if actualQty > 0 || ackViolations > 0 || duplicateOrders > 0 || unknownAcks > 0 {
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
	score -= float64(ackViolations) * 2.0
	score -= float64(duplicateOrders) * 2.0
	score -= float64(unknownAcks) * 2.0

	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}

	return score
}

// GetPhantomFills returns the count of phantom fills
func (v *Validator) GetPhantomFills() int64 {
	v.mu.RLock()
	defer v.mu.RUnlock()
	var total int64
	for _, s := range v.shards {
		s.mu.Lock()
		total += s.phantomFills
		s.mu.Unlock()
	}
	return total
}

// GetPriorityViolations returns the count of priority violations
func (v *Validator) GetPriorityViolations() int64 {
	v.mu.RLock()
	defer v.mu.RUnlock()
	var total int64
	for _, s := range v.shards {
		s.mu.Lock()
		total += s.priorityViolations
		s.mu.Unlock()
	}
	return total
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

func botID(orderID int64) int64 {
	return orderID >> 32
}

func isSelfCross(incomingID, restingID int64) bool {
	return botID(incomingID) != 0 && botID(incomingID) == botID(restingID)
}