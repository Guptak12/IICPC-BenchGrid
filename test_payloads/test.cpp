#include <iostream>
#include <string>
#include <sstream>
#include <vector>
#include <cstring>
#include <sys/socket.h>
#include <sys/types.h>
#include <netinet/in.h>
#include <unistd.h>
#include <openssl/sha.h>
#include <openssl/bio.h>
#include <openssl/evp.h>
#include <openssl/buffer.h>
#include <signal.h>

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

// Read full HTTP request until \r\n\r\n
static std::string readHTTPRequest(int fd) {
    std::string req;
    char buf[1];
    while (true) {
        int n = recv(fd, buf, 1, 0);
        if (n <= 0) break;
        req += buf[0];
        if (req.size() >= 4 &&
            req.substr(req.size() - 4) == "\r\n\r\n") break;
    }
    return req;
}

// Extract header value from raw HTTP request
static std::string getHeader(const std::string& req, const std::string& name) {
    std::string search = name + ": ";
    size_t pos = req.find(search);
    if (pos == std::string::npos) return "";
    pos += search.size();
    size_t end = req.find("\r\n", pos);
    return req.substr(pos, end - pos);
}

// Perform WebSocket upgrade handshake
static bool doHandshake(int fd) {
    std::string req = readHTTPRequest(fd);
    if (req.empty()) return false;

    std::string key = getHeader(req, "Sec-WebSocket-Key");
    if (key.empty()) {
        // Not a WebSocket request — send plain HTTP 400
        std::string resp = "HTTP/1.1 400 Bad Request\r\n"
                           "Content-Length: 0\r\n\r\n";
        send(fd, resp.c_str(), resp.size(), 0);
        return false;
    }

    // Compute accept key: SHA1(key + magic) then base64
    std::string magic = key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11";
    unsigned char hash[SHA_DIGEST_LENGTH];
    SHA1(reinterpret_cast<const unsigned char*>(magic.c_str()),
         magic.size(), hash);
    std::string accept = base64Encode(hash, SHA_DIGEST_LENGTH);

    // Send 101 Switching Protocols — this is what the bot expects
    std::string resp =
        "HTTP/1.1 101 Switching Protocols\r\n"
        "Upgrade: websocket\r\n"
        "Connection: Upgrade\r\n"
        "Sec-WebSocket-Accept: " + accept + "\r\n"
        "\r\n";

    send(fd, resp.c_str(), resp.size(), 0);
    return true;
}

// Read one WebSocket frame, return payload (handles masking)
static std::string readFrame(int fd) {
    // Read first 2 bytes of frame header
    uint8_t header[2] = {};
    if (recv(fd, header, 2, MSG_WAITALL) != 2) return "";

    uint8_t opcode = header[0] & 0x0F;

    // Opcode 8 = close frame
    if (opcode == 8) return "";

    bool masked = (header[1] & 0x80) != 0;
    uint64_t payloadLen = header[1] & 0x7F;

    // Extended payload length
    if (payloadLen == 126) {
        uint8_t ext[2];
        if (recv(fd, ext, 2, MSG_WAITALL) != 2) return "";
        payloadLen = (uint64_t(ext[0]) << 8) | ext[1];
    } else if (payloadLen == 127) {
        uint8_t ext[8];
        if (recv(fd, ext, 8, MSG_WAITALL) != 8) return "";
        payloadLen = 0;
        for (int i = 0; i < 8; i++)
            payloadLen = (payloadLen << 8) | ext[i];
    }

    // Read masking key if present
    uint8_t maskKey[4] = {};
    if (masked) {
        if (recv(fd, maskKey, 4, MSG_WAITALL) != 4) return "";
    }

    // Read payload
    std::vector<uint8_t> payload(payloadLen);
    size_t received = 0;
    while (received < payloadLen) {
        int n = recv(fd, payload.data() + received,
                     payloadLen - received, 0);
        if (n <= 0) return "";
        received += n;
    }

    // Unmask
    if (masked) {
        for (size_t i = 0; i < payloadLen; i++)
            payload[i] ^= maskKey[i % 4];
    }

    return std::string(payload.begin(), payload.end());
}

// Send one unmasked WebSocket text frame
static void sendFrame(int fd, const std::string& msg) {
    std::vector<uint8_t> frame;
    frame.push_back(0x81); // FIN=1, opcode=1 (text)

    size_t len = msg.size();
    if (len <= 125) {
        frame.push_back(static_cast<uint8_t>(len));
    } else if (len <= 65535) {
        frame.push_back(126);
        frame.push_back((len >> 8) & 0xFF);
        frame.push_back(len & 0xFF);
    } else {
        frame.push_back(127);
        for (int i = 7; i >= 0; i--)
            frame.push_back((len >> (8 * i)) & 0xFF);
    }

    frame.insert(frame.end(), msg.begin(), msg.end());
    send(fd, frame.data(), frame.size(), MSG_NOSIGNAL);
}

// Extract order_id from JSON without a full parser
static std::string extractOrderID(const std::string& json) {
    const std::string key = "\"order_id\":";
    size_t pos = json.find(key);
    if (pos == std::string::npos) return "0";
    pos += key.size();
    // Skip whitespace
    while (pos < json.size() && json[pos] == ' ') pos++;
    size_t end = json.find_first_of(",}", pos);
    return json.substr(pos, end - pos);
}

// Handle one WebSocket client connection
static void handleClient(int clientFd) {
    if (!doHandshake(clientFd)) {
        close(clientFd);
        return;
    }

    while (true) {
        std::string msg = readFrame(clientFd);
        if (msg.empty()) break;

        std::string orderID = extractOrderID(msg);

        // Send ack with matching order_id
        std::string ack =
            "{\"order_id\":" + orderID + ","
            "\"status\":\"accepted\"}";

        sendFrame(clientFd, ack);
    }

    close(clientFd);
}

int main() {
    // Ignore SIGPIPE — prevents crash when client disconnects mid-write
    signal(SIGPIPE, SIG_IGN);

    int serverFd = socket(AF_INET, SOCK_STREAM, 0);
    if (serverFd < 0) {
        std::cerr << "socket() failed\n";
        return 1;
    }

    int opt = 1;
    setsockopt(serverFd, SOL_SOCKET, SO_REUSEADDR, &opt, sizeof(opt));

    sockaddr_in addr{};
    addr.sin_family = AF_INET;
    addr.sin_addr.s_addr = INADDR_ANY;
    addr.sin_port = htons(8080);

    if (bind(serverFd, reinterpret_cast<sockaddr*>(&addr), sizeof(addr)) < 0) {
        std::cerr << "bind() failed\n";
        return 1;
    }

    if (listen(serverFd, 128) < 0) {
        std::cerr << "listen() failed\n";
        return 1;
    }

    std::cout << "[ENGINE] WebSocket order engine listening on port 8080\n";
    std::cout.flush();

    while (true) {
        int clientFd = accept(serverFd, nullptr, nullptr);
        if (clientFd < 0) continue;

        // Fork per connection — each client gets its own process
        pid_t pid = fork();
        if (pid == 0) {
            // Child process
            close(serverFd);
            handleClient(clientFd);
            _exit(0);
        } else {
            // Parent process — close client fd and keep accepting
            close(clientFd);
        }
    }

    return 0;
}