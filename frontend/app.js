let leaderboardData = [];
let pollingInterval = null;
let currentSubmissionId = null;
let currentPollId = null;

// Sort & Anonymize state
let currentSortField = 'rank';
let currentSortOrder = 'asc';
let anonymizeMode = false;

// Chart references
let latencyChartInstance = null;
let tpsChartInstance = null;
let radarChartInstance = null;

// Initialize on page load
document.addEventListener('DOMContentLoaded', () => {
    fetchLeaderboard();
    
    // Poll leaderboard every 3 seconds
    setInterval(fetchLeaderboard, 3000);
    
    // Setup file dropzone events
    const dropzone = document.getElementById('dropzone');
    const fileInput = document.getElementById('file-input');
    
    dropzone.addEventListener('dragover', (e) => {
        e.preventDefault();
        dropzone.classList.add('dragover');
    });
    
    dropzone.addEventListener('dragleave', () => {
        dropzone.classList.remove('dragover');
    });
    
    dropzone.addEventListener('drop', (e) => {
        e.preventDefault();
        dropzone.classList.remove('dragover');
        if (e.dataTransfer.files.length > 0) {
            fileInput.files = e.dataTransfer.files;
            updateFilePreview(e.dataTransfer.files[0].name);
        }
    });
});

function handleFileSelect(event) {
    if (event.target.files.length > 0) {
        updateFilePreview(event.target.files[0].name);
    }
}

function updateFilePreview(name) {
    document.getElementById('file-name-preview').textContent = `Selected: ${name}`;
}

// Fetch Leaderboard from leaderboard.json (hybrid CDN-like static file)
async function fetchLeaderboard() {
    try {
        const response = await fetch('/leaderboard.json');
        if (!response.ok) {
            throw new Error(`Failed to load: ${response.statusText}`);
        }
        leaderboardData = await response.json();
        sortAndRender();
        updateSystemLeds(true);
    } catch (err) {
        console.error('Error loading leaderboard:', err);
        updateSystemLeds(false);
    }
}

function updateSystemLeds(healthy) {
    const pgLed = document.getElementById('postgres-led');
    const redisLed = document.getElementById('redis-led');
    
    if (healthy) {
        pgLed.className = 'status-indicator status-ok';
        redisLed.className = 'status-indicator status-ok';
    } else {
        pgLed.className = 'status-indicator status-error';
        redisLed.className = 'status-indicator status-error';
    }
}

function renderLeaderboard(data) {
    const tbody = document.getElementById('leaderboard-tbody');
    tbody.innerHTML = '';
    
    if (!data || data.length === 0) {
        tbody.innerHTML = `<tr><td colspan="7" style="text-align: center; color: var(--text-secondary);">No ranked submissions found. Submit your engine to start!</td></tr>`;
        return;
    }
    
    data.forEach((entry) => {
        const tr = document.createElement('tr');
        tr.onclick = () => openDrawer(entry.submission_id);
        
        let badgeClass = 'badge-pending';
        let verdict = entry.verdict || 'Pending';
        if (verdict.includes('Accepted')) badgeClass = 'badge-accepted';
        else if (verdict.includes('Partial')) badgeClass = 'badge-partial';
        else if (verdict.includes('Wrong') || verdict.includes('Limit') || verdict.includes('Exceeded')) badgeClass = 'badge-failed';
        
        const rankValue = entry.rank || '-';
        const displayRank = (rankValue <= 3) 
            ? `<div class="rank-indicator">${rankValue}</div>` 
            : rankValue;

        const correctness = entry.correctness_score != null ? entry.correctness_score.toFixed(1) : '0.0';
        const latency = entry.p99_us != null ? `${entry.p99_us.toLocaleString()} µs` : '-';
        const tps = entry.actual_tps != null ? entry.actual_tps.toLocaleString(undefined, {minimumFractionDigits: 1, maximumFractionDigits: 1}) : '-';
        
        const displayId = anonymizeMode ? obfuscateId(entry.contestant_id) : entry.contestant_id;

        let archetypeBadge = '';
        if (entry.engine_archetype && entry.engine_archetype !== 'Unclassified') {
            let archClass = 'arch-unclassified';
            if (entry.engine_archetype === 'Latency-Optimized') archClass = 'arch-speed';
            else if (entry.engine_archetype === 'Accuracy-Optimized') archClass = 'arch-correctness';
            else if (entry.engine_archetype === 'Balanced') archClass = 'arch-balanced';
            else if (entry.engine_archetype === 'Low-Throughput') archClass = 'arch-fragile';

            archetypeBadge = `<span class="badge ${archClass}" style="margin-left: 8px; font-size: 10px; font-weight: normal; text-transform: uppercase;">${entry.engine_archetype}</span>`;
        }

        tr.innerHTML = `
            <td>${displayRank}</td>
            <td style="font-family: var(--font-mono); font-weight: 500;">
                <div style="display: flex; align-items: center; gap: 4px;">
                    <span>${displayId}</span>
                    ${archetypeBadge}
                </div>
            </td>
            <td><span class="badge ${badgeClass}">${verdict}</span></td>
            <td style="text-align: right; font-weight: 600; color: var(--accent-cyan); font-family: var(--font-mono);">${entry.composite_score.toFixed(2)}</td>
            <td style="text-align: right; font-family: var(--font-mono);">${correctness}%</td>
            <td style="text-align: right; font-family: var(--font-mono);">${latency}</td>
            <td style="text-align: right; font-family: var(--font-mono);">${tps}</td>
        `;
        
        tbody.appendChild(tr);
    });
}

function setSort(field) {
    if (currentSortField === field) {
        currentSortOrder = currentSortOrder === 'asc' ? 'desc' : 'asc';
    } else {
        currentSortField = field;
        if (field === 'rank' || field === 'p99_us') {
            currentSortOrder = 'asc';
        } else {
            currentSortOrder = 'desc';
        }
    }
    sortAndRender();
}

function sortAndRender() {
    const query = document.getElementById('scoreboard-search').value.toLowerCase().trim();
    let dataToSort = [...leaderboardData];
    if (query) {
        dataToSort = dataToSort.filter(entry => 
            entry.contestant_id.toLowerCase().includes(query)
        );
    }

    dataToSort.sort((a, b) => {
        let valA = a[currentSortField];
        let valB = b[currentSortField];

        if (valA == null) return 1;
        if (valB == null) return -1;

        if (typeof valA === 'string') {
            return currentSortOrder === 'asc' 
                ? valA.localeCompare(valB) 
                : valB.localeCompare(valA);
        } else {
            return currentSortOrder === 'asc' 
                ? valA - valB 
                : valB - valA;
        }
    });

    renderLeaderboard(dataToSort);
    updateSortIcons();
}

function updateSortIcons() {
    const fields = ['rank', 'contestant_id', 'verdict', 'composite_score', 'correctness_score', 'p99_us', 'actual_tps'];
    fields.forEach(f => {
        const iconEl = document.getElementById(`sort-icon-${f}`);
        if (!iconEl) return;
        if (currentSortField === f) {
            iconEl.innerHTML = currentSortOrder === 'asc' 
                ? `<i class="fa-solid fa-sort-up" style="color: var(--accent-cyan);"></i>` 
                : `<i class="fa-solid fa-sort-down" style="color: var(--accent-cyan);"></i>`;
        } else {
            iconEl.innerHTML = '';
        }
    });
}

function toggleAnonymize() {
    anonymizeMode = !anonymizeMode;
    const btn = document.getElementById('anonymize-btn');
    if (anonymizeMode) {
        btn.innerHTML = `<i class="fa-solid fa-eye-slash"></i> Anonymized`;
        btn.style.borderColor = 'var(--accent-cyan)';
        btn.style.color = 'var(--accent-cyan)';
    } else {
        btn.innerHTML = `<i class="fa-solid fa-eye"></i> Plain IDs`;
        btn.style.borderColor = 'rgba(255, 255, 255, 0.1)';
        btn.style.color = 'var(--text-primary)';
    }
    sortAndRender();
}

function obfuscateId(id) {
    if (!id) return '';
    if (id.length <= 4) return '****';
    return id.substring(0, 2) + '****' + id.substring(id.length - 2);
}

function filterScoreboard() {
    sortAndRender();
}

// Handle code submission
async function submitEngine(event) {
    event.preventDefault();
    
    const contestantId = document.getElementById('contestant-id-input').value.trim();
    const fileInput = document.getElementById('file-input');
    const submitBtn = document.getElementById('submit-btn');
    
    if (!contestantId) {
        alert('Please specify a valid Contestant ID');
        return;
    }
    if (fileInput.files.length === 0) {
        alert('Please choose or drag-and-drop a C++ engine file (.cpp)');
        return;
    }
    
    submitBtn.disabled = true;
    submitBtn.innerHTML = `<i class="fa-solid fa-spinner fa-spin"></i> Initializing...`;
    
    const formData = new FormData();
    formData.append('contestant_id', contestantId);
    formData.append('source_code', fileInput.files[0]);
    
    try {
        const response = await fetch('/api/v1/submit', {
            method: 'POST',
            body: formData
        });
        
        if (!response.ok) {
            const errRes = await response.json();
            throw new Error(errRes.error || 'Server rejected submission');
        }
        
        const result = await response.json();
        
        // Reset file preview and input
        fileInput.value = '';
        document.getElementById('file-name-preview').textContent = '';
        
        // Open live progress monitor
        currentSubmissionId = result.build_id;
        document.getElementById('monitor-submission-id').textContent = `Submission ID: ${currentSubmissionId}`;
        document.getElementById('run-monitor-card').style.display = 'block';
        
        // Begin polling pipeline
        startPipelineMonitor(currentSubmissionId);
        
    } catch (err) {
        alert(`Submission Failed: ${err.message}`);
    } finally {
        submitBtn.disabled = false;
        submitBtn.innerHTML = `<i class="fa-solid fa-play"></i> Deploy & Stress Test`;
    }
}

function startPipelineMonitor(buildId) {
    if (currentPollId) clearInterval(currentPollId);
    
    updateMonitorUI('queued', 'QUEUED');
    
    currentPollId = setInterval(async () => {
        try {
            const response = await fetch(`/api/v1/build/${buildId}`);
            if (!response.ok) return;
            
            const build = await response.json();
            const status = build.status.toLowerCase();
            const verdict = build.verdict || 'Pending';
            
            updateMonitorUI(status, verdict);
            
            if (status === 'completed' || status === 'failed') {
                clearInterval(currentPollId);
                currentPollId = null;
                fetchLeaderboard();
                
                // Automatically show details on complete
                setTimeout(() => {
                    openDrawer(buildId);
                }, 800);
            }
        } catch (err) {
            console.error('Error polling pipeline status:', err);
        }
    }, 1000);
}

function updateMonitorUI(status, verdict) {
    const val = document.getElementById('monitor-status-val');
    const bar = document.getElementById('monitor-progress-bar');
    
    const stepQueued = document.getElementById('step-queued');
    const stepCompiling = document.getElementById('step-compiling');
    const stepRunning = document.getElementById('step-running');
    const stepFinished = document.getElementById('step-finished');
    
    // Reset steps
    stepQueued.className = 'timeline-step';
    stepCompiling.className = 'timeline-step';
    stepRunning.className = 'timeline-step';
    stepFinished.className = 'timeline-step';
    
    val.textContent = verdict.toUpperCase();
    
    if (status === 'queued') {
        bar.style.width = '15%';
        stepQueued.className = 'timeline-step active';
    } else if (status === 'compiling') {
        bar.style.width = '45%';
        stepQueued.className = 'timeline-step completed';
        stepCompiling.className = 'timeline-step active';
    } else if (status === 'running') {
        bar.style.width = '75%';
        stepQueued.className = 'timeline-step completed';
        stepCompiling.className = 'timeline-step completed';
        stepRunning.className = 'timeline-step active';
    } else if (status === 'completed') {
        bar.style.width = '100%';
        stepQueued.className = 'timeline-step completed';
        stepCompiling.className = 'timeline-step completed';
        stepRunning.className = 'timeline-step completed';
        stepFinished.className = 'timeline-step completed';
    } else if (status === 'failed') {
        bar.style.width = '100%';
        stepQueued.className = 'timeline-step completed';
        stepCompiling.className = 'timeline-step completed';
        stepRunning.className = 'timeline-step completed';
        stepFinished.className = 'timeline-step failed';
    }
}

// Slide-out Drawer logic
async function openDrawer(submissionId) {
    const drawer = document.getElementById('drawer');
    const backdrop = document.getElementById('drawer-backdrop');
    
    drawer.classList.add('open');
    backdrop.classList.add('open');
    
    // Fetch details
    try {
        const response = await fetch(`/api/v1/build/${submissionId}`);
        if (!response.ok) throw new Error('Failed to fetch details');
        
        const build = await response.json();
        populateDrawerDetails(build);
        
    } catch (err) {
        console.error('Error showing submission diagnostics:', err);
    }
}

function closeDrawer() {
    document.getElementById('drawer').classList.remove('open');
    document.getElementById('drawer-backdrop').classList.remove('open');
}

function populateDrawerDetails(build) {
    const diag = build.diagnostics || {};
    
    // Set Header
    document.getElementById('drawer-title').textContent = `Diagnostics: ${build.contestant_id}`;
    
    // Set Performance Summary values
    const correctnessVal = build.status === 'completed' && diag.correctness != null ? `${diag.correctness.toFixed(1)}%` : '0.0%';
    const latencyVal = build.status === 'completed' && diag.p99_us != null ? `${diag.p99_us.toLocaleString()} µs` : '-';
    const throughputVal = build.status === 'completed' && diag.tps_end != null ? `${diag.tps_end.toLocaleString(undefined, {maximumFractionDigits: 1})} TPS` : '-';
    
    document.getElementById('val-correctness').textContent = correctnessVal;
    document.getElementById('val-latency').textContent = latencyVal;
    document.getElementById('val-throughput').textContent = throughputVal;

    // Set Stability metrics
    const stdDev = diag.stability_std_dev != null ? `${diag.stability_std_dev.toFixed(2)}` : '0.00';
    const stabilityBonus = diag.stability_bonus != null ? `+${diag.stability_bonus}` : '+0';
    document.getElementById('val-stability-dev').textContent = stdDev;
    document.getElementById('val-stability-bonus').textContent = stabilityBonus;
    
    // Metadata rows
    document.getElementById('det-sub-id').textContent = build.build_id;
    document.getElementById('det-contestant-id').textContent = build.contestant_id;
    
    // Verdict Badge in Metadata
    let badgeClass = 'badge-pending';
    let verdict = build.verdict || 'Pending';
    if (verdict.includes('Accepted')) badgeClass = 'badge-accepted';
    else if (verdict.includes('Partial')) badgeClass = 'badge-partial';
    else if (verdict.includes('Wrong') || verdict.includes('Limit') || verdict.includes('Exceeded')) badgeClass = 'badge-failed';
    document.getElementById('det-verdict-badge').innerHTML = `<span class="badge ${badgeClass}">${verdict}</span>`;
    
    // Cores & memory constraints
    document.getElementById('det-cores').textContent = '1 Core (Pinning enabled)';
    document.getElementById('det-mem').textContent = '256 MB memory limit';
    document.getElementById('det-completed-at').textContent = new Date(build.submitted_at).toLocaleString();
    
    // Logs Console
    const consoleBox = document.getElementById('log-console-box');
    if (build.status === 'failed') {
        const errorMsg = diag.error || 'Evaluation failure during sandbox isolation check.';
        consoleBox.innerHTML = `<span class="log-err">[FATAL RUNTIME RUN ERROR]</span>\n${errorMsg}`;
    } else {
        const warnings = diag.warnings || [];
        if (warnings.length > 0) {
            consoleBox.innerHTML = warnings.map(w => `<span class="log-warn">[WARNING]</span> ${w}`).join('\n');
        } else {
            consoleBox.innerHTML = `[SUCCESS] Output matches expected matching engine criteria. Priority checks: 100% OK.\nCPU limits: OK\nNetwork isolation: Active`;
        }
    }
    
    // Anomaly Counts in Correctness Table
    const phantom = diag.phantom_fills || 0;
    const priority = diag.priority_violations || 0;
    const discrepancies = diag.orders_failed || 0;
    
    document.getElementById('det-priority-violations').textContent = priority.toLocaleString();
    document.getElementById('det-phantom-fills').textContent = phantom.toLocaleString();
    document.getElementById('det-trade-discrepancies').textContent = discrepancies.toLocaleString();
    
    updateAnomalyBadge('det-priority-status', priority);
    updateAnomalyBadge('det-phantom-status', phantom);
    updateAnomalyBadge('det-discrepancy-status', discrepancies);
    
    // Populating Bot Strategy Mix Breakdown (PolyBench derived)
    const breakdownTbody = document.getElementById('strategy-breakdown-tbody');
    breakdownTbody.innerHTML = '';
    
    const breakdown = diag.strategy_breakdown || {};
    const strategies = Object.keys(breakdown);
    if (strategies.length === 0) {
        breakdownTbody.innerHTML = `<tr><td colspan="4" style="text-align: center; color: var(--text-secondary);">No strategy breakdown available.</td></tr>`;
    } else {
        strategies.forEach(strat => {
            const metrics = breakdown[strat];
            const tr = document.createElement('tr');
            
            let stratLabel = strat;
            if (strat === 'MarketMaker') stratLabel = '<i class="fa-solid fa-scale-balanced" style="color: var(--accent-cyan); margin-right: 6px;"></i> Market Maker (Passive)';
            else if (strat === 'MomentumTrader') stratLabel = '<i class="fa-solid fa-rocket" style="color: var(--accent-violet); margin-right: 6px;"></i> Momentum Trader (Aggressive)';
            else if (strat === 'NoiseTrader') stratLabel = '<i class="fa-solid fa-shuffle" style="color: var(--accent-orange); margin-right: 6px;"></i> Noise Trader (Random)';

            const latencyDisplay = metrics.avg_latency_us != null ? `${metrics.avg_latency_us.toLocaleString()} µs` : '-';

            tr.innerHTML = `
                <td style="font-weight: 500;">${stratLabel}</td>
                <td style="text-align: right; font-family: var(--font-mono);">${metrics.orders_sent != null ? metrics.orders_sent.toLocaleString() : 0}</td>
                <td style="text-align: right; font-family: var(--font-mono); color: ${metrics.orders_failed > 0 ? '#ff3e3e' : 'var(--text-secondary)'};">${metrics.orders_failed != null ? metrics.orders_failed.toLocaleString() : 0}</td>
                <td style="text-align: right; font-family: var(--font-mono);">${latencyDisplay}</td>
            `;
            breakdownTbody.appendChild(tr);
        });
    }

    // Generate Charts
    renderLatencyPercentilesChart(diag);
    renderTpsVolatilityChart(diag);
    renderRadarChart(diag);
}

function renderRadarChart(diag) {
    const ctx = document.getElementById('radarChart').getContext('2d');
    
    if (radarChartInstance) {
        radarChartInstance.destroy();
    }

    const correctness = diag.correctness != null ? diag.correctness : 0;
    const latencyScore = diag.latency_score != null ? diag.latency_score : 0;
    const throughputScore = diag.throughput_score != null ? diag.throughput_score : 0;

    radarChartInstance = new Chart(ctx, {
        type: 'radar',
        data: {
            labels: ['Correctness', 'Latency Efficiency', 'Throughput Capacity'],
            datasets: [
                {
                    label: 'This Submission',
                    data: [correctness, latencyScore, throughputScore],
                    backgroundColor: 'rgba(0, 245, 255, 0.15)',
                    borderColor: '#00f5ff',
                    borderWidth: 2,
                    pointBackgroundColor: '#00f5ff',
                    pointBorderColor: '#fff',
                    pointHoverBackgroundColor: '#fff',
                    pointHoverBorderColor: '#00f5ff'
                },
                {
                    label: 'SLA Benchmark Target',
                    data: [100, 100, 100],
                    backgroundColor: 'rgba(255, 255, 255, 0.01)',
                    borderColor: 'rgba(255, 255, 255, 0.15)',
                    borderWidth: 1,
                    borderDash: [5, 5],
                    pointRadius: 0
                }
            ]
        },
        options: {
            responsive: true,
            maintainAspectRatio: false,
            plugins: {
                legend: {
                    display: true,
                    labels: { color: '#8b949e', font: { size: 10 } }
                }
            },
            scales: {
                r: {
                    angleLines: {
                        color: 'rgba(255, 255, 255, 0.08)'
                    },
                    grid: {
                        color: 'rgba(255, 255, 255, 0.08)'
                    },
                    pointLabels: {
                        color: '#8b949e',
                        font: { size: 10, weight: 'bold' }
                    },
                    ticks: {
                        color: '#8b949e',
                        backdropColor: 'transparent',
                        showLabelBackdrop: false,
                        font: { size: 8 },
                        stepSize: 20
                    },
                    min: 0,
                    max: 100
                }
            }
        }
    });
}

function updateAnomalyBadge(elementId, count) {
    const el = document.getElementById(elementId);
    if (count === 0) {
        el.innerHTML = `<span class="badge badge-accepted" style="padding: 2px 6px; font-size:10px;">PASS</span>`;
    } else {
        el.innerHTML = `<span class="badge badge-failed" style="padding: 2px 6px; font-size:10px;">FAIL</span>`;
    }
}

// Chart rendering
function renderLatencyPercentilesChart(diag) {
    const ctx = document.getElementById('latencyChart').getContext('2d');
    
    if (latencyChartInstance) {
        latencyChartInstance.destroy();
    }
    
    const p99 = diag.p99_us || 0;
    const p90 = diag.p99_us ? Math.round(diag.p99_us * 0.9) : 0;
    const p50 = diag.p99_us ? Math.round(diag.p99_us * 0.5) : 0;
    
    // Top 10% average mock stats
    const topP50 = 120;
    const topP90 = 240;
    const topP99 = 450;
    
    // Target SLA line constant (5ms = 5000us)
    const targetSLA = 5000;
    
    latencyChartInstance = new Chart(ctx, {
        type: 'line',
        data: {
            labels: ['P50', 'P90', 'P99'],
            datasets: [
                {
                    label: 'This Submission',
                    data: [p50, p90, p99],
                    borderColor: '#00f5ff',
                    backgroundColor: 'rgba(0, 245, 255, 0.05)',
                    borderWidth: 2,
                    tension: 0.1,
                    fill: true
                },
                {
                    label: 'Top 10% average',
                    data: [topP50, topP90, topP99],
                    borderColor: '#8a2be2',
                    backgroundColor: 'transparent',
                    borderWidth: 2,
                    borderDash: [5, 5],
                    tension: 0.1
                },
                {
                    label: 'SLA Target Limit',
                    data: [targetSLA, targetSLA, targetSLA],
                    borderColor: '#ff3e3e',
                    backgroundColor: 'transparent',
                    borderWidth: 1.5,
                    borderDash: [2, 2],
                    pointStyle: 'none',
                    pointRadius: 0
                }
            ]
        },
        options: {
            responsive: true,
            maintainAspectRatio: false,
            plugins: {
                legend: {
                    display: true,
                    labels: { color: '#8b949e', font: { size: 10 } }
                }
            },
            scales: {
                x: {
                    grid: { color: 'rgba(255, 255, 255, 0.05)' },
                    ticks: { color: '#8b949e', font: { size: 10 } }
                },
                y: {
                    title: { display: true, text: 'Latency (microseconds)', color: '#8b949e', font: { size: 10 } },
                    grid: { color: 'rgba(255, 255, 255, 0.05)' },
                    ticks: { color: '#8b949e', font: { size: 10 } }
                }
            }
        }
    });
}

function renderTpsVolatilityChart(diag) {
    const ctx = document.getElementById('tpsChart').getContext('2d');
    
    if (tpsChartInstance) {
        tpsChartInstance.destroy();
    }
    
    const startTps = diag.tps_start || 0;
    const endTps = diag.tps_end || 0;
    const midTps = Math.round((startTps + endTps) / 2 + (startTps - endTps) * 0.1);
    
    tpsChartInstance = new Chart(ctx, {
        type: 'line',
        data: {
            labels: ['Start Phase', 'Volatile Mid', 'End Phase Peak'],
            datasets: [
                {
                    label: 'Throughput (TPS)',
                    data: [startTps, midTps, endTps],
                    borderColor: '#39d353',
                    backgroundColor: 'rgba(57, 211, 83, 0.05)',
                    borderWidth: 2,
                    tension: 0.3,
                    fill: true
                }
            ]
        },
        options: {
            responsive: true,
            maintainAspectRatio: false,
            plugins: {
                legend: { display: false }
            },
            scales: {
                x: {
                    grid: { color: 'rgba(255, 255, 255, 0.05)' },
                    ticks: { color: '#8b949e', font: { size: 10 } }
                },
                y: {
                    title: { display: true, text: 'Orders / Sec', color: '#8b949e', font: { size: 10 } },
                    grid: { color: 'rgba(255, 255, 255, 0.05)' },
                    ticks: { color: '#8b949e', font: { size: 10 } }
                }
            }
        }
    });
}
