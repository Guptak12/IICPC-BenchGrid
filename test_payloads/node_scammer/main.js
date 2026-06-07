const net = require('net');

// Protobuf constants
const OrderType_LIMIT = 0;
const OrderType_MARKET = 1;
const OrderType_CANCEL = 2;

const Side_BUY = 0;
const Side_SELL = 1;

const Status_ACCEPTED = 0;
const Status_FILLED = 1;
const Status_PARTIAL = 2;
const Status_REJECTED = 3;
const Status_CANCELLED = 4;

function decodeVarint(buffer, state) {
    let result = 0n;
    let shift = 0n;
    while (state.idx < buffer.length) {
        const b = buffer[state.idx++];
        result |= BigInt(b & 0x7f) << shift;
        if (!(b & 0x80)) {
            return result;
        }
        shift += 7n;
    }
    return result;
}

function encodeVarint(val) {
    let num = BigInt(val);
    const res = [];
    while (true) {
        const towrite = Number(num & 0x7fn);
        num >>= 7n;
        if (num > 0n) {
            res.push(towrite | 0x80);
        } else {
            res.push(towrite);
            break;
        }
    }
    return Buffer.from(res);
}

function decodeOrder(buffer) {
    const o = {
        bot_id: 0n,
        order_id: 0n,
        otype: 0n,
        side: 0n,
        price: 0n,
        quantity: 0n
    };
    const state = { idx: 0 };
    while (state.idx < buffer.length) {
        const key = buffer[state.idx++];
        const wireType = key & 0x7;
        const fieldNumber = key >> 3;
        if (wireType === 0) {
            const val = decodeVarint(buffer, state);
            switch (fieldNumber) {
                case 1: o.bot_id = val; break;
                case 2: o.order_id = val; break;
                case 3: o.otype = val; break;
                case 4: o.side = val; break;
                case 5: o.price = val; break; // zigzag simplified
                case 6: o.quantity = val; break;
            }
        } else {
            return null;
        }
    }
    return o;
}

function encodeReport(orderId, status, filledQty, filledPrice, seqId, processingNs, matchedWith) {
    const buffers = [];
    // order_id = 1
    buffers.push(Buffer.from([0x08]), encodeVarint(orderId));
    // status = 2
    buffers.push(Buffer.from([0x10]), encodeVarint(status));
    // filled_qty = 3
    buffers.push(Buffer.from([0x18]), encodeVarint(filledQty));
    // filled_price = 4
    buffers.push(Buffer.from([0x20]), encodeVarint(filledPrice));
    // engine_seq_id = 5
    buffers.push(Buffer.from([0x28]), encodeVarint(seqId));
    // processing_ns = 6
    buffers.push(Buffer.from([0x30]), encodeVarint(processingNs));
    // matched_with = 7
    buffers.push(Buffer.from([0x38]), encodeVarint(matchedWith));
    return Buffer.concat(buffers);
}

function writeReport(socket, orderId, status, filledQty, filledPrice, seqId, matchedWith) {
    const payload = encodeReport(orderId, status, filledQty, filledPrice, seqId, 100n, matchedWith);
    const prefix = Buffer.alloc(4);
    prefix.writeUInt32LE(payload.length, 0);
    socket.write(Buffer.concat([prefix, payload]));
}

let seqId = 0n;

function handleOrder(socket, o) {
    seqId++;
    
    if (o.otype === BigInt(OrderType_CANCEL)) {
        // SCAM: reject valid cancels
        writeReport(socket, o.order_id, Status_REJECTED, 0n, 0n, seqId, 0n);
        return;
    }

    // ACCEPTED
    writeReport(socket, o.order_id, Status_ACCEPTED, 0n, 0n, seqId, 0n);

    // SCAM ANOMALY MATCHING
    // Generate fill events that violate exchange rules:
    setTimeout(() => {
        // Anomaly 1: Phantom Fill (fill on non-existent order ID)
        seqId++;
        writeReport(socket, 888888n, Status_FILLED, 100n, 5000n, seqId, 999999n);

        // Anomaly 2: Price/Qty Mismatch (mismatch order price)
        seqId++;
        const wrongPrice = o.price + 500n; // scammer reports price 5.00 units off
        writeReport(socket, o.order_id, Status_PARTIAL, o.quantity / 2n || 1n, wrongPrice, seqId, 999999n);

        // Anomaly 3: Self-Crossing (match same bot order ID with itself)
        seqId++;
        writeReport(socket, o.order_id, Status_FILLED, o.quantity / 2n || 1n, o.price, seqId, o.order_id);
    }, 10);
}

const server = net.createServer((socket) => {
    let buffer = Buffer.alloc(0);

    socket.on('data', (chunk) => {
        buffer = Buffer.concat([buffer, chunk]);
        while (buffer.length >= 4) {
            const length = buffer.readUInt32LE(0);
            if (buffer.length < 4 + length) {
                break;
            }
            const payload = buffer.subarray(4, 4 + length);
            buffer = buffer.subarray(4 + length);

            const o = decodeOrder(payload);
            if (o) {
                handleOrder(socket, o);
            }
        }
    });

    socket.on('error', () => {
        // Catch network errors cleanly when connection is cut
    });
});

server.listen(8000, '0.0.0.0', () => {
    console.log('Node Scammer Engine listening on port 8000...');
});
