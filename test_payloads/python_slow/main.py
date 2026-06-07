import socket
import struct
import threading
import time

# Protobuf Constants
OrderType_LIMIT = 0
OrderType_MARKET = 1
OrderType_CANCEL = 2

Side_BUY = 0
Side_SELL = 1

Status_ACCEPTED = 0
Status_FILLED = 1
Status_PARTIAL = 2
Status_REJECTED = 3
Status_CANCELLED = 4

class Order:
    def __init__(self, bot_id, order_id, otype, side, price, quantity):
        self.bot_id = bot_id
        self.order_id = order_id
        self.type = otype
        self.side = side
        self.price = price
        self.quantity = quantity

class OrderBook:
    def __init__(self):
        self.lock = threading.Lock()
        self.buy_orders = []  # descending price
        self.sell_orders = []  # ascending price
        self.seq_id = 0

def decode_varint(data, index):
    result = 0
    shift = 0
    while index < len(data):
        b = data[index]
        index += 1
        result |= (b & 0x7f) << shift
        if not (b & 0x80):
            return result, index
        shift += 7
    return result, index

def encode_varint(value):
    res = bytearray()
    while True:
        towrite = value & 0x7f
        value >>= 7
        if value > 0:
            res.append(towrite | 0x80)
        else:
            res.append(towrite)
            break
    return bytes(res)

def decode_order(data):
    o = Order(0, 0, 0, 0, 0, 0)
    idx = 0
    while idx < len(data):
        key = data[idx]
        idx += 1
        wire_type = key & 0x7
        field_number = key >> 3
        if wire_type == 0:
            val, next_idx = decode_varint(data, idx)
            idx = next_idx
            if field_number == 1:
                o.bot_id = val
            elif field_number == 2:
                o.order_id = val
            elif field_number == 3:
                o.type = val
            elif field_number == 4:
                o.side = val
            elif field_number == 5:
                o.price = val
            elif field_number == 6:
                o.quantity = val
        else:
            return None
    return o

def encode_report(order_id, status, filled_qty, filled_price, seq_id, processing_ns, matched_with):
    payload = b""
    # uint64 order_id = 1 -> key 0x08
    payload += bytes([0x08]) + encode_varint(order_id)
    # ExecutionStatus status = 2 -> key 0x10
    payload += bytes([0x10]) + encode_varint(status)
    # uint64 filled_qty = 3 -> key 0x18
    payload += bytes([0x18]) + encode_varint(filled_qty)
    # int64 filled_price = 4 -> key 0x20
    payload += bytes([0x20]) + encode_varint(filled_price)
    # uint64 engine_seq_id = 5 -> key 0x28
    payload += bytes([0x28]) + encode_varint(seq_id)
    # uint64 processing_ns = 6 -> key 0x30
    payload += bytes([0x30]) + encode_varint(processing_ns)
    # uint64 matched_with = 7 -> key 0x38
    payload += bytes([0x38]) + encode_varint(matched_with)
    return payload

def write_report(conn, order_id, status, filled_qty, filled_price, seq_id, matched_with):
    # Artificially sleep for 10 milliseconds to simulate slow engine execution
    time.sleep(0.01)
    
    # 10ms converted to nanoseconds = 10,000,000 ns
    payload = encode_report(order_id, status, filled_qty, filled_price, seq_id, 10000000, matched_with)
    length_prefix = struct.pack('<I', len(payload))
    conn.sendall(length_prefix + payload)

def handle_order(conn, ob, o):
    with ob.lock:
        if o.type == OrderType_CANCEL:
            removed = False
            for i, ro in enumerate(ob.buy_orders):
                if ro.order_id == o.order_id:
                    ob.buy_orders.pop(i)
                    removed = True
                    break
            if not removed:
                for i, ro in enumerate(ob.sell_orders):
                    if ro.order_id == o.order_id:
                        ob.sell_orders.pop(i)
                        removed = True
                        break
            ob.seq_id += 1
            if removed:
                write_report(conn, o.order_id, Status_CANCELLED, 0, 0, ob.seq_id, 0)
            else:
                write_report(conn, o.order_id, Status_REJECTED, 0, 0, ob.seq_id, 0)
            return

        ob.seq_id += 1
        write_report(conn, o.order_id, Status_ACCEPTED, 0, 0, ob.seq_id, 0)

        # Match logic
        if o.side == Side_BUY:
            while len(ob.sell_orders) > 0 and o.quantity > 0:
                best_sell_idx = -1
                for i, ro in enumerate(ob.sell_orders):
                    if o.type == OrderType_LIMIT and ro.price > o.price:
                        break
                    if ro.bot_id == o.bot_id:
                        continue  # Self-crossing prevention
                    best_sell_idx = i
                    break

                if best_sell_idx == -1:
                    break

                best_sell = ob.sell_orders[best_sell_idx]
                match_qty = min(o.quantity, best_sell.quantity)

                o.quantity -= match_qty
                best_sell.quantity -= match_qty

                ob.seq_id += 1
                buy_status = Status_FILLED if o.quantity == 0 else Status_PARTIAL
                write_report(conn, o.order_id, buy_status, match_qty, best_sell.price, ob.seq_id, best_sell.order_id)

                sell_status = Status_FILLED if best_sell.quantity == 0 else Status_PARTIAL
                write_report(conn, best_sell.order_id, sell_status, match_qty, best_sell.price, ob.seq_id, o.order_id)

                if best_sell.quantity == 0:
                    ob.sell_orders.pop(best_sell_idx)

            if o.quantity > 0 and o.type == OrderType_LIMIT:
                insert_idx = len(ob.buy_orders)
                for i, ro in enumerate(ob.buy_orders):
                    if o.price > ro.price:
                        insert_idx = i
                        break
                ob.buy_orders.insert(insert_idx, o)
        else:
            # Sell Order matches BUY orders
            while len(ob.buy_orders) > 0 and o.quantity > 0:
                best_buy_idx = -1
                for i, ro in enumerate(ob.buy_orders):
                    if o.type == OrderType_LIMIT and ro.price < o.price:
                        break
                    if ro.bot_id == o.bot_id:
                        continue  # Self-crossing prevention
                    best_buy_idx = i
                    break

                if best_buy_idx == -1:
                    break

                best_buy = ob.buy_orders[best_buy_idx]
                match_qty = min(o.quantity, best_buy.quantity)

                o.quantity -= match_qty
                best_buy.quantity -= match_qty

                ob.seq_id += 1
                sell_status = Status_FILLED if o.quantity == 0 else Status_PARTIAL
                write_report(conn, o.order_id, sell_status, match_qty, best_buy.price, ob.seq_id, best_buy.order_id)

                buy_status = Status_FILLED if best_buy.quantity == 0 else Status_PARTIAL
                write_report(conn, best_buy.order_id, buy_status, match_qty, best_buy.price, ob.seq_id, o.order_id)

                if best_buy.quantity == 0:
                    ob.buy_orders.pop(best_buy_idx)

            if o.quantity > 0 and o.type == OrderType_LIMIT:
                insert_idx = len(ob.sell_orders)
                for i, ro in enumerate(ob.sell_orders):
                    if o.price < ro.price:
                        insert_idx = i
                        break
                ob.sell_orders.insert(insert_idx, o)

def handle_client(conn, ob):
    try:
        while True:
            len_bytes = conn.recv(4)
            if not len_bytes or len(len_bytes) < 4:
                break
            length = struct.unpack('<I', len_bytes)[0]
            
            payload = bytearray()
            while len(payload) < length:
                chunk = conn.recv(length - len(payload))
                if not chunk:
                    break
                payload.extend(chunk)
            if len(payload) < length:
                break
                
            o = decode_order(payload)
            if o is None:
                continue
            
            handle_order(conn, ob, o)
    except Exception:
        pass
    finally:
        conn.close()

def main():
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.bind(('0.0.0.0', 8000))
    s.listen(128)
    print("Python Slow Engine listening on port 8000...")
    ob = OrderBook()
    while True:
        conn, addr = s.accept()
        t = threading.Thread(target=handle_client, args=(conn, ob))
        t.daemon = True
        t.start()

if __name__ == '__main__':
    main()
