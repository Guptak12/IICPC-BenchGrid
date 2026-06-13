package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const (
	OrderType_LIMIT  = 0
	OrderType_MARKET = 1
	OrderType_CANCEL = 2

	Side_BUY  = 0
	Side_SELL = 1

	Status_ACCEPTED  = 0
	Status_FILLED    = 1
	Status_PARTIAL   = 2
	Status_REJECTED  = 3
	Status_CANCELLED = 4
)

type WSOrder struct {
	BotID    uint64 `json:"bot_id"`
	OrderID  uint64 `json:"order_id"`
	Type     string `json:"type"` // "LIMIT", "MARKET", "CANCEL"
	Side     string `json:"side"` // "BUY", "SELL"
	Price    int64  `json:"price"`
	Quantity uint64 `json:"quantity"`
}

type Order struct {
	BotID    uint64
	OrderID  uint64
	Type     uint64
	Side     uint64
	Price    int64
	Quantity uint64
}

type WSExecutionReport struct {
	OrderID      uint64 `json:"order_id"`
	Status       string `json:"status"` // "ACCEPTED", "FILLED", "PARTIAL", "REJECTED", "CANCELLED"
	FilledQty    uint64 `json:"filled_qty"`
	FilledPrice  int64  `json:"filled_price"`
	EngineSeqID  uint64 `json:"engine_seq_id"`
	ProcessingNs uint64 `json:"processing_ns"`
	MatchedWith  uint64 `json:"matched_with"`
}

type Broker struct {
	mu          sync.Mutex
	subscribers map[chan *WSExecutionReport]bool
}

func (b *Broker) Subscribe() chan *WSExecutionReport {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan *WSExecutionReport, 10000)
	b.subscribers[ch] = true
	return ch
}

func (b *Broker) Unsubscribe(ch chan *WSExecutionReport) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.subscribers, ch)
	close(ch)
}

func (b *Broker) Publish(report *WSExecutionReport) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subscribers {
		select {
		case ch <- report:
		default:
		}
	}
}

var broker = &Broker{
	subscribers: make(map[chan *WSExecutionReport]bool),
}

type OrderBook struct {
	mu         sync.Mutex
	buyOrders  []*Order // Descending price
	sellOrders []*Order // Ascending price
	seqID      uint64
}

func statusToString(status uint64) string {
	switch status {
	case Status_ACCEPTED:
		return "ACCEPTED"
	case Status_FILLED:
		return "FILLED"
	case Status_PARTIAL:
		return "PARTIAL"
	case Status_REJECTED:
		return "REJECTED"
	case Status_CANCELLED:
		return "CANCELLED"
	default:
		return "ACCEPTED"
	}
}

func (ob *OrderBook) publishReport(orderID uint64, status uint64, filledQty uint64, filledPrice int64, seqID uint64, processingNs uint64, matchedWith uint64) {
	report := &WSExecutionReport{
		OrderID:      orderID,
		Status:       statusToString(status),
		FilledQty:    filledQty,
		FilledPrice:  filledPrice,
		EngineSeqID:  seqID,
		ProcessingNs: processingNs,
		MatchedWith:  matchedWith,
	}
	broker.Publish(report)
}

func (ob *OrderBook) handleOrder(o *Order) {
	startTime := time.Now()
	ob.mu.Lock()
	defer ob.mu.Unlock()

	if o.Type == OrderType_CANCEL {
		removed := false
		for i, ro := range ob.buyOrders {
			if ro.OrderID == o.OrderID {
				ob.buyOrders = append(ob.buyOrders[:i], ob.buyOrders[i+1:]...)
				removed = true
				break
			}
		}
		if !removed {
			for i, ro := range ob.sellOrders {
				if ro.OrderID == o.OrderID {
					ob.sellOrders = append(ob.sellOrders[:i], ob.sellOrders[i+1:]...)
					removed = true
					break
				}
			}
		}
		ob.seqID++
		elapsedNs := uint64(time.Since(startTime).Nanoseconds())
		if removed {
			ob.publishReport(o.OrderID, Status_CANCELLED, 0, 0, ob.seqID, elapsedNs, 0)
		} else {
			ob.publishReport(o.OrderID, Status_REJECTED, 0, 0, ob.seqID, elapsedNs, 0)
		}
		return
	}

	ob.seqID++
	elapsedNs := uint64(time.Since(startTime).Nanoseconds())
	ob.publishReport(o.OrderID, Status_ACCEPTED, 0, 0, ob.seqID, elapsedNs, 0)

	if o.Side == Side_BUY {
		for len(ob.sellOrders) > 0 && o.Quantity > 0 {
			bestSellIdx := -1
			for i, ro := range ob.sellOrders {
				if o.Type == OrderType_LIMIT && ro.Price > o.Price {
					break
				}
				if (ro.OrderID >> 32) == (o.OrderID >> 32) {
					continue // self-cross prevention
				}
				bestSellIdx = i
				break
			}

			if bestSellIdx == -1 {
				break
			}

			bestSell := ob.sellOrders[bestSellIdx]
			matchQty := o.Quantity
			if bestSell.Quantity < matchQty {
				matchQty = bestSell.Quantity
			}

			o.Quantity -= matchQty
			bestSell.Quantity -= matchQty

			buyStatus := Status_PARTIAL
			if o.Quantity == 0 {
				buyStatus = Status_FILLED
			}
			elapsedNs = uint64(time.Since(startTime).Nanoseconds())
			ob.seqID++
			ob.publishReport(o.OrderID, uint64(buyStatus), matchQty, bestSell.Price, ob.seqID, elapsedNs, bestSell.OrderID)

			sellStatus := Status_PARTIAL
			if bestSell.Quantity == 0 {
				sellStatus = Status_FILLED
			}
			elapsedNs = uint64(time.Since(startTime).Nanoseconds())
			ob.seqID++
			ob.publishReport(bestSell.OrderID, uint64(sellStatus), matchQty, bestSell.Price, ob.seqID, elapsedNs, o.OrderID)

			if bestSell.Quantity == 0 {
				ob.sellOrders = append(ob.sellOrders[:bestSellIdx], ob.sellOrders[bestSellIdx+1:]...)
			}
		}

		if o.Quantity > 0 && o.Type == OrderType_LIMIT {
			insertIdx := len(ob.buyOrders)
			for i, ro := range ob.buyOrders {
				if o.Price > ro.Price {
					insertIdx = i
					break
				}
			}
			ob.buyOrders = append(ob.buyOrders, nil)
			copy(ob.buyOrders[insertIdx+1:], ob.buyOrders[insertIdx:])
			ob.buyOrders[insertIdx] = o
		}
	} else {
		for len(ob.buyOrders) > 0 && o.Quantity > 0 {
			bestBuyIdx := -1
			for i, ro := range ob.buyOrders {
				if o.Type == OrderType_LIMIT && ro.Price < o.Price {
					break
				}
				if (ro.OrderID >> 32) == (o.OrderID >> 32) {
					continue // self-cross prevention
				}
				bestBuyIdx = i
				break
			}

			if bestBuyIdx == -1 {
				break
			}

			bestBuy := ob.buyOrders[bestBuyIdx]
			matchQty := o.Quantity
			if bestBuy.Quantity < matchQty {
				matchQty = bestBuy.Quantity
			}

			o.Quantity -= matchQty
			bestBuy.Quantity -= matchQty

			sellStatus := Status_PARTIAL
			if o.Quantity == 0 {
				sellStatus = Status_FILLED
			}
			elapsedNs = uint64(time.Since(startTime).Nanoseconds())
			ob.seqID++
			ob.publishReport(o.OrderID, uint64(sellStatus), matchQty, bestBuy.Price, ob.seqID, elapsedNs, bestBuy.OrderID)

			buyStatus := Status_PARTIAL
			if bestBuy.Quantity == 0 {
				buyStatus = Status_FILLED
			}
			elapsedNs = uint64(time.Since(startTime).Nanoseconds())
			ob.seqID++
			ob.publishReport(bestBuy.OrderID, uint64(buyStatus), matchQty, bestBuy.Price, ob.seqID, elapsedNs, o.OrderID)

			if bestBuy.Quantity == 0 {
				ob.buyOrders = append(ob.buyOrders[:bestBuyIdx], ob.buyOrders[bestBuyIdx+1:]...)
			}
		}

		if o.Quantity > 0 && o.Type == OrderType_LIMIT {
			insertIdx := len(ob.sellOrders)
			for i, ro := range ob.sellOrders {
				if o.Price < ro.Price {
					insertIdx = i
					break
				}
			}
			ob.sellOrders = append(ob.sellOrders, nil)
			copy(ob.sellOrders[insertIdx+1:], ob.sellOrders[insertIdx:])
			ob.sellOrders[insertIdx] = o
		}
	}
}

func main() {
	ob := &OrderBook{}

	http.HandleFunc("/api/v1/orders", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		var wso WSOrder
		if err := json.NewDecoder(r.Body).Decode(&wso); err != nil {
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}

		var typeVal uint64
		switch wso.Type {
		case "LIMIT":
			typeVal = OrderType_LIMIT
		case "MARKET":
			typeVal = OrderType_MARKET
		case "CANCEL":
			typeVal = OrderType_CANCEL
		}

		var sideVal uint64
		switch wso.Side {
		case "BUY":
			sideVal = Side_BUY
		case "SELL":
			sideVal = Side_SELL
		}

		o := &Order{
			BotID:    wso.BotID,
			OrderID:  wso.OrderID,
			Type:     typeVal,
			Side:     sideVal,
			Price:    wso.Price,
			Quantity: wso.Quantity,
		}

		// Perform matching asynchronously or synchronously?
		// To ensure correct sequence numbering and concurrency safety, run it synchronously.
		ob.handleOrder(o)

		w.WriteHeader(http.StatusAccepted)
	})

	http.HandleFunc("/api/v1/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		ch := broker.Subscribe()
		defer broker.Unsubscribe(ch)

		notify := r.Context().Done()

		for {
			select {
			case <-notify:
				return
			case report := <-ch:
				payload, err := json.Marshal(report)
				if err != nil {
					continue
				}
				_, _ = fmt.Fprintf(w, "data: %s\n\n", string(payload))
				flusher.Flush()
			}
		}
	})

	fmt.Println("REST/SSE Mock Engine listening on 0.0.0.0:8000...")
	_ = http.ListenAndServe("0.0.0.0:8000", nil)
}
