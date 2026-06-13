package main

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"strconv"
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

type Order struct {
	BotID        uint64
	OrderID      uint64
	Type         uint64
	Side         uint64
	Price        int64
	Quantity     uint64
	Conn         net.Conn
	TargetCompID string
}

type OrderBook struct {
	mu         sync.Mutex
	buyOrders  []*Order // Descending price
	sellOrders []*Order // Ascending price
	seqID      uint64
}

func statusToFIXString(status uint64) string {
	switch status {
	case Status_ACCEPTED:
		return "0"
	case Status_PARTIAL:
		return "1"
	case Status_FILLED:
		return "2"
	case Status_CANCELLED:
		return "4"
	case Status_REJECTED:
		return "8"
	default:
		return "0"
	}
}

func writeFIXReport(conn net.Conn, targetCompID string, clOrdID uint64, status uint64, qty uint64, price int64, seqID uint64, processingNs uint64, matchedWith uint64) {
	if conn == nil {
		return
	}
	fields := map[int]string{
		8:    "FIX.4.4",
		35:   "8",
		49:   "CONTESTANT",
		56:   targetCompID,
		11:   strconv.FormatUint(clOrdID, 10),
		39:   statusToFIXString(status),
		32:   strconv.FormatUint(qty, 10),
		31:   strconv.FormatInt(price, 10),
		17:   strconv.FormatUint(seqID, 10),
		9000: strconv.FormatUint(processingNs, 10),
		9001: strconv.FormatUint(matchedWith, 10),
	}
	msg := BuildFIX(fields)
	_, _ = conn.Write(msg)
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
			writeFIXReport(o.Conn, o.TargetCompID, o.OrderID, Status_CANCELLED, 0, 0, ob.seqID, elapsedNs, 0)
		} else {
			writeFIXReport(o.Conn, o.TargetCompID, o.OrderID, Status_REJECTED, 0, 0, ob.seqID, elapsedNs, 0)
		}
		return
	}

	ob.seqID++
	elapsedNs := uint64(time.Since(startTime).Nanoseconds())
	writeFIXReport(o.Conn, o.TargetCompID, o.OrderID, Status_ACCEPTED, 0, 0, ob.seqID, elapsedNs, 0)

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
			writeFIXReport(o.Conn, o.TargetCompID, o.OrderID, uint64(buyStatus), matchQty, bestSell.Price, ob.seqID, elapsedNs, bestSell.OrderID)

			sellStatus := Status_PARTIAL
			if bestSell.Quantity == 0 {
				sellStatus = Status_FILLED
			}
			elapsedNs = uint64(time.Since(startTime).Nanoseconds())
			ob.seqID++
			writeFIXReport(bestSell.Conn, bestSell.TargetCompID, bestSell.OrderID, uint64(sellStatus), matchQty, bestSell.Price, ob.seqID, elapsedNs, o.OrderID)

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
			writeFIXReport(o.Conn, o.TargetCompID, o.OrderID, uint64(sellStatus), matchQty, bestBuy.Price, ob.seqID, elapsedNs, bestBuy.OrderID)

			buyStatus := Status_PARTIAL
			if bestBuy.Quantity == 0 {
				buyStatus = Status_FILLED
			}
			elapsedNs = uint64(time.Since(startTime).Nanoseconds())
			ob.seqID++
			writeFIXReport(bestBuy.Conn, bestBuy.TargetCompID, bestBuy.OrderID, uint64(buyStatus), matchQty, bestBuy.Price, ob.seqID, elapsedNs, o.OrderID)

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

func ParseFIX(msg []byte) map[int]string {
	fields := make(map[int]string)
	parts := bytes.Split(msg, []byte{1})
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		eqIdx := bytes.IndexByte(part, '=')
		if eqIdx == -1 {
			continue
		}
		tag, err := strconv.Atoi(string(part[:eqIdx]))
		if err != nil {
			continue
		}
		fields[tag] = string(part[eqIdx+1:])
	}
	return fields
}

func BuildFIX(fields map[int]string) []byte {
	var bodyBuf bytes.Buffer
	bodyBuf.WriteString(fmt.Sprintf("35=%s\x01", fields[35]))
	for tag, val := range fields {
		if tag == 8 || tag == 9 || tag == 35 || tag == 10 {
			continue
		}
		bodyBuf.WriteString(fmt.Sprintf("%d=%s\x01", tag, val))
	}
	body := bodyBuf.Bytes()

	var buf bytes.Buffer
	buf.WriteString(fmt.Sprintf("8=%s\x01", fields[8]))
	buf.WriteString(fmt.Sprintf("9=%d\x01", len(body)))
	buf.Write(body)

	checksum := CalculateFIXChecksum(buf.Bytes())
	buf.WriteString(fmt.Sprintf("10=%03d\x01", checksum))
	return buf.Bytes()
}

func CalculateFIXChecksum(data []byte) int {
	sum := 0
	for _, b := range data {
		sum += int(b)
	}
	return sum % 256
}

func handleConnection(conn net.Conn, ob *OrderBook) {
	defer conn.Close()
	reader := bufio.NewReader(conn)

	var targetCompID string

	for {
		respBytes, err := reader.ReadBytes(1)
		if err != nil {
			return
		}
		for {
			line, err := reader.ReadBytes(1)
			if err != nil {
				return
			}
			respBytes = append(respBytes, line...)
			if bytes.Contains(respBytes, []byte("\x0110=")) && bytes.HasSuffix(respBytes, []byte("\x01")) {
				break
			}
		}

		tags := ParseFIX(respBytes)
		msgType := tags[35]

		if msgType == "A" {
			targetCompID = tags[49]
			logonFields := map[int]string{
				8:   "FIX.4.4",
				35:  "A",
				49:  "CONTESTANT",
				56:  targetCompID,
				34:  "1",
				98:  "0",
				108: "30",
			}
			_, err = conn.Write(BuildFIX(logonFields))
			if err != nil {
				return
			}
			continue
		}

		if msgType == "D" {
			orderID, _ := strconv.ParseUint(tags[11], 10, 64)
			sideStr := tags[54]
			qty, _ := strconv.ParseUint(tags[38], 10, 64)
			price, _ := strconv.ParseInt(tags[44], 10, 64)
			typeStr := tags[40]
			botID, _ := strconv.ParseUint(tags[1], 10, 64)

			var sideVal uint64
			if sideStr == "1" {
				sideVal = Side_BUY
			} else {
				sideVal = Side_SELL
			}

			var typeVal uint64
			if typeStr == "1" {
				typeVal = OrderType_MARKET
			} else {
				typeVal = OrderType_LIMIT
			}

			o := &Order{
				BotID:        botID,
				OrderID:      orderID,
				Type:         typeVal,
				Side:         sideVal,
				Price:        price,
				Quantity:     qty,
				Conn:         conn,
				TargetCompID: targetCompID,
			}
			ob.handleOrder(o)
		} else if msgType == "F" {
			orderID, _ := strconv.ParseUint(tags[41], 10, 64)
			botID, _ := strconv.ParseUint(tags[1], 10, 64)

			o := &Order{
				BotID:        botID,
				OrderID:      orderID,
				Type:         OrderType_CANCEL,
				Conn:         conn,
				TargetCompID: targetCompID,
			}
			ob.handleOrder(o)
		}
	}
}

func main() {
	l, err := net.Listen("tcp", "0.0.0.0:8000")
	if err != nil {
		fmt.Printf("Listen failed: %v\n", err)
		return
	}
	defer l.Close()
	fmt.Println("FIX Mock Engine listening on 0.0.0.0:8000...")

	ob := &OrderBook{}

	for {
		conn, err := l.Accept()
		if err != nil {
			continue
		}
		go handleConnection(conn, ob)
	}
}
