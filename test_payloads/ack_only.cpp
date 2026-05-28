#include "iicpc_engine.hpp"

class AckOnlyEngine : public IICPCEngine {
public:
    void on_order(const Order& order) override {
        emit_ack(order.order_id);
    }
};

IICPCEngine* global_engine_instance = new AckOnlyEngine();
