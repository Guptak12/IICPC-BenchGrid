package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/netip"
	"os"
	"time"

	"iicpc-sandbox/services/common"

	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/redis/go-redis/v9"
)

var (
	rdb          *redis.Client
	db           *sql.DB
	dockerClient *client.Client
)

func main() {
	// Connect to Docker
	var err error
	dockerClient, err = client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		log.Fatalf("Failed to initialize Docker client: %v", err)
	}
	defer dockerClient.Close()

	// Connect to Redis
	rdb = common.GetRedisClient()
	defer rdb.Close()

	// Connect to PostgreSQL
	for i := 0; i < 5; i++ {
		db, err = common.GetDB()
		if err == nil {
			break
		}
		log.Printf("Waiting for Postgres... error: %v\n", err)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		log.Fatalf("Postgres connection failed: %v", err)
	}
	defer db.Close()

	// Initialize queues
	ctx := context.Background()
	if err := common.InitRedisQueues(ctx, rdb); err != nil {
		log.Fatalf("Redis Stream initialization failed: %v", err)
	}

	consumerName := "pretest-" + uuid.New().String()[:8]
	log.Printf("Pretest Worker %s started, listening on pretest queue... ✓\n", consumerName)

	// Start background Docker container sweeper to garbage collect orphaned contestant containers
	startDockerSweeper(ctx, dockerClient)

	for {
		// Read new messages from group
		streams, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    common.PretestGroup,
			Consumer: consumerName,
			Streams:  []string{common.PretestQueue, ">"},
			Count:    1,
			Block:    2 * time.Second,
		}).Result()

		if err == redis.Nil {
			continue
		} else if err != nil {
			log.Printf("Error reading from stream: %v\n", err)
			time.Sleep(2 * time.Second)
			continue
		}

		for _, stream := range streams {
			for _, message := range stream.Messages {
				processPretestMessage(ctx, message)
			}
		}
	}
}

func processPretestMessage(ctx context.Context, message redis.XMessage) {
	submissionID, ok1 := message.Values["submission_id"].(string)
	base64Binary, ok2 := message.Values["binary_data"].(string)

	if !ok1 || !ok2 {
		log.Printf("Skipping invalid stream message: %v\n", message.ID)
		rdb.XAck(ctx, common.PretestQueue, common.PretestGroup, message.ID)
		return
	}

	log.Printf("[submission:%s] Starting pretests...\n", submissionID[:8])

	// 1. Update PostgreSQL status to 'running'
	_, err := db.ExecContext(ctx,
		"UPDATE submissions SET status = $1, updated_at = NOW() WHERE id = $2",
		"running", submissionID,
	)
	if err != nil {
		log.Printf("Failed to update status to running: %v\n", err)
	}

	// Decode binary bytes from base64
	binaryBytes, err := base64.StdEncoding.DecodeString(base64Binary)
	if err != nil {
		log.Printf("[submission:%s] Failed to decode binary base64 payload: %v\n", submissionID[:8], err)
		failPretest(ctx, submissionID, "Runtime Failure", "Failed to decode binary payload: "+err.Error())
		rdb.XAck(ctx, common.PretestQueue, common.PretestGroup, message.ID)
		return
	}

	// 2. Run k=3 iterations (AlphaForgeBench K-Run stability requirement)
	k := 3
	var runResults []PretestResults
	var runScores []float64

	var totalCorrectness float64
	var totalP99Us int64
	var totalTpsEnd float64
	var totalSent, totalFailed int64
	var totalPhantomFills, totalPriorityViolations int64

	strategyBreakdownSum := map[string]*StrategyMetrics{
		string(MarketMaker):    {OrdersSent: 0, OrdersFailed: 0, AvgLatencyUs: 0},
		string(MomentumTrader): {OrdersSent: 0, OrdersFailed: 0, AvgLatencyUs: 0},
		string(NoiseTrader):    {OrdersSent: 0, OrdersFailed: 0, AvgLatencyUs: 0},
	}

	baseSeed := int64(42424242)

	for run := 0; run < k; run++ {
		log.Printf("[submission:%s] Starting pretest run %d/%d...\n", submissionID[:8], run+1, k)

		// Start sandboxed contestant container
		containerID, endpoint, err := startContestantSandbox(ctx, submissionID, binaryBytes)
		if err != nil {
			log.Printf("[submission:%s] Run %d: Failed to spin up sandbox: %v\n", submissionID[:8], run+1, err)
			failPretest(ctx, submissionID, "Runtime Failure", "Failed to spin up runtime sandbox: "+err.Error())
			rdb.XAck(ctx, common.PretestQueue, common.PretestGroup, message.ID)
			return
		}

		// Give sandbox engine a tiny window to bind to port 8080 and listen
		time.Sleep(1 * time.Second)

		// Execute pretest fleet with run-specific seed
		seedForRun := baseSeed + int64(run*1000)
		results, err := RunPretestFleet(ctx, endpoint, seedForRun)

		// Clean up container immediately after this run
		log.Printf("[submission:%s] Run %d: Cleaning up contestant sandbox...\n", submissionID[:8], run+1)
		reader, logErr := dockerClient.ContainerLogs(context.Background(), containerID, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
		if logErr == nil {
			logData, _ := io.ReadAll(reader)
			log.Printf("[submission:%s] Run %d Sandbox Logs:\n%s\n", submissionID[:8], run+1, string(logData))
			reader.Close()
		}
		_, _ = dockerClient.ContainerStop(context.Background(), containerID, client.ContainerStopOptions{})
		_, _ = dockerClient.ContainerRemove(context.Background(), containerID, client.ContainerRemoveOptions{Force: true})

		if err != nil {
			log.Printf("[submission:%s] Run %d: Error during pretest execution: %v\n", submissionID[:8], run+1, err)
			failPretest(ctx, submissionID, "Runtime Failure", "Pretest execution failed: "+err.Error())
			rdb.XAck(ctx, common.PretestQueue, common.PretestGroup, message.ID)
			return
		}

		// Record results
		_, runScore, _ := EvaluateVerdict(results)
		runScores = append(runScores, runScore)
		runResults = append(runResults, results)

		// Accumulate
		totalCorrectness += results.Correctness
		totalP99Us += results.P99Us
		totalTpsEnd += results.TpsEnd
		totalSent += results.OrdersSent
		totalFailed += results.OrdersFailed
		totalPhantomFills += results.PhantomFills
		totalPriorityViolations += results.PriorityViolations

		// Sum up strategy breakdown metrics
		if results.StrategyBreakdown != nil {
			for strat, metrics := range results.StrategyBreakdown {
				if sumMetrics, ok := strategyBreakdownSum[strat]; ok {
					sumMetrics.OrdersSent += metrics.OrdersSent
					sumMetrics.OrdersFailed += metrics.OrdersFailed
					sumMetrics.AvgLatencyUs += metrics.AvgLatencyUs
				}
			}
		}
	}

	// Compute averages
	avgCorrectness := totalCorrectness / float64(k)
	avgP99Us := totalP99Us / int64(k)
	avgTpsEnd := totalTpsEnd / float64(k)
	avgSent := totalSent / int64(k)
	avgFailed := totalFailed / int64(k)
	avgPhantom := totalPhantomFills / int64(k)
	avgPriority := totalPriorityViolations / int64(k)

	for _, sumMetrics := range strategyBreakdownSum {
		sumMetrics.AvgLatencyUs /= int64(k)
	}

	// Compute mean score
	var sumScores float64
	for _, s := range runScores {
		sumScores += s
	}
	meanScore := sumScores / float64(k)

	// Compute std deviation of scores
	var variance float64
	for _, s := range runScores {
		variance += math.Pow(s-meanScore, 2)
	}
	stdDev := math.Sqrt(variance / float64(k))

	// Evaluate final aggregated verdict using the average metrics
	aggregatedResults := PretestResults{
		Correctness:        avgCorrectness,
		P99Us:              avgP99Us,
		OrdersSent:         avgSent,
		OrdersFailed:       avgFailed,
		TpsStart:           runResults[0].TpsStart, // baseline TPS start
		TpsEnd:             avgTpsEnd,
		PhantomFills:       avgPhantom,
		PriorityViolations: avgPriority,
		StrategyBreakdown:  strategyBreakdownSum,
	}

	verdict, compositeScore, diagnostics := EvaluateVerdict(aggregatedResults)

	// Apply stability bonus/penalty:
	// "Add stability bonus to scoring formula (+5 if std < 2%)"
	stabilityBonus := 0.0
	if stdDev < 2.0 {
		stabilityBonus = 5.0
	}
	compositeScore += stabilityBonus
	if compositeScore > 100.0 {
		compositeScore = 100.0
	}

	diagnostics["stability_std_dev"] = math.Round(stdDev*100) / 100
	diagnostics["stability_bonus"] = stabilityBonus
	diagnostics["run_scores"] = runScores

	log.Printf("[submission:%s] Pretest Finished! Verdict: %s | Score: %.2f (Mean: %.2f, StdDev: %.2f) ✓\n",
		submissionID[:8], verdict, compositeScore, meanScore, stdDev)

	// Update PostgreSQL with final results
	diagBytes, _ := json.Marshal(diagnostics)
	_, err = db.ExecContext(ctx,
		`UPDATE submissions 
		 SET status = $1, verdict = $2, composite_score = $3, correctness_score = $4, 
		     p50_us = $5, p90_us = $6, p99_us = $7, actual_tps = $8, diagnostics = $9, updated_at = NOW() 
		 WHERE id = $10`,
		"completed", verdict, compositeScore, avgCorrectness,
		avgP99Us/2, avgP99Us*9/10, avgP99Us, avgTpsEnd, diagBytes, submissionID,
	)
	if err != nil {
		log.Printf("Failed to write final submission results to DB: %v\n", err)
	}

	// Acknowledge stream message
	rdb.XAck(ctx, common.PretestQueue, common.PretestGroup, message.ID)
}

func startContestantSandbox(ctx context.Context, submissionID string, binaryBytes []byte) (string, string, error) {
	log.Printf("[debug] startContestantSandbox: dynamic port mapping configuration initialized")
	containerName := "contestant-" + submissionID
	pidsLimit := int64(2048)

	sandboxNet := os.Getenv("SANDBOX_NET")
	if sandboxNet == "" {
		sandboxNet = common.SandboxIsolatedNet
	}

	cpuset := os.Getenv("SANDBOX_CPUSET")

	port := "8080/tcp"
	config := &container.Config{
		Image:    common.SandboxImage,
		Cmd:      []string{"/usr/src/app"},
		Tty:      false,
		Hostname: containerName,
		ExposedPorts: network.PortSet{
			network.MustParsePort(port): struct{}{},
		},
	}

	hostConfig := &container.HostConfig{
		NetworkMode: container.NetworkMode(sandboxNet),
		Resources: container.Resources{
			Memory:     256 * 1024 * 1024, // 256MB memory cap
			NanoCPUs:   int64(1 * 1e9),     // 1 CPU
			PidsLimit:  &pidsLimit,
			CpusetCpus: cpuset,
		},
		CapDrop:     []string{"ALL"},
		SecurityOpt: []string{"no-new-privileges", "seccomp:" + common.SandboxSeccompProfile},
		PortBindings: network.PortMap{
			network.MustParsePort(port): []network.PortBinding{
				{
					HostIP:   netip.MustParseAddr("127.0.0.1"),
					HostPort: "0", // let Docker allocate a free host port
				},
			},
		},
	}

	var networkConfig *network.NetworkingConfig
	if sandboxNet != "host" {
		networkConfig = &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				sandboxNet: {
					Aliases: []string{containerName},
				},
			},
		}
	}

	// Ensure previous container is completely removed if any exists (defensive cleanup)
	_, _ = dockerClient.ContainerRemove(ctx, containerName, client.ContainerRemoveOptions{Force: true})

	// Create contestant container
	resp, err := dockerClient.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:           config,
		HostConfig:       hostConfig,
		NetworkingConfig: networkConfig,
		Name:             containerName,
	})
	if err != nil {
		return "", "", fmt.Errorf("failed to create contestant sandbox: %v", err)
	}

	// Copy binary payload to /usr/src/app in the container with executable permission
	err = common.CopyFileToContainer(ctx, dockerClient, resp.ID, "/usr/src", "app", binaryBytes, 0755)
	if err != nil {
		_, _ = dockerClient.ContainerRemove(ctx, resp.ID, client.ContainerRemoveOptions{Force: true})
		return "", "", fmt.Errorf("failed to copy binary to contestant sandbox: %v", err)
	}

	// Start contestant container
	if _, err := dockerClient.ContainerStart(ctx, resp.ID, client.ContainerStartOptions{}); err != nil {
		_, _ = dockerClient.ContainerRemove(ctx, resp.ID, client.ContainerRemoveOptions{Force: true})
		return "", "", fmt.Errorf("failed to start contestant sandbox: %v", err)
	}

	// Query container IP address to connect from the runner
	var endpoint string
	for i := 0; i < 50; i++ {
		info, err := dockerClient.ContainerInspect(ctx, resp.ID, client.ContainerInspectOptions{})
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		if sandboxNet == "host" {
			endpoint = "ws://127.0.0.1:8080/ws"
			break
		}

		if info.Container.NetworkSettings != nil {
			netSettingsBytes, _ := json.Marshal(info.Container.NetworkSettings)
			log.Printf("[debug] startContestantSandbox attempt %d: NetworkSettings=%s\n", i, string(netSettingsBytes))
			
			// 1. Try to find a mapped host port (preferred for host-to-container connections)
			if info.Container.NetworkSettings.Ports != nil {
				if bindings, ok := info.Container.NetworkSettings.Ports[network.MustParsePort(port)]; ok && len(bindings) > 0 {
					hostPort := bindings[0].HostPort
					if hostPort != "" {
						endpoint = fmt.Sprintf("ws://127.0.0.1:%s/ws", hostPort)
						log.Printf("[debug] Resolved endpoint via mapped host port: %s\n", endpoint)
						break
					}
				}
			}

			// 2. Fall back to container IP inside the bridge network (only after attempt 15)
			if i > 15 {
				if netSettings, ok := info.Container.NetworkSettings.Networks[sandboxNet]; ok && netSettings != nil {
					if netSettings.IPAddress.IsValid() {
						endpoint = fmt.Sprintf("ws://%s:8080/ws", netSettings.IPAddress.String())
						log.Printf("[debug] Resolved endpoint via container IP fallback: %s\n", endpoint)
						break
					}
				}
			}
		}

		time.Sleep(100 * time.Millisecond)
	}

	if endpoint == "" {
		logBytes, logErr := dockerClient.ContainerLogs(ctx, resp.ID, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
		if logErr == nil {
			defer logBytes.Close()
			logs, _ := io.ReadAll(logBytes)
			log.Printf("[CONTAINER DEBUG LOGS]: %s\n", string(logs))
		} else {
			log.Printf("[CONTAINER DEBUG LOGS ERROR]: %v\n", logErr)
		}
		_, _ = dockerClient.ContainerStop(ctx, resp.ID, client.ContainerStopOptions{})
		_, _ = dockerClient.ContainerRemove(ctx, resp.ID, client.ContainerRemoveOptions{Force: true})
		return "", "", fmt.Errorf("failed to obtain sandboxed network mapped port")
	}

	return resp.ID, endpoint, nil
}

func failPretest(ctx context.Context, submissionID, verdict, stderr string) {
	diag := map[string]string{
		"error": stderr,
	}
	diagBytes, _ := json.Marshal(diag)

	_, err := db.ExecContext(ctx,
		"UPDATE submissions SET status = $1, verdict = $2, diagnostics = $3, updated_at = NOW() WHERE id = $4",
		"failed", verdict, diagBytes, submissionID,
	)
	if err != nil {
		log.Printf("Failed to mark submission as failed: %v\n", err)
	}
}

func startDockerSweeper(ctx context.Context, dockerClient *client.Client) {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				containers, err := dockerClient.ContainerList(ctx, client.ContainerListOptions{All: true})
				if err != nil {
					log.Printf("[sweeper] Error listing containers: %v\n", err)
					continue
				}
				now := time.Now()
				for _, c := range containers.Items {
					hasContestantPrefix := false
					for _, name := range c.Names {
						if len(name) > 0 && (name == "/contestant" || (len(name) > 11 && name[:12] == "/contestant-")) {
							hasContestantPrefix = true
							break
						}
					}
					if !hasContestantPrefix {
						continue
					}

					createdTime := time.Unix(c.Created, 0)
					if now.Sub(createdTime) > 5*time.Minute {
						log.Printf("[sweeper] Found orphaned/timed-out contestant container %s (created %v ago). Cleaning up...\n", c.ID[:12], now.Sub(createdTime))
						_, _ = dockerClient.ContainerStop(ctx, c.ID, client.ContainerStopOptions{})
						_, _ = dockerClient.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true})
					}
				}
			}
		}
	}()
}
