# IICPC Matching Engine Protocol

## WebSocket Connection
Connect to: ws://YOUR_HOST:8080/ws
Subprotocol: trading

## Order Message (Platform → Contestant)
{
  "bot_id":    "bot-42",          // string, identifies which bot sent this
  "order_id":  4294967298,        // int64, globally unique
  "type":      "LIMIT",           // "LIMIT" | "MARKET" | "CANCEL"
  "side":      "BUY",             // "BUY" | "SELL"
  "price":     10050,             // int64, scaled by 100 ($100.50 = 10050), 0 for MARKET
  "quantity":  730                // int64, scaled by 100 (7.30 units = 730)
}

## Execution Report (Contestant → Platform)
{
  "order_id":    4294967298,      // int64, MUST match the order_id received
  "status":      "accepted",      // "accepted" | "filled" | "partial" | "rejected" | "cancelled"
  "filled_qty":  0,               // int64, scaled, 0 if not filled
  "filled_price": 0               // int64, scaled, 0 if not filled
}

## Rules
1. Every order MUST receive exactly one immediate response (accepted/rejected)
2. Fill events MAY arrive asynchronously after the initial accepted response
3. CANCEL orders: respond with "cancelled" if found, "rejected" if not found
4. Server MUST bind to 0.0.0.0:8080
5. Server MUST handle concurrent connections (multiple bots connect simultaneously)