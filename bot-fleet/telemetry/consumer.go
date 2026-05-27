package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	hdr "github.com/HdrHistogram/hdrhistogram-go"
	"github.com/guptak12/bot-fleet/shadow"
	"github.com/twmb/franz-go/pkg/kgo"
)

// TelemetryResult is what the consumer produces after a job completes
type TelemetryResult struct {
    OrdersProcessed int64
    FillsProcessed  int64
    Histogram       *hdr.Histogram
    WorkersDone     int
    Correctness     float64
}

// Consumer reads from Kafka and aggregates telemetry for one job
type Consumer struct {
    client      *kgo.Client
    jobID       string
    numWorkers  int

    mu          sync.Mutex             // Point C: keep lock scope minimal
    hist        *hdr.Histogram
    orderCount  int64
    fillCount   int64
    workersDone int
    validator   *shadow.Validator
}

func NewConsumer(brokers []string, jobID string, numWorkers int) (*Consumer, error) {
    client, err := kgo.NewClient(
        kgo.SeedBrokers(brokers...),
        kgo.ConsumeTopics(TopicOrderEvents, TopicFillEvents),
        // Fresh consumer group per job — always reads from latest
        kgo.ConsumerGroup(fmt.Sprintf("master-%s", jobID)),
        kgo.ConsumeResetOffset(kgo.NewOffset().AtEnd()),
    )
    if err != nil {
        return nil, fmt.Errorf("kafka consumer init failed: %v", err)
    }

    return &Consumer{
        client:     client,
        jobID:      jobID,
        numWorkers: numWorkers,
        hist:       hdr.New(1, 3_600_000_000_000, 3),
        validator:  shadow.NewValidator(),
    }, nil
}

// Consume reads events until all workers report done + quiet period
// Point D: quiet period = 3s after last worker done with no new messages
func (c *Consumer) Consume(ctx context.Context) (*TelemetryResult, error) {
    lastMessage := time.Now()
    quietPeriod := 3 * time.Second

    for {
        // Check quiet period after all workers done
        c.mu.Lock()
        allDone := c.workersDone >= c.numWorkers
        c.mu.Unlock()

        if allDone && time.Since(lastMessage) > quietPeriod {
            log.Printf("[telemetry] quiet period elapsed — finalising\n")
            break
        }

        // Poll with 500ms timeout so we can check quiet period regularly
        pollCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
        fetches := c.client.PollFetches(pollCtx)
        cancel()

        if fetches.IsClientClosed() {
            break
        }

        fetches.EachError(func(t string, p int32, err error) {
            log.Printf("[telemetry] fetch error topic=%s partition=%d: %v\n", t, p, err)
        })

        fetches.EachRecord(func(r *kgo.Record) {
            lastMessage = time.Now()
            c.processRecord(r)
        })
    }

    c.mu.Lock()
    defer c.mu.Unlock()

    return &TelemetryResult{
        OrdersProcessed: c.orderCount,
        FillsProcessed:  c.fillCount,
        Histogram:       c.hist,
        WorkersDone:     c.workersDone,
        Correctness:     c.validator.GetCorrectnessScore(),
    }, nil
}

func (c *Consumer) processRecord(r *kgo.Record) {
    // Peek at type field first
    var envelope struct {
        Type  EventType `json:"type"`
        JobID string    `json:"job_id"`
    }
    if err := json.Unmarshal(r.Value, &envelope); err != nil {
        return
    }

    // Only process events for this job
    if envelope.JobID != c.jobID {
        return
    }

    // Point C: lock only for the state mutation, not for unmarshalling
    switch envelope.Type {
    case EventOrderSent:
        var event OrderEvent
        if err := json.Unmarshal(r.Value, &event); err != nil {
            return
        }
        c.mu.Lock()
        c.validator.ProcessOrder(event.OrderID, event.OrderType, event.Side, event.Price, event.Quantity)
        c.mu.Unlock()
        
    case EventOrderAck:
        var event AckEvent
        if err := json.Unmarshal(r.Value, &event); err != nil {
            return
        }
        c.mu.Lock()
        c.hist.RecordValue(event.LatencyNs)
        c.orderCount++
        c.validator.ProcessAck(event.OrderID, event.Status)
        c.mu.Unlock()

    case EventFill:
        var event FillEvent
        if err := json.Unmarshal(r.Value, &event); err != nil {
            return
        }
        c.mu.Lock()
        c.fillCount++
        c.validator.ProcessFill(event.OrderID, event.FilledQty, event.FilledPrice)
        c.mu.Unlock()

    case EventWorkerDone:
        c.mu.Lock()
        c.workersDone++
        log.Printf("[telemetry] worker done (%d/%d)\n", c.workersDone, c.numWorkers)
        c.mu.Unlock()
    }
}

func (c *Consumer) Close() {
    c.client.Close()
}