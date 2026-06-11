/* ========================================================================
   IICPC BenchGrid — API Client
   ======================================================================== */

const BASE_URL = '/api/v1';

async function request(path, options = {}) {
    // Include HttpOnly cookies automatically
    options.credentials = 'include';
    
    // Default headers for JSON requests
    if (options.body && !(options.body instanceof FormData)) {
        options.headers = {
            'Content-Type': 'application/json',
            ...options.headers
        };
        options.body = JSON.stringify(options.body);
    }

    const response = await fetch(`${BASE_URL}${path}`, options);
    
    if (!response.ok) {
        let errorMessage = 'API request failed';
        try {
            const errData = await response.json();
            errorMessage = errData.error || errorMessage;
        } catch (_) {}
        throw new Error(errorMessage);
    }

    if (response.status === 204) return null;
    return response.json();
}

export const API = {
    // Auth
    register: (handle, email, password) => request('/auth/register', {
        method: 'POST',
        body: { handle, email, password }
    }),
    login: (handle, password) => request('/auth/login', {
        method: 'POST',
        body: { handle, password }
    }),
    logout: () => request('/auth/logout', { method: 'POST' }),
    me: () => request('/auth/me'),

    // Profiles
    getProfile: (id) => request(`/profile/${id}`),

    // Arenas
    listArenas: () => request('/arena'),
    getArena: (id) => request(`/arena/${id}`),
    registerArena: (id) => request(`/arena/${id}/register`, { method: 'POST' }),

    // Submissions
    submitCode: (formData) => request('/submit', {
        method: 'POST',
        body: formData
    }),
    getBuildStatus: (id) => request(`/build/${id}`),
    getSourceCode: (id) => request(`/submissions/${id}/source`),
    listBuilds: (arenaId) => {
        const query = arenaId ? `?arena_id=${encodeURIComponent(arenaId)}` : '';
        return request(`/builds${query}`);
    },

    // Leaderboards
    getLeaderboard: (arenaId) => request(`/leaderboard/${arenaId}`),

    // Admin Controls
    createArena: (arenaData) => request('/admin/arena', {
        method: 'POST',
        body: arenaData
    }),
    updateArena: (id, arenaData) => request(`/admin/arena/${id}`, {
        method: 'PUT',
        body: arenaData
    }),
    rejudgeArena: (id) => request(`/admin/arena/${id}/rejudge`, { method: 'POST' }),
    getWorkersTelemetry: () => request('/admin/workers')
};
