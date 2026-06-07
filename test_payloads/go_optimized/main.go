package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// Protobuf constants
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

type Order struct {
	BotID    uint64
	OrderID  uint64
	Type     uint64
	Side     uint64
	Price    int64
	Quantity uint64
	Conn     net.Conn // connection that submitted the order
}

type OrderBook struct {
	mu         sync.Mutex
	buyOrders  []*Order // Descending price
	sellOrders []*Order // Ascending price
	seqID      uint64
}

func decodeVarint(data []byte, idx *int) uint64 {
	var result uint64
	var shift int
	for *idx < len(data) {
		b := data[*idx]
		*idx++
		result |= uint64(b&0x7F) << shift
		if b&0x80 == 0 {
			return result
		}
		shift += 7
	}
	return result
}

func encodeVarint(val uint64) []byte {
	var res []byte
	for {
		towrite := byte(val & 0x7F)
		val >>= 7
		if val > 0 {
			res = append(res, towrite|0x80)
		} else {
			res = append(res, towrite)
			break
		}
	}
	return res
}

func decodeOrder(data []byte) (*Order, error) {
	o := &Order{}
	idx := 0
	for idx < len(data) {
		key := data[idx]
		idx++
		wireType := key & 0x7
		fieldNum := key >> 3
		if wireType == 0 {
			val := decodeVarint(data, &idx)
			switch fieldNum {
			case 1:
				o.BotID = val
			case 2:
				o.OrderID = val
			case 3:
				o.Type = val
			case 4:
				o.Side = val
			case 5:
				// zigzag or direct cast
				o.Price = int64(val)
			case 6:
				o.Quantity = val
			}
		} else {
			// Skip other wire types
			return nil, fmt.Errorf("unsupported wire type")
		}
	}
	return o, nil
}

func encodeReport(orderID uint64, status uint64, filledQty uint64, filledPrice int64, seqID uint64, processingNs uint64, matchedWith uint64) []byte {
	var payload []byte
	// uint64 order_id = 1 -> tag 1 << 3 | 0 = 0x08
	payload = append(payload, 0x08)
	payload = append(payload, encodeVarint(orderID)...)

	// ExecutionStatus status = 2 -> tag 2 << 3 | 0 = 0x10
	payload = append(payload, 0x10)
	payload = append(payload, encodeVarint(status)...)

	// uint64 filled_qty = 3 -> tag 3 << 3 | 0 = 0x18
	payload = append(payload, 0x18)
	payload = append(payload, encodeVarint(filledQty)...)

	// int64 filled_price = 4 -> tag 4 << 3 | 0 = 0x20
	payload = append(payload, 0x20)
	payload = append(payload, encodeVarint(uint64(filledPrice))...)

	// uint64 engine_seq_id = 5 -> tag 5 << 3 | 0 = 0x28
	payload = append(payload, 0x28)
	payload = append(payload, encodeVarint(seqID)...)

	// uint64 processing_ns = 6 -> tag 6 << 3 | 0 = 0x30
	payload = append(payload, 0x30)
	payload = append(payload, encodeVarint(processingNs)...)

	// uint64 matched_with = 7 -> tag 7 << 3 | 0 = 0x38
	payload = append(payload, 0x38)
	payload = append(payload, encodeVarint(matchedWith)...)

	return payload
}

func writeReport(conn net.Conn, orderID uint64, status uint64, filledQty uint64, filledPrice int64, seqID uint64, processingNs uint64, matchedWith uint64) {
	if conn == nil {
		return
	}
	payload := encodeReport(orderID, status, filledQty, filledPrice, seqID, processingNs, matchedWith)
	buf := make([]byte, 4+len(payload))
	binary.LittleEndian.PutUint32(buf[:4], uint32(len(payload)))
	copy(buf[4:], payload)
	_, _ = conn.Write(buf)
}

func (ob *OrderBook) handleOrder(o *Order) {
	startTime := time.Now()
	ob.mu.Lock()
	defer ob.mu.Unlock()

	if o.Type == OrderType_CANCEL {
		// Cancel order logic
		removed := false
		// Search buy book
		for i, ro := range ob.buyOrders {
			if ro.OrderID == o.OrderID {
				ob.buyOrders = append(ob.buyOrders[:i], ob.buyOrders[i+1:]...)
				removed = true
				break
			}
		}
		if !removed {
			// Search sell book
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
			writeReport(o.Conn, o.OrderID, Status_CANCELLED, 0, 0, ob.seqID, elapsedNs, 0)
		} else {
			writeReport(o.Conn, o.OrderID, Status_REJECTED, 0, 0, ob.seqID, elapsedNs, 0)
		}
		return
	}

	// Ack standard limit/market order
	ob.seqID++
	elapsedNs := uint64(time.Since(startTime).Nanoseconds())
	writeReport(o.Conn, o.OrderID, Status_ACCEPTED, 0, 0, ob.seqID, elapsedNs, 0)

	// Matching execution logic
	if o.Side == Side_BUY {
		// Match against sells
		for len(ob.sellOrders) > 0 && o.Quantity > 0 {
			bestSellIdx := -1
			// Find first best sell that is not from the same bot (Self-Crossing prevention)
			for i, ro := range ob.sellOrders {
				if o.Type == OrderType_LIMIT && ro.Price > o.Price {
					break
				}
				if (ro.OrderID >> 32) == (o.OrderID >> 32) {
					continue // self-cross: skip
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

			// Report fill on incoming BUY order
			buyStatus := Status_PARTIAL
			if o.Quantity == 0 {
				buyStatus = Status_FILLED
			}
			elapsedNs = uint64(time.Since(startTime).Nanoseconds())
			ob.seqID++
			writeReport(o.Conn, o.OrderID, uint64(buyStatus), matchQty, bestSell.Price, ob.seqID, elapsedNs, bestSell.OrderID)

			// Report fill on resting SELL order
			sellStatus := Status_PARTIAL
			if bestSell.Quantity == 0 {
				sellStatus = Status_FILLED
			}
			elapsedNs = uint64(time.Since(startTime).Nanoseconds())
			ob.seqID++
			writeReport(bestSell.Conn, bestSell.OrderID, uint64(sellStatus), matchQty, bestSell.Price, ob.seqID, elapsedNs, o.OrderID)

			if bestSell.Quantity == 0 {
				ob.sellOrders = append(ob.sellOrders[:bestSellIdx], ob.sellOrders[bestSellIdx+1:]...)
			}
		}

		// Insert remaining limit quantity into book
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
		// Match against buys
		for len(ob.buyOrders) > 0 && o.Quantity > 0 {
			bestBuyIdx := -1
			for i, ro := range ob.buyOrders {
				if o.Type == OrderType_LIMIT && ro.Price < o.Price {
					break
				}
				if (ro.OrderID >> 32) == (o.OrderID >> 32) {
					continue // self-cross: skip
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

			// Report fill on incoming SELL order
			sellStatus := Status_PARTIAL
			if o.Quantity == 0 {
				sellStatus = Status_FILLED
			}
			elapsedNs = uint64(time.Since(startTime).Nanoseconds())
			ob.seqID++
			writeReport(o.Conn, o.OrderID, uint64(sellStatus), matchQty, bestBuy.Price, ob.seqID, elapsedNs, bestBuy.OrderID)

			// Report fill on resting BUY order
			buyStatus := Status_PARTIAL
			if bestBuy.Quantity == 0 {
				buyStatus = Status_FILLED
			}
			elapsedNs = uint64(time.Since(startTime).Nanoseconds())
			ob.seqID++
			writeReport(bestBuy.Conn, bestBuy.OrderID, uint64(buyStatus), matchQty, bestBuy.Price, ob.seqID, elapsedNs, o.OrderID)

			if bestBuy.Quantity == 0 {
				ob.buyOrders = append(ob.buyOrders[:bestBuyIdx], ob.buyOrders[bestBuyIdx+1:]...)
			}
		}

		// Insert remaining limit quantity
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

func handleConnection(conn net.Conn, ob *OrderBook) {
	defer conn.Close()
	for {
		var length uint32
		err := binary.Read(conn, binary.LittleEndian, &length)
		if err != nil {
			return
		}

		payload := make([]byte, length)
		_, err = io.ReadFull(conn, payload)
		if err != nil {
			return
		}

		o, err := decodeOrder(payload)
		if err != nil {
			continue
		}
		o.Conn = conn

		ob.handleOrder(o)
	}
}

func main() {
	l, err := net.Listen("tcp", "0.0.0.0:8000")
	if err != nil {
		fmt.Printf("Listen failed: %v\n", err)
		return
	}
	defer l.Close()
	fmt.Println("Go Optimized Engine listening on port 8000...")

	ob := &OrderBook{}

	for {
		conn, err := l.Accept()
		if err != nil {
			continue
		}
		go handleConnection(conn, ob)
	}
}
