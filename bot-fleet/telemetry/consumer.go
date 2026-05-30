package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"
	"sort"
	hdr "github.com/HdrHistogram/hdrhistogram-go"
	"github.com/guptak12/bot-fleet/shadow"
	"github.com/twmb/franz-go/pkg/kgo"
)

const (
    // If the buffer holds this many events ahead of the expected seq ID,
    // assume the missing event was dropped by the contestant's engine.
    // Advancing avoids unbounded memory growth and infinite quiet-period loops.
    jitterGapThreshold = 100
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
	client     *kgo.Client
	jobID      string
	numWorkers int

	mu          sync.Mutex // Point C: keep lock scope minimal
	hist        *hdr.Histogram
	orderCount  int64
	fillCount   int64
	workersDone int
	validator   *shadow.Validator

	nextSeqID    int64
	jitterBuffer map[int64]interface{}
}

func NewConsumer(brokers []string, jobID string, numWorkers int) (*Consumer, error) {
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumeTopics(TopicOrderEvents),
		// Fresh consumer group per job — always reads from latest
		kgo.ConsumerGroup(fmt.Sprintf("master-%s", jobID)),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtEnd()),
	)
	if err != nil {
		return nil, fmt.Errorf("kafka consumer init failed: %v", err)
	}

	return &Consumer{
		client:       client,
		jobID:        jobID,
		numWorkers:   numWorkers,
		hist:         hdr.New(1, 3_600_000_000_000, 3),
		validator:    shadow.NewValidator(),
		nextSeqID:    1, // C++ Engine sequence starts at 1
		jitterBuffer: make(map[int64]interface{}),
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
			c.flushRemainingBuffer() // drain anything stuck behinf a gap
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
		if err := json.Unmarshal(r.Value, &event); err == nil {
			c.mu.Lock()
			c.validator.ProcessOrder(event.OrderID, event.OrderType, event.Side, event.Price, event.Quantity)
			c.mu.Unlock()
		}

	case EventOrderAck:
		var event AckEvent
		if err := json.Unmarshal(r.Value, &event); err == nil {
			c.mu.Lock()
			c.jitterBuffer[event.EngineSeqID] = event
			c.drainJitterBuffer()
			c.mu.Unlock()
		}

	case EventFill:
		var event FillEvent
		if err := json.Unmarshal(r.Value, &event); err == nil {
			c.mu.Lock()
			c.jitterBuffer[event.EngineSeqID] = event
			c.drainJitterBuffer()
			c.mu.Unlock()
		}

	case EventWorkerDone:
		c.mu.Lock()
		c.workersDone++
		c.mu.Unlock()
	}
}

// drainJitterBuffer ensures the Validator only processes events in the EXACT
// sequence the C++ engine handled them, regardless of Kafka partition lag.
func (c *Consumer) drainJitterBuffer() {
	for {
		event, ok := c.jitterBuffer[c.nextSeqID]
		if !ok {
			 // Gap detected — check if buffer has grown past the threshold.
            // If so, the missing seq ID was almost certainly dropped, not delayed.
            // Advance past it so we don't stall forever.
            if len(c.jitterBuffer) >= jitterGapThreshold {
                log.Printf("[telemetry] seq gap at %d (buffer=%d) — advancing past dropped event\n",
                    c.nextSeqID, len(c.jitterBuffer))
                c.nextSeqID++
                continue // try the next ID immediately
            }
            break // buffer is small — event is just delayed, wait for it
		}
		delete(c.jitterBuffer, c.nextSeqID)

		switch e := event.(type) {
		case AckEvent:
			c.hist.RecordValue(e.LatencyNs)
			c.orderCount++
			c.validator.ProcessAck(e.OrderID, e.Status) // Validator runs matching here!
		case FillEvent:
			c.fillCount++
			c.validator.ProcessFill(e.OrderID, e.FilledQty, e.FilledPrice, e.MatchedWith)
		}

		c.nextSeqID++
	}
}

func (c *Consumer) Close() {
	c.client.Close()
}

// flushRemainingBuffer processes whatever is left in the jitter buffer after
// the quiet period fires. Events are processed in sequence order.
// Any remaining gaps are skipped — the missing events count against correctness.
func (c *Consumer) flushRemainingBuffer() {
    c.mu.Lock()
    defer c.mu.Unlock()

    if len(c.jitterBuffer) == 0 {
        return
    }

    log.Printf("[telemetry] flushing %d remaining buffered events on quiet period\n",
        len(c.jitterBuffer))

    // Sort remaining seq IDs — process in order so validator sees
    // events as close to engine-sequence order as possible
    seqIDs := make([]int64, 0, len(c.jitterBuffer))
    for id := range c.jitterBuffer {
        seqIDs = append(seqIDs, id)
    }
    sort.Slice(seqIDs, func(i, j int) bool { return seqIDs[i] < seqIDs[j] })

    for _, id := range seqIDs {
        event := c.jitterBuffer[id]
        delete(c.jitterBuffer, id)
        switch e := event.(type) {
        case AckEvent:
            c.hist.RecordValue(e.LatencyNs)
            c.orderCount++
            c.validator.ProcessAck(e.OrderID, e.Status)
        case FillEvent:
            c.fillCount++
            c.validator.ProcessFill(e.OrderID, e.FilledQty, e.FilledPrice, e.MatchedWith)
        }
    }
}