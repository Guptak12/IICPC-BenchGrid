/* ========================================================================
   IICPC BenchGrid — Auth Views
   ======================================================================== */

import { API } from '../api.js';
import { updateNavbar } from '../router.js';

export function renderLogin() {
    const urlParams = new URLSearchParams(window.location.hash.split('?')[1]);
    const errorParam = urlParams.get('error');
    let errorBannerHtml = '';
    
    if (errorParam) {
        let displayError = 'Authentication failed.';
        if (errorParam === 'oauth_unconfigured') displayError = 'GitHub OAuth is unconfigured on the server.';
        else if (errorParam === 'no_code') displayError = 'No OAuth authorization code received.';
        else if (errorParam.startsWith('unauthorized_')) displayError = `GitHub authorization denied: ${errorParam.replace('unauthorized_', '')}`;
        
        errorBannerHtml = `
            <div class="error-banner error-banner--visible" style="position:static; margin-bottom:20px; width:100%; box-sizing:border-box;">
                <div style="display:flex; justify-content:space-between; align-items:center;">
                    <span id="error-banner-text">${displayError}</span>
                </div>
            </div>
        `;
    }

    return `
        <div class="auth-container" style="max-width:400px; margin: 60px auto; padding: 20px;">
            <div class="card" style="padding:32px; border:1px solid var(--border-subtle);">
                <div style="text-align:center; margin-bottom:25px;">
                    <h2 style="font-family:var(--font-display); font-size:1.4rem; font-weight:700; color:var(--text-primary); margin-bottom:5px;">Sign In</h2>
                    <p style="color:var(--text-secondary); font-size:0.85rem;">Access the BenchGrid Benchmarking Terminal</p>
                </div>

                ${errorBannerHtml}
                <div id="auth-error-banner" class="error-banner" style="position:static; margin-bottom:20px; width:100%; box-sizing:border-box;">
                    <span id="auth-error-text"></span>
                </div>

                <!-- GitHub OAuth Button -->
                <a href="/api/v1/auth/github" class="btn btn-outline" style="text-decoration:none; margin-bottom:20px; background:#201d1d; border:1px solid #302c2c; color:#fdfcfc; display:flex; align-items:center; justify-content:center; gap:8px;">
                    <i class="fa-brands fa-github" style="font-size:16px;"></i>
                    <span>Sign in with GitHub</span>
                </a>

                <div style="display:flex; align-items:center; justify-content:center; margin-bottom:20px;">
                    <span style="height:1px; background:var(--border-subtle); flex-grow:1;"></span>
                    <span style="padding:0 10px; font-size:10px; color:var(--text-secondary); text-transform:uppercase; font-family:var(--font-mono);">[or credentials]</span>
                    <span style="height:1px; background:var(--border-subtle); flex-grow:1;"></span>
                </div>

                <form id="login-form" style="display:flex; flex-direction:column; gap:15px;">
                    <div>
                        <label class="control-label" style="display:block; margin-bottom:6px; font-size:10px; text-transform:uppercase; font-family:var(--font-mono); color:var(--text-secondary);">Handle or Email</label>
                        <input type="text" id="login-handle" class="form-input" required placeholder="handle or email" style="width:100%;">
                    </div>
                    <div>
                        <label class="control-label" style="display:block; margin-bottom:6px; font-size:10px; text-transform:uppercase; font-family:var(--font-mono); color:var(--text-secondary);">Password</label>
                        <input type="password" id="login-password" class="form-input" required placeholder="••••••••" style="width:100%;">
                    </div>
                    <button type="submit" id="auth-submit-btn" class="btn btn-primary" style="margin-top:10px;">Sign In</button>
                </form>

                <div style="margin-top:25px; text-align:center; font-size:0.85rem; color:var(--text-secondary);">
                    Don't have an account? <a href="#/register" style="color:var(--accent); text-decoration:underline;">Register here</a>
                </div>
            </div>
        </div>
    `;
}

export function renderRegister() {
    return `
        <div class="auth-container" style="max-width:400px; margin: 60px auto; padding: 20px;">
            <div class="card" style="padding:32px; border:1px solid var(--border-subtle);">
                <div style="text-align:center; margin-bottom:25px;">
                    <h2 style="font-family:var(--font-display); font-size:1.4rem; font-weight:700; color:var(--text-primary); margin-bottom:5px;">Create Account</h2>
                    <p style="color:var(--text-secondary); font-size:0.85rem;">Join the HFT Benchmarking Arena</p>
                </div>

                <div id="auth-error-banner" class="error-banner" style="position:static; margin-bottom:20px; width:100%; box-sizing:border-box;">
                    <span id="auth-error-text"></span>
                </div>

                <!-- GitHub OAuth Button -->
                <a href="/api/v1/auth/github" class="btn btn-outline" style="text-decoration:none; margin-bottom:20px; background:#201d1d; border:1px solid #302c2c; color:#fdfcfc; display:flex; align-items:center; justify-content:center; gap:8px;">
                    <i class="fa-brands fa-github" style="font-size:16px;"></i>
                    <span>Register with GitHub</span>
                </a>

                <div style="display:flex; align-items:center; justify-content:center; margin-bottom:20px;">
                    <span style="height:1px; background:var(--border-subtle); flex-grow:1;"></span>
                    <span style="padding:0 10px; font-size:10px; color:var(--text-secondary); text-transform:uppercase; font-family:var(--font-mono);">[or credentials]</span>
                    <span style="height:1px; background:var(--border-subtle); flex-grow:1;"></span>
                </div>

                <form id="register-form" style="display:flex; flex-direction:column; gap:15px;">
                    <div>
                        <label class="control-label" style="display:block; margin-bottom:6px; font-size:10px; text-transform:uppercase; font-family:var(--font-mono); color:var(--text-secondary);">Handle (Username)</label>
                        <input type="text" id="reg-handle" class="form-input" required placeholder="e.g. tourister" style="width:100%;">
                    </div>
                    <div>
                        <label class="control-label" style="display:block; margin-bottom:6px; font-size:10px; text-transform:uppercase; font-family:var(--font-mono); color:var(--text-secondary);">Email Address</label>
                        <input type="email" id="reg-email" class="form-input" required placeholder="user@example.com" style="width:100%;">
                    </div>
                    <div>
                        <label class="control-label" style="display:block; margin-bottom:6px; font-size:10px; text-transform:uppercase; font-family:var(--font-mono); color:var(--text-secondary);">Password</label>
                        <input type="password" id="reg-password" class="form-input" required placeholder="at least 6 characters" style="width:100%;">
                    </div>
                    <button type="submit" id="auth-submit-btn" class="btn btn-primary" style="margin-top:10px;">Create Account</button>
                </form>

                <div style="margin-top:25px; text-align:center; font-size:0.85rem; color:var(--text-secondary);">
                    Already have an account? <a href="#/login" style="color:var(--accent); text-decoration:underline;">Sign in here</a>
                </div>
            </div>
        </div>
    `;
}

export function hydrateAuth(viewType) {
    const errorBanner = document.getElementById('auth-error-banner');
    const errorText = document.getElementById('auth-error-text');

    function showError(msg) {
        errorText.textContent = msg;
        errorBanner.classList.add('error-banner--visible');
    }

    if (viewType === 'login') {
        const form = document.getElementById('login-form');
        if (!form) return;

        form.addEventListener('submit', async (e) => {
            e.preventDefault();
            errorBanner.classList.remove('error-banner--visible');

            const handle = document.getElementById('login-handle').value.trim();
            const password = document.getElementById('login-password').value;

            try {
                await API.login(handle, password);
                await updateNavbar();
                window.location.hash = '#/arena';
            } catch (err) {
                showError(err.message);
            }
        });
    } else if (viewType === 'register') {
        const form = document.getElementById('register-form');
        if (!form) return;

        form.addEventListener('submit', async (e) => {
            e.preventDefault();
            errorBanner.classList.remove('error-banner--visible');

            const handle = document.getElementById('reg-handle').value.trim();
            const email = document.getElementById('reg-email').value.trim();
            const password = document.getElementById('reg-password').value;

            try {
                await API.register(handle, email, password);
                await updateNavbar();
                window.location.hash = '#/arena';
            } catch (err) {
                showError(err.message);
            }
        });
    }
}
