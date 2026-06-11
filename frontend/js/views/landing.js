/* ========================================================================
   IICPC BenchGrid — Landing Page View
   ======================================================================== */

export function renderLanding() {
    return `
        <div class="landing-container" style="max-width:800px; margin: 40px auto; padding: 20px;">
            <div style="text-align:center; margin-bottom: 60px;">
                <div class="glitch-title" style="font-family:var(--font-display); font-size:2.6rem; font-weight:700; letter-spacing:-0.5px; margin-bottom:15px; color:var(--text-primary);">
                    IICPC <span style="color:var(--accent);">[BENCHGRID]</span>
                </div>
                <div style="font-family:var(--font-mono); color:var(--text-secondary); font-size:0.85rem; max-width:550px; margin:0 auto; line-height:1.6;">
                    High-Frequency Trading Matching Engine sandboxing and benchmarking arena for the IICPC Summer Hackathon 2026.
                </div>
            </div>

            <div class="grid" style="display:grid; grid-template-columns:1fr 1fr; gap:20px; margin-bottom:40px;">
                <div class="card" style="padding:24px; border:1px solid var(--border-subtle);">
                    <div style="font-family:var(--font-mono); font-size:0.75rem; color:var(--accent); text-transform:uppercase; margin-bottom:10px;">[+] Timeline</div>
                    <h3 style="font-family:var(--font-display); font-size:1.15rem; font-weight:700; margin-bottom:12px; color:var(--text-primary);">Hackathon Status</h3>
                    <p style="color:var(--text-secondary); font-size:0.85rem; line-height:1.5; margin-bottom:20px;">
                        The submission window for the HFT Arena is open. Teams can upload their C++ matching engines, trigger bot fleet load generation, and view telemetry data live.
                    </p>
                    <a href="#/arena" class="btn btn-primary" style="text-decoration:none; text-align:center; display:block;">Enter Arena</a>
                </div>

                <div class="card" style="padding:24px; border:1px solid var(--border-subtle); display:flex; flex-direction:column; justify-content:space-between;">
                    <div>
                        <div style="font-family:var(--font-mono); font-size:0.75rem; color:var(--accent); text-transform:uppercase; margin-bottom:10px;">[•] Countdown</div>
                        <h3 style="font-family:var(--font-display); font-size:1.15rem; font-weight:700; margin-bottom:12px; color:var(--text-primary);">Time Remaining</h3>
                        <div id="countdown-clock" style="font-family:var(--font-mono); font-size:1.6rem; font-weight:700; color:var(--text-primary); margin: 15px 0;">
                            00d 00h 00m 00s
                        </div>
                    </div>
                    <a href="#/protocol" class="btn btn-outline" style="text-decoration:none; text-align:center; display:block;">Read Technical Spec</a>
                </div>
            </div>

            <div class="card" style="padding:20px; border:1px solid var(--border-subtle); border-left: 4px solid var(--accent);">
                <div style="display:flex; justify-content:space-between; align-items:center;">
                    <div>
                        <div style="font-weight:700; color:var(--text-primary); margin-bottom:4px;">[x] Latest Announcement</div>
                        <div style="font-size:0.8rem; color:var(--text-secondary);">System testing phase will be triggered automatically upon contest completion.</div>
                    </div>
                    <span class="badge" style="padding:2px 8px; font-size:10px; background:var(--text-primary); color:var(--bg-base); border-radius:var(--radius-sm);">LIVE</span>
                </div>
            </div>
        </div>
    `;
}

export function hydrateProtocol() {
    const targetDate = new Date("2026-06-15T23:59:59Z").getTime();
    
    function updateClock() {
        const now = new Date().getTime();
        const diff = targetDate - now;

        const clock = document.getElementById("countdown-clock");
        if (!clock) return;

        if (diff <= 0) {
            clock.textContent = "Contest Ended";
            return;
        }

        const days = Math.floor(diff / (1000 * 60 * 60 * 24));
        const hours = Math.floor((diff % (1000 * 60 * 60 * 24)) / (1000 * 60 * 60));
        const minutes = Math.floor((diff % (1000 * 60 * 60)) / (1000 * 60));
        const seconds = Math.floor((diff % (1000 * 60)) / 1000);

        clock.textContent = `${days}d ${hours}h ${minutes}m ${seconds}s`;
    }

    updateClock();
    const timer = setInterval(updateClock, 1000);

    const app = document.getElementById("app");
    const observer = new MutationObserver(() => {
        if (!document.getElementById("countdown-clock")) {
            clearInterval(timer);
            observer.disconnect();
        }
    });
    observer.observe(app, { childList: true });
}
export { hydrateProtocol as hydrateProtocolLanding };
