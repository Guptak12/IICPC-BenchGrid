package main

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>IICPC Benchmarking - Developer Diagnostics Dashboard</title>
    <link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/font-awesome/6.4.0/css/all.min.css">
    <style>
        :root {
            --bg-color: #0d1117;
            --card-bg: rgba(22, 27, 34, 0.7);
            --border-color: rgba(240, 246, 252, 0.1);
            --text-color: #c9d1d9;
            --text-muted: #8b949e;
            --accent-violet: #8a2be2;
            --accent-cyan: #00f5ff;
            --accent-green: #238636;
            --accent-red: #da3637;
            --accent-amber: #d29922;
            --glow-cyan: rgba(0, 245, 255, 0.15);
            --glow-violet: rgba(138, 43, 226, 0.15);
        }

        * {
            box-sizing: border-box;
            margin: 0;
            padding: 0;
        }

        body {
            background-color: var(--bg-color);
            color: var(--text-color);
            font-family: 'Segoe UI', -apple-system, BlinkMacSystemFont, Roboto, Helvetica, Arial, sans-serif;
            font-size: 14px;
            line-height: 1.5;
            padding: 20px;
            overflow-x: hidden;
        }

        header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            margin-bottom: 20px;
            padding: 10px 20px;
            background: var(--card-bg);
            border: 1px solid var(--border-color);
            border-radius: 8px;
            backdrop-filter: blur(10px);
        }

        .logo-container {
            display: flex;
            align-items: center;
            gap: 12px;
        }

        .logo-container i {
            font-size: 24px;
            color: var(--accent-cyan);
            text-shadow: 0 0 10px var(--glow-cyan);
        }

        .logo-container h1 {
            font-size: 20px;
            font-weight: 600;
            letter-spacing: 0.5px;
            background: linear-gradient(90deg, #00f5ff, #8a2be2);
            -webkit-background-clip: text;
            -webkit-text-fill-color: transparent;
        }

        .system-status {
            display: flex;
            align-items: center;
            gap: 20px;
        }

        .status-pill {
            display: flex;
            align-items: center;
            gap: 8px;
            padding: 6px 12px;
            border-radius: 20px;
            background: rgba(255, 255, 255, 0.05);
            border: 1px solid var(--border-color);
            font-size: 12px;
        }

        .status-pill.link-pill {
            transition: all 0.2s ease-in-out;
        }

        .status-pill.link-pill:hover {
            background: rgba(0, 245, 255, 0.15) !important;
            border-color: var(--accent-cyan) !important;
            box-shadow: 0 0 10px rgba(0, 245, 255, 0.3);
            transform: translateY(-1px);
        }

        .status-indicator {
            width: 8px;
            height: 8px;
            border-radius: 50%;
        }

        .status-ok { background-color: var(--accent-green); box-shadow: 0 0 8px var(--accent-green); }
        .status-warning { background-color: var(--accent-amber); box-shadow: 0 0 8px var(--accent-amber); }
        .status-error { background-color: var(--accent-red); box-shadow: 0 0 8px var(--accent-red); }

        /* KPI Stat Grid */
        .kpi-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
            gap: 20px;
            margin-bottom: 20px;
        }

        .kpi-card {
            background: var(--card-bg);
            border: 1px solid var(--border-color);
            border-radius: 8px;
            padding: 20px;
            display: flex;
            flex-direction: column;
            justify-content: space-between;
            position: relative;
            overflow: hidden;
            backdrop-filter: blur(10px);
            transition: transform 0.2s, border-color 0.2s;
        }

        .kpi-card:hover {
            transform: translateY(-2px);
            border-color: rgba(0, 245, 255, 0.3);
        }

        .kpi-title {
            font-size: 12px;
            text-transform: uppercase;
            color: var(--text-muted);
            letter-spacing: 1px;
            margin-bottom: 10px;
        }

        .kpi-value {
            font-size: 28px;
            font-weight: 700;
            color: #ffffff;
            margin-bottom: 5px;
        }

        .kpi-detail {
            font-size: 11px;
            color: var(--text-muted);
        }

        .kpi-card::after {
            content: '';
            position: absolute;
            bottom: 0;
            left: 0;
            width: 100%;
            height: 3px;
            background: linear-gradient(90deg, transparent, var(--accent-cyan), transparent);
        }

        /* Charts Section */
        .dashboard-grid {
            display: grid;
            grid-template-columns: repeat(2, 1fr);
            gap: 20px;
            margin-bottom: 20px;
        }

        @media (max-width: 900px) {
            .dashboard-grid {
                grid-template-columns: 1fr;
            }
        }

        .card {
            background: var(--card-bg);
            border: 1px solid var(--border-color);
            border-radius: 8px;
            padding: 20px;
            backdrop-filter: blur(10px);
        }

        .card-header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            margin-bottom: 15px;
            border-bottom: 1px solid var(--border-color);
            padding-bottom: 10px;
        }

        .card-title {
            font-size: 16px;
            font-weight: 600;
            display: flex;
            align-items: center;
            gap: 8px;
        }

        .chart-container {
            position: relative;
            height: 250px;
            width: 100%;
        }

        /* Stat Panel (Single Large Value like Grafana) */
        .stat-panel-content {
            display: flex;
            flex-direction: column;
            justify-content: center;
            align-items: center;
            height: 250px;
        }

        .stat-huge {
            font-size: 110px;
            font-weight: 800;
            color: var(--accent-green);
            text-shadow: 0 0 25px rgba(35, 134, 54, 0.4);
            line-height: 1;
        }

        .stat-label {
            font-size: 16px;
            text-transform: uppercase;
            letter-spacing: 2px;
            color: var(--text-muted);
            margin-top: 10px;
        }

        /* Control Deck & Table Section */
        .bottom-section {
            display: grid;
            grid-template-columns: 1fr 3fr;
            gap: 20px;
            margin-bottom: 20px;
        }

        @media (max-width: 1100px) {
            .bottom-section {
                grid-template-columns: 1fr;
            }
        }

        .control-deck {
            display: flex;
            flex-direction: column;
            gap: 15px;
        }

        .btn {
            display: inline-flex;
            align-items: center;
            justify-content: center;
            gap: 10px;
            padding: 12px 20px;
            font-size: 13px;
            font-weight: 600;
            border-radius: 6px;
            cursor: pointer;
            border: 1px solid transparent;
            transition: all 0.2s;
            text-decoration: none;
            width: 100%;
        }

        .btn-primary {
            background-color: var(--accent-violet);
            color: #ffffff;
            box-shadow: 0 0 10px rgba(138, 43, 226, 0.3);
        }

        .btn-primary:hover {
            background-color: #9b42f5;
            box-shadow: 0 0 15px rgba(138, 43, 226, 0.5);
        }

        .btn-danger {
            background-color: rgba(218, 54, 55, 0.1);
            color: #ff6b6b;
            border: 1px solid rgba(218, 54, 55, 0.4);
        }

        .btn-danger:hover {
            background-color: var(--accent-red);
            color: #ffffff;
            box-shadow: 0 0 10px rgba(218, 54, 55, 0.3);
        }

        .btn-outline {
            background-color: transparent;
            color: var(--text-color);
            border: 1px solid var(--border-color);
        }

        .btn-outline:hover {
            background-color: rgba(255, 255, 255, 0.05);
            border-color: var(--text-color);
        }

        /* Table CSS */
        .table-wrapper {
            overflow-x: auto;
            max-height: 400px;
        }

        table {
            width: 100%;
            border-collapse: collapse;
            text-align: left;
        }

        th {
            background-color: rgba(0, 0, 0, 0.2);
            color: var(--text-muted);
            font-weight: 600;
            font-size: 12px;
            text-transform: uppercase;
            letter-spacing: 0.5px;
            padding: 12px 16px;
            border-bottom: 1px solid var(--border-color);
            position: sticky;
            top: 0;
            z-index: 10;
        }

        td {
            padding: 12px 16px;
            border-bottom: 1px solid var(--border-color);
            color: var(--text-color);
        }

        tr {
            transition: background-color 0.15s;
            cursor: pointer;
        }

        tr:hover td {
            background-color: rgba(255, 255, 255, 0.02);
        }

        .badge {
            display: inline-block;
            padding: 2px 8px;
            border-radius: 12px;
            font-size: 11px;
            font-weight: 600;
            text-transform: uppercase;
        }

        .badge-queued { background-color: rgba(139, 148, 158, 0.15); color: var(--text-muted); }
        .badge-compiling { background-color: rgba(210, 153, 34, 0.15); color: var(--accent-amber); }
        .badge-running { background-color: rgba(0, 245, 255, 0.15); color: var(--accent-cyan); }
        .badge-completed { background-color: rgba(35, 134, 54, 0.15); color: var(--accent-green); }
        .badge-failed { background-color: rgba(218, 54, 55, 0.15); color: #ff6b6b; }

        /* Console Log Box */
        .console-card {
            margin-top: 20px;
        }

        .console-box {
            background-color: #05070a;
            border: 1px solid var(--border-color);
            border-radius: 6px;
            padding: 15px;
            font-family: 'Courier New', Courier, monospace;
            font-size: 12px;
            color: #00ff66;
            height: 180px;
            overflow-y: auto;
            white-space: pre-wrap;
            box-shadow: inset 0 0 10px rgba(0, 0, 0, 0.8);
        }

        .console-box .log-time {
            color: var(--text-muted);
            margin-right: 10px;
        }

        .console-box .log-err {
            color: #ff3333;
        }

        .console-box .log-warn {
            color: #ffb700;
        }

        /* Detail Slide-out Drawer */
        .drawer {
            position: fixed;
            top: 0;
            right: -550px;
            width: 550px;
            height: 100%;
            background-color: #161b22;
            border-left: 1px solid var(--border-color);
            box-shadow: -10px 0 30px rgba(0,0,0,0.5);
            z-index: 1000;
            transition: right 0.3s cubic-bezier(0.16, 1, 0.3, 1);
            display: flex;
            flex-direction: column;
        }

        .drawer.open {
            right: 0;
        }

        .drawer-header {
            padding: 20px;
            border-bottom: 1px solid var(--border-color);
            display: flex;
            justify-content: space-between;
            align-items: center;
        }

        .drawer-header h3 {
            font-size: 18px;
            font-weight: 600;
        }

        .drawer-close {
            background: transparent;
            border: none;
            color: var(--text-muted);
            font-size: 20px;
            cursor: pointer;
            transition: color 0.15s;
        }

        .drawer-close:hover {
            color: #ffffff;
        }

        .drawer-content {
            padding: 20px;
            overflow-y: auto;
            flex-grow: 1;
            display: flex;
            flex-direction: column;
            gap: 20px;
        }

        .drawer-section-title {
            font-size: 12px;
            text-transform: uppercase;
            color: var(--text-muted);
            letter-spacing: 1px;
            margin-bottom: 8px;
        }

        .json-pre {
            background-color: #0d1117;
            border: 1px solid var(--border-color);
            border-radius: 6px;
            padding: 15px;
            font-family: 'Courier New', Courier, monospace;
            font-size: 12px;
            overflow-x: auto;
            color: #58a6ff;
            max-height: 350px;
        }

        .code-box {
            background-color: #0d1117;
            border: 1px solid var(--border-color);
            border-radius: 6px;
            padding: 15px;
            font-family: 'Courier New', Courier, monospace;
            font-size: 12px;
            overflow-x: auto;
            color: #e6edf3;
            max-height: 250px;
        }

        .detail-row {
            display: flex;
            justify-content: space-between;
            border-bottom: 1px solid rgba(240, 246, 252, 0.05);
            padding: 8px 0;
        }

        .detail-row .label {
            color: var(--text-muted);
        }

        .detail-row .val {
            font-weight: 500;
        }

        /* Backdrop */
        .backdrop {
            position: fixed;
            top: 0;
            left: 0;
            width: 100%;
            height: 100%;
            background-color: rgba(0, 0, 0, 0.5);
            z-index: 999;
            display: none;
            backdrop-filter: blur(4px);
        }

        .backdrop.open {
            display: block;
        }
    </style>
</head>
<body>

    <!-- Header -->
    <header>
        <div class="logo-container">
            <i class="fa-solid fa-gauge-high"></i>
            <h1>IICPC Platform Diagnostics</h1>
        </div>
        <div class="system-status">
            <a href="/" target="_blank" class="status-pill link-pill" style="background: rgba(0, 245, 255, 0.15); border: 1px solid rgba(0, 245, 255, 0.4); text-decoration: none; color: var(--text-color); cursor: pointer; display: flex; align-items: center; gap: 8px;">
                <i class="fa-solid fa-trophy" style="color: var(--accent-cyan);"></i>
                <span>Contestant Arena</span>
            </a>
            <a href="http://localhost:3001" target="_blank" class="status-pill link-pill" style="background: rgba(210, 153, 34, 0.15); border: 1px solid rgba(210, 153, 34, 0.4); text-decoration: none; color: var(--text-color); cursor: pointer; display: flex; align-items: center; gap: 8px;">
                <i class="fa-solid fa-chart-line" style="color: var(--accent-amber);"></i>
                <span>Grafana Metrics</span>
            </a>
            <a href="http://localhost:9090" target="_blank" class="status-pill link-pill" style="background: rgba(138, 43, 226, 0.15); border: 1px solid rgba(138, 43, 226, 0.4); text-decoration: none; color: var(--text-color); cursor: pointer; display: flex; align-items: center; gap: 8px;">
                <i class="fa-solid fa-fire" style="color: var(--accent-violet);"></i>
                <span>Prometheus</span>
            </a>
            <div class="status-pill">
                <div class="status-indicator status-ok" id="db-led"></div>
                <span>Postgres</span>
            </div>
            <div class="status-pill">
                <div class="status-indicator status-ok" id="redis-led"></div>
                <span>Redis Broker</span>
            </div>
            <div class="status-pill" style="background: rgba(138, 43, 226, 0.15); border: 1px solid rgba(138, 43, 226, 0.4);">
                <i class="fa-solid fa-clock" style="color: var(--accent-violet);"></i>
                <span id="uptime-label">Uptime: 00:00:00</span>
            </div>
        </div>
    </header>

    <!-- KPI Grid -->
    <div class="kpi-grid">
        <div class="kpi-card">
            <span class="kpi-title">Total Submissions</span>
            <div class="kpi-value" id="kpi-total-subs">0</div>
            <div class="kpi-detail">Queued/Running: <span id="kpi-active-subs" style="color:var(--accent-cyan)">0</span></div>
        </div>
        <div class="kpi-card">
            <span class="kpi-title">Compilation Queue Depth</span>
            <div class="kpi-value" id="kpi-compile-queue">0</div>
            <div class="kpi-detail">Stream backlog length</div>
        </div>
        <div class="kpi-card">
            <span class="kpi-title">Testing Queue Depth</span>
            <div class="kpi-value" id="kpi-pretest-queue">0</div>
            <div class="kpi-detail">Active evaluation backlog</div>
        </div>
        <div class="kpi-card">
            <span class="kpi-title">Max Composite Score</span>
            <div class="kpi-value" id="kpi-max-score" style="color: var(--accent-green)">0.00</div>
            <div class="kpi-detail">Best engine correctness</div>
        </div>
    </div>

    <!-- Kubernetes Pod Status Grid -->
    <div class="card" style="margin-bottom: 20px;">
        <div class="card-header">
            <div class="card-title">
                <i class="fa-solid fa-cubes" style="color: var(--accent-cyan);"></i>
                <span>Kubernetes Cluster Pods Status</span>
            </div>
            <span id="cluster-mode-badge" class="badge" style="background-color: rgba(0, 245, 255, 0.15); color: var(--accent-cyan);">K8s In-Cluster Mode</span>
        </div>
        <div class="k8s-pod-grid" style="display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 20px; padding: 10px 0;">
            <div class="k8s-pod-card" style="background: rgba(255, 255, 255, 0.02); border: 1px solid var(--border-color); border-radius: 8px; padding: 15px; text-align: center; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 8px;">
                <i class="fa-solid fa-network-wired" style="font-size: 24px; color: var(--accent-cyan);"></i>
                <span style="font-size: 11px; text-transform: uppercase; color: var(--text-muted); letter-spacing: 0.5px;">Gateway Pods</span>
                <span id="pod-count-gateway" style="font-size: 32px; font-weight: 700; color: #ffffff;">0</span>
            </div>
            <div class="k8s-pod-card" style="background: rgba(255, 255, 255, 0.02); border: 1px solid var(--border-color); border-radius: 8px; padding: 15px; text-align: center; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 8px;">
                <i class="fa-solid fa-gears" style="font-size: 24px; color: var(--accent-amber);"></i>
                <span style="font-size: 11px; text-transform: uppercase; color: var(--text-muted); letter-spacing: 0.5px;">Compiler Pods</span>
                <span id="pod-count-compiler" style="font-size: 32px; font-weight: 700; color: #ffffff;">0</span>
            </div>
            <div class="k8s-pod-card" style="background: rgba(255, 255, 255, 0.02); border: 1px solid var(--border-color); border-radius: 8px; padding: 15px; text-align: center; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 8px;">
                <i class="fa-solid fa-vial-virus" style="font-size: 24px; color: var(--accent-violet);"></i>
                <span style="font-size: 11px; text-transform: uppercase; color: var(--text-muted); letter-spacing: 0.5px;">Testing Pods</span>
                <span id="pod-count-testing" style="font-size: 32px; font-weight: 700; color: #ffffff;">0</span>
            </div>
            <div class="k8s-pod-card" style="background: rgba(255, 255, 255, 0.02); border: 1px solid var(--border-color); border-radius: 8px; padding: 15px; text-align: center; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 8px;">
                <i class="fa-solid fa-database" style="font-size: 24px; color: var(--accent-green);"></i>
                <span style="font-size: 11px; text-transform: uppercase; color: var(--text-muted); letter-spacing: 0.5px;">Postgres Pods</span>
                <span id="pod-count-postgres" style="font-size: 32px; font-weight: 700; color: #ffffff;">0</span>
            </div>
            <div class="k8s-pod-card" style="background: rgba(255, 255, 255, 0.02); border: 1px solid var(--border-color); border-radius: 8px; padding: 15px; text-align: center; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 8px;">
                <i class="fa-solid fa-memory" style="font-size: 24px; color: var(--accent-red);"></i>
                <span style="font-size: 11px; text-transform: uppercase; color: var(--text-muted); letter-spacing: 0.5px;">Redis Broker</span>
                <span id="pod-count-redis" style="font-size: 32px; font-weight: 700; color: #ffffff;">0</span>
            </div>
            <div class="k8s-pod-card" style="background: linear-gradient(135deg, rgba(0, 245, 255, 0.05), rgba(138, 43, 226, 0.05)); border: 1px solid rgba(0, 245, 255, 0.2); border-radius: 8px; padding: 15px; text-align: center; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 8px;">
                <i class="fa-solid fa-cube" style="font-size: 24px; color: #ffffff; text-shadow: 0 0 10px rgba(255, 255, 255, 0.5);"></i>
                <span style="font-size: 11px; text-transform: uppercase; color: var(--text-muted); letter-spacing: 0.5px;">Total Pods</span>
                <span id="pod-count-total" style="font-size: 32px; font-weight: 700; color: #00f5ff; text-shadow: 0 0 10px var(--glow-cyan);">0</span>
            </div>
        </div>
    </div>

    <!-- Bottom Actions / Submissions Section -->
    <div class="bottom-section">
        <!-- Control Deck -->
        <div class="control-deck">
            <div class="card" style="height: 100%;">
                <div class="card-header" style="margin-bottom: 20px;">
                    <div class="card-title">
                        <i class="fa-solid fa-sliders"></i>
                        <span>Developer Deck</span>
                    </div>
                </div>
                <div style="display: flex; flex-direction: column; gap: 15px;">
                    <div style="margin-bottom: 5px;">
                        <label for="mock-engine-select" style="font-size: 11px; text-transform: uppercase; color: var(--text-muted); display: block; margin-bottom: 8px;">Mock Engine Type</label>
                        <select id="mock-engine-select" style="background-color: #0d1117; color: var(--text-color); border: 1px solid var(--border-color); border-radius: 6px; padding: 10px 12px; width: 100%; font-size: 13px; font-weight: 500; outline: none;">
                            <option value="go_optimized">Go (Optimized - 100% correct)</option>
                            <option value="python_slow">Python (Slow - 10ms delay)</option>
                            <option value="rust_crash">Rust (Crash after 10 orders)</option>
                            <option value="node_scammer">Node.js (Scammer/Anomalies)</option>
                            <option value="cpp_basic">C++ (Basic/Normal)</option>
                            <option value="go_ws">Go (WebSocket Protocol Mock)</option>
                            <option value="go_rest">Go (REST/SSE Protocol Mock)</option>
                            <option value="go_fix">Go (FIX Protocol Mock)</option>
                        </select>
                    </div>
					<button id="btn-mock-testing" class="btn btn-primary" onclick="triggerMockSubmission(false)" style="background-color: var(--accent-cyan); color: #0d1117; box-shadow: 0 0 10px rgba(0, 245, 255, 0.2); border-color: var(--accent-cyan); margin-bottom: 8px;">
						<i class="fa-solid fa-flask"></i> Mock Pretests
					</button>
					<button id="btn-mock-systest" class="btn btn-primary" onclick="triggerMockSubmission(true)" style="background-color: var(--accent-violet); color: #ffffff; box-shadow: 0 0 10px rgba(138, 43, 226, 0.2); border-color: var(--accent-violet);">
						<i class="fa-solid fa-bolt"></i> Mock System Tests
					</button>
					<button class="btn btn-outline" onclick="pollMetrics()" style="margin-top: 8px;">
						<i class="fa-solid fa-arrows-rotate"></i> Refresh Metrics
					</button>
                    <div style="margin-top: 20px; border-top: 1px solid var(--border-color); padding-top: 20px;">
                        <span style="font-size: 11px; text-transform: uppercase; color: var(--text-muted); display: block; margin-bottom: 10px;">Dangerous Actions</span>
                        <button class="btn btn-danger" onclick="confirmResetDB()">
                            <i class="fa-solid fa-trash-can"></i> Reset Environment Data
                        </button>
                    </div>
                </div>
            </div>
        </div>

        <!-- Recent Submissions Table -->
        <div class="card">
            <div class="card-header">
                <div class="card-title">
                    <i class="fa-solid fa-list-check"></i>
                    <span>Recent Submission Lifecycles</span>
                </div>
                <span style="font-size:11px; color:var(--text-muted)">Showing last 30 submissions (Click row for full telemetry)</span>
            </div>
            <div class="table-wrapper">
                <table>
                    <thead>
                        <tr>
                            <th>Contestant</th>
                            <th>Status</th>
                            <th>Verdict</th>
                            <th>Score</th>
                            <th>P99 (us)</th>
                            <th>TPS</th>
                            <th>Submitted</th>
                        </tr>
                    </thead>
                    <tbody id="submissions-tbody">
                        <tr>
                            <td colspan="7" style="text-align: center; color: var(--text-muted);">No submissions found. Use the control deck to trigger a mock run!</td>
                        </tr>
                    </tbody>
                </table>
            </div>
        </div>
    </div>

    <!-- Live Console Log -->
    <div class="card console-card">
        <div class="card-header">
            <div class="card-title">
                <i class="fa-solid fa-terminal" style="color: var(--accent-cyan);"></i>
                <span>Diagnostics Events Console</span>
            </div>
            <button class="btn btn-outline" style="padding: 4px 10px; font-size:11px; width:auto;" onclick="clearConsole()">Clear</button>
        </div>
        <div class="console-box" id="console-logs"></div>
    </div>

    <!-- Detail Drawer -->
    <div class="backdrop" id="drawer-backdrop" onclick="closeDrawer()"></div>
    <div class="drawer" id="drawer">
        <div class="drawer-header">
            <h3 id="drawer-title">Submission Telemetry</h3>
            <button class="drawer-close" onclick="closeDrawer()"><i class="fa-solid fa-xmark"></i></button>
        </div>
        <div class="drawer-content">
            <div>
                <div class="drawer-section-title">Submission Information</div>
                <div class="detail-row"><span class="label">Submission ID</span><span class="val" id="det-id">-</span></div>
                <div class="detail-row"><span class="label">Contestant</span><span class="val" id="det-contestant">-</span></div>
                <div class="detail-row"><span class="label">Status</span><span class="val" id="det-status">-</span></div>
                <div class="detail-row"><span class="label">Verdict</span><span class="val" id="det-verdict">-</span></div>
                <div class="detail-row"><span class="label">Composite Score</span><span class="val" id="det-score">-</span></div>
                <div class="detail-row"><span class="label">Submitted At</span><span class="val" id="det-time">-</span></div>
            </div>
            <div>
                <div class="drawer-section-title">Diagnostics Data</div>
                <pre class="json-pre" id="det-diagnostics">{}</pre>
            </div>
            <div id="drawer-code-section" style="display:none;">
                <div class="drawer-section-title">Submitted C++ Code</div>
                <pre class="code-box" id="det-code"></pre>
            </div>
        </div>
    </div>

    <script>


        // Initialize Console Logger
        function addLog(message, level) {
            if (!level) level = 'info';
            const consoleBox = document.getElementById('console-logs');
            const now = new Date();
            const timeStr = now.toTimeString().split(' ')[0];
            const logElement = document.createElement('div');
            
            let colorClass = '';
            if (level === 'error') colorClass = 'log-err';
            if (level === 'warning') colorClass = 'log-warn';

            logElement.innerHTML = "&gt; <span class=\"log-time\">" + timeStr + "</span> <span class=\"" + colorClass + "\">" + message + "</span>";
            consoleBox.appendChild(logElement);
            consoleBox.scrollTop = consoleBox.scrollHeight;
        }

        function clearConsole() {
            document.getElementById('console-logs').innerHTML = '';
            addLog('Console cleared.');
        }

        // Uptime Timer
        const startTime = Date.now();
        setInterval(() => {
            const elapsed = Date.now() - startTime;
            const hours = String(Math.floor(elapsed / 3600000)).padStart(2, '0');
            const minutes = String(Math.floor((elapsed % 3600000) / 60000)).padStart(2, '0');
            const seconds = String(Math.floor((elapsed % 60000) / 1000)).padStart(2, '0');
            document.getElementById('uptime-label').textContent = "Uptime: " + hours + ":" + minutes + ":" + seconds;
        }, 1000);

        // Core Dashboard Polling
        let requestCount = 0;
        let lastTimestamp = Date.now();
        let cachedSubmissions = [];

        function formatTime(isoString) {
            const date = new Date(isoString);
            return date.toLocaleTimeString();
        }

        async function pollMetrics() {
            try {
                const response = await fetch('/api/v1/dashboard/metrics');
                if (!response.ok) throw new Error('API server status unhealthy');
                const data = await response.json();
                requestCount++;

                // Update LED Indicators
                document.getElementById('db-led').className = data.db_healthy ? 'status-indicator status-ok' : 'status-indicator status-error';
                document.getElementById('redis-led').className = data.redis_healthy ? 'status-indicator status-ok' : 'status-indicator status-error';

                // Update KPIs
                document.getElementById('kpi-total-subs').textContent = data.total_submissions;
                document.getElementById('kpi-active-subs').textContent = data.active_submissions;
                document.getElementById('kpi-compile-queue').textContent = data.compilation_queue_depth;
                document.getElementById('kpi-pretest-queue').textContent = data.pretest_queue_depth;
                document.getElementById('kpi-max-score').textContent = data.max_composite_score.toFixed(2);

                // Update Kubernetes Pod status metrics
                if (data.k8s_status) {
                    const k8s = data.k8s_status;
                    document.getElementById('pod-count-gateway').textContent = k8s.gateway_pods;
                    document.getElementById('pod-count-compiler').textContent = k8s.compiler_pods;
                    document.getElementById('pod-count-testing').textContent = k8s.testing_pods > 0 ? k8s.testing_pods : "0 (Host)";
                    document.getElementById('pod-count-postgres').textContent = k8s.postgres_pods;
                    document.getElementById('pod-count-redis').textContent = k8s.redis_pods;
                    document.getElementById('pod-count-total').textContent = k8s.total_pods;

                    const badge = document.getElementById('cluster-mode-badge');
                    if (k8s.is_cluster_mode) {
                        badge.textContent = "K8s In-Cluster Mode";
                        badge.style.backgroundColor = "rgba(0, 245, 255, 0.15)";
                        badge.style.color = "var(--accent-cyan)";
                    } else {
                        badge.textContent = "Local Hybrid Mode";
                        badge.style.backgroundColor = "rgba(138, 43, 226, 0.15)";
                        badge.style.color = "var(--accent-violet)";
                    }
                }

                // Toggle Mock buttons state based on active runs count - Always enabled for concurrency testing
                const testingBtn = document.getElementById('btn-mock-testing');
                const systestBtn = document.getElementById('btn-mock-systest');
                if (testingBtn && systestBtn) {
                    testingBtn.disabled = false;
                    systestBtn.disabled = false;
                    testingBtn.style.opacity = '1.0';
                    testingBtn.style.cursor = 'pointer';
                    systestBtn.style.opacity = '1.0';
                    systestBtn.style.cursor = 'pointer';
                }



                // Check for new console logs by looking at state differences
                if (cachedSubmissions.length !== data.recent_submissions.length) {
                    addLog("Fetched " + data.recent_submissions.length + " active submissions from database");
                }
                
                // Look for state changes to print in console
                data.recent_submissions.forEach(sub => {
                    const cached = cachedSubmissions.find(c => c.build_id === sub.build_id);
                    if (!cached) {
                        addLog("New submission registered: ID " + sub.build_id.substring(0,8) + "... for contestant '" + sub.contestant_id + "'", 'warning');
                    } else if (cached.status !== sub.status || cached.verdict !== sub.verdict) {
                        addLog("Submission " + sub.build_id.substring(0,8) + "... transitioned status: " + cached.status + " -> " + sub.status + " [Verdict: " + sub.verdict + "]", 'info');
                    }
                });

                cachedSubmissions = data.recent_submissions;

                // Update Submissions Table
                const tbody = document.getElementById('submissions-tbody');
                if (data.recent_submissions.length === 0) {
                    tbody.innerHTML = '<tr><td colspan="7" style="text-align: center; color: var(--text-muted);">No submissions found. Use the control deck to trigger a mock run!</td></tr>';
                } else {
                    tbody.innerHTML = '';
                    data.recent_submissions.forEach(sub => {
                        const tr = document.createElement('tr');
                        tr.onclick = function() { openDrawer(sub); };
                        
                        let verdictClass = 'badge-queued';
                        if (sub.status === 'compiling') verdictClass = 'badge-compiling';
                        if (sub.status === 'running') verdictClass = 'badge-running';
                        if (sub.status === 'completed') verdictClass = 'badge-completed';
                        if (sub.status === 'failed') verdictClass = 'badge-failed';

                        const diag = sub.diagnostics || {};
                        const p99 = diag.p99_us || 0;
                        const tps = diag.tps_end || diag.actual_tps || 0;

                        let verdictColor = "var(--accent-red)";
                        if (sub.verdict === 'Accepted') {
                            verdictColor = "var(--accent-green)";
                        } else if (sub.verdict === 'Tail Latency Exceeded (TLE)') {
                            verdictColor = "var(--accent-amber)";
                        } else if (sub.verdict === 'Pending') {
                            verdictColor = "var(--text-secondary)";
                        }

                        tr.innerHTML = "<td><strong>" + sub.contestant_id + "</strong></td>" +
                            "<td><span class=\"badge " + verdictClass + "\">" + sub.status + "</span></td>" +
                            "<td><span style=\"font-weight:600; color:" + verdictColor + "\">" + sub.verdict + "</span></td>" +
                            "<td>" + sub.composite_score.toFixed(2) + "</td>" +
                            "<td>" + (p99 > 0 ? p99.toLocaleString() : '-') + "</td>" +
                            "<td>" + (tps > 0 ? Math.round(tps) : '-') + "</td>" +
                            "<td>" + formatTime(sub.submitted_at) + "</td>";
                        tbody.appendChild(tr);
                    });
                }

            } catch (err) {
                addLog("Metrics fetch failed: " + err.message, 'error');
            }
        }

        // Open Telemetry Details Drawer
        async function openDrawer(sub) {
            document.getElementById('det-id').textContent = sub.build_id;
            document.getElementById('det-contestant').textContent = sub.contestant_id;
            document.getElementById('det-status').textContent = sub.status;
            document.getElementById('det-verdict').textContent = sub.verdict;
            document.getElementById('det-score').textContent = sub.composite_score.toFixed(2);
            document.getElementById('det-time').textContent = new Date(sub.submitted_at).toLocaleString();
            document.getElementById('det-diagnostics').textContent = JSON.stringify(sub.diagnostics, null, 4);

            const codeSec = document.getElementById('drawer-code-section');
            document.getElementById('det-code').textContent = "Loading source code...";
            codeSec.style.display = 'block';

            try {
                const response = await fetch('/api/v1/submissions/' + sub.build_id + '/source');
                if (response.ok) {
                    const data = await response.json();
                    document.getElementById('det-code').textContent = data.source_code;
                } else {
                    document.getElementById('det-code').textContent = "Source code not found.";
                }
            } catch (err) {
                document.getElementById('det-code').textContent = "Failed to load source code: " + err.message;
            }

            document.getElementById('drawer').classList.add('open');
            document.getElementById('drawer-backdrop').classList.add('open');
            addLog("Viewing detailed diagnostics for submission " + sub.build_id.substring(0,8) + "...");
        }

        function closeDrawer() {
            document.getElementById('drawer').classList.remove('open');
            document.getElementById('drawer-backdrop').classList.remove('open');
        }

        // Trigger Operations
        async function triggerMockSubmission(isSystest) {
            const engine = document.getElementById('mock-engine-select').value;
            const modeName = isSystest ? "System Tests" : "Pretests";
            addLog('Sending request to dispatch mock submission (' + engine + ') for ' + modeName + '...');
            try {
                const res = await fetch('/api/v1/dashboard/actions/mock-submission?engine=' + encodeURIComponent(engine) + '&is_systest=' + isSystest, { method: 'POST' });
                if (!res.ok) {
                    if (res.status === 423) {
                        throw new Error('Another benchmark is currently running. Please wait for it to complete.');
                    }
                    throw new Error(await res.text());
                }
                const data = await res.json();
                addLog("Mock submission (" + engine + ") successfully accepted for " + modeName + "! BuildID: " + data.build_id, 'warning');
                pollMetrics();
            } catch (err) {
                addLog("Failed to trigger mock submission: " + err.message, 'error');
            }
        }

        function confirmResetDB() {
            if (confirm("WARNING: This will delete ALL submissions and stats from the PostgreSQL database, and flush all Redis queues. Are you sure you want to proceed?")) {
                resetDB();
            }
        }

        async function resetDB() {
            addLog('Wiping database table and flushing Redis Streams...');
            try {
                const res = await fetch('/api/v1/dashboard/actions/clean-db', { method: 'POST' });
                if (!res.ok) throw new Error(await res.text());
                addLog('PostgreSQL truncated & Redis queue flushed successfully ✓', 'warning');
                pollMetrics();
            } catch (err) {
                addLog("Reset request failed: " + err.message, 'error');
            }
        }

        // Initial setup
        addLog('Developer Dashboard loaded successfully. Initializing polling sequence...');
        pollMetrics();
        setInterval(pollMetrics, 2000);
    </script>
</body>
</html>
`
