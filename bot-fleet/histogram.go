package main

import (
    "bytes"
    "encoding/gob"
    "fmt"

    hdr "github.com/HdrHistogram/hdrhistogram-go"
)

func newHistogram() *hdr.Histogram {
    return hdr.New(1, 3600000000000, 3)
}

// serialiseHistogram encodes histogram to bytes for gRPC transport
func serialiseHistogram(h *hdr.Histogram) ([]byte, error) {
    snap := h.Export()
    var buf bytes.Buffer
    if err := gob.NewEncoder(&buf).Encode(snap); err != nil {
        return nil, fmt.Errorf("encode failed: %v", err)
    }
    return buf.Bytes(), nil
}

// deserialiseHistogram decodes bytes back to histogram
func deserialiseHistogram(data []byte) (*hdr.Histogram, error) {
    var snap hdr.Snapshot
    if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&snap); err != nil {
        return nil, fmt.Errorf("decode failed: %v", err)
    }
    return hdr.Import(&snap), nil
}