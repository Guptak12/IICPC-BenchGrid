#include <iostream>
#include <vector>
#include <string>
#include <cstring>
#include <algorithm>
#include <unistd.h>
#include <sys/socket.h>
#include <netinet/in.h>
#include <thread>
#include <mutex>

// Protobuf constants
const uint64_t ORDER_TYPE_LIMIT = 0;
const uint64_t ORDER_TYPE_MARKET = 1;
const uint64_t ORDER_TYPE_CANCEL = 2;

const uint64_t SIDE_BUY = 0;
const uint64_t SIDE_SELL = 1;

const uint64_t STATUS_ACCEPTED = 0;
const uint64_t STATUS_FILLED = 1;
const uint64_t STATUS_PARTIAL = 2;
const uint64_t STATUS_REJECTED = 3;
const uint64_t STATUS_CANCELLED = 4;

struct Order {
    uint64_t bot_id;
    uint64_t order_id;
    uint64_t type;
    uint64_t side;
    int64_t price;
    uint64_t quantity;
};

class OrderBook {
public:
    std::mutex mu;
    std::vector<Order> buy_orders;  // descending price
    std::vector<Order> sell_orders; // ascending price
    uint64_t seq_id = 0;
};

uint64_t decode_varint(const uint8_t* data, size_t& idx, size_t limit) {
    uint64_t result = 0;
    int shift = 0;
    while (idx < limit) {
        uint8_t b = data[idx++];
        result |= (uint64_t(b & 0x7F) << shift);
        if (!(b & 0x80)) {
            return result;
        }
        shift += 7;
    }
    return result;
}

void encode_varint(std::vector<uint8_t>& buf, uint64_t value) {
    while (true) {
        uint8_t towrite = value & 0x7F;
        value >>= 7;
        if (value > 0) {
            buf.push_back(towrite | 0x80);
        } else {
            buf.push_back(towrite);
            break;
        }
    }
}

Order decode_order(const uint8_t* data, size_t length) {
    Order o = {0, 0, 0, 0, 0, 0};
    size_t idx = 0;
    while (idx < length) {
        uint8_t key = data[idx++];
        uint8_t wire_type = key & 0x7;
        uint8_t field_number = key >> 3;
        if (wire_type == 0) {
            uint64_t val = decode_varint(data, idx, length);
            switch (field_number) {
                case 1: o.bot_id = val; break;
                case 2: o.order_id = val; break;
                case 3: o.type = val; break;
                case 4: o.side = val; break;
                case 5: o.price = (int64_t)val; break;
                case 6: o.quantity = val; break;
            }
        } else {
            break;
        }
    }
    return o;
}

std::vector<uint8_t> encode_report(uint64_t order_id, uint64_t status, uint64_t filled_qty, int64_t filled_price, uint64_t seq_id, uint64_t processing_ns, uint64_t matched_with) {
    std::vector<uint8_t> payload;
    // order_id = 1
    payload.push_back(0x08);
    encode_varint(payload, order_id);
    // status = 2
    payload.push_back(0x10);
    encode_varint(payload, status);
    // filled_qty = 3
    payload.push_back(0x18);
    encode_varint(payload, filled_qty);
    // filled_price = 4
    payload.push_back(0x20);
    encode_varint(payload, (uint64_t)filled_price);
    // engine_seq_id = 5
    payload.push_back(0x28);
    encode_varint(payload, seq_id);
    // processing_ns = 6
    payload.push_back(0x30);
    encode_varint(payload, processing_ns);
    // matched_with = 7
    payload.push_back(0x38);
    encode_varint(payload, matched_with);
    return payload;
}

void write_report(int fd, uint64_t order_id, uint64_t status, uint64_t filled_qty, int64_t filled_price, uint64_t seq_id, uint64_t matched_with) {
    std::vector<uint8_t> payload = encode_report(order_id, status, filled_qty, filled_price, seq_id, 400, matched_with);
    uint32_t len_val = payload.size();
    uint8_t prefix[4];
    prefix[0] = len_val & 0xFF;
    prefix[1] = (len_val >> 8) & 0xFF;
    prefix[2] = (len_val >> 16) & 0xFF;
    prefix[3] = (len_val >> 24) & 0xFF;

    ::write(fd, prefix, 4);
    ::write(fd, payload.data(), payload.size());
}

void handle_order(int fd, OrderBook& ob, Order o) {
    std::lock_guard<std::mutex> lock(ob.mu);

    if (o.type == ORDER_TYPE_CANCEL) {
        bool removed = false;
        // Search buy book
        for (auto it = ob.buy_orders.begin(); it != ob.buy_orders.end(); ++it) {
            if (it->order_id == o.order_id) {
                ob.buy_orders.erase(it);
                removed = true;
                break;
            }
        }
        if (!removed) {
            // Search sell book
            for (auto it = ob.sell_orders.begin(); it != ob.sell_orders.end(); ++it) {
                if (it->order_id == o.order_id) {
                    ob.sell_orders.erase(it);
                    removed = true;
                    break;
                }
            }
        }
        ob.seq_id++;
        if (removed) {
            write_report(fd, o.order_id, STATUS_CANCELLED, 0, 0, ob.seq_id, 0);
        } else {
            write_report(fd, o.order_id, STATUS_REJECTED, 0, 0, ob.seq_id, 0);
        }
        return;
    }

    ob.seq_id++;
    write_report(fd, o.order_id, STATUS_ACCEPTED, 0, 0, ob.seq_id, 0);

    if (o.side == SIDE_BUY) {
        // Match against sells
        while (!ob.sell_orders.empty() && o.quantity > 0) {
            int best_sell_idx = -1;
            for (size_t i = 0; i < ob.sell_orders.size(); ++i) {
                auto& ro = ob.sell_orders[i];
                if (o.type == ORDER_TYPE_LIMIT && ro.price > o.price) {
                    break;
                }
                if (ro.bot_id == o.bot_id) {
                    continue; // Self-crossing prevention
                }
                best_sell_idx = i;
                break;
            }

            if (best_sell_idx == -1) {
                break;
            }

            Order& best_sell = ob.sell_orders[best_sell_idx];
            uint64_t match_qty = std::min(o.quantity, best_sell.quantity);

            o.quantity -= match_qty;
            best_sell.quantity -= match_qty;

            ob.seq_id++;
            uint64_t buy_status = (o.quantity == 0) ? STATUS_FILLED : STATUS_PARTIAL;
            write_report(fd, o.order_id, buy_status, match_qty, best_sell.price, ob.seq_id, best_sell.order_id);

            uint64_t sell_status = (best_sell.quantity == 0) ? STATUS_FILLED : STATUS_PARTIAL;
            write_report(fd, best_sell.order_id, sell_status, match_qty, best_sell.price, ob.seq_id, o.order_id);

            if (best_sell.quantity == 0) {
                ob.sell_orders.erase(ob.sell_orders.begin() + best_sell_idx);
            }
        }

        // Insert remaining limit
        if (o.quantity > 0 && o.type == ORDER_TYPE_LIMIT) {
            auto it = ob.buy_orders.begin();
            for (; it != ob.buy_orders.end(); ++it) {
                if (o.price > it->price) {
                    break;
                }
            }
            ob.buy_orders.insert(it, o);
        }
    } else {
        // Match against buys
        while (!ob.buy_orders.empty() && o.quantity > 0) {
            int best_buy_idx = -1;
            for (size_t i = 0; i < ob.buy_orders.size(); ++i) {
                auto& ro = ob.buy_orders[i];
                if (o.type == ORDER_TYPE_LIMIT && ro.price < o.price) {
                    break;
                }
                if (ro.bot_id == o.bot_id) {
                    continue; // Self-crossing prevention
                }
                best_buy_idx = i;
                break;
            }

            if (best_buy_idx == -1) {
                break;
            }

            Order& best_buy = ob.buy_orders[best_buy_idx];
            uint64_t match_qty = std::min(o.quantity, best_buy.quantity);

            o.quantity -= match_qty;
            best_buy.quantity -= match_qty;

            ob.seq_id++;
            uint64_t sell_status = (o.quantity == 0) ? STATUS_FILLED : STATUS_PARTIAL;
            write_report(fd, o.order_id, sell_status, match_qty, best_buy.price, ob.seq_id, best_buy.order_id);

            uint64_t buy_status = (best_buy.quantity == 0) ? STATUS_FILLED : STATUS_PARTIAL;
            write_report(fd, best_buy.order_id, buy_status, match_qty, best_buy.price, ob.seq_id, o.order_id);

            if (best_buy.quantity == 0) {
                ob.buy_orders.erase(ob.buy_orders.begin() + best_buy_idx);
            }
        }

        // Insert remaining limit
        if (o.quantity > 0 && o.type == ORDER_TYPE_LIMIT) {
            auto it = ob.sell_orders.begin();
            for (; it != ob.sell_orders.end(); ++it) {
                if (o.price < it->price) {
                    break;
                }
            }
            ob.sell_orders.insert(it, o);
        }
    }
}

void handle_client(int fd, OrderBook& ob) {
    while (true) {
        uint8_t prefix[4];
        ssize_t r = ::read(fd, prefix, 4);
        if (r <= 0) {
            break;
        }
        uint32_t length = prefix[0] | (prefix[1] << 8) | (prefix[2] << 16) | (prefix[3] << 24);
        std::vector<uint8_t> payload(length);
        r = ::read(fd, payload.data(), length);
        if (r <= 0) {
            break;
        }

        Order o = decode_order(payload.data(), length);
        handle_order(fd, ob, o);
    }
    ::close(fd);
}

int main() {
    int server_fd = ::socket(AF_INET, SOCK_STREAM, 0);
    int opt = 1;
    ::setsockopt(server_fd, SOL_SOCKET, SO_REUSEADDR, &opt, sizeof(opt));

    sockaddr_in address;
    std::memset(&address, 0, sizeof(address));
    address.sin_family = AF_INET;
    address.sin_addr.s_addr = INADDR_ANY;
    address.sin_port = htons(8000);

    if (::bind(server_fd, (sockaddr*)&address, sizeof(address)) < 0) {
        std::cerr << "Bind failed" << std::endl;
        return 1;
    }

    if (::listen(server_fd, 128) < 0) {
        std::cerr << "Listen failed" << std::endl;
        return 1;
    }

    std::cout << "C++ Basic Engine listening on port 8000..." << std::endl;
    OrderBook ob;

    while (true) {
        int client_fd = ::accept(server_fd, nullptr, nullptr);
        if (client_fd < 0) {
            continue;
        }
        std::thread(handle_client, client_fd, std::ref(ob)).detach();
    }
    return 0;
}
