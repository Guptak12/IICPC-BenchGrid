/* ========================================================================
   IICPC BenchGrid — Protocol Specification View
   ======================================================================== */

export function renderProtocol() {
    return `
        <div class="protocol-container" style="max-width:900px; margin: 40px auto; padding: 20px;">
            <!-- Back navigation -->
            <div style="margin-bottom:25px;">
                <a href="#/arena" class="nav-back-link" style="text-decoration:none; display:inline-flex; align-items:center; gap:8px; font-size:12px; font-family:var(--font-mono); color:var(--text-secondary); transition:color var(--duration-fast) var(--ease-out-expo);">
                    <i class="fa-solid fa-arrow-left"></i>
                    <span>Back to Arenas</span>
                </a>
            </div>

            <!-- Page Title -->
            <div style="margin-bottom:35px;">
                <h2 style="font-family:var(--font-display); font-size:1.6rem; font-weight:700; color:var(--text-primary); margin-bottom:5px;">TCP & Protobuf Spec</h2>
                <p style="color:var(--text-secondary); font-size:0.85rem;">Interface agreements, wire framing, and verification protocols for contestant matching engines.</p>
            </div>

            <!-- Warning about GitHub OAuth and Zip uploads -->
            <div class="alert-amber" style="margin-bottom:30px; padding:16px; border-radius:4px; display:flex; align-items:start; gap:12px;">
                <i class="fa-solid fa-triangle-exclamation" style="color:var(--status-warning); font-size:16px; margin-top:3px;"></i>
                <div style="font-size:0.85rem; line-height:1.5;">
                    <strong style="color:var(--text-primary); display:block; margin-bottom:4px;">[!] Warning: GitHub Rate Limiting Policies</strong>
                    During peak contest periods, parallel cloning triggers rate limiting on GitHub's API. To guarantee successful compile runs under deadline pressure, contestants are **strongly encouraged to upload a standalone ZIP archive** containing their source files and a Dockerfile.
                </div>
            </div>

            <!-- BYOS and TCP Segment -->
            <section class="card" style="padding:28px; border:1px solid var(--border-subtle); margin-bottom:28px; border-radius:4px;">
                <h3 style="font-family:var(--font-display); font-size:1.15rem; font-weight:700; color:var(--text-primary); margin-bottom:15px; display:flex; align-items:center; gap:8px;">
                    <i class="fa-solid fa-network-wired" style="color:var(--accent);"></i>
                    <span>[+] Bring Your Own Server (BYOS) TCP Contract</span>
                </h3>
                
                <p style="color:var(--text-secondary); font-size:0.85rem; line-height:1.6; margin-bottom:20px;">
                    Contestants submit a containerized server (exposing **Port 8000**) that listens for raw TCP streams from the platform Bot Fleet. The framing enforces a 4-byte Little-Endian unsigned integer prefix specifying the payload size.
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
                    <div style="display:flex; justify-content:space-between; border-bottom:1px solid var(--border-subtle); padding-bottom:6px;"><span>Protocol Standard</span><strong style="color:var(--text-primary);">Raw TCP Sockets</strong></div>
                    <div style="display:flex; justify-content:space-between; padding-bottom:6px;"><span>Framing Type</span><strong style="color:var(--text-primary);">Length Prefixed (Little-Endian uint32)</strong></div>
                </div>
            </section>

            <!-- Protobuf definitions -->
            <section class="card" style="padding:28px; border:1px solid var(--border-subtle); margin-bottom:28px; border-radius:4px;">
                <h3 style="font-family:var(--font-display); font-size:1.15rem; font-weight:700; color:var(--text-primary); margin-bottom:15px; display:flex; align-items:center; gap:8px;">
                    <i class="fa-solid fa-code" style="color:var(--accent);"></i>
                    <span>[+] Protocol Buffer Schema Schemas</span>
                </h3>
                
                <p style="color:var(--text-secondary); font-size:0.85rem; line-height:1.6; margin-bottom:20px;">
                    Serializing and deserializing matching transactions must follow the schemas below. Price and quantity metrics are scaled by a factor of 100 (e.g. $100.50 is transmitted as 10050).
                </p>

                <!-- Order protobuf -->
                <div style="margin-bottom:20px;">
                    <div style="display:flex; justify-content:space-between; align-items:center; margin-bottom:6px;">
                        <span style="font-family:var(--font-mono); font-size:11px; text-transform:uppercase; color:var(--text-secondary);">1. Order (Platform &rarr; Contestant)</span>
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
    uint64 bot_id = 1;        // Uniquely identifies the bot placing the order
    uint64 order_id = 2;      // Unsigned globally unique Order ID
    OrderType type = 3;
    Side side = 4;
    int64 price = 5;          // Scaled by 100 (e.g. $100.50 = 10050), 0 for MARKET
    uint64 quantity = 6;      // Scaled by 100 (e.g. 7.30 units = 730)
}</pre>
                </div>

                <!-- Execution Report protobuf -->
                <div>
                    <div style="display:flex; justify-content:space-between; align-items:center; margin-bottom:6px;">
                        <span style="font-family:var(--font-mono); font-size:11px; text-transform:uppercase; color:var(--text-secondary);">2. Execution Report (Contestant &rarr; Platform)</span>
                        <button class="btn btn-outline copy-btn" data-target="report-proto" style="padding:2px 8px; font-size:10px; width:auto; height:20px;">Copy</button>
                    </div>
                    <pre id="report-proto" class="dark-terminal console-box" style="font-size:11px; max-height:250px; overflow-y:auto; border-radius:4px; padding:15px; margin:0; border:1px solid var(--border-strong);">syntax = "proto3";

enum ExecutionStatus {
    ACCEPTED = 0;
    FILLED = 1;
    PARTIAL = 2;
    REJECTED = 3;
    CANCELLED = 4;
}

message ExecutionReport {
    uint64 order_id = 1;      // MUST match the order_id of the corresponding Order
    ExecutionStatus status = 2;
    uint64 filled_qty = 3;    // Scaled by 100
    int64 filled_price = 4;   // Scaled by 100
    uint64 engine_seq_id = 5; // Monotonically increasing sequence ID generated by matching engine
    uint64 processing_ns = 6; // Matching engine internal latency in nanoseconds (excluding network I/O)
    uint64 matched_with = 7;  // Counterparty order ID for shadow book priority validator check
}</pre>
                </div>
            </section>

            <!-- Important Rules & Constraints -->
            <section class="card" style="padding:28px; border:1px solid var(--border-subtle); margin-bottom:10px; border-radius:4px;">
                <h3 style="font-family:var(--font-display); font-size:1.15rem; font-weight:700; color:var(--text-primary); margin-bottom:15px; display:flex; align-items:center; gap:8px;">
                    <i class="fa-solid fa-circle-check" style="color:var(--accent);"></i>
                    <span>[+] Compliance & Rules Checklist</span>
                </h3>

                <ul style="color:var(--text-secondary); font-size:0.85rem; padding-left:20px; display:flex; flex-direction:column; gap:12px; line-height:1.6; list-style:none;">
                    <li>
                        <strong style="color:var(--text-primary);">[+] Strict Port Binding:</strong> Containers must listen on <code style="font-family:var(--font-mono); color:var(--accent);">0.0.0.0:8000</code>. Binding to localhost limits external communication.
                    </li>
                    <li>
                        <strong style="color:var(--text-primary);">[+] Multi-threading:</strong> The matching engine must support concurrent TCP client connections as bots issue trades simultaneously.
                    </li>
                    <li>
                        <strong style="color:var(--text-primary);">[+] Self-Crossing Skid Skip:</strong> To prevent wash trading, orders placed by the same bot must not cross. Skip crossing orders based on <code style="font-family:var(--font-mono);">bot_id</code>.
                    </li>
                    <li>
                        <strong style="color:var(--text-primary);">[+] Echoing Order IDs:</strong> Execution reports must match the order's <code style="font-family:var(--font-mono);">order_id</code> exactly.
                    </li>
                    <li>
                        <strong style="color:var(--text-primary);">[+] Sequential Receipts:</strong> An immediate <code style="font-family:var(--font-mono);">ACCEPTED</code> or <code style="font-family:var(--font-mono);">REJECTED</code> report is expected. Subsequent events are issued asynchronously.
                    </li>
                </ul>
            </section>
        </div>
    `;
}

export function hydrateProtocol() {
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
