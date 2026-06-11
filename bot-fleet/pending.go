package main

import (
	"sync"
)

const pendingShards = 16

type pendingShard struct {
	mu sync.Mutex
	m  map[int64]int64
}

type pendingTable struct {
	shards [pendingShards]*pendingShard
}

func newPendingTable() *pendingTable {
	t := &pendingTable{}
	for i := 0; i < pendingShards; i++ {
		t.shards[i] = &pendingShard{
			m: make(map[int64]int64),
		}
	}
	return t
}

func (t *pendingTable) set(orderID, sendTime int64) {
	shard := t.shards[uint64(orderID)%pendingShards]
	shard.mu.Lock()
	shard.m[orderID] = sendTime
	shard.mu.Unlock()
}

func (t *pendingTable) remove(orderID int64) (int64, bool) {
	shard := t.shards[uint64(orderID)%pendingShards]
	shard.mu.Lock()
	sendTime, ok := shard.m[orderID]
	if ok {
		delete(shard.m, orderID)
	}
	shard.mu.Unlock()
	return sendTime, ok
}

func (t *pendingTable) len() int {
	n := 0
	for i := 0; i < pendingShards; i++ {
		shard := t.shards[i]
		shard.mu.Lock()
		n += len(shard.m)
		shard.mu.Unlock()
	}
	return n
}

func (t *pendingTable) clear() {
	for i := 0; i < pendingShards; i++ {
		shard := t.shards[i]
		shard.mu.Lock()
		for k := range shard.m {
			delete(shard.m, k)
		}
		shard.mu.Unlock()
	}
}

func (t *pendingTable) getExpiredAndRemove(deadlineNs int64) []int64 {
	var expired []int64
	for i := 0; i < pendingShards; i++ {
		shard := t.shards[i]
		shard.mu.Lock()
		for orderID, sentAt := range shard.m {
			if sentAt <= deadlineNs {
				expired = append(expired, orderID)
				delete(shard.m, orderID)
			}
		}
		shard.mu.Unlock()
	}
	return expired
}
