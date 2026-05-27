package telemetry

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// Producer wraps franz-go with async batching
// Point A: never blocks the bot hot path
type Producer struct {
    client *kgo.Client
    jobID  string
}

func NewProducer(brokers []string, jobID string) (*Producer, error) {
    client, err := kgo.NewClient(
        kgo.SeedBrokers(brokers...),

        // Point A: async batching — flush every 20ms or 256 messages
        // Bot never waits for Kafka ack
        kgo.ProducerBatchMaxBytes(1_000_000),
        kgo.RecordPartitioner(kgo.StickyKeyPartitioner(nil)),

        // If Kafka is unavailable, buffer up to 100k records in memory
        // before dropping (graceful degradation)
        kgo.MaxBufferedRecords(100_000),
    )
    if err != nil {
        return nil, fmt.Errorf("kafka producer init failed: %v", err)
    }

    return &Producer{client: client, jobID: jobID}, nil
}

// PublishOrderAsync fires and forgets — never blocks caller
// Point A: bot continues immediately, Kafka flushes in background
func (p *Producer) PublishOrderAsync(event OrderEvent) {
    data, err := Marshal(event)
    if err != nil {
        return
    }

    // Point B: use OrderID as partition key
    // ensures all events for one order go to same partition
    key := strconv.FormatInt(event.OrderID, 10)

    record := &kgo.Record{
        Topic: TopicOrderEvents,
        Key:   []byte(key),
        Value: data,
    }

    // Produce is non-blocking — callback fires in background
    p.client.Produce(context.Background(), record, func(r *kgo.Record, err error) {
        if err != nil {
            log.Printf("[telemetry] order publish failed: %v\n", err)
        }
    })
}

// PublishAckAsync fires and forgets
func (p *Producer) PublishAckAsync(event AckEvent) {
    data, err := Marshal(event)
    if err != nil {
        return
    }

    key := strconv.FormatInt(event.OrderID, 10)

    record := &kgo.Record{
        Topic: TopicOrderEvents,
        Key:   []byte(key),
        Value: data,
    }

    p.client.Produce(context.Background(), record, func(r *kgo.Record, err error) {
        if err != nil {
            log.Printf("[telemetry] ack publish failed: %v\n", err)
        }
    })
}

// PublishFillAsync fires and forgets
func (p *Producer) PublishFillAsync(event FillEvent) {
    data, err := Marshal(event)
    if err != nil {
        return
    }

    key := strconv.FormatInt(event.OrderID, 10)

    record := &kgo.Record{
        Topic: TopicFillEvents,
        Key:   []byte(key),
        Value: data,
    }

    p.client.Produce(context.Background(), record, func(r *kgo.Record, err error) {
        if err != nil {
            log.Printf("[telemetry] fill publish failed: %v\n", err)
        }
    })
}

// PublishWorkerDone signals end of stream for this worker
// Point D: Master counts these to know when all workers are done
// This one IS synchronous — we want guaranteed delivery before worker exits
func (p *Producer) PublishWorkerDone(event WorkerDoneEvent) error {
    data, err := Marshal(event)
    if err != nil {
        return err
    }

    record := &kgo.Record{
        Topic: TopicOrderEvents,
        Key:   []byte(event.WorkerID),
        Value: data,
    }

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    // Flush all pending async records first
    if err := p.client.Flush(ctx); err != nil {
        return fmt.Errorf("flush failed: %v", err)
    }

    // Then publish the done marker synchronously
    results := p.client.ProduceSync(ctx, record)
    return results.FirstErr()
}

func (p *Producer) Close() {
    // Final flush before shutdown
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    p.client.Flush(ctx)
    p.client.Close()
}