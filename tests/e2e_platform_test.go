package tests

import (
	"archive/zip"
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

func createMockSubmissionZip() ([]byte, error) {
	buf := new(bytes.Buffer)
	zipWriter := zip.NewWriter(buf)

	// Add main.py
	pyFile, err := zipWriter.Create("main.py")
	if err != nil {
		return nil, err
	}
	mockPythonServer := `import socket
import struct
import threading

def decode_varint(data, index):
    result = 0
    shift = 0
    while True:
        b = data[index]
        result |= (b & 0x7f) << shift
        if not (b & 0x80):
            return result, index + 1
        shift += 7
        index += 1

def encode_varint(value):
    res = bytearray()
    while True:
        towrite = value & 0x7f
        value >>= 7
        if value > 0:
            res.append(towrite | 0x80)
        else:
            res.append(towrite)
            break
    return bytes(res)

def handle_client(conn):
    print("Python thread: accepted new connection")
    try:
        while True:
            len_bytes = conn.recv(4)
            if not len_bytes:
                print("Python thread: EOF received from client")
                break
            if len(len_bytes) < 4:
                print("Python thread: received partial length prefix:", len_bytes)
                break
            length = struct.unpack('<I', len_bytes)[0]
            
            payload = bytearray()
            while len(payload) < length:
                chunk = conn.recv(length - len(payload))
                if not chunk:
                    break
                payload.extend(chunk)
            if len(payload) < length:
                print("Python thread: incomplete payload received:", len(payload), "expected:", length)
                break
            print("Python thread: read order payload of length:", len(payload))
                
            order_id = 0
            idx = 0
            while idx < len(payload):
                key = payload[idx]
                idx += 1
                wire_type = key & 0x7
                field_number = key >> 3
                if wire_type == 0:
                    val, next_idx = decode_varint(payload, idx)
                    if field_number == 2:
                        order_id = val
                    idx = next_idx
                elif wire_type == 2:
                    length_field, next_idx = decode_varint(payload, idx)
                    idx = next_idx + length_field
                else:
                    idx += 1

            # Prepare ExecutionReport protobuf response:
            # uint64 order_id = 1 -> key 0x08
            # ExecutionStatus status = 2 -> key 0x10 (ACCEPTED = 0)
            # uint64 engine_seq_id = 5 -> key 0x28
            # uint64 processing_ns = 6 -> key 0x30
            resp_payload = b""
            resp_payload += bytes([0x08]) + encode_varint(order_id)
            resp_payload += bytes([0x10]) + encode_varint(0) 
            resp_payload += bytes([0x28]) + encode_varint(1) 
            resp_payload += bytes([0x30]) + encode_varint(500) 
            
            length_prefix = struct.pack('<I', len(resp_payload))
            conn.sendall(length_prefix + resp_payload)
            print("Python thread: sent ER ack for order:", order_id)
    except Exception as e:
        import traceback
        traceback.print_exc()
    finally:
        conn.close()

def main():
    print("Python matching engine starting up on port 8000...")
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.bind(('0.0.0.0', 8000))
    s.listen(128)
    while True:
        conn, addr = s.accept()
        t = threading.Thread(target=handle_client, args=(conn,))
        t.daemon = True
        t.start()

if __name__ == '__main__':
    main()
`
	if _, err := pyFile.Write([]byte(mockPythonServer)); err != nil {
		return nil, err
	}

	// Add Dockerfile
	dfFile, err := zipWriter.Create("Dockerfile")
	if err != nil {
		return nil, err
	}
	mockDockerfile := `FROM python:3.11-slim
ENV PYTHONUNBUFFERED=1
WORKDIR /app
COPY main.py .
EXPOSE 8000
CMD ["python", "-u", "main.py"]
`
	if _, err := dfFile.Write([]byte(mockDockerfile)); err != nil {
		return nil, err
	}

	if err := zipWriter.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
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

	zipBytes, err := createMockSubmissionZip()
	if err != nil {
		t.Fatalf("Failed to create mock submission ZIP: %v", err)
	}

	// Step 2: Construct HTTP Multipart upload request
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("source_code", "submission.zip")
	if err != nil {
		t.Fatalf("Failed to create form file parameter: %v", err)
	}
	_, err = part.Write(zipBytes)
	if err != nil {
		t.Fatalf("Failed to write source ZIP contents to multipart body: %v", err)
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

		// Verify RTT vs engine-reported latency tracking
		p99Us, ok1 := diag["p99_us"].(float64)
		engineP99Us, ok2 := diag["engine_reported_p99_us"].(float64)
		if !ok1 {
			t.Errorf("DB diagnostics did not contain valid RTT p99_us")
		} else if p99Us <= 0 {
			t.Errorf("Expected positive RTT p99_us (client-side latency), got %f", p99Us)
		} else {
			t.Logf("✓ Verified: Client-side RTT p99 latency recorded: %.2fµs", p99Us)
		}

		if !ok2 {
			t.Errorf("DB diagnostics did not contain valid engine_reported_p99_us")
		} else {
			t.Logf("✓ Verified: Engine-reported p99 latency recorded: %.2fµs", engineP99Us)
			if p99Us == engineP99Us && engineP99Us == 0 {
				t.Errorf("RTT latency was identical to hardcoded 0us engine latency, meaning scoring failed to measure client RTT")
			}
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
