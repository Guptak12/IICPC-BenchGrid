/* ========================================================================
   IICPC BenchGrid — Profile & Trophy Cabinet View
   ======================================================================== */

import { API } from '../api.js';

export async function renderProfile(handle) {
    let stats = {
        handle: handle,
        highest_score: 0,
        lowest_p99: 0,
        trophies: [],
        contests_played: 0
    };

    try {
        stats = await API.getProfile(handle);
    } catch (err) {
        console.error("Failed to load user profile:", err);
        return `
            <div style="border:1px dashed var(--status-danger); padding:40px; margin:40px auto; max-width:600px; border-radius:8px; background:rgba(239,68,68,0.05); text-align:center;">
                <div style="color:var(--status-danger); font-weight:700; margin-bottom:10px; font-size:1.1rem;">
                    <i class="fa-solid fa-triangle-exclamation" style="margin-right:8px;"></i>Failed to Load Profile
                </div>
                <div style="color:var(--text-secondary); font-family:var(--font-mono); font-size:0.8rem; margin-bottom:20px;">
                    ${err.message}
                </div>
                <a href="#/arena" class="btn btn-outline" style="width:auto; display:inline-flex;">Return to Arenas</a>
            </div>
        `;
    }

    // Format metrics
    const highestScoreStr = stats.highest_score ? stats.highest_score.toFixed(2) : '0.00';
    const lowestP99Str = stats.lowest_p99 ? `${(stats.lowest_p99 / 1000).toFixed(3)} ms` : 'N/A';
    const rawP99Str = stats.lowest_p99 ? `(${stats.lowest_p99.toLocaleString()} μs)` : '';

    // Render trophies
    let trophiesHtml = '';
    if (!stats.trophies || stats.trophies.length === 0) {
        trophiesHtml = `
            <div style="grid-column:1/-1; border:1px dashed var(--border-subtle); border-radius:4px; padding:40px; text-align:center; background:rgba(15,0,0,0.01);">
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" style="width:40px; height:40px; color:var(--text-muted); margin:0 auto 12px; display:block;">
                    <circle cx="12" cy="12" r="10"/>
                    <path d="M8 12h8"/>
                </svg>
                <div style="font-weight:700; color:var(--text-primary); margin-bottom:6px;">Cabinet Empty</div>
                <div style="font-size:0.8rem; color:var(--text-secondary); max-width:280px; margin:0 auto;">
                    Earn digital badges by placing in the top 3 of any official benchmarking contest.
                </div>
            </div>
        `;
    } else {
        stats.trophies.forEach(t => {
            let badgeBg = '';
            let borderStyle = '';
            let badgeText = '';
            
            if (t.type === 'gold') {
                badgeBg = '#fcd34d';
                borderStyle = '1px solid #d97706';
                badgeText = 'Gold Winner';
            } else if (t.type === 'silver') {
                badgeBg = '#f3f4f6';
                borderStyle = '1px solid #4b5563';
                badgeText = 'Silver Runner';
            } else {
                badgeBg = '#fed7aa';
                borderStyle = '1px solid #c2410c';
                badgeText = 'Bronze Finisher';
            }

            trophiesHtml += `
                <div class="trophy-card" style="border:1px solid var(--border-subtle); border-radius:4px; padding:16px; display:flex; align-items:center; gap:16px;">
                    <div class="trophy-badge" style="background:${badgeBg}; border:${borderStyle}; width:44px; height:44px; border-radius:50%; display:flex; align-items:center; justify-content:center; position:relative; flex-shrink:0;">
                        <svg viewBox="0 0 24 24" fill="none" stroke="#201d1d" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" style="width:20px; height:20px;">
                            <circle cx="12" cy="8" r="7"/>
                            <polyline points="8.21 13.89 7 23 12 20 17 23 15.79 13.88"/>
                        </svg>
                        <span class="trophy-rank-num" style="position:absolute; bottom:-2px; right:-2px; background:var(--bg-base); color:var(--text-primary); font-size:9px; font-weight:700; width:16px; height:16px; border-radius:50%; display:flex; align-items:center; justify-content:center; border:1px solid var(--border-strong);">${t.rank}</span>
                    </div>
                    <div style="flex-grow:1;">
                        <div style="font-family:var(--font-mono); font-size:10px; font-weight:700; text-transform:uppercase; color:var(--text-muted); margin-bottom:4px;">
                            ${badgeText}
                        </div>
                        <h4 style="color:var(--text-primary); font-size:1.05rem; font-weight:700; margin-bottom:2px;">
                            ${t.title}
                        </h4>
                        <div style="font-size:0.75rem; color:var(--text-secondary);">
                            Official Benchmarking Standings
                        </div>
                    </div>
                </div>
            `;
        });
    }

    return `
        <div class="profile-container" style="max-width:900px; margin: 40px auto; padding: 20px;">
            <!-- Back navigation -->
            <div style="margin-bottom:25px;">
                <a href="#/arena" class="nav-back-link" style="text-decoration:none; display:inline-flex; align-items:center; gap:8px; font-size:12px; font-family:var(--font-mono); color:var(--text-secondary); transition:color var(--duration-fast) var(--ease-out-expo);">
                    <i class="fa-solid fa-arrow-left"></i>
                    <span>Back to Arenas</span>
                </a>
            </div>

            <!-- Profile Header -->
            <div class="card" style="padding:32px; border:1px solid var(--border-subtle); margin-bottom:24px; position:relative; overflow:hidden; border-radius:4px;">
                <div style="position:absolute; right:0; top:0; transform:translate(15%, -15%); opacity:0.02; pointer-events:none;">
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1" style="width:300px; height:300px; color:var(--text-primary);">
                        <circle cx="12" cy="12" r="10"/>
                        <polyline points="12 6 12 12 16 14"/>
                    </svg>
                </div>

                <div style="display:flex; align-items:center; gap:24px;">
                    <div class="avatar-wrap">
                        <i class="fa-solid fa-user-astronaut"></i>
                    </div>
                    <div>
                        <div style="font-family:var(--font-mono); font-size:10px; text-transform:uppercase; letter-spacing:0.05em; color:var(--accent); margin-bottom:4px;">
                            [Contestant Profile]
                        </div>
                        <h2 style="font-family:var(--font-display); font-size:1.8rem; font-weight:700; color:var(--text-primary); margin:0; line-height:1.1;">
                            ${stats.handle}
                        </h2>
                    </div>
                </div>
            </div>

            <!-- Profile Statistics Grid -->
            <div style="display:grid; grid-template-columns:repeat(3, 1fr); gap:20px; margin-bottom:32px;">
                <div class="card" style="padding:24px; border:1px solid var(--border-subtle); text-align:center; border-radius:4px;">
                    <div style="font-family:var(--font-mono); font-size:10px; text-transform:uppercase; color:var(--text-secondary); margin-bottom:8px;">
                        [Highest Score]
                    </div>
                    <div class="cell-mono" style="font-size:1.6rem; font-weight:700; color:var(--text-primary);">
                        ${highestScoreStr}
                    </div>
                    <div style="font-size:0.75rem; color:var(--text-muted); margin-top:4px;">
                        Global Index Score
                    </div>
                </div>

                <div class="card" style="padding:24px; border:1px solid var(--border-subtle); text-align:center; border-radius:4px;">
                    <div style="font-family:var(--font-mono); font-size:10px; text-transform:uppercase; color:var(--text-secondary); margin-bottom:8px;">
                        [Lowest P99]
                    </div>
                    <div class="cell-mono" style="font-size:1.6rem; font-weight:700; color:var(--accent);">
                        ${lowestP99Str}
                    </div>
                    <div style="font-size:0.75rem; color:var(--text-muted); margin-top:4px;">
                        ${rawP99Str}
                    </div>
                </div>

                <div class="card" style="padding:24px; border:1px solid var(--border-subtle); text-align:center; border-radius:4px;">
                    <div style="font-family:var(--font-mono); font-size:10px; text-transform:uppercase; color:var(--text-secondary); margin-bottom:8px;">
                        [Contests Played]
                    </div>
                    <div class="cell-mono" style="font-size:1.6rem; font-weight:700; color:var(--text-primary);">
                        ${stats.contests_played}
                    </div>
                    <div style="font-size:0.75rem; color:var(--text-muted); margin-top:4px;">
                        Completed Arenas
                    </div>
                </div>
            </div>

            <!-- Trophy Cabinet Section -->
            <div style="margin-bottom:20px;">
                <h3 style="font-family:var(--font-display); font-size:1.2rem; font-weight:700; color:var(--text-primary); margin-bottom:5px;">Trophy Cabinet</h3>
                <p style="color:var(--text-secondary); font-size:0.85rem; margin-bottom:20px;">Achievements unlocked in official matching engine challenges.</p>
            </div>

            <div class="trophy-grid" style="display:grid; grid-template-columns:repeat(auto-fill, minmax(260px, 1fr)); gap:20px;">
                ${trophiesHtml}
            </div>
        </div>
    `;
}

export function hydrateProfile(handle) {
    // Hydration code
}
