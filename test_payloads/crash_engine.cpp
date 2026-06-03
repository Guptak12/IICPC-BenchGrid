#include "iicpc_engine.hpp"
#include <iostream>
#include <cstdlib>

class CrashEngine : public IICPCEngine {
public:
    void on_order(const Order& order) override {
        // Crash immediately on first order
        std::cerr << "[CRASH_ENGINE] Simulating segmentation fault!\n";
        int* ptr = nullptr;
        *ptr = 42; // Segfault
    }
};

IICPCEngine* global_engine_instance = new CrashEngine();
