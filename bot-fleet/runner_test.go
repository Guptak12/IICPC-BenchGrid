package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type immediateStrategy struct{}

func (immediateStrategy) Wait(context.Context) error { return nil }
func (immediateStrategy) Name() string               { return "immediate" }

func TestRunBotDoesNotCountFillFramesAsAcks(t *testing.T) {
	var received atomic.Int64

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local socket listen unavailable: %v", err)
	}

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{"trading"},
		})
		if err != nil {
			t.Errorf("accept failed: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")

		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		for i := 1; i <= 3; i++ {
			_, payload, err := conn.Read(ctx)
			if err != nil {
				t.Errorf("read order %d failed: %v", i, err)
				return
			}
			received.Add(1)

			var msg OrderMessage
			if err := json.Unmarshal(payload, &msg); err != nil {
				t.Errorf("unmarshal order %d failed: %v", i, err)
				return
			}

			status := "accepted"
			if i == 3 {
				status = "cancelled"
			}
			ack := fmt.Sprintf(`{"order_id":%d,"status":"%s","engine_seq_id":%d}`, msg.OrderID, status, i)
			if err := conn.Write(ctx, websocket.MessageText, []byte(ack)); err != nil {
				t.Errorf("write ack %d failed: %v", i, err)
				return
			}

			if i == 1 {
				fill := `{"order_id":999999,"status":"filled","filled_qty":1,"filled_price":100,"matched_with":1,"engine_seq_id":99}`
				if err := conn.Write(ctx, websocket.MessageText, []byte(fill)); err != nil {
					t.Errorf("write interleaved fill failed: %v", err)
					return
				}
			}
		}
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()

	endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	cfg := NewBotConfig(1, "bot-1", MarketMaker, 100.0, 0.10, 3, 1000.0, 42)
	var totalSent atomic.Int64

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	result := runBot(ctx, NewBot(cfg), endpoint, immediateStrategy{}, &totalSent, nil, "job-1", "worker-1")
	if result.OrdersSent != 2 {
		t.Fatalf("expected 2 acked orders, got %d", result.OrdersSent)
	}
	if result.OrdersFailed != 0 {
		t.Fatalf("expected 0 failed orders, got %d", result.OrdersFailed)
	}
	if totalSent.Load() != 2 {
		t.Fatalf("expected totalSent 2, got %d", totalSent.Load())
	}
	if received.Load() != 3 {
		t.Fatalf("expected server to receive 3 orders, got %d", received.Load())
	}
}
