package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
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
	Conn     *SafeWSConn
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

type SafeWSConn struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (s *SafeWSConn) WriteReport(report *WSExecutionReport) {
	if s == nil || s.conn == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	payload, err := json.Marshal(report)
	if err != nil {
		return
	}
	_ = s.conn.Write(context.Background(), websocket.MessageText, payload)
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
			report := &WSExecutionReport{
				OrderID:      o.OrderID,
				Status:       statusToString(Status_CANCELLED),
				FilledQty:    0,
				FilledPrice:  0,
				EngineSeqID:  ob.seqID,
				ProcessingNs: elapsedNs,
				MatchedWith:  0,
			}
			o.Conn.WriteReport(report)
		} else {
			report := &WSExecutionReport{
				OrderID:      o.OrderID,
				Status:       statusToString(Status_REJECTED),
				FilledQty:    0,
				FilledPrice:  0,
				EngineSeqID:  ob.seqID,
				ProcessingNs: elapsedNs,
				MatchedWith:  0,
			}
			o.Conn.WriteReport(report)
		}
		return
	}

	ob.seqID++
	elapsedNs := uint64(time.Since(startTime).Nanoseconds())
	o.Conn.WriteReport(&WSExecutionReport{
		OrderID:      o.OrderID,
		Status:       statusToString(Status_ACCEPTED),
		FilledQty:    0,
		FilledPrice:  0,
		EngineSeqID:  ob.seqID,
		ProcessingNs: elapsedNs,
		MatchedWith:  0,
	})

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
			o.Conn.WriteReport(&WSExecutionReport{
				OrderID:      o.OrderID,
				Status:       statusToString(uint64(buyStatus)),
				FilledQty:    matchQty,
				FilledPrice:  bestSell.Price,
				EngineSeqID:  ob.seqID,
				ProcessingNs: elapsedNs,
				MatchedWith:  bestSell.OrderID,
			})

			sellStatus := Status_PARTIAL
			if bestSell.Quantity == 0 {
				sellStatus = Status_FILLED
			}
			elapsedNs = uint64(time.Since(startTime).Nanoseconds())
			ob.seqID++
			bestSell.Conn.WriteReport(&WSExecutionReport{
				OrderID:      bestSell.OrderID,
				Status:       statusToString(uint64(sellStatus)),
				FilledQty:    matchQty,
				FilledPrice:  bestSell.Price,
				EngineSeqID:  ob.seqID,
				ProcessingNs: elapsedNs,
				MatchedWith:  o.OrderID,
			})

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
			o.Conn.WriteReport(&WSExecutionReport{
				OrderID:      o.OrderID,
				Status:       statusToString(uint64(sellStatus)),
				FilledQty:    matchQty,
				FilledPrice:  bestBuy.Price,
				EngineSeqID:  ob.seqID,
				ProcessingNs: elapsedNs,
				MatchedWith:  bestBuy.OrderID,
			})

			buyStatus := Status_PARTIAL
			if bestBuy.Quantity == 0 {
				buyStatus = Status_FILLED
			}
			elapsedNs = uint64(time.Since(startTime).Nanoseconds())
			ob.seqID++
			bestBuy.Conn.WriteReport(&WSExecutionReport{
				OrderID:      bestBuy.OrderID,
				Status:       statusToString(uint64(buyStatus)),
				FilledQty:    matchQty,
				FilledPrice:  bestBuy.Price,
				EngineSeqID:  ob.seqID,
				ProcessingNs: elapsedNs,
				MatchedWith:  o.OrderID,
			})

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

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		})
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")

		safeConn := &SafeWSConn{conn: conn}
		ctx := r.Context()

		for {
			typ, payload, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if typ != websocket.MessageText {
				continue
			}

			var wso WSOrder
			if err := json.Unmarshal(payload, &wso); err != nil {
				continue
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
				Conn:     safeConn,
			}

			ob.handleOrder(o)
		}
	})

	fmt.Println("WS Mock Engine listening on 0.0.0.0:8000...")
	_ = http.ListenAndServe("0.0.0.0:8000", nil)
}
