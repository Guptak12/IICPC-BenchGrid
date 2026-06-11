package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

var jwtSecret = []byte(getEnv("JWT_SECRET", "benchgrid_super_secret_key_12345"))

type Claims struct {
	UserID string `json:"user_id"`
	Handle string `json:"handle"`
	Role   string `json:"role"`
	jwt.RegisteredClaims
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func generateToken(userID, handle, role string) (string, error) {
	expirationTime := time.Now().Add(24 * time.Hour)
	claims := &Claims{
		UserID: userID,
		Handle: handle,
		Role:   role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expirationTime),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret)
}

func setJWTCookie(c fiber.Ctx, tokenString string) {
	secure := os.Getenv("SECURE_COOKIES") == "true"
	c.Cookie(&fiber.Cookie{
		Name:     "token",
		Value:    tokenString,
		Expires:  time.Now().Add(24 * time.Hour),
		HTTPOnly: true,
		Secure:   secure,
		SameSite: "Strict",
		Path:     "/",
	})
}

func clearJWTCookie(c fiber.Ctx) {
	secure := os.Getenv("SECURE_COOKIES") == "true"
	c.Cookie(&fiber.Cookie{
		Name:     "token",
		Value:    "",
		Expires:  time.Now().Add(-1 * time.Hour),
		HTTPOnly: true,
		Secure:   secure,
		SameSite: "Strict",
		Path:     "/",
	})
}

// Middleware: Optional Authentication
func optionalAuth(c fiber.Ctx) error {
	tokenString := c.Cookies("token")
	if tokenString == "" {
		// Also support Bearer token fallback for testing
		authHeader := c.Get("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			tokenString = strings.TrimPrefix(authHeader, "Bearer ")
		}
	}

	if tokenString != "" {
		claims := &Claims{}
		token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
			return jwtSecret, nil
		})

		if err == nil && token.Valid {
			c.Locals("user_id", claims.UserID)
			c.Locals("handle", claims.Handle)
			c.Locals("role", claims.Role)
		}
	}

	return c.Next()
}

// Middleware: Required Authentication
func requireAuth(c fiber.Ctx) error {
	tokenString := c.Cookies("token")
	if tokenString == "" {
		authHeader := c.Get("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			tokenString = strings.TrimPrefix(authHeader, "Bearer ")
		}
	}

	if tokenString == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Authentication required"})
	}

	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
		return jwtSecret, nil
	})

	if err != nil || !token.Valid {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Invalid or expired token"})
	}

	c.Locals("user_id", claims.UserID)
	c.Locals("handle", claims.Handle)
	c.Locals("role", claims.Role)
	return c.Next()
}

// Middleware: Admin Only
func requireAdmin(c fiber.Ctx) error {
	roleVal := c.Locals("role")
	if roleVal == nil || roleVal.(string) != "admin" {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "Admin privileges required"})
	}
	return c.Next()
}

// User registration handler
func handleRegister(c fiber.Ctx) error {
	var body struct {
		Handle   string `json:"handle"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}

	if err := c.Bind().JSON(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}

	body.Handle = strings.TrimSpace(body.Handle)
	body.Email = strings.TrimSpace(body.Email)

	if body.Handle == "" || body.Email == "" || body.Password == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Handle, email, and password are required"})
	}

	if len(body.Password) < 6 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Password must be at least 6 characters"})
	}

	// Generate bcrypt hash
	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Password hashing failed"})
	}

	// Bootstrap 'admin' handle as role admin for testing, others default to contestant
	role := "contestant"
	if strings.ToLower(body.Handle) == "admin" {
		role = "admin"
	}

	userID := uuid.New().String()
	ctx := context.Background()

	_, err = db.ExecContext(ctx,
		"INSERT INTO users (id, handle, email, password_hash, role) VALUES ($1, $2, $3, $4, $5)",
		userID, body.Handle, body.Email, string(hash), role,
	)
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "Handle or email already registered"})
		}
		log.Printf("Registration insert error: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to create user"})
	}

	// Generate JWT
	tokenString, err := generateToken(userID, body.Handle, role)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to generate token"})
	}

	setJWTCookie(c, tokenString)

	return c.JSON(fiber.Map{
		"user_id": userID,
		"handle":  body.Handle,
		"email":   body.Email,
		"role":    role,
	})
}

// User login handler
func handleLogin(c fiber.Ctx) error {
	var body struct {
		Handle   string `json:"handle"`
		Password string `json:"password"`
	}

	if err := c.Bind().JSON(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}

	body.Handle = strings.TrimSpace(body.Handle)

	var (
		userID       string
		handle       string
		email        sql.NullString
		passwordHash sql.NullString
		role         string
	)

	ctx := context.Background()
	err := db.QueryRowContext(ctx,
		"SELECT id, handle, email, password_hash, role FROM users WHERE handle = $1 OR email = $1",
		body.Handle,
	).Scan(&userID, &handle, &email, &passwordHash, &role)

	if err == sql.ErrNoRows {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Invalid credentials"})
	} else if err != nil {
		log.Printf("Login DB lookup error: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Database error"})
	}

	if !passwordHash.Valid || passwordHash.String == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Please sign in using GitHub"})
	}

	err = bcrypt.CompareHashAndPassword([]byte(passwordHash.String), []byte(body.Password))
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Invalid credentials"})
	}

	// Generate JWT
	tokenString, err := generateToken(userID, handle, role)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to generate token"})
	}

	setJWTCookie(c, tokenString)

	return c.JSON(fiber.Map{
		"user_id": userID,
		"handle":  handle,
		"email":   email.String,
		"role":    role,
	})
}

// User logout handler
func handleLogout(c fiber.Ctx) error {
	clearJWTCookie(c)
	return c.JSON(fiber.Map{"status": "ok"})
}

// Me handler (check session)
func handleMe(c fiber.Ctx) error {
	userID := c.Locals("user_id")
	handle := c.Locals("handle")
	role := c.Locals("role")

	if userID == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Not authenticated"})
	}

	return c.JSON(fiber.Map{
		"user_id": userID.(string),
		"handle":  handle.(string),
		"role":    role.(string),
	})
}

// GitHub Login Redirect Handler
func handleGitHubLogin(c fiber.Ctx) error {
	clientID := os.Getenv("GITHUB_CLIENT_ID")
	if clientID == "" {
		// Mock config for local testing if not set
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "GitHub OAuth is not configured on the server. Please register manually.",
		})
	}
	redirectURI := os.Getenv("GITHUB_REDIRECT_URI")
	if redirectURI == "" {
		redirectURI = "http://localhost:3000/api/v1/auth/github/callback"
	}

	authURL := fmt.Sprintf(
		"https://github.com/login/oauth/authorize?client_id=%s&redirect_uri=%s&scope=user:email",
		url.QueryEscape(clientID),
		url.QueryEscape(redirectURI),
	)
	return c.Redirect().Status(http.StatusTemporaryRedirect).To(authURL)
}

// GitHub OAuth Callback Handler
func handleGitHubCallback(c fiber.Ctx) error {
	code := c.Query("code")
	if code == "" {
		return c.Redirect().Status(http.StatusTemporaryRedirect).To("/#/login?error=no_code")
	}

	clientID := os.Getenv("GITHUB_CLIENT_ID")
	clientSecret := os.Getenv("GITHUB_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		return c.Redirect().Status(http.StatusTemporaryRedirect).To("/#/login?error=oauth_unconfigured")
	}

	// 1. Exchange OAuth code for Access Token
	tokenURL := "https://github.com/login/oauth/access_token"
	data := url.Values{}
	data.Set("client_id", clientID)
	data.Set("client_secret", clientSecret)
	data.Set("code", code)

	req, err := http.NewRequest("POST", tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return c.Redirect().Status(http.StatusTemporaryRedirect).To("/#/login?error=request_creation_failed")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return c.Redirect().Status(http.StatusTemporaryRedirect).To("/#/login?error=token_exchange_failed")
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return c.Redirect().Status(http.StatusTemporaryRedirect).To("/#/login?error=token_read_failed")
	}

	var oauthResponse struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(respBody, &oauthResponse); err != nil {
		return c.Redirect().Status(http.StatusTemporaryRedirect).To("/#/login?error=token_json_failed")
	}

	if oauthResponse.Error != "" || oauthResponse.AccessToken == "" {
		return c.Redirect().Status(http.StatusTemporaryRedirect).To("/#/login?error=unauthorized_" + oauthResponse.Error)
	}

	// 2. Fetch User Details from GitHub API
	userReq, err := http.NewRequest("GET", "https://api.github.com/user", nil)
	if err != nil {
		return c.Redirect().Status(http.StatusTemporaryRedirect).To("/#/login?error=user_request_creation_failed")
	}
	userReq.Header.Set("Authorization", "Bearer "+oauthResponse.AccessToken)
	userReq.Header.Set("Accept", "application/json")

	userResp, err := client.Do(userReq)
	if err != nil {
		return c.Redirect().Status(http.StatusTemporaryRedirect).To("/#/login?error=user_fetch_failed")
	}
	defer userResp.Body.Close()

	userRespBody, err := io.ReadAll(userResp.Body)
	if err != nil {
		return c.Redirect().Status(http.StatusTemporaryRedirect).To("/#/login?error=user_read_failed")
	}

	var githubUser struct {
		ID    int64  `json:"id"`
		Login string `json:"login"` // Handle
		Email string `json:"email"`
	}
	if err := json.Unmarshal(userRespBody, &githubUser); err != nil {
		return c.Redirect().Status(http.StatusTemporaryRedirect).To("/#/login?error=user_json_failed")
	}

	if githubUser.Login == "" {
		return c.Redirect().Status(http.StatusTemporaryRedirect).To("/#/login?error=missing_github_handle")
	}

	// 3. Upsert User in PostgreSQL
	ctx := context.Background()
	var (
		userID string
		role   string
		handle string
	)

	githubIDStr := fmt.Sprintf("%d", githubUser.ID)

	err = db.QueryRowContext(ctx,
		"SELECT id, handle, role FROM users WHERE github_id = $1",
		githubIDStr,
	).Scan(&userID, &handle, &role)

	if err == sql.ErrNoRows {
		// New User Registration via GitHub
		userID = uuid.New().String()
		role = "contestant"
		handle = githubUser.Login

		// If admin handle is registered via Github, make them admin for local ease
		if strings.ToLower(handle) == "admin" {
			role = "admin"
		}

		// Ensure handle is unique (append random digits if conflict)
		var exists bool
		_ = db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE handle = $1)", handle).Scan(&exists)
		if exists {
			handle = fmt.Sprintf("%s-%d", handle, time.Now().Unix()%1000)
		}

		_, err = db.ExecContext(ctx,
			"INSERT INTO users (id, handle, email, github_id, role) VALUES ($1, $2, $3, $4, $5)",
			userID, handle, githubUser.Email, githubIDStr, role,
		)
		if err != nil {
			log.Printf("GitHub user insert failure: %v", err)
			return c.Redirect().Status(http.StatusTemporaryRedirect).To("/#/login?error=db_insert_failed")
		}
	} else if err != nil {
		log.Printf("GitHub DB fetch failure: %v", err)
		return c.Redirect().Status(http.StatusTemporaryRedirect).To("/#/login?error=db_fetch_failed")
	}

	// 4. Generate JWT & Set Cookie
	tokenString, err := generateToken(userID, handle, role)
	if err != nil {
		return c.Redirect().Status(http.StatusTemporaryRedirect).To("/#/login?error=token_generation_failed")
	}

	setJWTCookie(c, tokenString)

	// Redirect to main arena landing page
	return c.Redirect().Status(http.StatusTemporaryRedirect).To("/#/arena")
}
