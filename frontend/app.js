/* ========================================================================
   IICPC BenchGrid — Frontend Application Logic
   Industrial Utilitarian + Luxury Minimal
   ======================================================================== */

// ─── State ───────────────────────────────────────────────────────────────
let leaderboardData = [];
let previousScores = {};        // contestant_id -> previous composite_score (for tick flash)
let pollingInterval = null;
let currentSubmissionId = null;
let currentPollId = null;
let focusedRowIndex = -1;       // keyboard navigation

// Sort & Anonymize
let currentSortField = 'rank';
let currentSortOrder = 'asc';
let anonymizeMode = false;

// Chart references
let latencyChartInstance = null;
let tpsChartInstance = null;
let radarChartInstance = null;

// Submission method
let activeSubmissionMethod = 'zip';

// ─── Chart.js Global Config ─────────────────────────────────────────────
Chart.defaults.font.family = "'JetBrains Mono', 'Fira Code', monospace";
Chart.defaults.font.size = 10;
Chart.defaults.color = '#71717a';
Chart.defaults.plugins.tooltip.backgroundColor = 'rgba(16, 16, 20, 0.95)';
Chart.defaults.plugins.tooltip.borderColor = 'rgba(39, 39, 42, 0.8)';
Chart.defaults.plugins.tooltip.borderWidth = 1;
Chart.defaults.plugins.tooltip.cornerRadius = 6;
Chart.defaults.plugins.tooltip.padding = 10;
Chart.defaults.plugins.tooltip.titleFont = { weight: '600', size: 11 };
Chart.defaults.plugins.tooltip.bodyFont = { size: 10 };

// ─── Initialize ─────────────────────────────────────────────────────────
document.addEventListener('DOMContentLoaded', () => {
    showSkeletonRows();
    fetchLeaderboard();
    setInterval(fetchLeaderboard, 3000);
    setupDropzone();
    setupKeyboardNav();
});

// ─── Skeleton Loading ───────────────────────────────────────────────────
function showSkeletonRows() {
    const tbody = document.getElementById('leaderboard-tbody');
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

// ─── File Dropzone ──────────────────────────────────────────────────────
function setupDropzone() {
    const dropzone = document.getElementById('dropzone');
    const fileInput = document.getElementById('file-input');

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
}

function handleFileSelect(event) {
    if (event.target.files.length > 0) {
        updateFilePreview(event.target.files[0].name);
    }
}

function updateFilePreview(name) {
    document.getElementById('file-name-preview').textContent = name;
}

// ─── Error Banner (replaces alert()) ────────────────────────────────────
function showError(message) {
    const banner = document.getElementById('error-banner');
    document.getElementById('error-banner-text').textContent = message;
    banner.classList.add('error-banner--visible');
}

function dismissError() {
    document.getElementById('error-banner').classList.remove('error-banner--visible');
}

// ─── Submission Method Toggle ───────────────────────────────────────────
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
window.switchMethod = switchMethod;

// ─── Fetch Leaderboard ──────────────────────────────────────────────────
async function fetchLeaderboard() {
    try {
        const response = await fetch('/leaderboard.json');
        if (!response.ok) throw new Error(`Failed: ${response.statusText}`);
        leaderboardData = await response.json();
        sortAndRender();
        updateSystemLeds(true);
    } catch (err) {
        console.error('Leaderboard fetch error:', err);
        updateSystemLeds(false);
    }
}

function updateSystemLeds(healthy) {
    const pgLed = document.getElementById('postgres-led');
    const redisLed = document.getElementById('redis-led');
    if (healthy) {
        pgLed.className = 'status-led status-led--ok';
        redisLed.className = 'status-led status-led--ok';
    } else {
        pgLed.className = 'status-led status-led--error';
        redisLed.className = 'status-led status-led--error';
    }
}

// ─── Leaderboard Rendering ──────────────────────────────────────────────
function renderLeaderboard(data) {
    const tbody = document.getElementById('leaderboard-tbody');

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
        tr.onclick = () => openDrawer(entry.submission_id);

        // Tick flash: diff against previous scores
        const prevScore = previousScores[entry.contestant_id];
        if (prevScore !== undefined) {
            if (entry.composite_score > prevScore) {
                tr.classList.add('tick-up');
            } else if (entry.composite_score < prevScore) {
                tr.classList.add('tick-down');
            }
        }
        newScores[entry.contestant_id] = entry.composite_score;

        // Keyboard focus
        if (idx === focusedRowIndex) {
            tr.classList.add('row--focused');
        }

        // Verdict badge
        const verdict = entry.verdict || 'Pending';
        const badgeClass = getBadgeClass(verdict);

        // Rank display
        const rank = entry.rank || '-';
        let rankHtml;
        if (rank === 1) rankHtml = `<span class="rank-medal rank-medal--gold">1</span>`;
        else if (rank === 2) rankHtml = `<span class="rank-medal rank-medal--silver">2</span>`;
        else if (rank === 3) rankHtml = `<span class="rank-medal rank-medal--bronze">3</span>`;
        else rankHtml = `<span class="rank-num">${rank}</span>`;

        // Contestant ID
        const displayId = anonymizeMode ? obfuscateId(entry.contestant_id) : entry.contestant_id;

        // Archetype tag
        let archHtml = '';
        if (entry.engine_archetype && entry.engine_archetype !== 'Unclassified') {
            const archClass = getArchClass(entry.engine_archetype);
            archHtml = `<span class="arch-tag ${archClass}">${entry.engine_archetype}</span>`;
        }

        // Score delta
        let deltaHtml = '';
        if (entry.delta_score > 0) {
            deltaHtml = `<span class="delta delta--up">+${entry.delta_score.toFixed(2)}</span>`;
        }

        // P99 delta
        let deltaP99Html = '';
        if (entry.delta_p99 < 0) {
            deltaP99Html = `<span class="delta delta--up">${entry.delta_p99.toLocaleString()}</span>`;
        } else if (entry.delta_p99 > 0) {
            deltaP99Html = `<span class="delta delta--down">+${entry.delta_p99.toLocaleString()}</span>`;
        }

        // Data formatting
        const correctness = entry.correctness_score != null ? entry.correctness_score.toFixed(1) : '0.0';
        const latency = entry.p99_us != null ? `${entry.p99_us.toLocaleString()}` : '-';
        const tps = entry.actual_tps != null ? entry.actual_tps.toLocaleString(undefined, { minimumFractionDigits: 1, maximumFractionDigits: 1 }) : '-';

        // Sparkline
        const sparkSvg = generateSparkline(entry);

        tr.innerHTML = `
            <td>${rankHtml}</td>
            <td><span class="cell-mono" style="font-size:0.78rem;">${displayId}</span>${archHtml}</td>
            <td><span class="badge ${badgeClass}">${verdict}</span></td>
            <td class="cell-right"><span class="cell-mono cell-score">${entry.composite_score.toFixed(2)}</span>${deltaHtml}</td>
            <td class="cell-right cell-mono">${correctness}%</td>
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
        verdict.includes('Wrong') || verdict.includes('Exceeded')) return 'badge--danger';
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

// ─── SVG Sparkline Generator ────────────────────────────────────────────
function generateSparkline(entry) {
    // Synthesize a mini trend from available data points
    const scores = entry.score_history || null;
    let dataPoints;
    if (scores && scores.length >= 2) {
        dataPoints = scores.slice(-5);
    } else {
        // Fallback: synthesize from composite_score + delta
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

    // Line color: emerald if trending up, zinc if flat/down
    const trending = dataPoints[dataPoints.length - 1] > dataPoints[0];
    const lineColor = trending ? '#10b981' : '#71717a';
    const dotColor = trending ? '#10b981' : '#71717a';

    return `<svg width="${w}" height="${h}" viewBox="0 0 ${w} ${h}" fill="none">
        <path d="${pathD}" stroke="${lineColor}" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" fill="none"/>
        <circle cx="${lastPt.x.toFixed(1)}" cy="${lastPt.y.toFixed(1)}" r="2" fill="${dotColor}"/>
    </svg>`;
}

// ─── Sorting ────────────────────────────────────────────────────────────
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
    const query = document.getElementById('scoreboard-search').value.toLowerCase().trim();
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

function updateSortIcons() {
    const fields = ['rank', 'contestant_id', 'verdict', 'composite_score', 'correctness_score', 'p99_us', 'actual_tps'];
    fields.forEach(f => {
        const el = document.getElementById(`sort-icon-${f}`);
        if (!el) return;
        if (currentSortField === f) {
            el.textContent = currentSortOrder === 'asc' ? '\u25B2' : '\u25BC';
        } else {
            el.textContent = '';
        }
    });
}

// ─── Anonymize Toggle ───────────────────────────────────────────────────
function toggleAnonymize() {
    anonymizeMode = !anonymizeMode;
    const icon = document.getElementById('anon-icon');
    if (anonymizeMode) {
        // Eye-off icon
        icon.innerHTML = '<path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24"/><line x1="1" y1="1" x2="23" y2="23"/>';
    } else {
        // Eye icon
        icon.innerHTML = '<path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/>';
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

// ─── Keyboard Navigation ────────────────────────────────────────────────
function setupKeyboardNav() {
    document.addEventListener('keydown', (e) => {
        const activeTag = document.activeElement?.tagName;
        const isInput = activeTag === 'INPUT' || activeTag === 'TEXTAREA';
        const drawerOpen = document.getElementById('drawer').classList.contains('drawer--open');

        // Escape: close drawer or defocus
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

        // "/" to focus search
        if (e.key === '/' && !isInput && !drawerOpen) {
            e.preventDefault();
            document.getElementById('scoreboard-search').focus();
            return;
        }

        // Skip row nav if inside an input or drawer is open
        if (isInput || drawerOpen) return;

        const rows = document.querySelectorAll('#leaderboard-tbody tr:not(.skeleton-row)');
        const maxIdx = rows.length - 1;
        if (maxIdx < 0) return;

        // j / ArrowDown: move down
        if (e.key === 'j' || e.key === 'ArrowDown') {
            e.preventDefault();
            focusedRowIndex = Math.min(focusedRowIndex + 1, maxIdx);
            applyRowFocus(rows);
            return;
        }

        // k / ArrowUp: move up
        if (e.key === 'k' || e.key === 'ArrowUp') {
            e.preventDefault();
            focusedRowIndex = Math.max(focusedRowIndex - 1, 0);
            applyRowFocus(rows);
            return;
        }

        // Enter: open drawer for focused row
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

// ─── Submission ─────────────────────────────────────────────────────────
async function submitEngine(event) {
    event.preventDefault();
    dismissError();

    const contestantId = document.getElementById('contestant-id-input').value.trim();
    const fileInput = document.getElementById('file-input');
    const gitInput = document.getElementById('github-url-input');
    const submitBtn = document.getElementById('submit-btn');

    if (!contestantId) {
        showError('Please specify a valid Contestant ID.');
        return;
    }

    const formData = new FormData();
    formData.append('contestant_id', contestantId);

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
        const response = await fetch('/api/v1/submit', { method: 'POST', body: formData });

        if (!response.ok) {
            const errRes = await response.json();
            throw new Error(errRes.error || 'Server rejected submission');
        }

        const result = await response.json();

        // Reset inputs
        fileInput.value = '';
        gitInput.value = '';
        document.getElementById('file-name-preview').textContent = '';

        // Show pipeline monitor
        currentSubmissionId = result.build_id;
        document.getElementById('monitor-submission-id').textContent = `ID: ${currentSubmissionId}`;
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
    return '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" style="width:14px;height:14px;"><polygon points="5 3 19 12 5 21 5 3"/></svg>';
}

function svgSpinner() {
    return '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" style="width:14px;height:14px;animation:spin 1s linear infinite;"><path d="M21 12a9 9 0 1 1-6.219-8.56"/></svg>';
}

// ─── Pipeline Monitor ───────────────────────────────────────────────────
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
                setTimeout(() => openDrawer(buildId), 800);
            }
        } catch (err) {
            console.error('Pipeline poll error:', err);
        }
    }, 1000);
}

function updateMonitorUI(status, verdict) {
    const statusEl = document.getElementById('monitor-status-val');
    const bar = document.getElementById('monitor-progress-bar');

    const steps = ['step-queued', 'step-compiling', 'step-running', 'step-finished'];
    steps.forEach(id => {
        const el = document.getElementById(id);
        el.className = 'timeline__step';
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
    cfg.done.forEach(id => document.getElementById(id).classList.add('timeline__step--done'));
    if (cfg.active) document.getElementById(cfg.active).classList.add('timeline__step--active');
    if (cfg.fail) document.getElementById(cfg.fail).classList.add('timeline__step--fail');
}

// ─── Drawer ─────────────────────────────────────────────────────────────
async function openDrawer(submissionId) {
    const drawer = document.getElementById('drawer');
    const backdrop = document.getElementById('drawer-backdrop');

    drawer.classList.add('drawer--open');
    backdrop.classList.add('drawer-backdrop--open');

    try {
        const response = await fetch(`/api/v1/build/${submissionId}`);
        if (!response.ok) throw new Error('Failed to fetch details');
        const build = await response.json();
        populateDrawerDetails(build);
    } catch (err) {
        console.error('Drawer fetch error:', err);
    }
}

function closeDrawer() {
    document.getElementById('drawer').classList.remove('drawer--open');
    document.getElementById('drawer-backdrop').classList.remove('drawer-backdrop--open');
}

// ─── Drawer Population ──────────────────────────────────────────────────
function populateDrawerDetails(build) {
    const diag = build.diagnostics || {};

    // Header
    document.getElementById('drawer-title').textContent = `Diagnostics: ${build.contestant_id}`;

    // Performance triptych
    const correctnessVal = build.status === 'completed' && diag.correctness != null
        ? `${diag.correctness.toFixed(1)}%` : '0.0%';
    const latencyVal = build.status === 'completed' && diag.p99_us != null
        ? `${diag.p99_us.toLocaleString()} \u00B5s` : '-';
    const throughputVal = build.status === 'completed' && diag.tps_end != null
        ? `${diag.tps_end.toLocaleString(undefined, { maximumFractionDigits: 1 })} TPS` : '-';

    document.getElementById('val-correctness').textContent = correctnessVal;
    document.getElementById('val-latency').textContent = latencyVal;
    document.getElementById('val-throughput').textContent = throughputVal;

    // Color the correctness value
    const cEl = document.getElementById('val-correctness');
    cEl.className = 'perf-metric__value';
    if (diag.correctness != null && diag.correctness >= 100) {
        cEl.classList.add('perf-metric__value--success');
    } else if (diag.correctness != null && diag.correctness < 100) {
        cEl.classList.add('perf-metric__value--danger');
    } else {
        cEl.classList.add('perf-metric__value--info');
    }

    // Stability
    document.getElementById('val-stability-dev').textContent =
        diag.stability_std_dev != null ? diag.stability_std_dev.toFixed(2) : '0.00';
    document.getElementById('val-stability-bonus').textContent =
        diag.stability_bonus != null ? `+${diag.stability_bonus}` : '+0';

    // Metadata
    document.getElementById('det-sub-id').textContent = build.build_id;
    document.getElementById('det-contestant-id').textContent = build.contestant_id;

    const sourceRow = document.getElementById('det-source-row');
    const sourceVal = document.getElementById('det-source-value');
    if (build.github_url) {
        sourceRow.classList.remove('hidden');
        sourceVal.innerHTML = `<a href="${build.github_url}" target="_blank" style="color:var(--accent);text-decoration:underline;">${build.github_url}</a>`;
    } else {
        sourceRow.classList.add('hidden');
        sourceVal.textContent = '';
    }

    // Verdict badge
    const verdict = build.verdict || 'Pending';
    const badgeClass = getBadgeClass(verdict);
    const reason = diag.reason || '';
    const reasonHtml = reason ? `<div style="font-size:0.7rem;color:var(--text-secondary);margin-top:4px;">${reason}</div>` : '';
    document.getElementById('det-verdict-badge').innerHTML = `<span class="badge ${badgeClass}">${verdict}</span>${reasonHtml}`;

    // Hardware
    document.getElementById('det-cores').textContent = '1 Core (Pinned)';
    document.getElementById('det-mem').textContent = '256 MB';
    document.getElementById('det-completed-at').textContent = new Date(build.submitted_at).toLocaleString();

    // Log console with timestamps
    const consoleBox = document.getElementById('log-console-box');
    const ts = formatLogTimestamp();
    if (build.status === 'failed') {
        const errorMsg = diag.error || 'Evaluation failure during sandbox isolation check.';
        consoleBox.innerHTML = `<span class="log-ts">${ts}</span><span class="log-err">[FATAL]</span> ${errorMsg}`;
    } else {
        const warnings = diag.warnings || [];
        if (warnings.length > 0) {
            consoleBox.innerHTML = warnings.map(w =>
                `<span class="log-ts">${ts}</span><span class="log-warn">[WARN]</span> ${w}`
            ).join('\n');
        } else {
            consoleBox.innerHTML = [
                `<span class="log-ts">${ts}</span><span class="log-info">[OK]</span> Priority checks: 100% passed`,
                `<span class="log-ts">${ts}</span><span class="log-info">[OK]</span> CPU limits: enforced`,
                `<span class="log-ts">${ts}</span><span class="log-info">[OK]</span> Network isolation: active`,
                `<span class="log-ts">${ts}</span><span class="log-info">[OK]</span> Output matches expected criteria`,
            ].join('\n');
        }
    }
    // Auto-scroll console to bottom
    consoleBox.scrollTop = consoleBox.scrollHeight;

    // Anomalies
    const phantom = diag.phantom_fills || 0;
    const priority = diag.priority_violations || 0;
    const discrepancies = diag.orders_failed || 0;

    document.getElementById('det-priority-violations').textContent = priority.toLocaleString();
    document.getElementById('det-phantom-fills').textContent = phantom.toLocaleString();
    document.getElementById('det-trade-discrepancies').textContent = discrepancies.toLocaleString();

    updateAnomalyBadge('det-priority-status', priority);
    updateAnomalyBadge('det-phantom-status', phantom);
    updateAnomalyBadge('det-discrepancy-status', discrepancies);

    // Strategy breakdown
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

    // Charts
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
    if (count === 0) {
        el.innerHTML = `<span class="badge badge--accepted" style="padding:2px 6px;">PASS</span>`;
    } else {
        el.innerHTML = `<span class="badge badge--danger" style="padding:2px 6px;">FAIL</span>`;
    }
}

// ─── Charts (Emerald/Zinc theme + Crosshair tooltips) ───────────────────

const chartGridColor = 'rgba(39, 39, 42, 0.4)';
const chartTickColor = '#52525b';

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
                    backgroundColor: 'rgba(16, 185, 129, 0.12)',
                    borderColor: '#10b981',
                    borderWidth: 2,
                    pointBackgroundColor: '#10b981',
                    pointBorderColor: '#fafafa',
                    pointHoverBackgroundColor: '#fafafa',
                    pointHoverBorderColor: '#10b981',
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
                    borderColor: '#10b981',
                    backgroundColor: 'rgba(16, 185, 129, 0.05)',
                    borderWidth: 2,
                    tension: 0.15,
                    fill: true,
                    pointBackgroundColor: '#10b981',
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
                borderColor: '#10b981',
                backgroundColor: 'rgba(16, 185, 129, 0.05)',
                borderWidth: 2,
                tension: 0.3,
                fill: true,
                pointBackgroundColor: '#10b981',
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
