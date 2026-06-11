/* ========================================================================
   IICPC BenchGrid — SPA Router
   ======================================================================== */

import { API } from './api.js';

let currentStream = null;
let currentUser = null;

export function setCurrentStream(stream) {
    if (currentStream) {
        console.log("[Router] Closing active SSE stream to prevent memory leak");
        currentStream.close();
    }
    currentStream = stream;
}

export function getCurrentUser() {
    return currentUser;
}

export async function updateNavbar() {
    const navRight = document.getElementById('nav-right');
    if (!navRight) return;

    try {
        currentUser = await API.me();
        
        let adminLink = '';
        if (currentUser.role === 'admin') {
            adminLink = `<a href="#/admin" class="nav-link">Admin Console</a>`;
        }

        navRight.innerHTML = `
            ${adminLink}
            <a href="#/profile/${currentUser.handle}" class="nav-link profile-link">
                <i class="fa-solid fa-user" style="color:var(--accent);"></i>
                <span class="cell-mono">${currentUser.handle}</span>
            </a>
            <button id="logout-btn" class="btn btn-outline" style="padding:4px 10px; font-size:11px; width:auto; height:24px;">Logout</button>
        `;

        document.getElementById('logout-btn').addEventListener('click', async () => {
            await API.logout();
            currentUser = null;
            updateNavbar();
            window.location.hash = '#/login';
        });

    } catch (_) {
        currentUser = null;
        navRight.innerHTML = `
            <a href="#/login" class="nav-link">Login</a>
            <a href="#/register" class="btn btn-primary" style="padding:4px 12px; font-size:11px; width:auto; text-decoration:none; line-height:1.2;">Register</a>
        `;
    }
}

// Map of view renderers
const routes = {
    '/': async () => {
        const { renderLanding } = await import('./views/landing.js');
        return renderLanding();
    },
    '/login': async () => {
        const { renderLogin } = await import('./views/auth.js');
        return renderLogin();
    },
    '/register': async () => {
        const { renderRegister } = await import('./views/auth.js');
        return renderRegister();
    },
    '/arena': async () => {
        const { renderArenaList } = await import('./views/arena.js');
        return renderArenaList();
    },
    '/arena/:id': async (params) => {
        const { renderArenaDetail } = await import('./views/arena.js');
        return renderArenaDetail(params.id);
    },
    '/profile/:id': async (params) => {
        const { renderProfile } = await import('./views/profile.js');
        return renderProfile(params.id);
    },
    '/admin': async () => {
        const { renderAdmin } = await import('./views/admin.js');
        return renderAdmin();
    },
    '/protocol': async () => {
        const { renderProtocol } = await import('./views/protocol.js');
        return renderProtocol();
    }
};

// Route matching engine
async function handleRoute() {
    // Make sure SSE stream is closed on route change
    setCurrentStream(null);

    const hash = window.location.hash || '#/';
    const path = hash.substring(1) || '/';

    // Parse route and parameters
    let matchedRoute = null;
    let params = {};

    for (const routePath in routes) {
        const routeRegex = new RegExp('^' + routePath.replace(/:[^\s/]+/g, '([^/]+)') + '$');
        const match = path.match(routeRegex);
        if (match) {
            matchedRoute = routes[routePath];
            const paramNames = (routePath.match(/:[^\s/]+/g) || []).map(p => p.substring(1));
            paramNames.forEach((name, idx) => {
                params[name] = match[idx + 1];
            });
            break;
        }
    }

    const appContainer = document.getElementById('app');
    if (!appContainer) return;

    // Defensively check user session state
    await updateNavbar();

    if (matchedRoute) {
        try {
            appContainer.innerHTML = `<div class="text-center" style="color:var(--text-secondary);padding:80px;"><i class="fa-solid fa-spinner fa-spin" style="margin-right:8px;"></i>Loading Terminal View...</div>`;
            const viewHtml = await matchedRoute(params);
            appContainer.innerHTML = viewHtml;
            
            // Post render hydration if the view defines it
            const viewName = matchedRoute.toString();
            // Dynamically query loaded hydration scripts
            const pathParts = path.split('/');
            const rootPath = pathParts[1] || '';
            
            if (rootPath === 'arena' && pathParts.length > 2) {
                const { hydrateArenaDetail } = await import('./views/arena.js');
                hydrateArenaDetail(pathParts[2]);
            } else if (rootPath === 'login' || rootPath === 'register') {
                const { hydrateAuth } = await import('./views/auth.js');
                hydrateAuth(rootPath);
            } else if (rootPath === 'admin') {
                const { hydrateAdmin } = await import('./views/admin.js');
                hydrateAdmin();
            } else if (rootPath === 'profile') {
                const { hydrateProfile } = await import('./views/profile.js');
                hydrateProfile(params.id);
            } else if (rootPath === 'arena') {
                const { hydrateArenaList } = await import('./views/arena.js');
                hydrateArenaList();
            } else if (rootPath === 'protocol' || rootPath === '') {
                const { hydrateProtocol } = await import('./views/protocol.js');
                hydrateProtocol();
            }
        } catch (err) {
            console.error('Routing load error:', err);
            appContainer.innerHTML = `
                <div style="border:1px dashed var(--status-danger); padding:40px; margin:20px auto; max-width:600px; border-radius:8px; background:rgba(239,68,68,0.05); text-align:center;">
                    <div style="color:var(--status-danger); font-weight:600; margin-bottom:10px; font-size:1.1rem;">
                        <i class="fa-solid fa-triangle-exclamation" style="margin-right:8px;"></i>Terminal Error
                    </div>
                    <div style="color:var(--text-secondary); font-family:var(--font-mono); font-size:0.8rem; margin-bottom:20px;">
                        ${err.message}
                    </div>
                    <a href="#/arena" class="btn btn-outline" style="width:auto; display:inline-flex;">Return to Arenas</a>
                </div>
            `;
        }
    } else {
        appContainer.innerHTML = `
            <div style="border:1px dashed var(--border); padding:60px; margin:40px auto; max-width:500px; border-radius:8px; text-align:center;">
                <h2 style="font-weight:600; margin-bottom:10px; color:#ffffff;">404 TERMINAL NOT FOUND</h2>
                <p style="color:var(--text-secondary); margin-bottom:20px; font-size:0.9rem;">Requested node does not exist or has been archived.</p>
                <a href="#/" class="btn btn-primary" style="width:auto; display:inline-flex;">Return Home</a>
            </div>
        `;
    }
}

export function initRouter() {
    window.addEventListener('hashchange', handleRoute);
    window.addEventListener('DOMContentLoaded', handleRoute);
}
