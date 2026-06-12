package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
	"iicpc-sandbox/pkg/protocol"
)

// Helper: parse raw FIX bytes in test
func parseTestFIX(msg []byte) map[int]string {
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
		if err == nil {
			fields[tag] = string(part[eqIdx+1:])
		}
	}
	return fields
}

func TestTCPProtobufAdapter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 1. Start Mock TCP/Protobuf Server
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer listener.Close()

	orderChan := make(chan *protocol.Order, 10)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		// Read Length Prefixed Protobuf Order
		var length uint32
		if err := binary.Read(conn, binary.LittleEndian, &length); err != nil {
			return
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(conn, payload); err != nil {
			return
		}

		var order protocol.Order
		if err := proto.Unmarshal(payload, &order); err == nil {
			orderChan <- &order
		}

		// Write Length Prefixed Execution Report (Fill)
		report := &protocol.ExecutionReport{
			OrderId:      42,
			Status:       protocol.ExecutionStatus_FILLED,
			FilledQty:    100,
			FilledPrice:  10000,
			EngineSeqId:  1,
			ProcessingNs: 150,
		}
		repBytes, _ := proto.Marshal(report)
		var repLength = uint32(len(repBytes))
		_ = binary.Write(conn, binary.LittleEndian, repLength)
		_, _ = conn.Write(repBytes)
	}()

	// 2. Instantiate and test TCP_PROTOBUF adapter
	adapter := &TCPProtobufAdapter{}
	err = adapter.Init(ctx, listener.Addr().String(), 1)
	if err != nil {
		t.Fatalf("adapter.Init failed: %v", err)
	}
	defer adapter.Close()

	testOrder := &protocol.Order{
		BotId:    1,
		OrderId:  42,
		Type:     protocol.OrderType_LIMIT,
		Side:     protocol.Side_BUY,
		Price:    10000,
		Quantity: 100,
	}

	err = adapter.SendOrder(ctx, testOrder)
	if err != nil {
		t.Fatalf("adapter.SendOrder failed: %v", err)
	}

	// Verify server got order
	select {
	case received := <-orderChan:
		if received.OrderId != 42 {
			t.Errorf("expected OrderId 42, got %d", received.OrderId)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout waiting for server to receive order")
	}

	// Verify receiver gets report
	eventChan := make(chan PretestEvent, 10)
	err = adapter.StartReceiver(ctx, eventChan)
	if err != nil {
		t.Fatalf("adapter.StartReceiver failed: %v", err)
	}

	select {
	case ev := <-eventChan:
		if ev.Report.OrderId != 42 || ev.Report.Status != protocol.ExecutionStatus_FILLED {
			t.Errorf("unexpected report: %+v", ev.Report)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout waiting for receiver to get fill report")
	}
}

func TestWebSocketAdapter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 1. Start Mock WebSocket Server
	orderChan := make(chan map[string]interface{}, 10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusInternalError, "closing")

		// Read order frame
		_, payload, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		var orderMap map[string]interface{}
		if err := json.Unmarshal(payload, &orderMap); err == nil {
			orderChan <- orderMap
		}

		// Write fill report frame
		reportMap := map[string]interface{}{
			"order_id":      99,
			"status":        "FILLED",
			"filled_qty":    500,
			"filled_price":  25000,
			"engine_seq_id": 2,
			"processing_ns": 200,
			"matched_with":  101,
		}
		repBytes, _ := json.Marshal(reportMap)
		_ = conn.Write(r.Context(), websocket.MessageText, repBytes)
	}))
	defer server.Close()

	// Convert http URL to ws URL
	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)

	// 2. Instantiate and test WS adapter
	adapter := &WebSocketAdapter{}
	err := adapter.Init(ctx, wsURL, 2)
	if err != nil {
		t.Fatalf("adapter.Init failed: %v", err)
	}
	defer adapter.Close()

	testOrder := &protocol.Order{
		BotId:    2,
		OrderId:  99,
		Type:     protocol.OrderType_LIMIT,
		Side:     protocol.Side_SELL,
		Price:    25000,
		Quantity: 500,
	}

	err = adapter.SendOrder(ctx, testOrder)
	if err != nil {
		t.Fatalf("adapter.SendOrder failed: %v", err)
	}

	// Verify server got order
	select {
	case received := <-orderChan:
		if id, ok := received["order_id"].(float64); !ok || id != 99 {
			t.Errorf("expected order_id 99, got %v", received["order_id"])
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout waiting for WS server to receive order")
	}

	// Verify receiver gets report
	eventChan := make(chan PretestEvent, 10)
	err = adapter.StartReceiver(ctx, eventChan)
	if err != nil {
		t.Fatalf("adapter.StartReceiver failed: %v", err)
	}

	select {
	case ev := <-eventChan:
		if ev.Report.OrderId != 99 || ev.Report.Status != protocol.ExecutionStatus_FILLED || ev.Report.MatchedWith != 101 {
			t.Errorf("unexpected report: %+v", ev.Report)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout waiting for WS receiver to get report")
	}
}

func TestRESTAdapter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	orderChan := make(chan map[string]interface{}, 10)
	sseCloseChan := make(chan struct{})

	// 1. Start Mock HTTP Server (POST /api/v1/orders & GET /api/v1/events)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/v1/orders" {
			body, _ := io.ReadAll(r.Body)
			var orderMap map[string]interface{}
			if err := json.Unmarshal(body, &orderMap); err == nil {
				orderChan <- orderMap
			}
			w.WriteHeader(http.StatusAccepted)
			return
		}

		if r.Method == "GET" && r.URL.Path == "/api/v1/events" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
				return
			}

			// Send mock SSE execution report event
			reportMap := map[string]interface{}{
				"order_id":      888,
				"status":        "ACCEPTED",
				"filled_qty":    0,
				"filled_price":  0,
				"engine_seq_id": 3,
				"processing_ns": 50,
			}
			data, _ := json.Marshal(reportMap)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", string(data))
			flusher.Flush()

			// Block until context or test done
			select {
			case <-r.Context().Done():
			case <-sseCloseChan:
			}
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	defer close(sseCloseChan)

	// Strip http:// prefix to pass clean endpoint (since adapter handles scheme)
	endpoint := strings.TrimPrefix(server.URL, "http://")

	// 2. Instantiate and test REST adapter
	adapter := &RESTAdapter{}
	err := adapter.Init(ctx, endpoint, 3)
	if err != nil {
		t.Fatalf("adapter.Init failed: %v", err)
	}
	defer adapter.Close()

	testOrder := &protocol.Order{
		BotId:    3,
		OrderId:  888,
		Type:     protocol.OrderType_LIMIT,
		Side:     protocol.Side_BUY,
		Price:    15000,
		Quantity: 200,
	}

	err = adapter.SendOrder(ctx, testOrder)
	if err != nil {
		t.Fatalf("adapter.SendOrder failed: %v", err)
	}

	// Verify server got order via HTTP POST
	select {
	case received := <-orderChan:
		if id, ok := received["order_id"].(float64); !ok || id != 888 {
			t.Errorf("expected order_id 888, got %v", received["order_id"])
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout waiting for REST server to receive order via POST")
	}

	// Verify receiver gets report via SSE GET
	eventChan := make(chan PretestEvent, 10)
	err = adapter.StartReceiver(ctx, eventChan)
	if err != nil {
		t.Fatalf("adapter.StartReceiver failed: %v", err)
	}

	select {
	case ev := <-eventChan:
		if ev.Report.OrderId != 888 || ev.Report.Status != protocol.ExecutionStatus_ACCEPTED {
			t.Errorf("unexpected report: %+v", ev.Report)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout waiting for REST receiver to get report via SSE")
	}
}

func TestFIXAdapter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 1. Start Mock FIX TCP Server
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer listener.Close()

	orderChan := make(chan map[int]string, 10)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)
		// 1. Handle Logon message
		var logonBytes []byte
		for {
			b, err := reader.ReadByte()
			if err != nil {
				return
			}
			logonBytes = append(logonBytes, b)
			if bytes.Contains(logonBytes, []byte("\x0110=")) && bytes.HasSuffix(logonBytes, []byte("\x01")) {
				break
			}
		}
		logonTags := parseTestFIX(logonBytes)
		if logonTags[35] != "A" {
			return
		}

		// Reply with Logon (A) confirmation
		logonConfirm := map[int]string{
			8:   "FIX.4.4",
			35:  "A",
			49:  "CONTESTANT",
			56:  "CLIENT",
			34:  "1",
			98:  "0",
			108: "30",
		}
		_, _ = conn.Write(BuildFIX(logonConfirm))

		// 2. Read NewOrderSingle (D)
		var orderBytes []byte
		for {
			b, err := reader.ReadByte()
			if err != nil {
				return
			}
			orderBytes = append(orderBytes, b)
			if bytes.Contains(orderBytes, []byte("\x0110=")) && bytes.HasSuffix(orderBytes, []byte("\x01")) {
				break
			}
		}

		orderTags := parseTestFIX(orderBytes)
		orderChan <- orderTags

		// Write ExecutionReport (8) containing custom Tags 9000 & 9001
		execReport := map[int]string{
			8:    "FIX.4.4",
			35:   "8",
			49:   "CONTESTANT",
			56:   "CLIENT",
			34:   "2",
			11:   orderTags[11], // ClOrdID
			17:   "1001",        // ExecID
			150:  "2",           // ExecType = Fill
			39:   "2",           // OrdStatus = Fill
			38:   orderTags[38], // Quantity
			32:   orderTags[38], // LastQty
			31:   orderTags[44], // Price
			9000: "250",         // Custom: ProcessingNs = 250ns
			9001: "99999",       // Custom: MatchedWith = 99999
		}
		_, _ = conn.Write(BuildFIX(execReport))
	}()

	// 2. Instantiate and test FIX adapter
	adapter := &FIXAdapter{}
	err = adapter.Init(ctx, listener.Addr().String(), 4)
	if err != nil {
		t.Fatalf("adapter.Init failed: %v", err)
	}
	defer adapter.Close()

	testOrder := &protocol.Order{
		BotId:    4,
		OrderId:  777,
		Type:     protocol.OrderType_LIMIT,
		Side:     protocol.Side_BUY,
		Price:    12000,
		Quantity: 300,
	}

	err = adapter.SendOrder(ctx, testOrder)
	if err != nil {
		t.Fatalf("adapter.SendOrder failed: %v", err)
	}

	// Verify server got FIX order
	select {
	case received := <-orderChan:
		if received[11] != "777" || received[54] != "1" {
			t.Errorf("unexpected FIX fields: %+v", received)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout waiting for FIX server to receive order via TCP socket")
	}

	// Verify receiver gets report and maps custom tags 9000/9001 correctly
	eventChan := make(chan PretestEvent, 10)
	err = adapter.StartReceiver(ctx, eventChan)
	if err != nil {
		t.Fatalf("adapter.StartReceiver failed: %v", err)
	}

	select {
	case ev := <-eventChan:
		if ev.Report.OrderId != 777 || ev.Report.Status != protocol.ExecutionStatus_FILLED {
			t.Errorf("unexpected report mapping: %+v", ev.Report)
		}
		if ev.Report.ProcessingNs != 250 || ev.Report.MatchedWith != 99999 {
			t.Errorf("failed to map custom tags: processing=%d, matchedWith=%d", ev.Report.ProcessingNs, ev.Report.MatchedWith)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout waiting for FIX receiver to get report")
	}
}
