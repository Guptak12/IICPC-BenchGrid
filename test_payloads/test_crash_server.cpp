#include <iostream>
#include <string>
#include <vector>
#include <thread>
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

std::atomic<int> connectionCount{0};
const int CRASH_AFTER = 10; // crash after 10 connections

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
    char buf[1];
    char last[4] = {0,0,0,0};
    while (true) {
        int n = recv(fd, buf, 1, 0);
        if (n <= 0) break;
        req += buf[0];
        last[0]=last[1]; last[1]=last[2];
        last[2]=last[3]; last[3]=buf[0];
        if (last[0]=='\r' && last[1]=='\n' &&
            last[2]=='\r' && last[3]=='\n') break;
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
    if (key.empty()) return false;

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
    if (opcode == 8) return "";
    bool masked = (header[1] & 0x80) != 0;
    uint64_t payloadLen = header[1] & 0x7F;
    if (payloadLen == 126) {
        uint8_t ext[2];
        if (recv(fd, ext, 2, MSG_WAITALL) != 2) return "";
        payloadLen = (uint64_t(ext[0]) << 8) | ext[1];
    }
    uint8_t maskKey[4] = {};
    if (masked) recv(fd, maskKey, 4, MSG_WAITALL);
    std::vector<uint8_t> payload(payloadLen);
    size_t received = 0;
    while (received < payloadLen) {
        int n = recv(fd, payload.data()+received, payloadLen-received, 0);
        if (n <= 0) return "";
        received += n;
    }
    if (masked)
        for (size_t i = 0; i < payloadLen; i++)
            payload[i] ^= maskKey[i%4];
    return std::string(payload.begin(), payload.end());
}

static void sendFrame(int fd, const std::string& msg) {
    std::vector<uint8_t> frame;
    frame.push_back(0x81);
    size_t len = msg.size();
    if (len <= 125) {
        frame.push_back((uint8_t)len);
    } else {
        frame.push_back(126);
        frame.push_back((len >> 8) & 0xFF);
        frame.push_back(len & 0xFF);
    }
    frame.insert(frame.end(), msg.begin(), msg.end());
    send(fd, frame.data(), frame.size(), MSG_NOSIGNAL);
}

static std::string extractOrderID(const std::string& json) {
    const std::string key = "\"order_id\":";
    size_t pos = json.find(key);
    if (pos == std::string::npos) return "0";
    pos += key.size();
    while (pos < json.size() && json[pos] == ' ') pos++;
    size_t end = json.find_first_of(",}", pos);
    return json.substr(pos, end - pos);
}

static void handleClient(int clientFd) {
    int connNum = connectionCount.fetch_add(1) + 1;

    if (!doHandshake(clientFd)) {
        close(clientFd);
        return;
    }

    // All connections handle orders, but connection 3 dies after 10 orders
    int limit = (connNum == 3) ? 10 : 1000;
    int ordersHandled = 0;

    while (ordersHandled < limit) {
        std::string msg = readFrame(clientFd);
        if (msg.empty()) break;
        std::string orderID = extractOrderID(msg);
        std::string ack = "{\"order_id\":" + orderID + ",\"status\":\"accepted\"}";
        sendFrame(clientFd, ack);
        ordersHandled++;

        // FORCEFUL PROCESS KILL: Cuts off the socket buffers immediately
        if (connNum == 3 && ordersHandled >= 10) {
            std::cerr << "[CRASH SERVER] Catastrophic process failure triggered by conn-3! Killing server.\n";
            std::cerr.flush();
            _exit(1); // Hard kernel-level termination of the entire engine
        }
    }

    std::cerr << "[CRASH] conn-" << connNum << " dropping after "
              << ordersHandled << " orders\n";
    std::cerr.flush();
    shutdown(clientFd, SHUT_RDWR);
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

    std::cout << "[CRASH SERVER] Listening on port 8080\n";
    std::cout << "[CRASH SERVER] Will crash after " << CRASH_AFTER << " connections\n";
    std::cout.flush();

    while (true) {
        int clientFd = accept(serverFd, nullptr, nullptr);
        if (clientFd < 0) continue;
        std::thread t(handleClient, clientFd);
        t.detach();
    }

    return 0;
}