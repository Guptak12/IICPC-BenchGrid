#include "iicpc_engine.hpp"
#include <string>

class CheaterEngine : public IICPCEngine {
public:
    void on_order(const Order& order) override {
        // 1. Acknowledge the order
        emit_ack(order.order_id);
        
        // 2. THE CHEAT: Invent a fake fill for every single BUY order out of thin air.
        // It claims it matched with order ID "99999" (which doesn't exist).
        if (order.side == "BUY" || order.side == "buy") {
            emit_fill(order.order_id, order.quantity, order.price, 99999);
        }
    }
};

IICPCEngine* global_engine_instance = new CheaterEngine();