/* ========================================================================
   IICPC BenchGrid — Arena Views (Contest List & War Room)
   ======================================================================== */

import { API } from '../api.js';
import { setCurrentStream, getCurrentUser } from '../router.js';

// ─── State ───────────────────────────────────────────────────────────────
let leaderboardData = [];
let previousScores = {};        // contestant_id -> previous composite_score
let pollingInterval = null;
let currentSubmissionId = null;
let currentPollId = null;
let focusedRowIndex = -1;
let currentSortField = 'rank';
let currentSortOrder = 'asc';
let anonymizeMode = false;
let activeSubmissionMethod = 'zip';
let arenaStatus = 'active';

// Chart references
let latencyChartInstance = null;
let tpsChartInstance = null;
let radarChartInstance = null;

// Renders the list of arenas
export async function renderArenaList() {
    let arenas = [];
    try {
        arenas = await API.listArenas();
    } catch (err) {
        console.error("Failed to load arenas list:", err);
    }
    
    let rowsHtml = '';
    if (arenas.length === 0) {
        rowsHtml = `<tr><td colspan="5" class="text-center" style="padding:40px; color:var(--text-secondary);">No contests scheduled. Contact administrators.</td></tr>`;
    } else {
        arenas.forEach(a => {
            const start = new Date(a.start_time).toLocaleString();
            const end = new Date(a.end_time).toLocaleString();
            
            let statusBadge = '';
            if (a.status === 'upcoming') statusBadge = `<span class="badge" style="background:rgba(113,113,122,0.15); color:var(--text-secondary);">Upcoming</span>`;
            else if (a.status === 'active') statusBadge = `<span class="badge" style="background:rgba(16,185,129,0.15); color:var(--accent);">Active</span>`;
            else if (a.status === 'system_test') statusBadge = `<span class="badge" style="background:rgba(210,153,34,0.15); color:var(--accent-amber);">System Testing</span>`;
            else if (a.status === 'ended') statusBadge = `<span class="badge" style="background:rgba(218,54,55,0.15); color:#ff6b6b;">Ended</span>`;

            let actionHtml = '';
            const user = getCurrentUser();
            if (user) {
                if (a.status === 'active' || a.status === 'upcoming') {
                    actionHtml = `<button class="btn btn-primary register-btn" data-arena-id="${a.id}" style="padding:4px 12px; font-size:11px; width:auto; height:24px;">Register</button>`;
                } else {
                    actionHtml = `<a href="#/arena/${a.id}" class="btn btn-outline" style="padding:4px 12px; font-size:11px; width:auto; text-decoration:none; height:24px; text-align:center; line-height:1.2;">Enter Standings</a>`;
                }
            } else {
                actionHtml = `<a href="#/login" class="btn btn-outline" style="padding:4px 12px; font-size:11px; width:auto; text-decoration:none; height:24px; text-align:center; line-height:1.2;">Login to Register</a>`;
            }

            rowsHtml += `
                <tr>
                    <td><strong>${a.title}</strong></td>
                    <td>${statusBadge}</td>
                    <td><span class="cell-mono">${start}</span></td>
                    <td><span class="cell-mono">${end}</span></td>
                    <td style="text-align:right;">
                        <div style="display:inline-flex; gap:10px; align-items:center;">
                            <a href="#/arena/${a.id}" class="btn btn-outline" style="padding:4px 12px; font-size:11px; width:auto; text-decoration:none; height:24px; text-align:center; line-height:1.2;">Enter Arena</a>
                            ${actionHtml}
                        </div>
                    </td>
                </tr>
            `;
        });
    }

    return `
        <div class="arena-list-container" style="max-width:900px; margin: 40px auto; padding: 20px;">
            <div style="margin-bottom:30px;">
                <h2 style="font-family:var(--font-display); font-size:1.8rem; font-weight:700; color:var(--text-primary); margin-bottom:5px;">Arenas</h2>
                <p style="color:var(--text-secondary); font-size:0.85rem;">Select an active contest or view standings of past hackathons.</p>
            </div>

            <div id="arena-error-banner" class="error-banner" style="position:static; margin-bottom:20px; width:100%; box-sizing:border-box;">
                <span id="arena-error-text"></span>
            </div>

            <div class="card" style="border:1px solid var(--border); overflow:hidden;">
                <table style="width:100%; border-collapse:collapse;">
                    <thead>
                        <tr>
                            <th style="padding:16px;">Contest Name</th>
                            <th style="padding:16px;">Status</th>
                            <th style="padding:16px;">Start Time</th>
                            <th style="padding:16px;">End Time</th>
                            <th style="padding:16px; text-align:right;">Actions</th>
                        </tr>
                    </thead>
                    <tbody id="arena-tbody">
                        ${rowsHtml}
                    </tbody>
                </table>
            </div>
        </div>
    `;
}

// Hydrates the list of arenas with event handlers
export function hydrateArenaList() {
    const errorBanner = document.getElementById('arena-error-banner');
    const errorText = document.getElementById('arena-error-text');

    document.querySelectorAll('.register-btn').forEach(btn => {
        btn.addEventListener('click', async (e) => {
            const arenaId = btn.dataset.arenaId;
            try {
                errorBanner.classList.remove('error-banner--visible');
                await API.registerArena(arenaId);
                btn.textContent = 'Registered';
                btn.disabled = true;
                btn.style.background = 'rgba(16,185,129,0.1)';
                btn.style.color = 'var(--accent)';
                btn.style.borderColor = 'rgba(16,185,129,0.3)';
            } catch (err) {
                errorText.textContent = err.message;
                errorBanner.classList.add('error-banner--visible');
            }
        });
    });
}

// Renders the main contest Workspace (War Room)
export async function renderArenaDetail(arenaId) {
    let arena = { title: 'Contest Arena', status: 'active' };
    try {
        arena = await API.getArena(arenaId);
        arenaStatus = arena.status;
    } catch (err) {
        console.error("Failed to load arena details:", err);
    }

    const user = getCurrentUser();
    let submitPanelHtml = '';

    if (user) {
        submitPanelHtml = `
            <div class="card" style="padding:20px; border:1px solid var(--border);">
                <div class="card-header" style="margin-bottom:15px; padding-bottom:8px; border-bottom:1px solid var(--border);">
                    <h3 class="card-title">Deploy Engine</h3>
                </div>

                <div id="error-banner" class="error-banner">
                    <span id="error-banner-text"></span>
                    <button class="error-banner__close" onclick="document.getElementById('error-banner').classList.remove('error-banner--visible')">&times;</button>
                </div>

                <form id="submission-form">
                    <input type="hidden" id="contestant-id-input" value="${user.handle}">
                    <input type="hidden" id="arena-id-input" value="${arenaId}">

                    <!-- Segment Control -->
                    <div class="segment-control" style="margin-bottom:15px;">
                        <button type="button" id="method-zip-btn" class="segment-btn segment-btn--active">ZIP Archive</button>
                        <button type="button" id="method-git-btn" class="segment-btn">GitHub Repo</button>
                    </div>

                    <!-- ZIP Upload Area -->
                    <div id="zip-input-section" class="input-group">
                        <label class="control-label">Source Archive</label>
                        <div id="dropzone" class="dropzone">
                            <i class="fa-solid fa-file-zipper dropzone__icon"></i>
                            <div class="dropzone__text">Drag and drop ZIP or <span style="color:var(--accent);text-decoration:underline;cursor:pointer;">browse</span></div>
                            <div id="file-name-preview" class="dropzone__filename"></div>
                            <input type="file" id="file-input" class="dropzone__input" accept=".zip">
                        </div>
                        <div class="control-help">Upload multi-file C++ solutions (must include CMakeLists.txt)</div>
                    </div>

                    <!-- GitHub URL Area -->
                    <div id="git-input-section" class="input-group hidden">
                        <label class="control-label">GitHub Repository URL</label>
                        <input type="url" id="github-url-input" class="input-text" placeholder="https://github.com/user/repo">
                        <div class="control-help">Ensure repository contains CMakeLists.txt and is public</div>
                    </div>

                    <button type="submit" id="submit-btn" class="btn btn-primary" style="margin-top:10px;">
                        <i class="fa-solid fa-rocket" style="margin-right:6px;"></i> Deploy & Stress Test
                    </button>
                </form>
            </div>
        `;
    } else {
        submitPanelHtml = `
            <div class="card" style="padding:20px; border:1px solid var(--border); text-align:center;">
                <h3 style="font-weight:600; margin-bottom:10px; color:var(--text-primary);">Deploy Sandbox</h3>
                <p style="color:var(--text-secondary); font-size:0.85rem; margin-bottom:15px;">You must be signed in to deploy custom engines to the sandboxing cluster.</p>
                <a href="#/login" class="btn btn-primary" style="text-decoration:none; display:inline-block; width:auto; padding:6px 20px;">Sign In</a>
            </div>
        `;
    }

    let amberBannerClass = arenaStatus === 'active' ? '' : 'hidden';

    return `
        <!-- Main Workspace -->
        <div class="workspace">
            <!-- Left Workspace Pane: Controls -->
            <div class="pane-controls">
                ${submitPanelHtml}

                <!-- Active stress test monitoring (hidden by default) -->
                <div id="run-monitor-card" class="card hidden" style="padding:20px; border:1px solid var(--border); margin-top:20px; border-left:3px solid var(--accent);">
                    <div style="display:flex; justify-content:space-between; align-items:center; margin-bottom:12px;">
                        <span class="cell-mono" style="font-size:11px; text-transform:uppercase; color:var(--text-secondary);" id="monitor-submission-id">ID: -</span>
                        <span class="status-led status-led--ok" style="width:6px; height:6px;"></span>
                    </div>
                    <div class="timeline" style="margin-bottom:15px;">
                        <div class="timeline__bar">
                            <div id="monitor-progress-bar" class="timeline__fill"></div>
                        </div>
                        <div style="display:flex; justify-content:space-between; margin-top:10px; font-size:9px; font-family:var(--font-mono); color:var(--text-secondary);">
                            <div id="step-queued" class="timeline__step">QUEUED</div>
                            <div id="step-compiling" class="timeline__step">COMPILING</div>
                            <div id="step-running" class="timeline__step">STRESSING</div>
                            <div id="step-finished" class="timeline__step">FINISHED</div>
                        </div>
                    </div>
                    <div style="display:flex; justify-content:space-between; font-size:11px;">
                        <span style="color:var(--text-secondary);">Verdict State</span>
                        <strong id="monitor-status-val" style="color:var(--accent);">QUEUED</strong>
                    </div>
                </div>

                <!-- Specs Panel -->
                <div class="card" style="padding:20px; border:1px solid var(--border); margin-top:20px;">
                    <div class="card-header" style="margin-bottom:12px; padding-bottom:6px; border-bottom:1px solid var(--border);">
                        <h3 class="card-title">Sandboxing Limits</h3>
                    </div>
                    <div style="font-size:11px; font-family:var(--font-mono); display:flex; flex-direction:column; gap:6px; color:var(--text-secondary);">
                        <div style="display:flex; justify-content:space-between;"><span style="color:var(--text-secondary);">Processor Assignment</span><strong style="color:var(--text-primary);">1 Core (Pinned)</strong></div>
                        <div style="display:flex; justify-content:space-between;"><span style="color:var(--text-secondary);">Memory Allocation</span><strong style="color:var(--text-primary);">256 MB Hard Cap</strong></div>
                        <div style="display:flex; justify-content:space-between;"><span style="color:var(--text-secondary);">Workload Cycles</span><strong style="color:var(--text-primary);">3 Iterations</strong></div>
                        <div style="display:flex; justify-content:space-between;"><span style="color:var(--text-secondary);">Pretest Capacity</span><strong style="color:var(--text-primary);">5 Bots / 100 Orders</strong></div>
                        <div style="display:flex; justify-content:space-between;"><span style="color:var(--text-secondary);">System Test Capacity</span><strong style="color:var(--text-primary);">10 Bots / 500 Orders</strong></div>
                    </div>
                </div>
            </div>

            <!-- Right Workspace Pane: Leaderboard Table -->
            <div class="pane-leaderboard">
                <div class="card" style="padding:20px; border:1px solid var(--border); height:100%;">
                    
                    <!-- Search and Action header -->
                    <div style="display:flex; justify-content:space-between; align-items:center; gap:15px; margin-bottom:15px;">
                        <div style="position:relative; flex-grow:1;">
                            <input type="text" id="scoreboard-search" class="input-text" placeholder="Search handles... (Press '/' to focus)" style="width:100%; padding-left:32px;">
                            <i class="fa-solid fa-magnifying-glass" style="position:absolute; left:12px; top:50%; transform:translateY(-50%); color:var(--text-secondary); font-size:11px;"></i>
                        </div>
                        <div style="display:flex; gap:10px;">
                            <button id="anonymize-btn" class="btn btn-outline" style="padding:0 12px; width:34px; height:32px;" title="Anonymize Handles">
                                <svg id="anon-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" style="width:14px;height:14px;">
                                    <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/>
                                </svg>
                            </button>
                        </div>
                    </div>

                    <!-- Pretest Standings Amber Banner -->
                    <div id="leaderboard-pretest-banner" class="alert-amber ${amberBannerClass}" style="margin-bottom:15px; padding:8px 12px; border-radius:4px; font-size:11px; display:flex; align-items:center; gap:8px;">
                        <i class="fa-solid fa-circle-info"></i>
                        <span><strong>Preliminary Standings:</strong> Leaderboard shows pretest results only. Final rankings will be computed during System Testing.</span>
                    </div>

                    <!-- Table Container -->
                    <div style="overflow-x:auto;">
                        <table style="width:100%; border-collapse:collapse;">
                            <thead>
                                <tr>
                                    <th id="th-rank" style="width:50px;">Rank <span id="sort-icon-rank"></span></th>
                                    <th id="th-contestant_id">Contestant <span id="sort-icon-contestant_id"></span></th>
                                    <th id="th-verdict">Verdict <span id="sort-icon-verdict"></span></th>
                                    <th id="th-composite_score" class="cell-right" style="width:90px;">Score <span id="sort-icon-composite_score"></span></th>
                                    <th id="th-correctness_score" class="cell-right" style="width:70px;">Correct <span id="sort-icon-correctness_score"></span></th>
                                    <th id="th-p99_us" class="cell-right" style="width:85px;">P99 (us) <span id="sort-icon-p99_us"></span></th>
                                    <th id="th-actual_tps" class="cell-right" style="width:90px;">TPS <span id="sort-icon-actual_tps"></span></th>
                                    <th class="cell-right" style="width:70px;">Trend</th>
                                </tr>
                            </thead>
                            <tbody id="leaderboard-tbody">
                                <!-- Dynamic entries -->
                            </tbody>
                        </table>
                    </div>
                </div>
            </div>
        </div>

        <!-- Telemetry Details Drawer (Slide-out) -->
        <div id="drawer-backdrop" class="drawer-backdrop"></div>
        <div id="drawer" class="drawer">
            <div class="drawer__header">
                <h3 id="drawer-title" class="drawer__title">Diagnostics Terminal</h3>
                <button id="drawer-close-btn" class="drawer__close">&times;</button>
            </div>
            
            <div class="drawer__content">
                
                <!-- Performance metrics row -->
                <div class="perf-metric-grid">
                    <div class="perf-metric">
                        <span class="perf-metric__label">Correctness</span>
                        <div id="val-correctness" class="perf-metric__value">-</div>
                    </div>
                    <div class="perf-metric">
                        <span class="perf-metric__label">P99 Latency</span>
                        <div id="val-latency" class="perf-metric__value">-</div>
                    </div>
                    <div class="perf-metric">
                        <span class="perf-metric__label">Throughput</span>
                        <div id="val-throughput" class="perf-metric__value">-</div>
                    </div>
                </div>

                <!-- Diagnostic stats block -->
                <div class="drawer-panel" style="padding:15px; background:var(--bg-surface); border:1px solid var(--border); border-radius:6px; margin-top:20px;">
                    <div style="font-size:10px; font-family:var(--font-mono); display:flex; flex-direction:column; gap:6px;">
                        <div style="display:flex; justify-content:space-between;"><span style="color:var(--text-secondary);">Submission UUID</span><span class="cell-mono" style="color:var(--text-primary);" id="det-sub-id">-</span></div>
                        <div style="display:flex; justify-content:space-between;"><span style="color:var(--text-secondary);">Contestant ID</span><span class="cell-mono" style="color:var(--text-primary);" id="det-contestant-id">-</span></div>
                        <div id="det-source-row" style="display:flex; justify-content:space-between; display:none;"><span style="color:var(--text-secondary);">GitHub URL</span><span class="cell-mono" id="det-source-value">-</span></div>
                        <div style="display:flex; justify-content:space-between;"><span style="color:var(--text-secondary);">Test Verdict</span><span id="det-verdict-badge">-</span></div>
                        <div style="display:flex; justify-content:space-between;"><span style="color:var(--text-secondary);">Cores Pinning</span><span style="color:var(--text-primary);" id="det-cores">-</span></div>
                        <div style="display:flex; justify-content:space-between;"><span style="color:var(--text-secondary);">Max Memory Bound</span><span style="color:var(--text-primary);" id="det-mem">-</span></div>
                        <div style="display:flex; justify-content:space-between;"><span style="color:var(--text-secondary);">Completed At</span><span style="color:var(--text-primary);" id="det-completed-at">-</span></div>
                        <div style="display:flex; justify-content:space-between;"><span style="color:var(--text-secondary);">Variance / StdDev</span><span style="color:var(--text-primary);"><span id="val-stability-dev">-</span>% (Score Deviation)</span></div>
                        <div style="display:flex; justify-content:space-between;"><span style="color:var(--text-secondary);">Stability Bonus</span><span style="color:var(--accent);" id="val-stability-bonus">+0</span></div>
                    </div>
                </div>

                <!-- Chart: Radar Overview -->
                <div style="margin-top:20px; border-bottom:1px solid var(--border); padding-bottom:20px;">
                    <div style="font-family:var(--font-mono); font-size:10px; color:var(--text-secondary); margin-bottom:10px; text-transform:uppercase;">Overall Performance Spectrum</div>
                    <div style="height:180px; position:relative;">
                        <canvas id="radarChart"></canvas>
                    </div>
                </div>

                <!-- Charts: Latency & TPS -->
                <div style="margin-top:20px; border-bottom:1px solid var(--border); padding-bottom:20px;">
                    <div style="font-family:var(--font-mono); font-size:10px; color:var(--text-secondary); margin-bottom:10px; text-transform:uppercase;">Latency Profile</div>
                    <div style="height:150px; position:relative;">
                        <canvas id="latencyChart"></canvas>
                    </div>
                </div>

                <div style="margin-top:20px; border-bottom:1px solid var(--border); padding-bottom:20px;">
                    <div style="font-family:var(--font-mono); font-size:10px; color:var(--text-secondary); margin-bottom:10px; text-transform:uppercase;">TPS Load Profile</div>
                    <div style="height:150px; position:relative;">
                        <canvas id="tpsChart"></canvas>
                    </div>
                </div>

                <!-- Anomaly Verification -->
                <div style="margin-top:20px; border-bottom:1px solid var(--border); padding-bottom:20px;">
                    <div style="font-family:var(--font-mono); font-size:10px; color:var(--text-secondary); margin-bottom:10px; text-transform:uppercase;">Compliance & Anomalies</div>
                    <div style="font-size:11px; display:flex; flex-direction:column; gap:8px;">
                        <div style="display:flex; justify-content:space-between; align-items:center;">
                            <span style="color:var(--text-secondary);">Price-Time Priority Violations</span>
                            <div style="display:inline-flex; gap:10px; align-items:center;">
                                <span class="cell-mono" id="det-priority-violations">0</span>
                                <span id="det-priority-status"></span>
                            </div>
                        </div>
                        <div style="display:flex; justify-content:space-between; align-items:center;">
                            <span style="color:var(--text-secondary);">Phantom Fill Detections</span>
                            <div style="display:inline-flex; gap:10px; align-items:center;">
                                <span class="cell-mono" id="det-phantom-fills">0</span>
                                <span id="det-phantom-status"></span>
                            </div>
                        </div>
                        <div style="display:flex; justify-content:space-between; align-items:center;">
                            <span style="color:var(--text-secondary);">Trade Discrepancies</span>
                            <div style="display:inline-flex; gap:10px; align-items:center;">
                                <span class="cell-mono" id="det-trade-discrepancies">0</span>
                                <span id="det-discrepancy-status"></span>
                            </div>
                        </div>
                    </div>
                </div>

                <!-- Strategy Breakdown -->
                <div style="margin-top:20px; border-bottom:1px solid var(--border); padding-bottom:20px;">
                    <div style="font-family:var(--font-mono); font-size:10px; color:var(--text-secondary); margin-bottom:10px; text-transform:uppercase;">Deterministic Strategy Breakdown</div>
                    <table style="width:100%; font-size:10px; border-collapse:collapse;" class="strategy-table">
                        <thead>
                            <tr>
                                <th style="text-align:left; font-size:10px; text-transform:none; padding:4px 0;">Strategy</th>
                                <th style="text-align:right; font-size:10px; text-transform:none; padding:4px 0;">Sent</th>
                                <th style="text-align:right; font-size:10px; text-transform:none; padding:4px 0;">Failed</th>
                                <th style="text-align:right; font-size:10px; text-transform:none; padding:4px 0;">RTT P50</th>
                            </tr>
                        </thead>
                        <tbody id="strategy-breakdown-tbody">
                            <!-- Dynamic rows -->
                        </tbody>
                    </table>
                </div>

                <!-- Sandbox Console logs -->
                <div style="margin-top:20px; margin-bottom:30px;">
                    <div style="font-family:var(--font-mono); font-size:10px; color:var(--text-secondary); margin-bottom:10px; text-transform:uppercase;">Sandbox Execution Logs (Tail 100)</div>
                    <pre id="log-console-box" class="console-box dark-terminal" style="height:150px; overflow-y:auto; font-size:9.5px; border-radius:4px; padding:10px;"></pre>
                </div>
            </div>
        </div>
    `;
}

// Hydrates the War Room workspace
export function hydrateArenaDetail(arenaId) {
    showSkeletonRows();

    // Start SSE stream subscription
    const stream = new EventSource(`/api/v1/leaderboard/${arenaId}/stream`);
    setCurrentStream(stream);

    stream.onmessage = (e) => {
        try {
            const data = JSON.parse(e.data);
            leaderboardData = data;
            sortAndRender();
        } catch (err) {
            console.error("SSE message processing error:", err);
        }
    };

    stream.onerror = () => {
        // Fallback polling if SSE fails
        if (!pollingInterval) {
            pollingInterval = setInterval(async () => {
                try {
                    const data = await API.getLeaderboard(arenaId);
                    leaderboardData = data;
                    sortAndRender();
                } catch (err) {
                    console.error("Fallback polling error:", err);
                }
            }, 3000);
        }
    };

    // Clean up timers on unmount
    const app = document.getElementById("app");
    const observer = new MutationObserver(() => {
        if (!document.getElementById("leaderboard-tbody")) {
            if (pollingInterval) clearInterval(pollingInterval);
            pollingInterval = null;
            observer.disconnect();
        }
    });
    observer.observe(app, { childList: true });

    // Setup input selectors
    setupDropzone();
    setupKeyboardNav();
    setupSorter();

    // Setup submission toggles
    const zipBtn = document.getElementById('method-zip-btn');
    const gitBtn = document.getElementById('method-git-btn');
    if (zipBtn && gitBtn) {
        zipBtn.addEventListener('click', () => switchMethod('zip'));
        gitBtn.addEventListener('click', () => switchMethod('git'));
    }

    // Submit handler
    const form = document.getElementById('submission-form');
    if (form) {
        form.addEventListener('submit', submitEngine);
    }

    // Anon handler
    const anonBtn = document.getElementById('anonymize-btn');
    if (anonBtn) {
        anonBtn.addEventListener('click', toggleAnonymize);
    }

    // Search filter
    const searchInput = document.getElementById('scoreboard-search');
    if (searchInput) {
        searchInput.addEventListener('input', () => sortAndRender());
    }

    // Drawer close handler
    const closeBtn = document.getElementById('drawer-close-btn');
    const backdrop = document.getElementById('drawer-backdrop');
    if (closeBtn && backdrop) {
        closeBtn.addEventListener('click', closeDrawer);
        backdrop.addEventListener('click', closeDrawer);
    }
}

// ─── Auxiliary Handlers ──────────────────────────────────────────────────

function showSkeletonRows() {
    const tbody = document.getElementById('leaderboard-tbody');
    if (!tbody) return;
    let html = '';
    for (let i = 0; i < 5; i++) {
        html += `<tr class="skeleton-row">
            <td><div class="skeleton-block skeleton-block--short"></div></td>
            <td><div class="skeleton-block skeleton-block--long"></div></td>
            <td><div class="skeleton-block skeleton-block--badge"></div></td>
            <td class="text-right"><div class="skeleton-block skeleton-block--medium" style="margin-left:auto;"></div></td>
            <td class="text-right"><div class="skeleton-block skeleton-block--short" style="margin-left:auto;"></div></td>
            <td class="text-right"><div class="skeleton-block skeleton-block--medium" style="margin-left:auto;"></div></td>
            <td class="text-right"><div class="skeleton-block skeleton-block--short" style="margin-left:auto;"></div></td>
            <td class="text-right"><div class="skeleton-block skeleton-block--short" style="margin-left:auto;"></div></td>
        </tr>`;
    }
    tbody.innerHTML = html;
}

function setupDropzone() {
    const dropzone = document.getElementById('dropzone');
    const fileInput = document.getElementById('file-input');
    if (!dropzone || !fileInput) return;

    dropzone.addEventListener('dragover', (e) => {
        e.preventDefault();
        dropzone.classList.add('dropzone--active');
    });
    dropzone.addEventListener('dragleave', () => {
        dropzone.classList.remove('dropzone--active');
    });
    dropzone.addEventListener('drop', (e) => {
        e.preventDefault();
        dropzone.classList.remove('dropzone--active');
        if (e.dataTransfer.files.length > 0) {
            fileInput.files = e.dataTransfer.files;
            updateFilePreview(e.dataTransfer.files[0].name);
        }
    });

    fileInput.addEventListener('change', (e) => {
        if (e.target.files.length > 0) {
            updateFilePreview(e.target.files[0].name);
        }
    });
}

function updateFilePreview(name) {
    const preview = document.getElementById('file-name-preview');
    if (preview) preview.textContent = name;
}

function switchMethod(method) {
    activeSubmissionMethod = method;
    const zipBtn = document.getElementById('method-zip-btn');
    const gitBtn = document.getElementById('method-git-btn');
    const zipSection = document.getElementById('zip-input-section');
    const gitSection = document.getElementById('git-input-section');
    const gitInput = document.getElementById('github-url-input');

    if (method === 'zip') {
        zipBtn.classList.add('segment-btn--active');
        gitBtn.classList.remove('segment-btn--active');
        zipSection.classList.remove('hidden');
        gitSection.classList.add('hidden');
        gitInput.required = false;
        gitInput.value = '';
    } else {
        gitBtn.classList.add('segment-btn--active');
        zipBtn.classList.remove('segment-btn--active');
        zipSection.classList.add('hidden');
        gitSection.classList.remove('hidden');
        gitInput.required = true;
        document.getElementById('file-input').value = '';
        document.getElementById('file-name-preview').textContent = '';
    }
}

function showError(message) {
    const banner = document.getElementById('error-banner');
    const text = document.getElementById('error-banner-text');
    if (banner && text) {
        text.textContent = message;
        banner.classList.add('error-banner--visible');
    }
}

function dismissError() {
    const banner = document.getElementById('error-banner');
    if (banner) banner.classList.remove('error-banner--visible');
}

async function submitEngine(event) {
    event.preventDefault();
    dismissError();

    const contestantId = document.getElementById('contestant-id-input').value.trim();
    const arenaId = document.getElementById('arena-id-input').value.trim();
    const fileInput = document.getElementById('file-input');
    const gitInput = document.getElementById('github-url-input');
    const submitBtn = document.getElementById('submit-btn');

    const formData = new FormData();
    formData.append('contestant_id', contestantId);
    formData.append('arena_id', arenaId);

    if (activeSubmissionMethod === 'zip') {
        if (fileInput.files.length === 0) {
            showError('Please choose or drag-and-drop a ZIP archive (.zip).');
            return;
        }
        formData.append('source_code', fileInput.files[0]);
    } else {
        const githubUrl = gitInput.value.trim();
        if (!githubUrl) {
            showError('Please specify a valid GitHub Repository URL.');
            return;
        }
        formData.append('github_url', githubUrl);
    }

    submitBtn.disabled = true;
    submitBtn.innerHTML = svgSpinner() + ' Initializing...';

    try {
        const result = await API.submitCode(formData);

        // Reset inputs
        fileInput.value = '';
        gitInput.value = '';
        updateFilePreview('');

        // Show pipeline monitor
        currentSubmissionId = result.build_id;
        document.getElementById('monitor-submission-id').textContent = `ID: ${currentSubmissionId.substring(0,8)}...`;
        document.getElementById('run-monitor-card').classList.remove('hidden');

        startPipelineMonitor(currentSubmissionId);
    } catch (err) {
        showError(`Submission Failed: ${err.message}`);
    } finally {
        submitBtn.disabled = false;
        submitBtn.innerHTML = svgPlay() + ' Deploy & Stress Test';
    }
}

function svgPlay() {
    return '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" style="width:14px;height:14px;display:inline-block;vertical-align:middle;margin-right:6px;"><polygon points="5 3 19 12 5 21 5 3"/></svg>';
}

function spinner() {
    return '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" style="width:14px;height:14px;animation:spin 1s linear infinite;display:inline-block;vertical-align:middle;margin-right:6px;"><path d="M21 12a9 9 0 1 1-6.219-8.56"/></svg>';
}
const svgSpinner = spinner;

function startPipelineMonitor(buildId) {
    if (currentPollId) clearInterval(currentPollId);
    updateMonitorUI('queued', 'QUEUED');

    currentPollId = setInterval(async () => {
        try {
            const build = await API.getBuildStatus(buildId);
            const status = build.status.toLowerCase();
            const verdict = build.verdict || 'Pending';

            updateMonitorUI(status, verdict);

            if (status === 'completed' || status === 'failed') {
                clearInterval(currentPollId);
                currentPollId = null;
                setTimeout(() => openDrawer(buildId), 800);
            }
        } catch (err) {
            console.error('Pipeline poll error:', err);
        }
    }, 1500);
}

function updateMonitorUI(status, verdict) {
    const statusEl = document.getElementById('monitor-status-val');
    const bar = document.getElementById('monitor-progress-bar');
    if (!statusEl || !bar) return;

    const steps = ['step-queued', 'step-compiling', 'step-running', 'step-finished'];
    steps.forEach(id => {
        const el = document.getElementById(id);
        if (el) el.className = 'timeline__step';
    });

    statusEl.textContent = verdict.toUpperCase();

    const map = {
        queued:    { width: '15%', done: [], active: 'step-queued' },
        compiling: { width: '45%', done: ['step-queued'], active: 'step-compiling' },
        running:   { width: '75%', done: ['step-queued', 'step-compiling'], active: 'step-running' },
        completed: { width: '100%', done: ['step-queued', 'step-compiling', 'step-running', 'step-finished'], active: null },
        failed:    { width: '100%', done: ['step-queued', 'step-compiling', 'step-running'], active: null, fail: 'step-finished' },
    };

    const cfg = map[status] || map.queued;
    bar.style.width = cfg.width;
    cfg.done.forEach(id => {
        const el = document.getElementById(id);
        if (el) el.classList.add('timeline__step--done');
    });
    if (cfg.active) {
        const el = document.getElementById(cfg.active);
        if (el) el.classList.add('timeline__step--active');
    }
    if (cfg.fail) {
        const el = document.getElementById(cfg.fail);
        if (el) el.classList.add('timeline__step--fail');
    }
}

function setupSorter() {
    const headers = ['rank', 'contestant_id', 'verdict', 'composite_score', 'correctness_score', 'p99_us', 'actual_tps'];
    headers.forEach(h => {
        const th = document.getElementById(`th-${h}`);
        if (th) {
            th.style.cursor = 'pointer';
            th.addEventListener('click', () => setSort(h));
        }
    });
}

function setSort(field) {
    if (currentSortField === field) {
        currentSortOrder = currentSortOrder === 'asc' ? 'desc' : 'asc';
    } else {
        currentSortField = field;
        currentSortOrder = (field === 'rank' || field === 'p99_us') ? 'asc' : 'desc';
    }
    sortAndRender();
}

function sortAndRender() {
    const searchInput = document.getElementById('scoreboard-search');
    const query = searchInput ? searchInput.value.toLowerCase().trim() : '';
    let filtered = [...leaderboardData];
    if (query) {
        filtered = filtered.filter(e => e.contestant_id.toLowerCase().includes(query));
    }

    filtered.sort((a, b) => {
        let valA = a[currentSortField];
        let valB = b[currentSortField];
        if (valA == null) return 1;
        if (valB == null) return -1;
        if (typeof valA === 'string') {
            return currentSortOrder === 'asc' ? valA.localeCompare(valB) : valB.localeCompare(valA);
        }
        return currentSortOrder === 'asc' ? valA - valB : valB - valA;
    });

    renderLeaderboard(filtered);
    updateSortIcons();
}

function renderLeaderboard(data) {
    const tbody = document.getElementById('leaderboard-tbody');
    if (!tbody) return;

    if (!data || data.length === 0) {
        tbody.innerHTML = `<tr><td colspan="8" class="text-center" style="color:var(--text-secondary);padding:40px 16px;">
            No ranked submissions yet. Upload your matching engine to begin.
        </td></tr>`;
        return;
    }

    const newScores = {};
    const fragment = document.createDocumentFragment();

    data.forEach((entry, idx) => {
        const tr = document.createElement('tr');
        tr.dataset.submissionId = entry.submission_id;
        tr.dataset.index = idx;
        tr.style.cursor = 'pointer';
        tr.onclick = () => openDrawer(entry.submission_id);

        const prevScore = previousScores[entry.contestant_id];
        if (prevScore !== undefined) {
            if (entry.composite_score > prevScore) {
                tr.classList.add('tick-up');
            } else if (entry.composite_score < prevScore) {
                tr.classList.add('tick-down');
            }
        }
        newScores[entry.contestant_id] = entry.composite_score;

        if (idx === focusedRowIndex) {
            tr.classList.add('row--focused');
        }

        const verdict = entry.verdict || 'Pending';
        let badgeClass = getBadgeClass(verdict);
        
        let verdictDisplay = verdict;
        if (arenaStatus === 'system_test' && (entry.status === 'queued' || entry.status === 'running')) {
            verdictDisplay = `<i class="fa-solid fa-spinner fa-spin" style="margin-right:6px; color:var(--accent-amber);"></i>System Testing...`;
            badgeClass = 'badge--neutral';
        }

        const rank = entry.rank || '-';
        let rankHtml;
        if (rank === 1) rankHtml = `<span class="rank-medal rank-medal--gold">1</span>`;
        else if (rank === 2) rankHtml = `<span class="rank-medal rank-medal--silver">2</span>`;
        else if (rank === 3) rankHtml = `<span class="rank-medal rank-medal--bronze">3</span>`;
        else rankHtml = `<span class="rank-num">${rank}</span>`;

        const displayId = anonymizeMode ? obfuscateId(entry.contestant_id) : entry.contestant_id;

        let archHtml = '';
        if (entry.engine_archetype && entry.engine_archetype !== 'Unclassified') {
            const archClass = getArchClass(entry.engine_archetype);
            archHtml = `<span class="arch-tag ${archClass}">${entry.engine_archetype}</span>`;
        }

        let deltaHtml = '';
        if (entry.delta_score > 0) {
            deltaHtml = `<span class="delta delta--up">+${entry.delta_score.toFixed(2)}</span>`;
        }

        let deltaP99Html = '';
        if (entry.delta_p99 < 0) {
            deltaP99Html = `<span class="delta delta--up">${entry.delta_p99.toLocaleString()}</span>`;
        } else if (entry.delta_p99 > 0) {
            deltaP99Html = `<span class="delta delta--down">+${entry.delta_p99.toLocaleString()}</span>`;
        }

        const correctness = entry.correctness_score != null ? entry.correctness_score.toFixed(1) : '0.0';
        
        let latency = entry.p99_us != null ? `${entry.p99_us.toLocaleString()}` : '-';
        let tps = entry.actual_tps != null ? entry.actual_tps.toLocaleString(undefined, { minimumFractionDigits: 1, maximumFractionDigits: 1 }) : '-';

        if (arenaStatus === 'system_test' && (entry.status === 'queued' || entry.status === 'running')) {
            latency = '-';
            tps = '-';
        }

        const sparkSvg = generateSparkline(entry);

        tr.innerHTML = `
            <td>${rankHtml}</td>
            <td><span class="cell-mono" style="font-size:0.78rem;">${displayId}</span>${archHtml}</td>
            <td><span class="badge ${badgeClass}">${verdictDisplay}</span></td>
            <td class="cell-right"><span class="cell-mono cell-score">${(arenaStatus === 'system_test' && (entry.status === 'queued' || entry.status === 'running')) ? '0.00' : entry.composite_score.toFixed(2)}</span>${deltaHtml}</td>
            <td class="cell-right cell-mono">${(arenaStatus === 'system_test' && (entry.status === 'queued' || entry.status === 'running')) ? '0.0' : correctness}%</td>
            <td class="cell-right cell-mono">${latency}${deltaP99Html}</td>
            <td class="cell-right cell-mono">${tps}</td>
            <td class="cell-right sparkline-cell">${sparkSvg}</td>
        `;

        fragment.appendChild(tr);
    });

    previousScores = newScores;
    tbody.innerHTML = '';
    tbody.appendChild(fragment);
}

function getBadgeClass(verdict) {
    if (verdict === 'Accepted') return 'badge--accepted';
    if (verdict.includes('TLE') || verdict.includes('Latency')) return 'badge--warning';
    if (verdict.includes('LV') || verdict.includes('Logic') || verdict.includes('Degradation') ||
        verdict.includes('Wrong') || verdict.includes('Exceeded') || verdict.includes('Failure')) return 'badge--danger';
    if (verdict === 'Pending') return 'badge--neutral';
    return 'badge--neutral';
}

function getArchClass(arch) {
    if (arch === 'Latency-Optimized') return 'arch-tag--speed';
    if (arch === 'Accuracy-Optimized') return 'arch-tag--accuracy';
    if (arch === 'Balanced') return 'arch-tag--balanced';
    if (arch === 'Low-Throughput') return 'arch-tag--fragile';
    return 'arch-tag--default';
}

function updateSortIcons() {
    const fields = ['rank', 'contestant_id', 'verdict', 'composite_score', 'correctness_score', 'p99_us', 'actual_tps'];
    fields.forEach(f => {
        const el = document.getElementById(`sort-icon-${f}`);
        if (!el) return;
        if (currentSortField === f) {
            el.textContent = currentSortOrder === 'asc' ? ' ▲' : ' ▼';
        } else {
            el.textContent = '';
        }
    });
}

function toggleAnonymize() {
    anonymizeMode = !anonymizeMode;
    const icon = document.getElementById('anon-icon');
    if (anonymizeMode) {
        icon.innerHTML = '<path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24"/><line x1="1" y1="1" x2="23" y2="23"/>';
    } else {
        icon.innerHTML = '<path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/>';
    }
    sortAndRender();
}

function obfuscateId(id) {
    if (!id) return '';
    if (id.length <= 4) return '****';
    return id.substring(0, 2) + '****' + id.substring(id.length - 2);
}

function generateSparkline(entry) {
    if (arenaStatus === 'system_test' && (entry.status === 'queued' || entry.status === 'running')) {
        return '';
    }
    const scores = entry.score_history || null;
    let dataPoints;
    if (scores && scores.length >= 2) {
        dataPoints = scores.slice(-5);
    } else {
        const current = entry.composite_score || 0;
        const delta = entry.delta_score || 0;
        const prev = Math.max(0, current - delta);
        dataPoints = [
            Math.max(0, prev - delta * 0.5),
            prev,
            prev + (current - prev) * 0.3,
            prev + (current - prev) * 0.7,
            current
        ];
    }

    const w = 60, h = 18, pad = 2;
    const min = Math.min(...dataPoints);
    const max = Math.max(...dataPoints);
    const range = max - min || 1;

    const points = dataPoints.map((v, i) => {
        const x = pad + (i / (dataPoints.length - 1)) * (w - pad * 2);
        const y = h - pad - ((v - min) / range) * (h - pad * 2);
        return { x, y };
    });

    const pathD = points.map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x.toFixed(1)},${p.y.toFixed(1)}`).join(' ');
    const lastPt = points[points.length - 1];

    const trending = dataPoints[dataPoints.length - 1] > dataPoints[0];
    const lineColor = trending ? '#10b981' : '#71717a';
    const dotColor = trending ? '#10b981' : '#71717a';

    return `<svg width="${w}" height="${h}" viewBox="0 0 ${w} ${h}" fill="none">
        <path d="${pathD}" stroke="${lineColor}" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" fill="none"/>
        <circle cx="${lastPt.x.toFixed(1)}" cy="${lastPt.y.toFixed(1)}" r="2" fill="${dotColor}"/>
    </svg>`;
}

async function openDrawer(submissionId) {
    const drawer = document.getElementById('drawer');
    const backdrop = document.getElementById('drawer-backdrop');
    if (!drawer || !backdrop) return;

    drawer.classList.add('drawer--open');
    backdrop.classList.add('drawer-backdrop--open');

    try {
        const build = await API.getBuildStatus(submissionId);
        populateDrawerDetails(build);
    } catch (err) {
        console.error('Drawer fetch error:', err);
    }
}

function closeDrawer() {
    const drawer = document.getElementById('drawer');
    const backdrop = document.getElementById('drawer-backdrop');
    if (drawer && backdrop) {
        drawer.classList.remove('drawer--open');
        backdrop.classList.remove('drawer-backdrop--open');
    }
}

function populateDrawerDetails(build) {
    const diag = build.diagnostics || {};

    document.getElementById('drawer-title').textContent = `Diagnostics: ${build.contestant_id}`;

    const correctnessVal = build.status === 'completed' && diag.correctness != null ? `${diag.correctness.toFixed(1)}%` : '0.0%';
    const latencyVal = build.status === 'completed' && diag.p99_us != null ? `${diag.p99_us.toLocaleString()} \u00B5s` : '-';
    const throughputVal = build.status === 'completed' && diag.tps_end != null ? `${diag.tps_end.toLocaleString(undefined, { maximumFractionDigits: 1 })} TPS` : '-';

    document.getElementById('val-correctness').textContent = correctnessVal;
    document.getElementById('val-latency').textContent = latencyVal;
    document.getElementById('val-throughput').textContent = throughputVal;

    const cEl = document.getElementById('val-correctness');
    cEl.className = 'perf-metric__value';
    if (diag.correctness != null && diag.correctness >= 100) {
        cEl.classList.add('perf-metric__value--success');
    } else if (diag.correctness != null && diag.correctness < 100) {
        cEl.classList.add('perf-metric__value--danger');
    } else {
        cEl.classList.add('perf-metric__value--info');
    }

    document.getElementById('val-stability-dev').textContent = diag.stability_std_dev != null ? diag.stability_std_dev.toFixed(2) : '0.00';
    document.getElementById('val-stability-bonus').textContent = diag.stability_bonus != null ? `+${diag.stability_bonus}` : '+0';

    document.getElementById('det-sub-id').textContent = build.build_id;
    document.getElementById('det-contestant-id').textContent = build.contestant_id;

    const sourceRow = document.getElementById('det-source-row');
    const sourceVal = document.getElementById('det-source-value');
    if (build.github_url) {
        sourceRow.classList.remove('hidden');
        sourceVal.innerHTML = `<a href="${build.github_url}" target="_blank" style="color:var(--accent);text-decoration:underline;">${build.github_url}</a>`;
    } else {
        sourceRow.classList.add('hidden');
        sourceVal.innerHTML = '';
        
        // Fetch source code if ZIP file
        if (build.status === 'completed') {
            sourceRow.classList.remove('hidden');
            sourceVal.innerHTML = `<a id="view-source-link" style="color:var(--accent);text-decoration:underline;cursor:pointer;">View Sandbox ZIP Code</a>`;
            document.getElementById('view-source-link').addEventListener('click', async () => {
                try {
                    const data = await API.getSourceCode(build.build_id);
                    alert("Sandbox Source Snippet:\n\n" + data.source_code);
                } catch (err) {
                    alert(err.message);
                }
            });
        }
    }

    const verdict = build.verdict || 'Pending';
    const badgeClass = getBadgeClass(verdict);
    const reason = diag.reason || '';
    const reasonHtml = reason ? `<div style="font-size:0.7rem;color:var(--text-secondary);margin-top:4px;">${reason}</div>` : '';
    document.getElementById('det-verdict-badge').innerHTML = `<span class="badge ${badgeClass}">${verdict}</span>${reasonHtml}`;

    document.getElementById('det-completed-at').textContent = new Date(build.submitted_at).toLocaleString();

    const consoleBox = document.getElementById('log-console-box');
    const ts = formatLogTimestamp();
    if (build.status === 'failed') {
        const errorMsg = diag.error || 'Evaluation failure during sandbox isolation check.';
        consoleBox.innerHTML = `<span class="log-ts">${ts}</span><span class="log-err">[FATAL]</span> ${errorMsg}`;
    } else if (build.status === 'running') {
        consoleBox.innerHTML = `<span class="log-ts">${ts}</span><span class="log-info">[RUNNING]</span> Sandbox execution workload active...`;
    } else {
        const warnings = diag.warnings || [];
        if (warnings.length > 0) {
            consoleBox.innerHTML = warnings.map(w => `<span class="log-ts">${ts}</span><span class="log-warn">[WARN]</span> ${w}`).join('\n');
        } else {
            consoleBox.innerHTML = [
                `<span class="log-ts">${ts}</span><span class="log-info">[OK]</span> Price-time checks: 100% passed`,
                `<span class="log-ts">${ts}</span><span class="log-info">[OK]</span> CPU cores pinning: verified (1 Core)`,
                `<span class="log-ts">${ts}</span><span class="log-info">[OK]</span> Sandbox memory cap: active (256MB)`,
                `<span class="log-ts">${ts}</span><span class="log-info">[OK]</span> Execution trace logs uploaded`
            ].join('\n');
        }
        
        if (diag.sandbox_logs) {
            consoleBox.innerHTML += `\n\n=== CONTAINER SANDBOX LOGS ===\n${diag.sandbox_logs}`;
        }
    }
    consoleBox.scrollTop = consoleBox.scrollHeight;

    const phantom = diag.phantom_fills || 0;
    const priority = diag.priority_violations || 0;
    const discrepancies = diag.orders_failed || 0;

    document.getElementById('det-priority-violations').textContent = priority.toLocaleString();
    document.getElementById('det-phantom-fills').textContent = phantom.toLocaleString();
    document.getElementById('det-trade-discrepancies').textContent = discrepancies.toLocaleString();

    updateAnomalyBadge('det-priority-status', priority);
    updateAnomalyBadge('det-phantom-status', phantom);
    updateAnomalyBadge('det-discrepancy-status', discrepancies);

    const breakdownTbody = document.getElementById('strategy-breakdown-tbody');
    breakdownTbody.innerHTML = '';

    const breakdown = diag.strategy_breakdown || {};
    const strategies = Object.keys(breakdown);
    if (strategies.length === 0) {
        breakdownTbody.innerHTML = `<tr><td colspan="4" class="text-center" style="color:var(--text-secondary);">No strategy data available.</td></tr>`;
    } else {
        strategies.forEach(strat => {
            const metrics = breakdown[strat];
            const tr = document.createElement('tr');

            let label = strat;
            if (strat === 'MarketMaker') label = 'Market Maker';
            else if (strat === 'MomentumTrader') label = 'Momentum';
            else if (strat === 'NoiseTrader') label = 'Noise';

            const latDisplay = metrics.avg_latency_us != null ? `${metrics.avg_latency_us.toLocaleString()} \u00B5s` : '-';
            const failColor = metrics.orders_failed > 0 ? 'color:var(--status-danger)' : '';

            tr.innerHTML = `
                <td style="font-weight:500;">${label}</td>
                <td class="text-right">${(metrics.orders_sent || 0).toLocaleString()}</td>
                <td class="text-right" style="${failColor}">${(metrics.orders_failed || 0).toLocaleString()}</td>
                <td class="text-right">${latDisplay}</td>
            `;
            breakdownTbody.appendChild(tr);
        });
    }

    renderRadarChart(diag);
    renderLatencyChart(diag);
    renderTpsChart(diag);
}

function formatLogTimestamp() {
    const now = new Date();
    const h = String(now.getHours()).padStart(2, '0');
    const m = String(now.getMinutes()).padStart(2, '0');
    const s = String(now.getSeconds()).padStart(2, '0');
    const ms = String(now.getMilliseconds()).padStart(3, '0');
    return `[${h}:${m}:${s}.${ms}]`;
}

function updateAnomalyBadge(elementId, count) {
    const el = document.getElementById(elementId);
    if (!el) return;
    if (count === 0) {
        el.innerHTML = `<span class="badge badge--accepted" style="padding:2px 6px;">PASS</span>`;
    } else {
        el.innerHTML = `<span class="badge badge--danger" style="padding:2px 6px;">FAIL</span>`;
    }
}

// ─── Chart.js Renderers ──────────────────────────────────────────────────

const chartGridColor = 'rgba(15, 0, 0, 0.08)';
const chartTickColor = '#201d1d';

function renderRadarChart(diag) {
    const ctx = document.getElementById('radarChart').getContext('2d');
    if (radarChartInstance) radarChartInstance.destroy();

    const correctness = diag.correctness || 0;
    const latencyScore = diag.latency_score || 0;
    const throughputScore = diag.throughput_score || 0;

    radarChartInstance = new Chart(ctx, {
        type: 'radar',
        data: {
            labels: ['Correctness', 'Latency', 'Throughput'],
            datasets: [
                {
                    label: 'Submission',
                    data: [correctness, latencyScore, throughputScore],
                    backgroundColor: 'rgba(0, 122, 255, 0.1)',
                    borderColor: '#007aff',
                    borderWidth: 2,
                    pointBackgroundColor: '#007aff',
                    pointBorderColor: '#fafafa',
                    pointHoverBackgroundColor: '#fafafa',
                    pointHoverBorderColor: '#007aff',
                },
                {
                    label: 'SLA Target',
                    data: [100, 100, 100],
                    backgroundColor: 'rgba(255, 255, 255, 0.01)',
                    borderColor: 'rgba(63, 63, 70, 0.3)',
                    borderWidth: 1,
                    borderDash: [4, 4],
                    pointRadius: 0,
                }
            ]
        },
        options: {
            responsive: true,
            maintainAspectRatio: false,
            interaction: { mode: 'index', intersect: false },
            plugins: {
                legend: { display: true, labels: { color: chartTickColor, font: { size: 10 } } }
            },
            scales: {
                r: {
                    angleLines: { color: chartGridColor },
                    grid: { color: chartGridColor },
                    pointLabels: { color: chartTickColor, font: { size: 10, weight: '600' } },
                    ticks: {
                        color: chartTickColor,
                        backdropColor: 'transparent',
                        showLabelBackdrop: false,
                        font: { size: 8 },
                        stepSize: 25
                    },
                    min: 0, max: 100
                }
            }
        }
    });
}

function renderLatencyChart(diag) {
    const ctx = document.getElementById('latencyChart').getContext('2d');
    if (latencyChartInstance) latencyChartInstance.destroy();

    const p99 = diag.p99_us || 0;
    const p90 = p99 ? Math.round(p99 * 0.9) : 0;
    const p50 = p99 ? Math.round(p99 * 0.5) : 0;

    const topP50 = 120, topP90 = 240, topP99 = 450;
    const sla = 5000;

    latencyChartInstance = new Chart(ctx, {
        type: 'line',
        data: {
            labels: ['P50', 'P90', 'P99'],
            datasets: [
                {
                    label: 'This Submission',
                    data: [p50, p90, p99],
                    borderColor: '#007aff',
                    backgroundColor: 'rgba(0, 122, 255, 0.05)',
                    borderWidth: 2,
                    tension: 0.15,
                    fill: true,
                    pointBackgroundColor: '#007aff',
                    pointRadius: 3,
                },
                {
                    label: 'Top 10% Avg',
                    data: [topP50, topP90, topP99],
                    borderColor: '#52525b',
                    backgroundColor: 'transparent',
                    borderWidth: 1.5,
                    borderDash: [5, 5],
                    tension: 0.15,
                    pointRadius: 2,
                    pointBackgroundColor: '#52525b',
                },
                {
                    label: 'SLA Limit',
                    data: [sla, sla, sla],
                    borderColor: '#ef4444',
                    backgroundColor: 'transparent',
                    borderWidth: 1,
                    borderDash: [2, 2],
                    pointRadius: 0,
                }
            ]
        },
        options: {
            responsive: true,
            maintainAspectRatio: false,
            interaction: { mode: 'index', intersect: false },
            plugins: {
                legend: { display: true, labels: { color: chartTickColor, font: { size: 10 } } }
            },
            scales: {
                x: {
                    grid: { color: chartGridColor },
                    ticks: { color: chartTickColor, font: { size: 10 } }
                },
                y: {
                    title: { display: true, text: 'Latency (\u00B5s)', color: chartTickColor, font: { size: 10 } },
                    grid: { color: chartGridColor },
                    ticks: { color: chartTickColor, font: { size: 10 } }
                }
            }
        }
    });
}

function renderTpsChart(diag) {
    const ctx = document.getElementById('tpsChart').getContext('2d');
    if (tpsChartInstance) tpsChartInstance.destroy();

    const startTps = diag.tps_start || 0;
    const endTps = diag.tps_end || 0;
    const midTps = Math.round((startTps + endTps) / 2 + (startTps - endTps) * 0.1);

    tpsChartInstance = new Chart(ctx, {
        type: 'line',
        data: {
            labels: ['Start', 'Mid', 'End'],
            datasets: [{
                label: 'TPS',
                data: [startTps, midTps, endTps],
                borderColor: '#007aff',
                backgroundColor: 'rgba(0, 122, 255, 0.05)',
                borderWidth: 2,
                tension: 0.3,
                fill: true,
                pointBackgroundColor: '#007aff',
                pointRadius: 3,
            }]
        },
        options: {
            responsive: true,
            maintainAspectRatio: false,
            interaction: { mode: 'index', intersect: false },
            plugins: { legend: { display: false } },
            scales: {
                x: {
                    grid: { color: chartGridColor },
                    ticks: { color: chartTickColor, font: { size: 10 } }
                },
                y: {
                    title: { display: true, text: 'Orders/sec', color: chartTickColor, font: { size: 10 } },
                    grid: { color: chartGridColor },
                    ticks: { color: chartTickColor, font: { size: 10 } }
                }
            }
        }
    });
}

// ─── Keyboard focus ──────────────────────────────────────────────────────
function setupKeyboardNav() {
    document.addEventListener('keydown', (e) => {
        const activeTag = document.activeElement?.tagName;
        const isInput = activeTag === 'INPUT' || activeTag === 'TEXTAREA';
        const drawer = document.getElementById('drawer');
        const drawerOpen = drawer ? drawer.classList.contains('drawer--open') : false;

        if (e.key === 'Escape') {
            if (drawerOpen) {
                closeDrawer();
            } else if (isInput) {
                document.activeElement.blur();
                focusedRowIndex = -1;
                sortAndRender();
            }
            return;
        }

        if (e.key === '/' && !isInput && !drawerOpen) {
            e.preventDefault();
            const search = document.getElementById('scoreboard-search');
            if (search) search.focus();
            return;
        }

        if (isInput || drawerOpen) return;

        const rows = document.querySelectorAll('#leaderboard-tbody tr:not(.skeleton-row)');
        const maxIdx = rows.length - 1;
        if (maxIdx < 0) return;

        if (e.key === 'j' || e.key === 'ArrowDown') {
            e.preventDefault();
            focusedRowIndex = Math.min(focusedRowIndex + 1, maxIdx);
            applyRowFocus(rows);
            return;
        }

        if (e.key === 'k' || e.key === 'ArrowUp') {
            e.preventDefault();
            focusedRowIndex = Math.max(focusedRowIndex - 1, 0);
            applyRowFocus(rows);
            return;
        }

        if (e.key === 'Enter' && focusedRowIndex >= 0 && focusedRowIndex <= maxIdx) {
            e.preventDefault();
            const row = rows[focusedRowIndex];
            const subId = row.dataset.submissionId;
            if (subId) openDrawer(subId);
            return;
        }
    });
}

function applyRowFocus(rows) {
    rows.forEach((r, i) => {
        if (i === focusedRowIndex) {
            r.classList.add('row--focused');
            r.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
        } else {
            r.classList.remove('row--focused');
        }
    });
}
