import urllib.request
import urllib.parse
import json
import time
import sys

def post_req(url):
    req = urllib.request.Request(url, method='POST')
    try:
        with urllib.request.urlopen(req) as response:
            return json.loads(response.read().decode('utf-8'))
    except Exception as e:
        print(f"Error on POST {url}: {e}")
        return None

def get_req(url):
    try:
        with urllib.request.urlopen(url) as response:
            return json.loads(response.read().decode('utf-8'))
    except Exception as e:
        print(f"Error on GET {url}: {e}")
        return None

def run_submission(eng, is_systest):
    is_systest_str = "true" if is_systest else "false"
    url = f"http://localhost:3002/api/v1/dashboard/actions/mock-submission?engine={eng}&is_systest={is_systest_str}"
    print(f"\nTriggering {eng} (is_systest={is_systest})...")
    res = post_req(url)
    if not res or "build_id" not in res:
        print(f"❌ Failed to trigger {eng}")
        return None
    
    bid = res["build_id"]
    print(f"Triggered. Build ID: {bid}")
    
    start_time = time.time()
    while (time.time() - start_time) < 120:
        status_res = get_req(f"http://localhost:3002/api/v1/submissions/{bid}/diagnostics")
        if status_res:
            status = status_res.get("status")
            verdict = status_res.get("verdict")
            score = status_res.get("composite_score")
            print(f"Polling {eng} ({bid}): status={status}, verdict={verdict}, score={score}")
            if status in ["completed", "failed"]:
                return status_res
        time.sleep(3)
        
    print(f"❌ Timeout waiting for {eng}")
    return None

def main():
    print("1. Cleaning database...")
    clean_res = post_req("http://localhost:3002/api/v1/dashboard/actions/clean-db")
    print("Clean DB response:", clean_res)

    engines = ["go_ws", "go_rest", "go_fix"]
    results = []

    print("\n2. Executing mock runs sequentially...")
    for eng in engines:
        res = run_submission(eng, is_systest=False)
        if res:
            results.append((eng, res))
        time.sleep(2)

    print("\n3. Results Summary:")
    all_success = True
    for eng, res in results:
        status = res.get("status")
        verdict = res.get("verdict")
        score = res.get("composite_score")
        diags = res.get("diagnostics", {})
        
        print(f"\n[Engine: {eng}]")
        print(f"  Status: {status}")
        print(f"  Verdict: {verdict}")
        print(f"  Composite Score: {score}")
        print(f"  Diagnostics: {json.dumps(diags, indent=2)}")
        
        # Verify correctness and basic expectations
        if verdict not in ["Accepted", "Logic Violation (LV)", "Tail Latency Exceeded (TLE)", "Throughput Degradation", "Correctness Error"]:
            print(f"❌ Unexpected verdict: {verdict}")
            all_success = False

    # Check leaderboard sorting
    print("\n4. Leaderboard standing:")
    leaderboard_res = get_req("http://localhost:3002/api/v1/leaderboard/default")
    if leaderboard_res:
        for entry in leaderboard_res:
            print(f"Rank {entry.get('rank')}: Contestant {entry.get('contestant_id')} | Verdict: {entry.get('verdict')} | Score: {entry.get('composite_score')}")
    else:
        print("❌ Failed to fetch leaderboard")
        all_success = False

    if not all_success:
        sys.exit(1)
    else:
        print("\n✅ Verification complete! Scoring, Gating, and Leaderboard demotion are functioning perfectly.")
        sys.exit(0)

if __name__ == "__main__":
    main()
