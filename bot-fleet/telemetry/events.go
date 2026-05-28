package telemetry

import "encoding/json"

// Topic names
const (
    TopicOrderEvents = "order-events"
    TopicFillEvents  = "fill-events"
)

// EventType distinguishes message types on the wire
type EventType string

const (
    EventOrderSent EventType = "ORDER_SENT"
    EventOrderAck  EventType = "ORDER_ACK"
    EventFill      EventType = "FILL"
    EventWorkerDone EventType = "WORKER_DONE" // Point D: signals end of stream
)

// OrderEvent published by worker when a bot sends an order
type OrderEvent struct {
    Type       EventType `json:"type"`
    JobID      string    `json:"job_id"`
    WorkerID   string    `json:"worker_id"`
    BotID      string    `json:"bot_id"`
    OrderID    int64     `json:"order_id"`
    OrderType  string    `json:"order_type"` // LIMIT, MARKET, CANCEL
    Side       string    `json:"side"`
    Price      int64     `json:"price"`
    Quantity   int64     `json:"quantity"`
    SentAtNs   int64     `json:"sent_at_ns"`
}

// AckEvent published by worker when contestant's engine acks an order
type AckEvent struct {
    Type       EventType `json:"type"`
    JobID      string    `json:"job_id"`
    WorkerID   string    `json:"worker_id"`
    BotID      string    `json:"bot_id"`
    OrderID    int64     `json:"order_id"`
    Status     string    `json:"status"`
    LatencyNs  int64     `json:"latency_ns"`
    ReceivedNs int64     `json:"received_at_ns"`
    EngineSeqID int64     `json:"engine_seq_id"`
}

// FillEvent published by worker when contestant reports a fill
type FillEvent struct {
    Type        EventType `json:"type"`
    JobID       string    `json:"job_id"`
    WorkerID    string    `json:"worker_id"`
    OrderID     int64     `json:"order_id"`
    FilledQty   int64     `json:"filled_qty"`
    FilledPrice float64     `json:"filled_price"`
    MatchedWith int64     `json:"matched_with"`
    EngineSeqID int64     `json:"engine_seq_id"`
}

// WorkerDoneEvent signals that a worker finished all its orders
// Point D: Master uses this to detect end of stream
type WorkerDoneEvent struct {
    Type       EventType `json:"type"`
    JobID      string    `json:"job_id"`
    WorkerID   string    `json:"worker_id"`
    TotalSent  int64     `json:"total_sent"`
    TotalFailed int64    `json:"total_failed"`
}

func Marshal(v any) ([]byte, error) {
    return json.Marshal(v)
}