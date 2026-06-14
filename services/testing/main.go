package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"iicpc-sandbox/services/common"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

var (
	rdb               *redis.Client
	db                *sql.DB
	dockerClient      *client.Client
	s3Client          *minio.Client
	activeEvaluations atomic.Int64
)

func main() {
	// Connect to Docker (optional if running in Kubernetes)
	var err error
	if os.Getenv("KUBERNETES_SERVICE_HOST") == "" || os.Getenv("REGISTRY_URL") == "" {
		dockerClient, err = client.NewClientWithOpts(client.FromEnv)
		if err != nil {
			log.Fatalf("Failed to initialize Docker client: %v", err)
		}
		defer dockerClient.Close()
	}

	// Connect to Redis
	rdb = common.GetRedisClient()
	defer rdb.Close()

	// Connect to S3/MinIO
	ctx := context.Background()
	s3Client, err = common.GetS3Client()
	if err != nil {
		log.Fatalf("Failed to initialize S3 client: %v", err)
	}
	if err := common.EnsureS3Bucket(ctx, s3Client); err != nil {
		log.Fatalf("Failed to ensure S3 bucket: %v", err)
	}

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
	common.ConfigureDBPool(db)

	// Initialize queues
	if err := common.InitRedisQueues(ctx, rdb); err != nil {
		log.Fatalf("Redis Stream initialization failed: %v", err)
	}

	consumerName := "pretest-" + uuid.New().String()[:8]
	log.Printf("Pretest/Systest Worker %s started... ✓\n", consumerName)

	// Trap shutdown signals
	shutdownCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Start Prometheus metrics server
	go common.ServeMetrics(":9092")
	common.StartQueueDepthCollector(shutdownCtx, rdb, 5*time.Second)
	common.StartDBPoolCollector(shutdownCtx, db, "pretest", 5*time.Second)

	// Start PEL recovery for pretest and systest groups
	common.StartPELRecovery(shutdownCtx, rdb, common.PretestQueue, common.PretestGroup, consumerName, 2*time.Minute)
	common.StartPELRecovery(shutdownCtx, rdb, common.SystestQueue, common.SystestGroup, consumerName, 2*time.Minute)

	// Start background container sweeper
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		startK8sSweeper(shutdownCtx)
	} else {
		startDockerSweeper(shutdownCtx, dockerClient)
	}

	// Run pretest queue loop
	go startWorkerLoop(shutdownCtx, common.PretestQueue, common.PretestGroup, consumerName, func(c context.Context, msg redis.XMessage) {
		processPretestMessage(c, msg, false)
	})

	// Run system test queue loop (blocking)
	startWorkerLoop(shutdownCtx, common.SystestQueue, common.SystestGroup, consumerName, func(c context.Context, msg redis.XMessage) {
		processPretestMessage(c, msg, true)
	})
}

func startWorkerLoop(ctx context.Context, stream, group, consumerName string, handler func(context.Context, redis.XMessage)) {
	for {
		streams, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    group,
			Consumer: consumerName,
			Streams:  []string{stream, ">"},
			Count:    1,
			Block:    2 * time.Second,
		}).Result()

		if ctx.Err() != nil {
			log.Printf("Queue loop for %s shutting down...", stream)
			return
		}

		if err == redis.Nil {
			continue
		} else if err != nil {
			log.Printf("[%s] Error reading from stream: %v\n", stream, err)
			time.Sleep(2 * time.Second)
			continue
		}

		for _, s := range streams {
			for _, message := range s.Messages {
				handler(ctx, message)
			}
		}
	}
}

func processPretestMessage(ctx context.Context, message redis.XMessage, isSystest bool) {
	queueName := common.PretestQueue
	groupName := common.PretestGroup
	numBots := 5
	if val, err := strconv.Atoi(os.Getenv("PRETEST_NUM_BOTS")); err == nil && val > 0 {
		numBots = val
	}
	ordersPerBot := 200
	if val, err := strconv.Atoi(os.Getenv("PRETEST_ORDERS_PER_BOT")); err == nil && val > 0 {
		ordersPerBot = val
	}
	testType := "pretests"

	if isSystest {
		queueName = common.SystestQueue
		groupName = common.SystestGroup
		numBots = 500
		if val, err := strconv.Atoi(os.Getenv("SYSTEST_NUM_BOTS")); err == nil && val > 0 {
			numBots = val
		}
		ordersPerBot = 20000
		if val, err := strconv.Atoi(os.Getenv("SYSTEST_ORDERS_PER_BOT")); err == nil && val > 0 {
			ordersPerBot = val
		}
		testType = "system tests"
	}

	submissionID, ok1 := message.Values["submission_id"].(string)
	imageTag, ok2 := message.Values["image_tag"].(string)
	protocolFromMsg, _ := message.Values["protocol"].(string)

	if !ok1 || !ok2 {
		log.Printf("Skipping invalid stream message: %v\n", message.ID)
		common.AckAndDel(ctx, rdb, queueName, groupName, message.ID)
		return
	}

	log.Printf("[submission:%s] Starting %s...\n", submissionID[:8], testType)

	// Prometheus: track active jobs and total duration
	common.WorkerActiveJobs.WithLabelValues("pretest", submissionID[:8]).Inc()
	common.SubmissionsTotal.WithLabelValues("running").Inc()
	activeEvaluations.Add(1)
	totalStart := time.Now()
	defer func() {
		common.WorkerActiveJobs.WithLabelValues("pretest", submissionID[:8]).Dec()
		common.PretestDuration.WithLabelValues(testType).Observe(time.Since(totalStart).Seconds())
		// Delay resetting the gauges to 0 by 15 seconds so Prometheus has time to scrape the final metrics.
		// Use activeEvaluations atomic counter to avoid resetting gauges if another run starts in the meantime.
		go func() {
			time.Sleep(15 * time.Second)
			if activeEvaluations.Add(-1) == 0 {
				common.FleetTPS.Set(0)
				common.FleetP99Us.Set(0)
			}
		}()
	}()

	// 1. Update PostgreSQL status to 'running'
	_, err := db.ExecContext(ctx,
		"UPDATE submissions SET status = $1, updated_at = NOW() WHERE id = $2",
		"running", submissionID,
	)
	if err != nil {
		log.Printf("Failed to update status to running: %v\n", err)
	}

	// Burn CPU for 30 seconds to simulate pretest CPU load for HPA testing
	log.Printf("[submission:%s] Burning CPU for 30 seconds to simulate pretest CPU load for HPA scaling test...\n", submissionID[:8])
	burnStart := time.Now()
	for time.Since(burnStart) < 30*time.Second {
		// busy loop
	}

	// 2. Run k=1 iteration for post-contest evaluation/testing by default
	k := 1
	if val, err := strconv.Atoi(os.Getenv("K_RUNS")); err == nil && val > 0 {
		k = val
	}
	baseSeed := int64(42424242)

	type runResult struct {
		results PretestResults
		score   float64
		logs    string
	}
	runOutputs := make([]runResult, k)

	g, gctx := errgroup.WithContext(ctx)
	for run := 0; run < k; run++ {
		run := run // capture loop variable
		g.Go(func() error {
			runStart := time.Now()
			defer func() {
				common.PretestRunDuration.Observe(time.Since(runStart).Seconds())
			}()

			log.Printf("[submission:%s] Starting %s run %d/%d...\n", submissionID[:8], testType, run+1, k)

			// Each run gets a uniquely named container so parallel runs don't collide
			containerID, endpoint, err := startContestantSandbox(gctx, fmt.Sprintf("%s-run-%d", submissionID, run), imageTag)
			if err != nil {
				log.Printf("[submission:%s] Run %d: Failed to spin up sandbox: %v\n", submissionID[:8], run+1, err)
				return fmt.Errorf("run %d sandbox failed: %w", run+1, err)
			}

			// Determine protocol (prefer protocol from queue message, fallback to container config inspect)
			protocolStr := protocolFromMsg
			if protocolStr == "" {
				protocolStr = "TCP_PROTOBUF"
				if os.Getenv("KUBERNETES_SERVICE_HOST") == "" {
					inspectInfo, inspectErr := dockerClient.ContainerInspect(gctx, containerID, client.ContainerInspectOptions{})
					if inspectErr == nil && inspectInfo.Container.Config != nil {
						for _, env := range inspectInfo.Container.Config.Env {
							if strings.HasPrefix(env, "ENGINE_PROTOCOL=") {
								protocolStr = strings.TrimPrefix(env, "ENGINE_PROTOCOL=")
								break
							}
						}
					}
				}
			}
			log.Printf("[submission:%s] Run %d detected protocol: %s\n", submissionID[:8], run+1, protocolStr)

			// Execute bot fleet with run-specific seed
			seedForRun := baseSeed + int64(run*1000)
			results, fleetErr := RunFleet(gctx, endpoint, seedForRun, numBots, ordersPerBot, protocolStr, isSystest)

			// Clean up container immediately after this run and extract logs
			log.Printf("[submission:%s] Run %d: Cleaning up contestant sandbox...\n", submissionID[:8], run+1)
			var runLogs string

			if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
				config, err := rest.InClusterConfig()
				if err == nil {
					clientset, err := kubernetes.NewForConfig(config)
					if err == nil {
						sandboxNamespace := os.Getenv("SANDBOX_NAMESPACE")
						if sandboxNamespace == "" {
							sandboxNamespace = "iicpc-sandboxes"
						}
						req := clientset.CoreV1().Pods(sandboxNamespace).GetLogs(containerID, &corev1.PodLogOptions{})
						podLogs, logErr := req.Stream(context.Background())
						if logErr == nil {
							logData, _ := io.ReadAll(podLogs)
							runLogs = fmt.Sprintf("=== RUN %d LOGS ===\n%s\n", run+1, string(logData))
							log.Printf("[submission:%s] Run %d Sandbox Pod Logs:\n%s\n", submissionID[:8], run+1, string(logData))
							podLogs.Close()
						}
						deletePolicy := metav1.DeletePropagationBackground
						_ = clientset.CoreV1().Pods(sandboxNamespace).Delete(context.Background(), containerID, metav1.DeleteOptions{
							PropagationPolicy: &deletePolicy,
						})
					}
				}
			} else {
				reader, logErr := dockerClient.ContainerLogs(context.Background(), containerID, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
				if logErr == nil {
					logData, _ := io.ReadAll(reader)
					runLogs = fmt.Sprintf("=== RUN %d LOGS ===\n%s\n", run+1, string(logData))
					log.Printf("[submission:%s] Run %d Sandbox Logs:\n%s\n", submissionID[:8], run+1, string(logData))
					reader.Close()
				}
				_, _ = dockerClient.ContainerStop(context.Background(), containerID, client.ContainerStopOptions{})
				_, _ = dockerClient.ContainerRemove(context.Background(), containerID, client.ContainerRemoveOptions{Force: true})
			}

			if fleetErr != nil {
				log.Printf("[submission:%s] Run %d: Error during execution: %v\n", submissionID[:8], run+1, fleetErr)
				return fmt.Errorf("run %d execution failed: %w", run+1, fleetErr)
			}

			// Record results
			_, runScore, _ := EvaluateVerdict(results)

			// Update fleet Prometheus gauges from this run
			common.FleetOrdersSentTotal.Add(float64(results.OrdersSent))
			common.FleetTPS.Set(results.TpsEnd)
			common.FleetCorrectness.Set(results.Correctness)
			common.FleetP99Us.Set(float64(results.P99Us))

			runOutputs[run] = runResult{
				results: results,
				score:   runScore,
				logs:    runLogs,
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		log.Printf("[submission:%s] Parallel K-run failure: %v\n", submissionID[:8], err)
		if common.ShouldRetry(ctx, rdb, queueName, groupName, message.ID, message.Values, err) {
			return
		}
		var accumulatedLogs string
		for _, ro := range runOutputs {
			accumulatedLogs += ro.logs
		}
		failPretest(ctx, submissionID, "Runtime Failure", "Parallel K-run failed: "+err.Error(), accumulatedLogs)
		common.AckAndDel(ctx, rdb, queueName, groupName, message.ID)
		return
	}

	// Aggregate results from all K runs
	var (
		totalCorrectness                                   float64
		totalP50Us, totalP90Us, totalP99Us, totalEngineP99Us int64
		totalTpsEnd                                        float64
		totalSent, totalFailed                             int64
		totalPhantomFills, totalPriorityViolations          int64
		runScores                                          []float64
		accumulatedLogs                                    string
	)

	strategyBreakdownSum := map[string]*StrategyMetrics{
		string(MarketMaker):    {OrdersSent: 0, OrdersFailed: 0, AvgLatencyUs: 0},
		string(MomentumTrader): {OrdersSent: 0, OrdersFailed: 0, AvgLatencyUs: 0},
		string(NoiseTrader):    {OrdersSent: 0, OrdersFailed: 0, AvgLatencyUs: 0},
	}

	for _, ro := range runOutputs {
		results := ro.results
		runScores = append(runScores, ro.score)
		accumulatedLogs += ro.logs

		totalCorrectness += results.Correctness
		totalP50Us += results.P50Us
		totalP90Us += results.P90Us
		totalP99Us += results.P99Us
		totalEngineP99Us += results.EngineP99Us
		totalTpsEnd += results.TpsEnd
		totalSent += results.OrdersSent
		totalFailed += results.OrdersFailed
		totalPhantomFills += results.PhantomFills
		totalPriorityViolations += results.PriorityViolations

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
	avgP50Us := totalP50Us / int64(k)
	avgP90Us := totalP90Us / int64(k)
	avgP99Us := totalP99Us / int64(k)
	avgEngineP99Us := totalEngineP99Us / int64(k)
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
		Protocol:           runOutputs[0].results.Protocol,
		Correctness:        avgCorrectness,
		P50Us:              avgP50Us,
		P90Us:              avgP90Us,
		P99Us:              avgP99Us,
		EngineP99Us:        avgEngineP99Us,
		OrdersSent:         avgSent,
		OrdersFailed:       avgFailed,
		TpsStart:           runOutputs[0].results.TpsStart,
		TpsEnd:             avgTpsEnd,
		PhantomFills:       avgPhantom,
		PriorityViolations: avgPriority,
		StrategyBreakdown:  strategyBreakdownSum,
	}

	verdict, compositeScore, diagnostics := EvaluateVerdict(aggregatedResults)

	// Apply stability bonus/penalty
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
	diagnostics["sandbox_logs"] = tailLogs(accumulatedLogs, 100)

	log.Printf("[submission:%s] %s Finished! Verdict: %s | Score: %.2f (Mean: %.2f, StdDev: %.2f) \u2713\n",
		submissionID[:8], testType, verdict, compositeScore, meanScore, stdDev)

	// Update PostgreSQL with final results
	common.SubmissionsTotal.WithLabelValues("completed").Inc()
	diagBytes, _ := json.Marshal(diagnostics)
	_, err = db.ExecContext(ctx,
		`UPDATE submissions 
		 SET status = $1, verdict = $2, composite_score = $3, correctness_score = $4, 
		     p50_us = $5, p90_us = $6, p99_us = $7, actual_tps = $8, diagnostics = $9, updated_at = NOW() 
		 WHERE id = $10`,
		"completed", verdict, compositeScore, avgCorrectness,
		avgP50Us, avgP90Us, avgP99Us, avgTpsEnd, diagBytes, submissionID,
	)
	if err != nil {
		log.Printf("Failed to write final submission results to DB: %v\n", err)
	}

	// Acknowledge and delete stream message
	common.AckAndDel(ctx, rdb, queueName, groupName, message.ID)
}

func startContestantSandbox(ctx context.Context, submissionID string, imageTag string) (string, string, error) {
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" && os.Getenv("REGISTRY_URL") != "" {
		return startContestantSandboxK8s(ctx, submissionID, imageTag)
	}
	log.Printf("[debug] startContestantSandbox: dynamic port mapping configuration initialized (Port 8000)")
	containerName := "contestant-" + submissionID
	pidsLimit := int64(128)

	sandboxNet := os.Getenv("SANDBOX_NET")
	if sandboxNet == "" {
		sandboxNet = common.SandboxIsolatedNet
	}

	cpuset := os.Getenv("SANDBOX_CPUSET")

	port := "8000/tcp" // Port contract is Port 8000 TCP
	config := &container.Config{
		Image:    imageTag,
		Tty:      false,
		Hostname: containerName,
		ExposedPorts: network.PortSet{
			network.MustParsePort(port): struct{}{},
		},
	}

	sandboxRuntime := os.Getenv("SANDBOX_RUNTIME")

	hostConfig := &container.HostConfig{
		NetworkMode: container.NetworkMode(sandboxNet),
		Runtime:     sandboxRuntime,
		Resources: container.Resources{
			Memory:     512 * 1024 * 1024, // 512MB memory cap
			NanoCPUs:   int64(2 * 1e9),     // 2 CPU
			PidsLimit:  &pidsLimit,
			CpusetCpus: cpuset,
		},
		CapDrop:     []string{"ALL"},
		SecurityOpt: []string{"no-new-privileges", "seccomp:" + common.SandboxSeccompProfile},
		PortBindings: network.PortMap{
			network.MustParsePort(port): []network.PortBinding{
				{
					HostIP:   netip.MustParseAddr("0.0.0.0"),
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

	// Pull image in production distributed environment if needed
	registryURL := os.Getenv("REGISTRY_URL")
	if registryURL != "" && strings.Contains(imageTag, registryURL) {
		log.Printf("[debug] Pulling contestant image: %s\n", imageTag)
		reader, err := dockerClient.ImagePull(ctx, imageTag, client.ImagePullOptions{})
		if err == nil {
			_, _ = io.Copy(io.Discard, reader)
			reader.Close()
		}
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
			hostIP := "127.0.0.1"
			if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
				hostIP = "host.docker.internal"
			}
			endpoint = fmt.Sprintf("%s:8000", hostIP)
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
						hostIP := "127.0.0.1"
						if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
							hostIP = "host.docker.internal"
						}
						endpoint = fmt.Sprintf("%s:%s", hostIP, hostPort)
						log.Printf("[debug] Resolved endpoint via mapped host port: %s\n", endpoint)
						break
					}
				}
			}

			// 2. Fall back to container IP inside the bridge network (only after attempt 15)
			if i > 15 {
				if netSettings, ok := info.Container.NetworkSettings.Networks[sandboxNet]; ok && netSettings != nil {
					if netSettings.IPAddress.IsValid() {
						endpoint = fmt.Sprintf("%s:8000", netSettings.IPAddress.String())
						log.Printf("[debug] Resolved endpoint via container IP fallback: %s\n", endpoint)
						break
					}
				}
			}
		}

		time.Sleep(100 * time.Millisecond)
	}

	if endpoint == "" {
		if inspectInfo, inspectErr := dockerClient.ContainerInspect(ctx, resp.ID, client.ContainerInspectOptions{}); inspectErr == nil {
			log.Printf("[debug] startContestantSandbox failed. Inspect output: %+v\n", inspectInfo)
		} else {
			log.Printf("[debug] startContestantSandbox failed and inspect check failed: %v\n", inspectErr)
		}
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

func failPretest(ctx context.Context, submissionID, verdict, stderr string, sandboxLogs string) {
	stderr = strings.ReplaceAll(stderr, "\x00", "")
	stderr = strings.ToValidUTF8(stderr, "")
	diag := map[string]string{
		"error": stderr,
	}
	if sandboxLogs != "" {
		diag["sandbox_logs"] = tailLogs(sandboxLogs, 100)
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

func tailLogs(logs string, maxLines int) string {
	logs = strings.ReplaceAll(logs, "\x00", "")
	logs = strings.ToValidUTF8(logs, "")
	lines := strings.Split(logs, "\n")
	if len(lines) <= maxLines {
		return logs
	}
	return strings.Join(lines[len(lines)-maxLines:], "\n")
}

func startDockerSweeper(ctx context.Context, dockerClient *client.Client) {
	timeoutMin := 30
	if val, err := strconv.Atoi(os.Getenv("SWEEPER_TIMEOUT_MINUTES")); err == nil && val > 0 {
		timeoutMin = val
	}
	timeoutDur := time.Duration(timeoutMin) * time.Minute
	log.Printf("[sweeper] Initialized with timeout: %v\n", timeoutDur)

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
					if now.Sub(createdTime) > timeoutDur {
						log.Printf("[sweeper] Found orphaned/timed-out contestant container %s (created %v ago). Cleaning up...\n", c.ID[:12], now.Sub(createdTime))
						_, _ = dockerClient.ContainerStop(ctx, c.ID, client.ContainerStopOptions{})
						_, _ = dockerClient.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true})
					}
				}
			}
		}
	}()
}

func startContestantSandboxK8s(ctx context.Context, runID string, imageTag string) (string, string, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return "", "", fmt.Errorf("failed to get in-cluster config: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return "", "", fmt.Errorf("failed to create client: %w", err)
	}

	sandboxNamespace := os.Getenv("SANDBOX_NAMESPACE")
	if sandboxNamespace == "" {
		sandboxNamespace = "iicpc-sandboxes"
	}

	podName := "contestant-" + runID

	limits := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("2"),
		corev1.ResourceMemory: resource.MustParse("512Mi"),
	}
	requests := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("1"),
		corev1.ResourceMemory: resource.MustParse("256Mi"),
	}

	securityContext := &corev1.SecurityContext{
		AllowPrivilegeEscalation: pointerBool(false),
		RunAsNonRoot:             pointerBool(true),
		RunAsUser:                pointerInt64(10001),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
	}

	nodeSelector := map[string]string{
		"eks.amazonaws.com/nodegroup": "sandbox-executions",
	}

	tolerations := []corev1.Toleration{
		{
			Key:      "sandbox-only",
			Operator: corev1.TolerationOpEqual,
			Value:    "true",
			Effect:   corev1.TaintEffectNoSchedule,
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: sandboxNamespace,
			Labels: map[string]string{
				"app":           "contestant-sandbox",
				"submission-id": runID,
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			NodeSelector:  nodeSelector,
			Tolerations:   tolerations,
			Containers: []corev1.Container{
				{
					Name:            "sandbox",
					Image:           imageTag,
					ImagePullPolicy: corev1.PullIfNotPresent,
					Ports: []corev1.ContainerPort{
						{
							ContainerPort: 8000,
						},
					},
					Resources: corev1.ResourceRequirements{
						Limits:   limits,
						Requests: requests,
					},
					SecurityContext: securityContext,
				},
			},
		},
	}

	_, err = clientset.CoreV1().Pods(sandboxNamespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return "", "", fmt.Errorf("failed to create sandbox pod: %w", err)
	}

	var podIP string
	for i := 0; i < 60; i++ {
		currentPod, err := clientset.CoreV1().Pods(sandboxNamespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		if currentPod.Status.Phase == corev1.PodRunning && currentPod.Status.PodIP != "" {
			podIP = currentPod.Status.PodIP
			break
		}

		if currentPod.Status.Phase == corev1.PodFailed || currentPod.Status.Phase == corev1.PodUnknown {
			return podName, "", fmt.Errorf("sandbox pod transitioned to failed phase")
		}

		time.Sleep(500 * time.Millisecond)
	}

	if podIP == "" {
		return podName, "", fmt.Errorf("timed out waiting for sandbox pod IP address")
	}

	endpoint := fmt.Sprintf("%s:8000", podIP)
	log.Printf("[submission:%s] Sandbox pod %s running on EKS node at endpoint %s\n", runID[:8], podName, endpoint)

	return podName, endpoint, nil
}

func startK8sSweeper(ctx context.Context) {
	timeoutMin := 30
	if val, err := strconv.Atoi(os.Getenv("SWEEPER_TIMEOUT_MINUTES")); err == nil && val > 0 {
		timeoutMin = val
	}
	timeoutDur := time.Duration(timeoutMin) * time.Minute
	log.Printf("[k8s-sweeper] Initialized with timeout: %v\n", timeoutDur)

	config, err := rest.InClusterConfig()
	if err != nil {
		log.Printf("[k8s-sweeper] Failed to get in-cluster config: %v\n", err)
		return
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Printf("[k8s-sweeper] Failed to create clientset: %v\n", err)
		return
	}

	sandboxNamespace := os.Getenv("SANDBOX_NAMESPACE")
	if sandboxNamespace == "" {
		sandboxNamespace = "iicpc-sandboxes"
	}

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pods, err := clientset.CoreV1().Pods(sandboxNamespace).List(ctx, metav1.ListOptions{
					LabelSelector: "app=contestant-sandbox",
				})
				if err != nil {
					log.Printf("[k8s-sweeper] Error listing sandbox pods: %v\n", err)
					continue
				}
				now := time.Now()
				for _, p := range pods.Items {
					createdTime := p.CreationTimestamp.Time
					if now.Sub(createdTime) > timeoutDur {
						log.Printf("[k8s-sweeper] Found orphaned sandbox pod %s (created %v ago). Cleaning up...\n", p.Name, now.Sub(createdTime))
						deletePolicy := metav1.DeletePropagationBackground
						_ = clientset.CoreV1().Pods(sandboxNamespace).Delete(ctx, p.Name, metav1.DeleteOptions{
							PropagationPolicy: &deletePolicy,
						})
					}
				}
			}
		}
	}()
}

func pointerBool(b bool) *bool {
	return &b
}

func pointerInt64(i int64) *int64 {
	return &i
}

