/* ========================================================================
   IICPC BenchGrid — Admin Panel View
   ======================================================================== */

import { API } from '../api.js';

let telemetryInterval = null;

export async function renderAdmin() {
    let arenas = [];
    try {
        arenas = await API.listArenas();
    } catch (err) {
        console.error("Failed to load arenas in admin:", err);
    }

    let arenasHtml = '';
    if (arenas.length === 0) {
        arenasHtml = `<div style="padding:20px; text-align:center; color:var(--text-secondary); font-family:var(--font-mono); font-size:0.8rem;">[No arenas found in registry]</div>`;
    } else {
        arenas.forEach(a => {
            const isSystemTesting = a.status === 'system_test';
            const isEnded = a.status === 'ended';
            const isActive = a.status === 'active';
            const isUpcoming = a.status === 'upcoming';

            arenasHtml += `
                <div class="admin-arena-card" style="border:1px solid var(--border-subtle); background:var(--bg-base); padding:20px; border-radius:4px; margin-bottom:15px;">
                    <div style="display:flex; justify-content:space-between; align-items:start; margin-bottom:15px; border-bottom:1px solid var(--border-subtle); padding-bottom:10px;">
                        <div>
                            <h4 style="color:var(--text-primary); font-weight:700; margin-bottom:3px;">${a.title}</h4>
                            <span class="cell-mono" style="font-size:10px; color:var(--text-muted);">${a.id}</span>
                        </div>
                        <select class="status-select" data-arena-id="${a.id}">
                            <option value="upcoming" ${isUpcoming ? 'selected' : ''}>Upcoming</option>
                            <option value="active" ${isActive ? 'selected' : ''}>Active</option>
                            <option value="system_test" ${isSystemTesting ? 'selected' : ''}>System Testing</option>
                            <option value="ended" ${isEnded ? 'selected' : ''}>Ended</option>
                        </select>
                    </div>

                    <div style="font-family:var(--font-mono); font-size:11px; color:var(--text-secondary); display:flex; flex-direction:column; gap:6px; margin-bottom:15px;">
                        <div>Start: <span style="color:var(--text-primary); font-weight:500;">${new Date(a.start_time).toLocaleString()}</span></div>
                        <div>End: <span style="color:var(--text-primary); font-weight:500;">${new Date(a.end_time).toLocaleString()}</span></div>
                    </div>

                    <div style="display:flex; gap:10px;">
                        <button class="btn btn-outline rejudge-btn" data-arena-id="${a.id}" style="font-size:11px; padding:6px 12px; flex-grow:1;">
                            <i class="fa-solid fa-rotate" style="margin-right:6px;"></i> Rejudge Submissions
                        </button>
                        <a href="#/arena/${a.id}" class="btn btn-outline" style="font-size:11px; padding:6px 12px; text-decoration:none; text-align:center; line-height:1.2;">
                            War Room
                        </a>
                    </div>
                </div>
            `;
        });
    }

    return `
        <div class="admin-container" style="max-width:1100px; margin: 40px auto; padding: 20px;">
            <div style="margin-bottom:30px;">
                <h2 style="font-family:var(--font-display); font-size:1.6rem; font-weight:700; color:var(--text-primary); margin-bottom:5px;">Admin Console</h2>
                <p style="color:var(--text-secondary); font-size:0.85rem;">Manage benchmarking challenges, transitions, and system queue depth metrics.</p>
            </div>

            <div id="admin-error-banner" class="error-banner" style="position:static; margin-bottom:20px; width:100%; box-sizing:border-box;">
                <span id="admin-error-text"></span>
            </div>
            
            <div id="admin-success-banner" class="error-banner" style="position:static; margin-bottom:20px; width:100%; box-sizing:border-box; background:rgba(48,209,88,0.08); border-color:rgba(48,209,88,0.25); color:var(--status-success);">
                <span id="admin-success-text"></span>
            </div>

            <!-- Two Column Layout -->
            <div style="display:grid; grid-template-columns:1.5fr 1fr; gap:24px;">
                
                <!-- Left Stack: Arena lifecycle manager -->
                <div>
                    <div style="margin-bottom:15px;">
                        <h3 style="font-family:var(--font-display); font-size:1.2rem; font-weight:700; color:var(--text-primary); margin:0;">Active Registries</h3>
                    </div>
                    <div style="display:flex; flex-direction:column; gap:16px;">
                        ${arenasHtml}
                    </div>
                </div>

                <!-- Right Stack: Telemetry & Creation form -->
                <div style="display:flex; flex-direction:column; gap:24px;">
                    
                    <!-- Worker Telemetry card -->
                    <div class="card" style="padding:24px; border:1px solid var(--border-subtle); border-radius:4px;">
                        <div style="display:flex; justify-content:space-between; align-items:center; margin-bottom:16px; padding-bottom:8px; border-bottom:1px solid var(--border-subtle);">
                            <h3 style="font-family:var(--font-display); font-size:1.1rem; font-weight:700; color:var(--text-primary); margin:0;">[+] Telemetry</h3>
                            <span class="status-led status-led--ok" style="width:6px; height:6px;"></span>
                        </div>

                        <div style="display:flex; flex-direction:column; gap:16px;">
                            <!-- Compilation queue depth -->
                            <div>
                                <div style="display:flex; justify-content:space-between; font-size:11px; margin-bottom:6px; font-family:var(--font-mono);">
                                    <span style="color:var(--text-secondary);">Compilation Queue</span>
                                    <strong id="comp-queue-depth" style="color:var(--text-primary);">0</strong>
                                </div>
                                <div style="height:4px; background:var(--bg-surface); border-radius:2px; overflow:hidden;">
                                    <div id="comp-queue-bar" style="height:100%; width:0%; background:var(--accent); transition:width var(--duration-normal) ease;"></div>
                                </div>
                            </div>

                            <!-- Pretest queue depth -->
                            <div>
                                <div style="display:flex; justify-content:space-between; font-size:11px; margin-bottom:6px; font-family:var(--font-mono);">
                                    <span style="color:var(--text-secondary);">Pretest Stress Queue</span>
                                    <strong id="pretest-queue-depth" style="color:var(--text-primary);">0</strong>
                                </div>
                                <div style="height:4px; background:var(--bg-surface); border-radius:2px; overflow:hidden;">
                                    <div id="pretest-queue-bar" style="height:100%; width:0%; background:var(--status-success); transition:width var(--duration-normal) ease;"></div>
                                </div>
                            </div>

                            <!-- System queue depth -->
                            <div>
                                <div style="display:flex; justify-content:space-between; font-size:11px; margin-bottom:6px; font-family:var(--font-mono);">
                                    <span style="color:var(--text-secondary);">System Test Queue</span>
                                    <strong id="systest-queue-depth" style="color:var(--text-primary);">0</strong>
                                </div>
                                <div style="height:4px; background:var(--bg-surface); border-radius:2px; overflow:hidden;">
                                    <div id="systest-queue-bar" style="height:100%; width:0%; background:var(--status-warning); transition:width var(--duration-normal) ease;"></div>
                                </div>
                            </div>
                        </div>
                    </div>

                    <!-- Creation Form Card -->
                    <div class="card" style="padding:24px; border:1px solid var(--border-subtle); border-radius:4px;">
                        <div style="margin-bottom:16px; padding-bottom:8px; border-bottom:1px solid var(--border-subtle);">
                            <h3 style="font-family:var(--font-display); font-size:1.1rem; font-weight:700; color:var(--text-primary); margin:0;">[+] Create Arena</h3>
                        </div>

                        <form id="create-arena-form" style="display:flex; flex-direction:column; gap:12px;">
                            <div>
                                <label class="control-label" style="display:block; margin-bottom:5px; font-size:10px; font-family:var(--font-mono); color:var(--text-secondary); text-transform:uppercase;">Title</label>
                                <input type="text" id="arena-title" class="form-input" required placeholder="Arena title" style="width:100%;">
                            </div>
                            <div>
                                <label class="control-label" style="display:block; margin-bottom:5px; font-size:10px; font-family:var(--font-mono); color:var(--text-secondary); text-transform:uppercase;">Description</label>
                                <textarea id="arena-desc" class="form-input" placeholder="Constraints..." style="width:100%; height:60px; font-size:12px; resize:none;"></textarea>
                            </div>
                            <div>
                                <label class="control-label" style="display:block; margin-bottom:5px; font-size:10px; font-family:var(--font-mono); color:var(--text-secondary); text-transform:uppercase;">Start Time</label>
                                <input type="datetime-local" id="arena-start" class="form-input" required style="width:100%; font-size:11px;">
                            </div>
                            <div>
                                <label class="control-label" style="display:block; margin-bottom:5px; font-size:10px; font-family:var(--font-mono); color:var(--text-secondary); text-transform:uppercase;">End Time</label>
                                <input type="datetime-local" id="arena-end" class="form-input" required style="width:100%; font-size:11px;">
                            </div>
                            <button type="submit" class="btn btn-primary" style="margin-top:10px; font-size:12px;">Create Arena</button>
                        </form>
                    </div>

                </div>
            </div>
        </div>
    `;
}

export function hydrateAdmin() {
    const errorBanner = document.getElementById('admin-error-banner');
    const errorText = document.getElementById('admin-error-text');
    const successBanner = document.getElementById('admin-success-banner');
    const successText = document.getElementById('admin-success-text');

    function showError(msg) {
        successBanner.classList.remove('error-banner--visible');
        errorText.textContent = msg;
        errorBanner.classList.add('error-banner--visible');
    }

    function showSuccess(msg) {
        errorBanner.classList.remove('error-banner--visible');
        successText.textContent = msg;
        successBanner.classList.add('error-banner--visible');
        setTimeout(() => {
            const el = document.getElementById('admin-success-banner');
            if (el) el.classList.remove('error-banner--visible');
        }, 4000);
    }

    // Status Dropdown updates
    document.querySelectorAll('.status-select').forEach(select => {
        select.addEventListener('change', async (e) => {
            const arenaId = select.dataset.arenaId;
            const newStatus = select.value;
            try {
                await API.updateArena(arenaId, { status: newStatus });
                showSuccess(`Arena status updated to '${newStatus}' successfully.`);
            } catch (err) {
                showError(err.message);
            }
        });
    });

    // Rejudge triggering
    document.querySelectorAll('.rejudge-btn').forEach(btn => {
        btn.addEventListener('click', async () => {
            const arenaId = btn.dataset.arenaId;
            try {
                btn.disabled = true;
                btn.innerHTML = `<i class="fa-solid fa-spinner fa-spin" style="margin-right:6px;"></i> Triggering...`;
                await API.rejudgeArena(arenaId);
                showSuccess("System test rejudge triggered on contestants submissions.");
            } catch (err) {
                showError(err.message);
            } finally {
                btn.disabled = false;
                btn.innerHTML = `<i class="fa-solid fa-rotate" style="margin-right:6px;"></i> Rejudge Submissions`;
            }
        });
    });

    // Create Arena handler
    const form = document.getElementById('create-arena-form');
    if (form) {
        form.addEventListener('submit', async (e) => {
            e.preventDefault();
            const title = document.getElementById('arena-title').value.trim();
            const description = document.getElementById('arena-desc').value.trim();
            const startTime = new Date(document.getElementById('arena-start').value).toISOString();
            const endTime = new Date(document.getElementById('arena-end').value).toISOString();

            try {
                await API.createArena({
                    title,
                    description,
                    start_time: startTime,
                    end_time: endTime
                });
                showSuccess(`Arena '${title}' created successfully.`);
                form.reset();
                setTimeout(() => window.location.reload(), 1000);
            } catch (err) {
                showError(err.message);
            }
        });
    }

    // Telemetry polling loop
    async function updateTelemetry() {
        try {
            const tel = await API.getWorkersTelemetry();
            
            const comp = tel.compilation_queue_depth || 0;
            const pre = tel.pretest_queue_depth || 0;
            const sys = tel.systest_queue_depth || 0;

            const elCompVal = document.getElementById('comp-queue-depth');
            const elCompBar = document.getElementById('comp-queue-bar');
            if (elCompVal && elCompBar) {
                elCompVal.textContent = comp;
                elCompBar.style.width = `${Math.min(100, comp * 10)}%`;
            }

            const elPreVal = document.getElementById('pretest-queue-depth');
            const elPreBar = document.getElementById('pretest-queue-bar');
            if (elPreVal && elPreBar) {
                elPreVal.textContent = pre;
                elPreBar.style.width = `${Math.min(100, pre * 10)}%`;
            }

            const elSysVal = document.getElementById('systest-queue-depth');
            const elSysBar = document.getElementById('systest-queue-bar');
            if (elSysVal && elSysBar) {
                elSysVal.textContent = sys;
                elSysBar.style.width = `${Math.min(100, sys * 5)}%`;
            }
        } catch (err) {
            console.error("Telemetry query failed:", err);
        }
    }

    updateTelemetry();
    telemetryInterval = setInterval(updateTelemetry, 2000);

    const app = document.getElementById("app");
    const observer = new MutationObserver(() => {
        if (!document.getElementById("comp-queue-depth")) {
            clearInterval(telemetryInterval);
            telemetryInterval = null;
            observer.disconnect();
        }
    });
    observer.observe(app, { childList: true });
}
