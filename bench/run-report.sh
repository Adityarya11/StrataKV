#!/usr/bin/env bash
# Runs the whole benchmark report and saves it under
# bench/results/<date>_<machine>/.
#
#   bash bench/run-report.sh              run it
#   DRY_RUN=1 bash bench/run-report.sh    print the checks and commands, run nothing
#
# It won't start on battery power, with little free RAM, or with little free
# disk, because the numbers wouldn't be trustworthy. To override a check:
#   ALLOW_BATTERY=1  MIN_FREE_RAM_GB=1  MIN_FREE_DISK_GB=2
#
# Takes about 20 minutes, uses up to ~400 MiB of disk (removed afterwards) and
# ~300 MiB of RAM. Close browsers first: they hold the Windows timer at 1 ms
# and use the RAM the page cache needs.

set -u

cd "$(dirname "$0")/.." || exit 1

LEVEL="${STRATA_BENCH_LEVEL:-report}"
MAXMB="${STRATA_BENCH_MAX_MB:-512}"
DRY="${DRY_RUN:-0}"
ALLOW_BATTERY="${ALLOW_BATTERY:-0}"
MIN_FREE_RAM_GB="${MIN_FREE_RAM_GB:-2}"
MIN_FREE_DISK_GB="${MIN_FREE_DISK_GB:-3}"

export STRATA_BENCH_LEVEL="$LEVEL" STRATA_BENCH_MAX_MB="$MAXMB"

ps() { powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "$1" 2>/dev/null | tr -d '\r'; }

problems=0
check() { # check <ok 0|1> <message>
  if [ "$1" = 1 ]; then echo "  ok      $2"; else echo "  FAILED  $2"; problems=$((problems + 1)); fi
}

echo "== preflight (level=$LEVEL, fixture budget=${MAXMB} MiB) =="

# BatteryStatus 1, 4 and 5 mean on battery. No battery object means a desktop.
bat=$(ps '$b = Get-CimInstance Win32_Battery; if ($b) { $b.BatteryStatus } else { "none" }')
case "$bat" in
  1|4|5) on_ac=0 ;;
  *)     on_ac=1 ;;
esac
[ "$ALLOW_BATTERY" = 1 ] && on_ac=1
check "$on_ac" "on AC power (battery status: ${bat:-unknown}). On battery the CPU throttles and the run can die mid-write."

free_ram=$(ps '[math]::Round((Get-CimInstance Win32_OperatingSystem).FreePhysicalMemory / 1MB, 1)')
ram_ok=$(awk -v a="${free_ram:-0}" -v b="$MIN_FREE_RAM_GB" 'BEGIN { print (a >= b) ? 1 : 0 }')
check "$ram_ok" "free RAM ${free_ram:-?} GB (need ${MIN_FREE_RAM_GB}). Close browsers and heavy apps."

drive=$(pwd -W 2>/dev/null | cut -c1-1)
free_disk=$(ps "[math]::Round((Get-PSDrive ${drive:-C}).Free / 1GB, 1)")
disk_ok=$(awk -v a="${free_disk:-0}" -v b="$MIN_FREE_DISK_GB" 'BEGIN { print (a >= b) ? 1 : 0 }')
check "$disk_ok" "free disk on ${drive:-?}: ${free_disk:-?} GB (need ${MIN_FREE_DISK_GB})"

command -v go >/dev/null 2>&1; check "$([ $? = 0 ] && echo 1 || echo 0)" "go on PATH ($(go version 2>/dev/null))"
git rev-parse --git-dir >/dev/null 2>&1; check "$([ $? = 0 ] && echo 1 || echo 0)" "inside the git repository"
git rev-parse v1.0.3 >/dev/null 2>&1;    check "$([ $? = 0 ] && echo 1 || echo 0)" "tag v1.0.3 exists (needed for the A/B)"

if [ "$problems" -gt 0 ]; then
  if [ "$DRY" = 1 ]; then
    echo; echo "$problems check(s) failed. DRY_RUN is set, so continuing to show the plan."
  else
    echo; echo "$problems check(s) failed; nothing was run. Fix them or override knowingly (see the top of this script)."
    exit 2
  fi
fi

STAMP="$(date +%Y-%m-%d_%H%M)_${COMPUTERNAME:-machine}"
RUN="bench/results/$STAMP"
V1DIR="../strata-v103-bench"
EXE="${TMPDIR:-/tmp}/strata-workload.exe"

echo; echo "== results: $RUN =="

step() { # step <name> <outfile> <command...>
  local name="$1" out="$2"; shift 2
  echo ">>> $name"
  echo "    $*"
  [ "$DRY" = 1 ] && return 0
  local t0=$SECONDS
  "$@" > "$RUN/$out" 2>&1
  local rc=$?
  echo "    exit=$rc after $((SECONDS - t0))s -> $out"
  echo "$name | exit=$rc | $out" >> "$RUN/steps.log"
}

in_v1() { (cd "$V1DIR" && "$@"); }

if [ "$DRY" != 1 ]; then
  mkdir -p "$RUN"
  {
    powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$(cygpath -w bench/env.ps1)" 2>&1 | tr -d '\r'
    echo "go                   : $(go version)"
    echo "commit               : $(git rev-parse --short HEAD) $(git status --porcelain | grep -q . && echo '(working tree has uncommitted changes)')"
    echo "bench level / budget : $LEVEL / ${MAXMB} MiB"
    echo "clock for latencies  : QueryPerformanceCounter (workload driver); testing.B uses its own timer"
  } > "$RUN/env.txt"

  rm -rf "$V1DIR" && mkdir -p "$V1DIR" \
    && git archive v1.0.3 | tar -x -C "$V1DIR" \
    && cp -r bench "$V1DIR/bench" && rm -rf "$V1DIR/bench/.fixtures" "$V1DIR/bench/results" \
    || { echo "could not prepare the v1.0.3 tree"; exit 1; }

  go build -o "$EXE" ./bench/workload || { echo "could not build the workload driver"; exit 1; }
fi

T="-run ^$ -timeout 0"

# microbenchmarks (testing.B)
step "disk floor: write+fsync"        v2-fsync.txt    go test ./bench/ $T -bench FsyncFloor -benchtime=500x -count=5
step "point reads (v2)"               v2-get.txt      go test ./bench/ $T -bench 'Get$' -count=6
step "point reads (v1.0.3)"           v103-get.txt    in_v1 go test -tags v1 ./bench/ $T -bench 'Get$' -count=6
step "startup / open (v2)"            v2-open.txt     go test ./bench/ $T -bench 'Open$' -count=6
step "startup / open (v1.0.3)"        v103-open.txt   in_v1 go test -tags v1 ./bench/ $T -bench 'Open$' -count=6
step "bloom filter on/off (v2)"       v2-filter.txt   go test ./bench/ $T -bench GetFilter -count=6
step "compaction (v2)"                v2-compact.txt  go test ./bench/ $T -bench Compact -benchtime=1x -count=5
step "writes, sync=never (v2)"        v2-put-never.txt  go test ./bench/ $T -bench 'Put/sync=never' -benchtime=5000x -count=5
step "writes, sync=always (v2)"       v2-put-always.txt go test ./bench/ $T -bench 'Put/sync=always' -benchtime=200x -count=5
step "batched writes (v2)"            v2-batch.txt    go test ./bench/ $T -bench Batch -benchtime=50x -count=5

# YCSB-style workloads (tail latency and stalls)
W="-records 20000 -warmup 2s"
step "workload A  50/50 read/update"   ws-a.txt      "$EXE" -workload a $W -duration 15s -target-ops 6000  -csv "$RUN/ws-a.csv"
step "workload B  95/5 read/update"    ws-b.txt      "$EXE" -workload b $W -duration 15s -target-ops 10000 -csv "$RUN/ws-b.csv"
step "workload C  read only"           ws-c.txt      "$EXE" -workload c $W -duration 10s                   -csv "$RUN/ws-c.csv"
step "workload blan (cache fill)"      ws-blan.txt   "$EXE" -workload blan $W -duration 15s -target-ops 10000 -csv "$RUN/ws-blan.csv"
step "workload A + compaction at 8"    ws-a-compact.txt "$EXE" -workload a $W -duration 15s -target-ops 6000 -compact-at 8 -csv "$RUN/ws-a-compact.csv"
step "workload C after one compaction" ws-c-compacted.txt "$EXE" -workload c $W -duration 10s -compact-after-load -csv "$RUN/ws-c-compacted.csv"
step "workload B, sync=always"         ws-b-sync.txt "$EXE" -workload b -records 20000 -warmup 2s -duration 10s -sync always -target-ops 200 -max-write-mb 16 -csv "$RUN/ws-b-sync.csv"

# v1.0.3 vs v2 comparisons
if [ "$DRY" != 1 ]; then
  if command -v benchstat >/dev/null 2>&1; then
    for n in get open; do
      benchstat "$RUN/v103-$n.txt" "$RUN/v2-$n.txt" > "$RUN/benchstat-$n.txt" 2>&1
    done
    echo "benchstat comparisons written (v1.0.3 -> v2)."
  else
    echo "benchstat not installed; for the v1.0.3 -> v2 comparison run:"
    echo "  go install golang.org/x/perf/cmd/benchstat@latest"
    echo "  benchstat $RUN/v103-get.txt $RUN/v2-get.txt"
  fi

  rm -rf "$V1DIR" "$EXE"

  echo; echo "== done =="
  echo "results: $RUN"
  echo "step status:"; sed 's/^/  /' "$RUN/steps.log"
  grep -q 'exit=[1-9]' "$RUN/steps.log" && echo "SOME STEPS FAILED: read their output files before using any number."
  [ -z "$(ls -A bench/.fixtures 2>/dev/null)" ] || echo "note: bench/.fixtures still has files; delete it if no run is active."
fi
