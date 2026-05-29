#include "iicpc_engine.hpp"
#include <map>
#include <queue>
#include <algorithm>
#include <iostream>
#include <string>

class BaselineEngine : public IICPCEngine {
private:
    std::map<int64_t, std::queue<Order>, std::less<int64_t>> asks;    
    std::map<int64_t, std::queue<Order>, std::greater<int64_t>> bids; 

    // Keep track of active limit orders to handle lazy cancels
    std::map<int64_t, bool> active_orders;

    std::string to_upper(std::string s) {
        std::transform(s.begin(), s.end(), s.begin(), ::toupper);
        return s;
    }

public:
    void on_order(const Order& order) override {
        Order incoming = order;
        std::string type = to_upper(incoming.type);
        std::string side = to_upper(incoming.side);

        // 1. Handle Cancels Immediately
        if (type == "CANCEL") {
            if (active_orders.find(incoming.order_id) != active_orders.end() && active_orders[incoming.order_id]) {
                active_orders[incoming.order_id] = false; // Mark cancelled
                emit_ack(incoming.order_id, "cancelled");
            } else {
                emit_ack(incoming.order_id, "rejected"); // Never existed
            }
            return;
        }

        // 2. Limit & Market orders are always accepted
        emit_ack(incoming.order_id, "accepted");

        if (side == "BUY") {
            auto it = asks.begin();
            // MARKET orders ignore price checks. LIMIT orders stop at their limit.
            while (it != asks.end() && incoming.quantity > 0) {
                if (type == "LIMIT" && it->first > incoming.price) {
                    break; 
                }

                auto& queue = it->second;
                while (!queue.empty() && incoming.quantity > 0) {
                    Order resting_ask = queue.front();
                    
                    // Skip if this resting order was previously cancelled
                    if (!active_orders[resting_ask.order_id]) {
                        queue.pop();
                        continue;
                    }

                    int64_t fill_qty = std::min(incoming.quantity, resting_ask.quantity);
                    
                    // Emit fills for both Maker and Taker
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
            
            // Market orders that don't fill are killed. Limit orders rest on the book.
            if (type == "LIMIT" && incoming.quantity > 0) {
                bids[incoming.price].push(incoming);
                active_orders[incoming.order_id] = true;
            }
            
        } else if (side == "SELL") {
            auto it = bids.begin();
            
            while (it != bids.end() && incoming.quantity > 0) {
                if (type == "LIMIT" && it->first < incoming.price) {
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
            
            if (type == "LIMIT" && incoming.quantity > 0) {
                asks[incoming.price].push(incoming);
                active_orders[incoming.order_id] = true;
            }
        }
    }
};

IICPCEngine* global_engine_instance = new BaselineEngine();