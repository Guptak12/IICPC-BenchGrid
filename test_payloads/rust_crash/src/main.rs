use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};
use std::sync::{Arc, Mutex};
use std::thread;

// Protobuf constants
const STATUS_ACCEPTED: u64 = 0;
const STATUS_CANCELLED: u64 = 4;
const STATUS_REJECTED: u64 = 3;
const OrderType_CANCEL: u64 = 2;

struct Order {
    bot_id: u64,
    order_id: u64,
    otype: u64,
    side: u64,
    price: i64,
    quantity: u64,
}

struct OrderBook {
    seq_id: u64,
    total_orders_received: u64,
}

fn decode_varint(data: &[u8], idx: &mut usize) -> u64 {
    let mut result = 0u64;
    let mut shift = 0;
    while *idx < data.len() {
        let b = data[*idx];
        *idx += 1;
        result |= ((b & 0x7F) as u64) << shift;
        if (b & 0x80) == 0 {
            return result;
        }
        shift += 7;
    }
    result
}

fn encode_varint(mut val: u64) -> Vec<u8> {
    let mut res = Vec::new();
    loop {
        let towrite = (val & 0x7F) as u8;
        val >>= 7;
        if val > 0 {
            res.push(towrite | 0x80);
        } else {
            res.push(towrite);
            break;
        }
    }
    res
}

fn decode_order(data: &[u8]) -> Option<Order> {
    let mut o = Order {
        bot_id: 0,
        order_id: 0,
        otype: 0,
        side: 0,
        price: 0,
        quantity: 0,
    };
    let mut idx = 0;
    while idx < data.len() {
        let key = data[idx];
        idx += 1;
        let wire_type = key & 0x7;
        let field_number = key >> 3;
        if wire_type == 0 {
            let val = decode_varint(data, &mut idx);
            match field_number {
                1 => o.bot_id = val,
                2 => o.order_id = val,
                3 => o.otype = val,
                4 => o.side = val,
                5 => o.price = val as i64,
                6 => o.quantity = val,
                _ => {}
            }
        } else {
            return None;
        }
    }
    Some(o)
}

fn encode_report(
    order_id: u64,
    status: u64,
    filled_qty: u64,
    filled_price: i64,
    seq_id: u64,
    processing_ns: u64,
    matched_with: u64,
) -> Vec<u8> {
    let mut payload = Vec::new();
    // order_id = 1
    payload.push(0x08);
    payload.extend(encode_varint(order_id));
    // status = 2
    payload.push(0x10);
    payload.extend(encode_varint(status));
    // filled_qty = 3
    payload.push(0x18);
    payload.extend(encode_varint(filled_qty));
    // filled_price = 4
    payload.push(0x20);
    payload.extend(encode_varint(filled_price as u64));
    // engine_seq_id = 5
    payload.push(0x28);
    payload.extend(encode_varint(seq_id));
    // processing_ns = 6
    payload.push(0x30);
    payload.extend(encode_varint(processing_ns));
    // matched_with = 7
    payload.push(0x38);
    payload.extend(encode_varint(matched_with));
    payload
}

fn write_report(
    stream: &mut TcpStream,
    order_id: u64,
    status: u64,
    filled_qty: u64,
    filled_price: i64,
    seq_id: u64,
    matched_with: u64,
) {
    let payload = encode_report(order_id, status, filled_qty, filled_price, seq_id, 100, matched_with);
    let mut len_prefix = [0u8; 4];
    let len_val = payload.len() as u32;
    len_prefix[0] = (len_val & 0xFF) as u8;
    len_prefix[1] = ((len_val >> 8) & 0xFF) as u8;
    len_prefix[2] = ((len_val >> 16) & 0xFF) as u8;
    len_prefix[3] = ((len_val >> 24) & 0xFF) as u8;

    let _ = stream.write_all(&len_prefix);
    let _ = stream.write_all(&payload);
    let _ = stream.flush();
}

fn handle_client(mut stream: TcpStream, ob: Arc<Mutex<OrderBook>>) {
    loop {
        let mut len_prefix = [0u8; 4];
        if stream.read_exact(&mut len_prefix).is_err() {
            break;
        }
        let length = ((len_prefix[0] as u32) |
                      ((len_prefix[1] as u32) << 8) |
                      ((len_prefix[2] as u32) << 16) |
                      ((len_prefix[3] as u32) << 24)) as usize;

        let mut payload = vec![0u8; length];
        if stream.read_exact(&mut payload).is_err() {
            break;
        }

        let o = match decode_order(&payload) {
            Some(order) => order,
            None => continue,
        };

        let mut book = ob.lock().unwrap();
        book.total_orders_received += 1;
        
        // Critical Trigger: Crash engine if we receive more than 10 orders
        if book.total_orders_received > 10 {
            eprintln!("[FATAL ENGINE RUNTIME ERROR] Segmentation fault (core dumped)");
            eprintln!("Stack trace: thread 'main' panicked at 'Index out of bounds', src/main.rs:114");
            std::process::exit(159); // Exit with standard SIGSYS/seccomp or crash code
        }

        book.seq_id += 1;
        if o.otype == OrderType_CANCEL {
            write_report(&mut stream, o.order_id, STATUS_CANCELLED, 0, 0, book.seq_id, 0);
        } else {
            write_report(&mut stream, o.order_id, STATUS_ACCEPTED, 0, 0, book.seq_id, 0);
        }
    }
}

fn main() {
    let listener = TcpListener::bind("0.0.0.0:8000").unwrap();
    println!("Rust Crash Engine listening on port 8000...");
    let ob = Arc::new(Mutex::new(OrderBook {
        seq_id: 0,
        total_orders_received: 0,
    }));

    for stream in listener.incoming() {
        if let Ok(s) = stream {
            let ob_clone = ob.clone();
            thread::spawn(move || {
                handle_client(s, ob_clone);
            });
        }
    }
}
