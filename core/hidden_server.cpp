#include <iostream>
#include <string>
#include <vector>
#include <thread>
#include <mutex>
#include <atomic>
#include <cstring>
#include <sys/socket.h>
#include <netinet/in.h>
#include <unistd.h>
#include <signal.h>
#include <openssl/sha.h>
#include <openssl/bio.h>
#include <openssl/evp.h>
#include <openssl/buffer.h>

#include "iicpc_engine.hpp"

// --- Global State ---
std::atomic<uint64_t> global_engine_seq_id(1);
std::mutex engine_mutex; // Protects the contestant's orderbook from race conditions

// We use thread_local so the SDK knows exactly which WebSocket to reply to
// without the contestant ever needing to touch socket file descriptors.
thread_local int current_client_fd = -1; 

// --- Forward Declarations ---
static void sendFrame(int fd, const std::string& msg);

// --- SDK Implementations ---
void IICPCEngine::emit_ack(int64_t order_id) {
    if (current_client_fd == -1) return;
    
    uint64_t seq_id = global_engine_seq_id++;
    std::string json = "{\"status\":\"accepted\", \"order_id\":" + std::to_string(order_id) + 
                       ", \"engine_seq_id\":" + std::to_string(seq_id) + "}";
                       
    sendFrame(current_client_fd, json);
}

void IICPCEngine::emit_fill(int64_t order_id, int64_t filled_qty, int64_t filled_price, int64_t matched_with) {
    if (current_client_fd == -1) return;
    
    uint64_t seq_id = global_engine_seq_id++;
    std::string json = "{\"status\":\"filled\", \"order_id\":" + std::to_string(order_id) + 
                       ", \"filled_qty\":" + std::to_string(filled_qty) + 
                       ", \"filled_price\":" + std::to_string(filled_price) + 
                       ", \"matched_with\":" + std::to_string(matched_with) +
                       ", \"engine_seq_id\":" + std::to_string(seq_id) + "}";
                       
    sendFrame(current_client_fd, json);
}


// --- Your Original WebSocket Networking Code ---
static std::string base64Encode(const unsigned char* data, size_t len) {
    BIO* b64 = BIO_new(BIO_f_base64());
    BIO* mem = BIO_new(BIO_s_mem());
    b64 = BIO_push(b64, mem);
    BIO_set_flags(b64, BIO_FLAGS_BASE64_NO_NL);
    BIO_write(b64, data, (int)len);
    BIO_flush(b64);
    BUF_MEM* bptr;
    BIO_get_mem_ptr(b64, &bptr);
    std::string result(bptr->data, bptr->length);
    BIO_free_all(b64);
    return result;
}

static std::string readHTTPRequest(int fd) {
    std::string req;
    req.reserve(1024);
    char buf[1];
    char last[4] = {0,0,0,0};
    while (true) {
        int n = recv(fd, buf, 1, 0);
        if (n <= 0) break;
        req += buf[0];
        last[0] = last[1];
        last[1] = last[2];
        last[2] = last[3];
        last[3] = buf[0];
        if (last[0]=='\r' && last[1]=='\n' && last[2]=='\r' && last[3]=='\n') break;
    }
    return req;
}

static std::string getHeader(const std::string& req, const std::string& name) {
    std::string search = name + ": ";
    size_t pos = req.find(search);
    if (pos == std::string::npos) return "";
    pos += search.size();
    size_t end = req.find("\r\n", pos);
    return req.substr(pos, end - pos);
}

static bool doHandshake(int fd) {
    std::string req = readHTTPRequest(fd);
    if (req.empty()) return false;

    std::string key = getHeader(req, "Sec-WebSocket-Key");
    if (key.empty()) key = getHeader(req, "Sec-Websocket-Key");
    if (key.empty()) {
        std::string resp = "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\n\r\n";
        send(fd, resp.c_str(), resp.size(), 0);
        return false;
    }

    std::string magic = key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11";
    unsigned char hash[SHA_DIGEST_LENGTH];
    SHA1(reinterpret_cast<const unsigned char*>(magic.c_str()), magic.size(), hash);
    std::string accept = base64Encode(hash, SHA_DIGEST_LENGTH);

    std::string resp =
        "HTTP/1.1 101 Switching Protocols\r\n"
        "Upgrade: websocket\r\n"
        "Connection: Upgrade\r\n"
        "Sec-WebSocket-Accept: " + accept + "\r\n\r\n";

    send(fd, resp.c_str(), resp.size(), 0);
    return true;
}

static std::string readFrame(int fd) {
    uint8_t header[2] = {};
    if (recv(fd, header, 2, MSG_WAITALL) != 2) return "";

    uint8_t opcode = header[0] & 0x0F;
    if (opcode == 8) return ""; // close frame

    bool masked = (header[1] & 0x80) != 0;
    uint64_t payloadLen = header[1] & 0x7F;

    if (payloadLen == 126) {
        uint8_t ext[2];
        if (recv(fd, ext, 2, MSG_WAITALL) != 2) return "";
        payloadLen = (uint64_t(ext[0]) << 8) | ext[1];
    } else if (payloadLen == 127) {
        uint8_t ext[8];
        if (recv(fd, ext, 8, MSG_WAITALL) != 8) return "";
        payloadLen = 0;
        for (int i = 0; i < 8; i++) payloadLen = (payloadLen << 8) | ext[i];
    }

    uint8_t maskKey[4] = {};
    if (masked && recv(fd, maskKey, 4, MSG_WAITALL) != 4) return "";

    std::vector<uint8_t> payload(payloadLen);
    size_t received = 0;
    while (received < payloadLen) {
        int n = recv(fd, payload.data() + received, payloadLen - received, 0);
        if (n <= 0) return "";
        received += n;
    }

    if (masked)
        for (size_t i = 0; i < payloadLen; i++) payload[i] ^= maskKey[i % 4];

    return std::string(payload.begin(), payload.end());
}

static void sendFrame(int fd, const std::string& msg) {
    std::vector<uint8_t> frame;
    frame.push_back(0x81); // FIN + text opcode
    size_t len = msg.size();
    if (len <= 125) {
        frame.push_back((uint8_t)len);
    } else if (len <= 65535) {
        frame.push_back(126);
        frame.push_back((len >> 8) & 0xFF);
        frame.push_back(len & 0xFF);
    } else {
        frame.push_back(127);
        for (int i = 7; i >= 0; i--) frame.push_back((len >> (8*i)) & 0xFF);
    }
    frame.insert(frame.end(), msg.begin(), msg.end());
    send(fd, frame.data(), frame.size(), MSG_NOSIGNAL);
}

// --- JSON Parsers ---
static std::string extractStringField(const std::string& json, const std::string& key) {
    size_t pos = json.find(key);
    if (pos == std::string::npos) return "";
    pos += key.size();
    while (pos < json.size() && (json[pos] == ' ' || json[pos] == '\"')) pos++;
    size_t end = json.find_first_of("\"", pos);
    return json.substr(pos, end - pos);
}

static int64_t extractIntField(const std::string& json, const std::string& key) {
    size_t pos = json.find(key);
    if (pos == std::string::npos) return 0;
    pos += key.size();
    while (pos < json.size() && (json[pos] == ' ' || json[pos] == '\"')) pos++;
    size_t end = json.find_first_of(",}", pos);
    try { return std::stoll(json.substr(pos, end - pos)); } catch (...) { return 0; }
}

// --- Worker Thread ---
static void handleClient(int clientFd) {
    if (!doHandshake(clientFd)) {
        close(clientFd);
        return;
    }
    
    // Set the thread-local FD so emit_ack and emit_fill know where to send data
    current_client_fd = clientFd;

    while (true) {
        std::string raw_json = readFrame(clientFd);
        if (raw_json.empty()) break;

        Order new_order;
        new_order.order_id = extractIntField(raw_json, "\"order_id\":");
        new_order.type = extractStringField(raw_json, "\"type\":");
        new_order.side = extractStringField(raw_json, "\"side\":");
        new_order.price = extractIntField(raw_json, "\"price\":");
        new_order.quantity = extractIntField(raw_json, "\"quantity\":");

        if (global_engine_instance) {
            // Lock the mutex so the contestant's engine only processes one order at a time
            std::lock_guard<std::mutex> lock(engine_mutex);
            global_engine_instance->on_order(new_order);
        }
    }

    close(clientFd);
}

int main() {
    signal(SIGPIPE, SIG_IGN);

    int serverFd = socket(AF_INET, SOCK_STREAM, 0);
    int opt = 1;
    setsockopt(serverFd, SOL_SOCKET, SO_REUSEADDR, &opt, sizeof(opt));

    sockaddr_in addr{};
    addr.sin_family      = AF_INET;
    addr.sin_addr.s_addr = INADDR_ANY;
    addr.sin_port        = htons(8080);

    bind(serverFd, reinterpret_cast<sockaddr*>(&addr), sizeof(addr));
    listen(serverFd, 128);

    std::cout << "[WRAPPER] Secure WebSocket engine wrapper listening on port 8080\n";
    std::cout.flush();

    while (true) {
        int clientFd = accept(serverFd, nullptr, nullptr);
        if (clientFd < 0) continue;

        std::thread t(handleClient, clientFd);
        t.detach(); 
    }

    return 0;
}
