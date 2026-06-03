#include "iicpc_engine.hpp"
#include <map>
#include <queue>
#include <algorithm>
#include <iostream>
#include <string>
#include <unordered_map>

class OptimizedEngine : public IICPCEngine {
private:
    // Sorted maps for price levels.
    std::map<int64_t, std::queue<Order>, std::less<int64_t>> asks;    
    std::map<int64_t, std::queue<Order>, std::greater<int64_t>> bids; 

    // Hash map with reserved buckets to avoid re-hashing overhead.
    std::unordered_map<int64_t, bool> active_orders;

public:
    OptimizedEngine() {
        active_orders.reserve(10000);
    }

    void on_order(const Order& order) override {
        // Avoid string copies and allocations: inspect characters directly
        char type_char = order.type.empty() ? ' ' : order.type[0];
        char side_char = order.side.empty() ? ' ' : order.side[0];

        bool is_cancel = (type_char == 'C' || type_char == 'c');
        bool is_limit  = (type_char == 'L' || type_char == 'l');
        bool is_buy    = (side_char == 'B' || side_char == 'b');

        // 1. Handle Cancels
        if (is_cancel) {
            auto it = active_orders.find(order.order_id);
            if (it != active_orders.end() && it->second) {
                it->second = false; // Mark cancelled
                emit_ack(order.order_id, "cancelled");
            } else {
                emit_ack(order.order_id, "rejected");
            }
            return;
        }

        // 2. Limit & Market orders are accepted
        emit_ack(order.order_id, "accepted");

        Order incoming = order;

        if (is_buy) {
            auto it = asks.begin();
            while (it != asks.end() && incoming.quantity > 0) {
                if (is_limit && it->first > incoming.price) {
                    break; 
                }

                auto& queue = it->second;
                while (!queue.empty() && incoming.quantity > 0) {
                    Order resting_ask = queue.front();
                    
                    if (!active_orders[resting_ask.order_id]) {
                        queue.pop();
                        continue;
                    }

                    int64_t fill_qty = std::min(incoming.quantity, resting_ask.quantity);
                    
                    emit_fill(incoming.order_id, fill_qty, it->first, resting_ask.order_id);
                    emit_fill(resting_ask.order_id, fill_qty, it->first, incoming.order_id);

                    incoming.quantity -= fill_qty;
                    queue.front().quantity -= fill_qty; 

                    if (queue.front().quantity == 0) {
                        active_orders.erase(resting_ask.order_id);
                        queue.pop();
                    }
                }
                
                if (queue.empty()) {
                    it = asks.erase(it);
                } else {
                    ++it;
                }
            }
            
            if (is_limit && incoming.quantity > 0) {
                bids[incoming.price].push(incoming);
                active_orders[incoming.order_id] = true;
            }
            
        } else { // SELL
            auto it = bids.begin();
            while (it != bids.end() && incoming.quantity > 0) {
                if (is_limit && it->first < incoming.price) {
                    break;
                }

                auto& queue = it->second;
                while (!queue.empty() && incoming.quantity > 0) {
                    Order resting_bid = queue.front();

                    if (!active_orders[resting_bid.order_id]) {
                        queue.pop();
                        continue;
                    }

                    int64_t fill_qty = std::min(incoming.quantity, resting_bid.quantity);
                    
                    emit_fill(incoming.order_id, fill_qty, it->first, resting_bid.order_id);
                    emit_fill(resting_bid.order_id, fill_qty, it->first, incoming.order_id);

                    incoming.quantity -= fill_qty;
                    queue.front().quantity -= fill_qty; 

                    if (queue.front().quantity == 0) {
                        active_orders.erase(resting_bid.order_id);
                        queue.pop();
                    }
                }
                
                if (queue.empty()) {
                    it = bids.erase(it);
                } else {
                    ++it;
                }
            }
            
            if (is_limit && incoming.quantity > 0) {
                asks[incoming.price].push(incoming);
                active_orders[incoming.order_id] = true;
            }
        }
    }
};

IICPCEngine* global_engine_instance = new OptimizedEngine();
