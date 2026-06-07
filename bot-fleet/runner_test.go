package main

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	protocol "iicpc-sandbox/pkg/protocol"
)

type immediateStrategy struct{}

func (immediateStrategy) Wait(context.Context) error { return nil }
func (immediateStrategy) Name() string               { return "immediate" }

func TestRunBotDoesNotCountFillFramesAsAcks(t *testing.T) {
	var received atomic.Int64

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("local socket listen failed: %v", err)
	}
	defer listener.Close()

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		for i := 1; i <= 3; i++ {
			// Read 4-byte length prefix
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
			received.Add(1)

			var order protocol.Order
			if err := proto.Unmarshal(payload, &order); err != nil {
				return
			}

			// Send Ack: ACCEPTED
			status := protocol.ExecutionStatus_ACCEPTED
			if i == 3 {
				status = protocol.ExecutionStatus_CANCELLED
			}

			ackReport := &protocol.ExecutionReport{
				OrderId:     order.OrderId,
				Status:      status,
				EngineSeqId: uint64(i),
			}
			ackBytes, err := proto.Marshal(ackReport)
			if err != nil {
				return
			}

			lengthPrefix := make([]byte, 4)
			binary.LittleEndian.PutUint32(lengthPrefix, uint32(len(ackBytes)))
			_, _ = conn.Write(lengthPrefix)
			_, _ = conn.Write(ackBytes)

			// Interleave a Fill report on order 1
			if i == 1 {
				fillReport := &protocol.ExecutionReport{
					OrderId:     order.OrderId,
					Status:      protocol.ExecutionStatus_FILLED,
					FilledQty:   1,
					FilledPrice: 100,
					MatchedWith: 1,
					EngineSeqId: 99,
				}
				fillBytes, err := proto.Marshal(fillReport)
				if err != nil {
					return
				}
				binary.LittleEndian.PutUint32(lengthPrefix, uint32(len(fillBytes)))
				_, _ = conn.Write(lengthPrefix)
				_, _ = conn.Write(fillBytes)
			}
		}
	}()

	endpoint := listener.Addr().String()
	cfg := NewBotConfig(1, "bot-1", StrategyType("PROGRESS_BASED"), 100.0, 0.10, 3, 1000.0, 42)
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
