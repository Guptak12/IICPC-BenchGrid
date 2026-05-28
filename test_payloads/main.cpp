#include "iicpc_engine.hpp"

class DummyEngine : public IICPCEngine {
public:
    void on_order(const Order& order) override {
        // Automatically accept every order
        emit_ack(order.order_id);
        
        // If it's a Buy order, just fake a fill for testing
        if (order.side == "BUY") {
            emit_fill(order.order_id, order.quantity, order.price, 999);
        }
    }
};

// Register the engine
IICPCEngine* global_engine_instance = new DummyEngine();