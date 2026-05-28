#pragma once
#include <string>
#include <cstdint>

// The standard Order object the contestant will interact with
struct Order {
    int64_t order_id;
    std::string type; // "LIMIT", "MARKET"
    std::string side; // "BUY", "SELL"
    double price;
    int64_t quantity;
};

// The Base Class the contestant must inherit from
class IICPCEngine {
public:
    virtual ~IICPCEngine() = default;

    // The ONLY function the contestant has to implement
    virtual void on_order(const Order& order) = 0;

    // The functions the contestant calls to output data
    void emit_ack(int64_t order_id);
    void emit_fill(int64_t order_id, int64_t filled_qty, double filled_price, int64_t matched_with);
};

// Global pointer that will hold the contestant's engine instance
extern IICPCEngine* global_engine_instance;