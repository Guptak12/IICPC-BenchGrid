#include "iicpc_engine.hpp"
#include <map>
#include <queue>
#include <algorithm>
#include <cmath>

class BaselineEngine : public IICPCEngine {
private:
    // STRICTLY int64_t to prevent floating-point bucket fragmentation
    std::map<int64_t, std::queue<Order>, std::less<int64_t>> asks;    
    std::map<int64_t, std::queue<Order>, std::greater<int64_t>> bids; 

    // Helpers for scaled tick-math
    int64_t to_ticks(double price) {
        return std::round(price * 10000.0);
    }
    double to_price(int64_t ticks) {
        return ticks / 10000.0;
    }

public:
    void on_order(const Order& order) override {
        emit_ack(order.order_id);
        
        Order incoming = order;
        int64_t strict_price = to_ticks(incoming.price);

        if (incoming.side == "BUY") {
            auto it = asks.begin();
            
            while (it != asks.end() && incoming.quantity > 0 && it->first <= strict_price) {
                auto& queue = it->second;
                
                while (!queue.empty() && incoming.quantity > 0) {
                    Order& resting_ask = queue.front();
                    int64_t fill_qty = std::min(incoming.quantity, resting_ask.quantity);
                    
                    // JUST ONE EMIT: Taker ID, Qty, Executed Price, Maker ID
                    emit_fill(incoming.order_id, fill_qty, to_price(it->first), resting_ask.order_id);
                    
                    emit_fill(resting_ask.order_id, fill_qty, to_price(it->first), incoming.order_id); // ← add this


                    incoming.quantity -= fill_qty;
                    resting_ask.quantity -= fill_qty;

                    if (resting_ask.quantity == 0) {
                        queue.pop();
                    }
                }
                
                if (queue.empty()) {
                    it = asks.erase(it);
                } else {
                    ++it;
                }
            }
            
            if (incoming.quantity > 0) {
                bids[strict_price].push(incoming);
            }
            
        } else if (incoming.side == "SELL") {
            auto it = bids.begin();
            
            while (it != bids.end() && incoming.quantity > 0 && it->first >= strict_price) {
                auto& queue = it->second;
                
                while (!queue.empty() && incoming.quantity > 0) {
                    Order& resting_bid = queue.front();
                    int64_t fill_qty = std::min(incoming.quantity, resting_bid.quantity);
                    
                    // JUST ONE EMIT: Taker ID, Qty, Executed Price, Maker ID
                    emit_fill(incoming.order_id, fill_qty, to_price(it->first), resting_bid.order_id);
                    emit_fill(resting_bid.order_id, fill_qty, to_price(it->first), incoming.order_id); // ← add this

                    incoming.quantity -= fill_qty;
                    resting_bid.quantity -= fill_qty;

                    if (resting_bid.quantity == 0) {
                        queue.pop();
                    }
                }
                
                if (queue.empty()) {
                    it = bids.erase(it);
                } else {
                    ++it;
                }
            }
            
            if (incoming.quantity > 0) {
                asks[strict_price].push(incoming);
            }
        }
    }
};

IICPCEngine* global_engine_instance = new BaselineEngine();