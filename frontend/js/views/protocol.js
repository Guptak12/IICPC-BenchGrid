/* ========================================================================
   IICPC BenchGrid — Protocol Specification View
   ======================================================================== */

export function renderProtocol() {
    return `
        <div class="protocol-container" style="max-width:960px; margin: 40px auto; padding: 20px;">
            <!-- Back navigation -->
            <div style="margin-bottom:25px;">
                <a href="#/arena" class="nav-back-link" style="text-decoration:none; display:inline-flex; align-items:center; gap:8px; font-size:12px; font-family:var(--font-mono); color:var(--text-secondary); transition:color var(--duration-fast) var(--ease-out-expo);">
                    <i class="fa-solid fa-arrow-left"></i>
                    <span>Back to Arenas</span>
                </a>
            </div>

            <!-- Page Title -->
            <div style="margin-bottom:30px;">
                <h2 style="font-family:var(--font-display); font-size:1.8rem; font-weight:700; color:var(--text-primary); margin-bottom:5px;">Matching Engine Protocol Specifications</h2>
                <p style="color:var(--text-secondary); font-size:0.875rem;">Interface contracts, port configurations, and payload schemas for all four supported matching engine protocols.</p>
            </div>

            <!-- Protocol Navigation Tabs -->
            <div class="protocol-tabs" style="display: flex; gap: 8px; margin-bottom: 25px; border-bottom: 1px solid var(--border-subtle); padding-bottom: 12px; overflow-x: auto;">
                <button class="protocol-tab-btn active" data-target="tab-tcp" style="background: var(--accent-muted); border: none; padding: 8px 18px; color: var(--accent); cursor: pointer; font-family: var(--font-display); font-size: 13.5px; font-weight: 600; border-radius: 6px; transition: all 0.2s; white-space: nowrap;">
                    <i class="fa-solid fa-network-wired" style="margin-right: 6px;"></i>TCP / Protobuf
                </button>
                <button class="protocol-tab-btn" data-target="tab-ws" style="background: transparent; border: none; padding: 8px 18px; color: var(--text-secondary); cursor: pointer; font-family: var(--font-display); font-size: 13.5px; font-weight: 600; border-radius: 6px; transition: all 0.2s; white-space: nowrap;">
                    <i class="fa-solid fa-bolt" style="margin-right: 6px;"></i>WebSocket (WS)
                </button>
                <button class="protocol-tab-btn" data-target="tab-rest" style="background: transparent; border: none; padding: 8px 18px; color: var(--text-secondary); cursor: pointer; font-family: var(--font-display); font-size: 13.5px; font-weight: 600; border-radius: 6px; transition: all 0.2s; white-space: nowrap;">
                    <i class="fa-solid fa-globe" style="margin-right: 6px;"></i>HTTP REST / SSE
                </button>
                <button class="protocol-tab-btn" data-target="tab-fix" style="background: transparent; border: none; padding: 8px 18px; color: var(--text-secondary); cursor: pointer; font-family: var(--font-display); font-size: 13.5px; font-weight: 600; border-radius: 6px; transition: all 0.2s; white-space: nowrap;">
                    <i class="fa-solid fa-file-invoice" style="margin-right: 6px;"></i>FIX 4.4 Protocol
                </button>
            </div>

            <!-- WARNING CALLOUT -->
            <div class="alert-amber" style="margin-bottom:30px; padding:16px; border-radius:4px; display:flex; align-items:start; gap:12px; background-color: rgba(245, 158, 11, 0.08); border: 1px solid rgba(245, 158, 11, 0.2);">
                <i class="fa-solid fa-triangle-exclamation" style="color:rgb(245, 158, 11); font-size:16px; margin-top:3px;"></i>
                <div style="font-size:0.85rem; line-height:1.5;">
                    <strong style="color:var(--text-primary); display:block; margin-bottom:4px;">Warning: Env Variable Setup</strong>
                    Contestant matching engines must expose **Port 8000** and bind to **0.0.0.0**. The platform loader identifies your protocol using the container's environment configuration. Be sure your Dockerfile specifies: <code style="font-family:var(--font-mono); color:var(--accent);">ENV ENGINE_PROTOCOL=WS|REST|FIX|TCP_PROTOBUF</code>.
                </div>
            </div>

            <!-- ==========================================
                 TAB 1: TCP/PROTOBUF
                 ========================================== -->
            <div id="tab-tcp" class="protocol-section" style="display: block;">
                <!-- Concept Card -->
                <section class="card" style="padding:28px; border:1px solid var(--border-subtle); margin-bottom:28px; border-radius:4px;">
                    <h3 style="font-family:var(--font-display); font-size:1.15rem; font-weight:700; color:var(--text-primary); margin-bottom:15px;">
                        [+] Length-Prefixed TCP Protocol Buffer Stream
                    </h3>
                    <p style="color:var(--text-secondary); font-size:0.85rem; line-height:1.6; margin-bottom:20px;">
                        The platform establishes direct TCP connections to port 8000. All messages are preceded by a 4-byte Little-Endian unsigned integer defining the size of the following Protocol Buffer payload.
                    </p>

                    <!-- Binary Frame Diagram -->
                    <div class="dark-terminal" style="font-family:var(--font-mono); font-size:11px; border:1px solid var(--border-strong); border-radius:4px; overflow-x:auto; padding:15px; margin-bottom:20px; text-align:center;">
                        +---------------------------+-----------------------------------+<br>
                        |  Length Prefix (4 bytes)  | Protobuf Binary Payload (N bytes) |<br>
                        |  Little-Endian uint32     | (Order or ExecutionReport)        |<br>
                        +---------------------------+-----------------------------------+
                    </div>

                    <div style="font-family:var(--font-mono); font-size:11px; display:flex; flex-direction:column; gap:8px; color:var(--text-secondary);">
                        <div style="display:flex; justify-content:space-between; border-bottom:1px solid var(--border-subtle); padding-bottom:6px;"><span>Port Connection</span><strong style="color:var(--text-primary);">8000</strong></div>
                        <div style="display:flex; justify-content:space-between; border-bottom:1px solid var(--border-subtle); padding-bottom:6px;"><span>Framing Type</span><strong style="color:var(--text-primary);">Length-Prefixed Little-Endian Binary</strong></div>
                        <div style="display:flex; justify-content:space-between; padding-bottom:6px;"><span>Env Config (Dockerfile)</span><strong style="color:var(--text-primary);">ENV ENGINE_PROTOCOL=TCP_PROTOBUF</strong></div>
                    </div>
                </section>

                <!-- Protobuf definitions -->
                <section class="card" style="padding:28px; border:1px solid var(--border-subtle); margin-bottom:28px; border-radius:4px;">
                    <h3 style="font-family:var(--font-display); font-size:1.15rem; font-weight:700; color:var(--text-primary); margin-bottom:15px;">
                        [+] Protobuf Schema Schemas
                    </h3>
                    
                    <div style="margin-bottom:20px;">
                        <div style="display:flex; justify-content:space-between; align-items:center; margin-bottom:6px;">
                            <span style="font-family:var(--font-mono); font-size:11px; text-transform:uppercase; color:var(--text-secondary);">Order Schema (Platform &rarr; Engine)</span>
                            <button class="btn btn-outline copy-btn" data-target="order-proto" style="padding:2px 8px; font-size:10px; width:auto; height:20px;">Copy</button>
                        </div>
                        <pre id="order-proto" class="dark-terminal console-box" style="font-size:11px; max-height:220px; overflow-y:auto; border-radius:4px; padding:15px; margin:0; border:1px solid var(--border-strong);">syntax = "proto3";

enum OrderType {
    LIMIT = 0;
    MARKET = 1;
    CANCEL = 2;
}

enum Side {
    BUY = 0;
    SELL = 1;
}

message Order {
    uint64 bot_id = 1;
    uint64 order_id = 2;
    OrderType type = 3;
    Side side = 4;
    int64 price = 5;          // Scaled by 100 ($10.50 = 1050)
    uint64 quantity = 6;      // Scaled by 100 (7.5 units = 750)
}</pre>
                    </div>

                    <div>
                        <div style="display:flex; justify-content:space-between; align-items:center; margin-bottom:6px;">
                            <span style="font-family:var(--font-mono); font-size:11px; text-transform:uppercase; color:var(--text-secondary);">ExecutionReport Schema (Engine &rarr; Platform)</span>
                            <button class="btn btn-outline copy-btn" data-target="report-proto" style="padding:2px 8px; font-size:10px; width:auto; height:20px;">Copy</button>
                        </div>
                        <pre id="report-proto" class="dark-terminal console-box" style="font-size:11px; max-height:220px; overflow-y:auto; border-radius:4px; padding:15px; margin:0; border:1px solid var(--border-strong);">syntax = "proto3";

enum ExecutionStatus {
    ACCEPTED = 0;
    FILLED = 1;
    PARTIAL = 2;
    REJECTED = 3;
    CANCELLED = 4;
}

message ExecutionReport {
    uint64 order_id = 1;
    ExecutionStatus status = 2;
    uint64 filled_qty = 3;
    int64 filled_price = 4;
    uint64 engine_seq_id = 5;
    uint64 processing_ns = 6; // Matching latency inside engine
    uint64 matched_with = 7;  // Counterparty order_id
}</pre>
                    </div>
                </section>
            </div>

            <!-- ==========================================
                 TAB 2: WEBSOCKET (WS)
                 ========================================== -->
            <div id="tab-ws" class="protocol-section" style="display: none;">
                <section class="card" style="padding:28px; border:1px solid var(--border-subtle); margin-bottom:28px; border-radius:4px;">
                    <h3 style="font-family:var(--font-display); font-size:1.15rem; font-weight:700; color:var(--text-primary); margin-bottom:15px;">
                        [+] WebSocket Real-Time Handshake
                    </h3>
                    <p style="color:var(--text-secondary); font-size:0.85rem; line-height:1.6; margin-bottom:20px;">
                        The platform establishes a persistent WebSocket connection to the root path (<code style="font-family:var(--font-mono); color:var(--accent);">ws://&lt;ip&gt;:8000/</code>). Payload transfers use WebSocket text messages containing JSON payloads.
                    </p>

                    <div style="font-family:var(--font-mono); font-size:11px; display:flex; flex-direction:column; gap:8px; color:var(--text-secondary); margin-bottom:20px;">
                        <div style="display:flex; justify-content:space-between; border-bottom:1px solid var(--border-subtle); padding-bottom:6px;"><span>WebSocket URL</span><strong style="color:var(--text-primary);">ws://&lt;ip&gt;:8000/</strong></div>
                        <div style="display:flex; justify-content:space-between; border-bottom:1px solid var(--border-subtle); padding-bottom:6px;"><span>Message Encoding</span><strong style="color:var(--text-primary);">JSON Text Frames</strong></div>
                        <div style="display:flex; justify-content:space-between; padding-bottom:6px;"><span>Env Config (Dockerfile)</span><strong style="color:var(--text-primary);">ENV ENGINE_PROTOCOL=WS</strong></div>
                    </div>
                </section>

                <section class="card" style="padding:28px; border:1px solid var(--border-subtle); margin-bottom:28px; border-radius:4px;">
                    <h3 style="font-family:var(--font-display); font-size:1.15rem; font-weight:700; color:var(--text-primary); margin-bottom:15px;">
                        [+] WebSocket Message JSON Contracts
                    </h3>

                    <div style="margin-bottom:20px;">
                        <div style="display:flex; justify-content:space-between; align-items:center; margin-bottom:6px;">
                            <span style="font-family:var(--font-mono); font-size:11px; text-transform:uppercase; color:var(--text-secondary);">Order JSON (Platform &rarr; Engine)</span>
                            <button class="btn btn-outline copy-btn" data-target="order-json-ws" style="padding:2px 8px; font-size:10px; width:auto; height:20px;">Copy</button>
                        </div>
                        <pre id="order-json-ws" class="dark-terminal console-box" style="font-size:11px; border-radius:4px; padding:15px; margin:0; border:1px solid var(--border-strong);">{
  "bot_id": 1,
  "order_id": 100529,
  "type": "LIMIT",      // "LIMIT", "MARKET", "CANCEL"
  "side": "BUY",        // "BUY", "SELL"
  "price": 10250,       // Scaled by 100 ($102.50)
  "quantity": 500       // Scaled by 100 (5.00 units)
}</pre>
                    </div>

                    <div>
                        <div style="display:flex; justify-content:space-between; align-items:center; margin-bottom:6px;">
                            <span style="font-family:var(--font-mono); font-size:11px; text-transform:uppercase; color:var(--text-secondary);">ExecutionReport JSON (Engine &rarr; Platform)</span>
                            <button class="btn btn-outline copy-btn" data-target="report-json-ws" style="padding:2px 8px; font-size:10px; width:auto; height:20px;">Copy</button>
                        </div>
                        <pre id="report-json-ws" class="dark-terminal console-box" style="font-size:11px; border-radius:4px; padding:15px; margin:0; border:1px solid var(--border-strong);">{
  "order_id": 100529,
  "status": "ACCEPTED", // "ACCEPTED", "FILLED", "PARTIAL", "REJECTED", "CANCELLED"
  "filled_qty": 0,
  "filled_price": 0,
  "engine_seq_id": 1,
  "processing_ns": 450, // Nano-seconds matching latency
  "matched_with": 0
}</pre>
                    </div>
                </section>
            </div>

            <!-- ==========================================
                 TAB 3: HTTP REST & SSE
                 ========================================== -->
            <div id="tab-rest" class="protocol-section" style="display: none;">
                <section class="card" style="padding:28px; border:1px solid var(--border-subtle); margin-bottom:28px; border-radius:4px;">
                    <h3 style="font-family:var(--font-display); font-size:1.15rem; font-weight:700; color:var(--text-primary); margin-bottom:15px;">
                        [+] HTTP REST Submission & SSE Broadcaster
                    </h3>
                    <p style="color:var(--text-secondary); font-size:0.85rem; line-height:1.6; margin-bottom:20px;">
                        A dual-channel approach where the platform sends orders over HTTP REST endpoints, and subscribes to a persistent Server-Sent Events (SSE) stream to consume executions.
                    </p>

                    <div style="font-family:var(--font-mono); font-size:11px; display:flex; flex-direction:column; gap:8px; color:var(--text-secondary); margin-bottom:20px;">
                        <div style="display:flex; justify-content:space-between; border-bottom:1px solid var(--border-subtle); padding-bottom:6px;"><span>Order Endpoint</span><strong style="color:var(--text-primary);">POST /api/v1/orders</strong></div>
                        <div style="display:flex; justify-content:space-between; border-bottom:1px solid var(--border-subtle); padding-bottom:6px;"><span>Execution Stream</span><strong style="color:var(--text-primary);">GET /api/v1/events</strong></div>
                        <div style="display:flex; justify-content:space-between; border-bottom:1px solid var(--border-subtle); padding-bottom:6px;"><span>SSE Event Headers</span><strong style="color:var(--text-primary);">Content-Type: text/event-stream; Cache-Control: no-cache</strong></div>
                        <div style="display:flex; justify-content:space-between; padding-bottom:6px;"><span>Env Config (Dockerfile)</span><strong style="color:var(--text-primary);">ENV ENGINE_PROTOCOL=REST</strong></div>
                    </div>
                </section>

                <section class="card" style="padding:28px; border:1px solid var(--border-subtle); margin-bottom:28px; border-radius:4px;">
                    <h3 style="font-family:var(--font-display); font-size:1.15rem; font-weight:700; color:var(--text-primary); margin-bottom:15px;">
                        [+] REST / SSE Schema & Format
                    </h3>

                    <div style="margin-bottom:20px;">
                        <span style="font-family:var(--font-mono); font-size:11px; text-transform:uppercase; color:var(--text-secondary); display:block; margin-bottom:6px;">1. Order REST Payload (POST body)</span>
                        <pre class="dark-terminal console-box" style="font-size:11px; border-radius:4px; padding:15px; margin:0; border:1px solid var(--border-strong);">{
  "bot_id": 1,
  "order_id": 200501,
  "type": "LIMIT",
  "side": "SELL",
  "price": 10300,
  "quantity": 100
}</pre>
                    </div>

                    <div>
                        <span style="font-family:var(--font-mono); font-size:11px; text-transform:uppercase; color:var(--text-secondary); display:block; margin-bottom:6px;">2. SSE Execution Format (GET Stream chunk)</span>
                        <pre class="dark-terminal console-box" style="font-size:11px; border-radius:4px; padding:15px; margin:0; border:1px solid var(--border-strong);">data: {"order_id": 200501, "status": "ACCEPTED", "filled_qty": 0, "filled_price": 0, "engine_seq_id": 12, "processing_ns": 120, "matched_with": 0}

data: {"order_id": 200501, "status": "FILLED", "filled_qty": 100, "filled_price": 10300, "engine_seq_id": 13, "processing_ns": 320, "matched_with": 200388}</pre>
                    </div>
                </section>
            </div>

            <!-- ==========================================
                 TAB 4: FIX 4.4
                 ========================================== -->
            <div id="tab-fix" class="protocol-section" style="display: none;">
                <section class="card" style="padding:28px; border:1px solid var(--border-subtle); margin-bottom:28px; border-radius:4px;">
                    <h3 style="font-family:var(--font-display); font-size:1.15rem; font-weight:700; color:var(--text-primary); margin-bottom:15px;">
                        [+] Financial Information eXchange (FIX 4.4)
                    </h3>
                    <p style="color:var(--text-secondary); font-size:0.85rem; line-height:1.6; margin-bottom:20px;">
                        Standard FIX 4.4 sessions over raw TCP. The engine must support the Logon handshake flow, track sequences, and format execution metrics using FIX tags, including custom performance metadata.
                    </p>

                    <div style="font-family:var(--font-mono); font-size:11px; display:flex; flex-direction:column; gap:8px; color:var(--text-secondary); margin-bottom:20px;">
                        <div style="display:flex; justify-content:space-between; border-bottom:1px solid var(--border-subtle); padding-bottom:6px;"><span>FIX Protocol Version</span><strong style="color:var(--text-primary);">FIX.4.4</strong></div>
                        <div style="display:flex; justify-content:space-between; border-bottom:1px solid var(--border-subtle); padding-bottom:6px;"><span>Logon MsgType</span><strong style="color:var(--text-primary);">35=A (Bidirectional Handshake)</strong></div>
                        <div style="display:flex; justify-content:space-between; border-bottom:1px solid var(--border-subtle); padding-bottom:6px;"><span>Delimiter Code</span><strong style="color:var(--text-primary);">SOH (\x01)</strong></div>
                        <div style="display:flex; justify-content:space-between; padding-bottom:6px;"><span>Env Config (Dockerfile)</span><strong style="color:var(--text-primary);">ENV ENGINE_PROTOCOL=FIX</strong></div>
                    </div>
                </section>

                <section class="card" style="padding:28px; border:1px solid var(--border-subtle); margin-bottom:28px; border-radius:4px;">
                    <h3 style="font-family:var(--font-display); font-size:1.15rem; font-weight:700; color:var(--text-primary); margin-bottom:15px;">
                        [+] FIX Field Tag Mappings
                    </h3>

                    <div style="overflow-x: auto; margin-bottom: 20px;">
                        <table style="width: 100%; border-collapse: collapse; font-size: 12px; color: var(--text-secondary); text-align: left;">
                            <thead>
                                <tr style="border-bottom: 2px solid var(--border-strong); color: var(--text-primary);">
                                    <th style="padding: 10px 5px;">Tag</th>
                                    <th style="padding: 10px 5px;">Name</th>
                                    <th style="padding: 10px 5px;">Values / Context</th>
                                </tr>
                            </thead>
                            <tbody>
                                <tr style="border-bottom: 1px solid var(--border-subtle);">
                                    <td style="padding: 8px 5px; font-family: var(--font-mono); color: var(--accent);">8</td>
                                    <td style="padding: 8px 5px;">BeginString</td>
                                    <td style="padding: 8px 5px;">FIX.4.4</td>
                                </tr>
                                <tr style="border-bottom: 1px solid var(--border-subtle);">
                                    <td style="padding: 8px 5px; font-family: var(--font-mono); color: var(--accent);">35</td>
                                    <td style="padding: 8px 5px;">MsgType</td>
                                    <td style="padding: 8px 5px;">A=Logon, D=New Order Single, F=Order Cancel Request, 8=Execution Report</td>
                                </tr>
                                <tr style="border-bottom: 1px solid var(--border-subtle);">
                                    <td style="padding: 8px 5px; font-family: var(--font-mono); color: var(--accent);">11</td>
                                    <td style="padding: 8px 5px;">ClOrdID</td>
                                    <td style="padding: 8px 5px;">Uniquely identifies the order</td>
                                </tr>
                                <tr style="border-bottom: 1px solid var(--border-subtle);">
                                    <td style="padding: 8px 5px; font-family: var(--font-mono); color: var(--accent);">41</td>
                                    <td style="padding: 8px 5px;">OrigClOrdID</td>
                                    <td style="padding: 8px 5px;">Order ID to cancel (used in MsgType=F)</td>
                                </tr>
                                <tr style="border-bottom: 1px solid var(--border-subtle);">
                                    <td style="padding: 8px 5px; font-family: var(--font-mono); color: var(--accent);">39</td>
                                    <td style="padding: 8px 5px;">OrdStatus</td>
                                    <td style="padding: 8px 5px;">0=New/Accepted, 1=Partially Filled, 2=Filled, 4=Cancelled, 8=Rejected</td>
                                </tr>
                                <tr style="border-bottom: 1px solid var(--border-subtle);">
                                    <td style="padding: 8px 5px; font-family: var(--font-mono); color: var(--accent);">54</td>
                                    <td style="padding: 8px 5px;">Side</td>
                                    <td style="padding: 8px 5px;">1=Buy, 2=Sell</td>
                                </tr>
                                <tr style="border-bottom: 1px solid var(--border-subtle);">
                                    <td style="padding: 8px 5px; font-family: var(--font-mono); color: var(--accent);">38</td>
                                    <td style="padding: 8px 5px;">OrderQty</td>
                                    <td style="padding: 8px 5px;">Quantity, scaled by 100</td>
                                </tr>
                                <tr style="border-bottom: 1px solid var(--border-subtle);">
                                    <td style="padding: 8px 5px; font-family: var(--font-mono); color: var(--accent);">44</td>
                                    <td style="padding: 8px 5px;">Price</td>
                                    <td style="padding: 8px 5px;">Price, scaled by 100</td>
                                </tr>
                                <tr style="border-bottom: 1px solid var(--border-subtle);">
                                    <td style="padding: 8px 5px; font-family: var(--font-mono); color: var(--accent);">1</td>
                                    <td style="padding: 8px 5px;">Account</td>
                                    <td style="padding: 8px 5px;">Bot numeric identifier</td>
                                </tr>
                                <tr style="border-bottom: 1px solid var(--border-subtle);">
                                    <td style="padding: 8px 5px; font-family: var(--font-mono); color: var(--accent);">9000</td>
                                    <td style="padding: 8px 5px;">Latency (Custom)</td>
                                    <td style="padding: 8px 5px;">Internal processing latency in nanoseconds</td>
                                </tr>
                                <tr>
                                    <td style="padding: 8px 5px; font-family: var(--font-mono); color: var(--accent);">9001</td>
                                    <td style="padding: 8px 5px;">Counterparty ID</td>
                                    <td style="padding: 8px 5px;">Matched counterparty Order ID</td>
                                </tr>
                            </tbody>
                        </table>
                    </div>
                </section>
            </div>

            <!-- Important Rules & Constraints -->
            <section class="card" style="padding:28px; border:1px solid var(--border-subtle); margin-bottom:10px; border-radius:4px;">
                <h3 style="font-family:var(--font-display); font-size:1.15rem; font-weight:700; color:var(--text-primary); margin-bottom:15px; display:flex; align-items:center; gap:8px;">
                    <i class="fa-solid fa-circle-check" style="color:var(--accent);"></i>
                    <span>[+] Common Compliance Rules Checklist</span>
                </h3>

                <ul style="color:var(--text-secondary); font-size:0.85rem; padding-left:20px; display:flex; flex-direction:column; gap:12px; line-height:1.6; list-style:none;">
                    <li>
                        <strong style="color:var(--text-primary);">[+] Strict Port Binding:</strong> Containers must listen on <code style="font-family:var(--font-mono); color:var(--accent);">0.0.0.0:8000</code>. Binding to localhost limits external communication.
                    </li>
                    <li>
                        <strong style="color:var(--text-primary);">[+] Multi-threading:</strong> The matching engine must support concurrent TCP/HTTP/WebSocket connections as bots issue trades simultaneously.
                    </li>
                    <li>
                        <strong style="color:var(--text-primary);">[+] Self-Crossing Prevention:</strong> To prevent wash trading, orders placed by the same bot must not cross. Skip crossing orders based on the bot identifier (e.g. `bot_id` or `Account`).
                    </li>
                    <li>
                        <strong style="color:var(--text-primary);">[+] Echoing Order IDs:</strong> Execution reports must match the order's unique client identifier (e.g., `order_id` or `ClOrdID`) exactly.
                    </li>
                </ul>
            </section>
        </div>
    `;
}

export function hydrateProtocol() {
    // Tab switching logic
    const tabs = document.querySelectorAll('.protocol-tab-btn');
    const sections = document.querySelectorAll('.protocol-section');
    
    tabs.forEach(tab => {
        tab.addEventListener('click', () => {
            tabs.forEach(t => {
                t.classList.remove('active');
                t.style.backgroundColor = 'transparent';
                t.style.color = 'var(--text-secondary)';
            });
            sections.forEach(s => s.style.display = 'none');
            
            tab.classList.add('active');
            tab.style.backgroundColor = 'var(--accent-muted)';
            tab.style.color = 'var(--accent)';
            
            const target = tab.dataset.target;
            document.getElementById(target).style.display = 'block';
        });
    });

    // Copy buttons logic
    document.querySelectorAll('.copy-btn').forEach(btn => {
        btn.addEventListener('click', () => {
            const targetId = btn.dataset.target;
            const pre = document.getElementById(targetId);
            if (!pre) return;
            
            navigator.clipboard.writeText(pre.textContent).then(() => {
                const originalText = btn.textContent;
                btn.textContent = 'Copied!';
                btn.style.color = 'var(--accent)';
                btn.style.borderColor = 'var(--accent)';
                setTimeout(() => {
                    btn.textContent = originalText;
                    btn.style.color = '';
                    btn.style.borderColor = '';
                }, 1500);
            }).catch(err => {
                console.error('Failed to copy text: ', err);
            });
        });
    });
}
