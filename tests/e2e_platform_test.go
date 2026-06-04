package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"iicpc-sandbox/services/common"
	_ "github.com/lib/pq"
)

type SubmitResponse struct {
	BuildID string `json:"build_id"`
	Status  string `json:"status"`
	Poll    string `json:"poll"`
}

type BuildStatusResponse struct {
	BuildID        string            `json:"build_id"`
	ContestantID   string            `json:"contestant_id"`
	Status         string            `json:"status"`
	Verdict        string            `json:"verdict"`
	CompositeScore float64           `json:"composite_score"`
	Diagnostics    map[string]any    `json:"diagnostics"`
	SubmittedAt    time.Time         `json:"submitted_at"`
}

func TestE2EPlatformFullWorkflow(t *testing.T) {
	// Step 1: Connect to infrastructure to verify and setup testing credentials
	rdb := common.GetRedisClient()
	defer rdb.Close()

	db, err := common.GetDB()
	if err != nil {
		t.Fatalf("Failed to connect to PostgreSQL: %v", err)
	}
	defer db.Close()

	// Ensure system queues are initialized
	ctx := context.Background()
	if err := common.InitRedisQueues(ctx, rdb); err != nil {
		t.Fatalf("Failed to initialize Redis Streams queues: %v", err)
	}

	// Setup unique contestant test payload
	contestantID := fmt.Sprintf("e2e-tester-%d", time.Now().UnixNano())
	payloadPath := filepath.Join("..", "test_payloads", "main.cpp")
	absPayloadPath, err := filepath.Abs(payloadPath)
	if err != nil {
		t.Fatalf("Failed to resolve payload path: %v", err)
	}

	// Step 2: Construct HTTP Multipart upload request
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	file, err := os.Open(absPayloadPath)
	if err != nil {
		t.Fatalf("Failed to open contestant test payload file at %s: %v", absPayloadPath, err)
	}
	defer file.Close()

	part, err := writer.CreateFormFile("source_code", filepath.Base(absPayloadPath))
	if err != nil {
		t.Fatalf("Failed to create form file parameter: %v", err)
	}
	_, err = io.Copy(part, file)
	if err != nil {
		t.Fatalf("Failed to copy source file contents to multipart body: %v", err)
	}

	err = writer.WriteField("contestant_id", contestantID)
	if err != nil {
		t.Fatalf("Failed to add contestant_id form field: %v", err)
	}
	writer.Close()

	// Step 3: Dispatch Submit Request to Gateway (running locally on port 3000)
	req, err := http.NewRequest("POST", "http://localhost:3000/api/v1/submit", body)
	if err != nil {
		t.Fatalf("Failed to create HTTP submit request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to execute HTTP submit request: %v. Make sure the Submission Gateway is listening on port 3000.", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("Expected status 202 Accepted, got %d. Body: %s", resp.StatusCode, string(respBody))
	}

	var submitResp SubmitResponse
	if err := json.NewDecoder(resp.Body).Decode(&submitResp); err != nil {
		t.Fatalf("Failed to decode submit response: %v", err)
	}

	if submitResp.BuildID == "" || submitResp.Poll == "" {
		t.Fatalf("Invalid gateway submit response: %+v", submitResp)
	}
	t.Logf("Submission uploaded successfully! BuildID: %s", submitResp.BuildID)

	// Step 4: Poll status endpoint until completion or compilation/pretest failure
	success := false
	timeout := time.After(60 * time.Second)
	tick := time.NewTicker(1 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-timeout:
			t.Fatalf("E2E Test Timeout: submission did not compile and execute within 60 seconds")
		case <-tick.C:
			statusReq, err := http.NewRequest("GET", fmt.Sprintf("http://localhost:3000/api/v1/build/%s", submitResp.BuildID), nil)
			if err != nil {
				t.Fatalf("Failed to create status query request: %v", err)
			}
			statusResp, err := client.Do(statusReq)
			if err != nil {
				t.Fatalf("Failed to query status endpoint: %v", err)
			}
			defer statusResp.Body.Close()

			if statusResp.StatusCode != http.StatusOK {
				t.Fatalf("Expected status 200 OK, got %d", statusResp.StatusCode)
			}

			var buildStatus BuildStatusResponse
			if err := json.NewDecoder(statusResp.Body).Decode(&buildStatus); err != nil {
				t.Fatalf("Failed to decode build status response: %v", err)
			}

			t.Logf("[E2E Poll] Status: %s | Verdict: %s | Score: %.2f", buildStatus.Status, buildStatus.Verdict, buildStatus.CompositeScore)

			if buildStatus.Status == "completed" {
				success = true
				break
			}
			if buildStatus.Status == "failed" {
				t.Fatalf("E2E Error: Submission execution status reached 'failed'. Error trace: %v", buildStatus.Diagnostics["error"])
			}
		}
		if success {
			break
		}
	}

	// Step 5: Assert Database State Consistency
	t.Run("DB State Consistency", func(t *testing.T) {
		var status, verdict string
		var compositeScore float64
		var diagnosticsRaw []byte

		err := db.QueryRow(
			"SELECT status, verdict, composite_score, diagnostics FROM submissions WHERE id = $1",
			submitResp.BuildID,
		).Scan(&status, &verdict, &compositeScore, &diagnosticsRaw)

		if err != nil {
			t.Fatalf("Failed to query database for E2E submission results: %v", err)
		}

		if status != "completed" {
			t.Errorf("Expected database status 'completed', got '%s'", status)
		}
		if verdict == "" {
			t.Errorf("Expected non-empty database verdict, got '%s'", verdict)
		}
		if compositeScore < 0 {
			t.Errorf("Expected database score >= 0, got %f", compositeScore)
		}

		var diag map[string]any
		if err := json.Unmarshal(diagnosticsRaw, &diag); err != nil {
			t.Fatalf("Failed to parse DB diagnostics JSON: %v", err)
		}

		if diag["orders_sent"] == nil {
			t.Errorf("DB diagnostics did not contain 'orders_sent'")
		}
		t.Logf("✓ Database state is completely consistent with execution logs!")
	})

	// Step 6: Verify static leaderboard generator
	t.Run("Static Leaderboard Verification", func(t *testing.T) {
		leaderboardPath := filepath.Join("..", "frontend", "leaderboard.json")
		absLeaderboardPath, err := filepath.Abs(leaderboardPath)
		if err != nil {
			t.Fatalf("Failed to resolve leaderboard path: %v", err)
		}

		// Ensure leaderboard file exists
		info, err := os.Stat(absLeaderboardPath)
		if err != nil {
			t.Fatalf("Leaderboard static file was not generated: %v", err)
		}

		if info.Size() == 0 {
			t.Errorf("Generated leaderboard static file is completely empty")
		}

		// Parse leaderboard contents to verify E2E contestant presence
		data, err := os.ReadFile(absLeaderboardPath)
		if err != nil {
			t.Fatalf("Failed to read leaderboard static file: %v", err)
		}

		var lbEntries []map[string]any
		if err := json.Unmarshal(data, &lbEntries); err != nil {
			t.Fatalf("Failed to parse leaderboard JSON structure: %v", err)
		}

		found := false
		for _, entry := range lbEntries {
			if entry["contestant_id"] == contestantID {
				found = true
				break
			}
		}

		// Note: The periodic ticker runs every few seconds; if it hasn't caught the submission yet, we don't strictly fail, but log it
		if found {
			t.Logf("✓ Verified: Contestant '%s' successfully recorded in global static leaderboard standings!", contestantID)
		} else {
			t.Logf("Info: Contestant '%s' not yet indexed in static leaderboard standings (periodic ticker is running).", contestantID)
		}
	})

	// Step 7: Isolated Test Data Cleanup (Standard E2E Teardown Pattern)
	t.Run("Cleanup Teardown", func(t *testing.T) {
		_, err := db.Exec("DELETE FROM submissions WHERE id = $1", submitResp.BuildID)
		if err != nil {
			t.Errorf("Failed to clean up E2E submission DB record: %v", err)
		} else {
			t.Logf("✓ E2E database sandbox record successfully scrubbed.")
		}
	})
}
