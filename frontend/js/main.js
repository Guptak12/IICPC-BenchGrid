/* ========================================================================
   IICPC BenchGrid — main.js Entrypoint
   ======================================================================== */

import { initRouter } from './router.js?v=1.0.2';

async function checkHealth() {
    const pgLed = document.getElementById('postgres-led');
    const redisLed = document.getElementById('redis-led');
    if (!pgLed || !redisLed) return;

    try {
        const response = await fetch('/leaderboard.json');
        if (response.ok) {
            pgLed.className = 'status-led status-led--ok';
            redisLed.className = 'status-led status-led--ok';
        } else {
            pgLed.className = 'status-led status-led--error';
            redisLed.className = 'status-led status-led--error';
        }
    } catch (_) {
        pgLed.className = 'status-led status-led--error';
        redisLed.className = 'status-led status-led--error';
    }
}

// Initial health check and periodic status updates
checkHealth();
setInterval(checkHealth, 5000);

// Boot application
initRouter();
