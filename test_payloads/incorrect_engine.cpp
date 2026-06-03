#include "iicpc_engine.hpp"
#include <iostream>
#include <string>

class IncorrectEngine : public IICPCEngine {
public:
    void on_order(const Order& order) override {
        // Just accept everything and never perform any matching or emit fills
        if (order.type == "CANCEL" || order.type == "cancel") {
            emit_ack(order.order_id, "rejected");
        } else {
            emit_ack(order.order_id, "accepted");
        }
    }
};

IICPCEngine* global_engine_instance = new IncorrectEngine();
