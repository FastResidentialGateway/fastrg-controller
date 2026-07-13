#!/usr/bin/env bash

# =============================================================================
# FastRG Controller — Stateless Recovery E2E Test Suite
#
# Self-relocating test runner that automatically uploads itself to a remote
# test runner and executes tests across multiple components (etcd, DB, node).
#
# Usage:
#   ./run_e2e_test.sh [OPTIONS]
#
# Options:
#   --runner-host  IP     E2E test runner host (default: 192.168.10.104)
#   --runner-user  USER   SSH user on runner (default: root)
#   --runner-port  PORT   SSH port on runner (default: 2222)
#   --controller-host IP  Controller IP (default: 192.168.10.212)
#   --node-host    IP     FastRG Node IP (default: 192.168.10.211)
#   --etcd-host    IP     etcd host (default: 192.168.10.212)
#   --db-host      IP     PostgreSQL host (default: 192.168.10.212)
#   --compose-dir  PATH   Docker Compose project directory on controller (default: /root/fastrg-controller/e2e_test)
#   --ssh-key      PATH   SSH identity file (default: auto-detect)
#   --phase        N      Run specific phase (1-4) (default: all)
#   --help                Show this help
#
# Requirements (local machine):
#   - bash >= 4.0
#   - ssh / scp
#   - jq, curl (on runner)
#   - ssh access from runner to controller
#
# Requirements (remote runner):
#   - Docker Compose stack on controller host
# =============================================================================

set -euo pipefail

# ---------------------------------------------------------------------------
# Colors & logging
# ---------------------------------------------------------------------------
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

log_info()  { printf "${CYAN}[INFO]${NC}   %s\n" "$*"; }
log_success() { printf "${GREEN}[✓]${NC}     %s\n" "$*"; }
log_warn()  { printf "${YELLOW}[WARN]${NC}   %s\n" "$*"; }
log_error() { printf "${RED}[ERROR]${NC}  %s\n" "$*" >&2; }
log_bold()  { printf "${BOLD}%s${NC}\n" "$*"; }

# ---------------------------------------------------------------------------
# Pre-scan for --runner-host before full argument parsing
# ---------------------------------------------------------------------------
_E2E_RUNNER_HOST="${_E2E_RUNNER_HOST:-192.168.10.104}"
_E2E_RUNNER_USER="root"
_E2E_RUNNER_PORT="2222"
_E2E_REMOTE_DIR='~/fastrg_e2e'
_E2E_REMOTE_PATH="${_E2E_REMOTE_DIR}/run_e2e_test.sh"

# Quick pre-scan for runner host (both forms: --runner-host=IP and --runner-host IP)
for _arg in "$@"; do
    if [[ "$_arg" == --runner-host=* ]]; then
        _E2E_RUNNER_HOST="${_arg#--runner-host=}"
    fi
done
_prev=""
for _arg in "$@"; do
    if [[ "$_prev" == "--runner-host" ]]; then
        _E2E_RUNNER_HOST="$_arg"
    fi
    _prev="$_arg"
done
unset _prev _arg

# ---------------------------------------------------------------------------
# Self-relocation: upload script and re-execute on runner
# ---------------------------------------------------------------------------
if [[ -z "${_FASTRG_E2E_RELOCATED:-}" ]]; then
    # Check if we're running on the runner host
    _my_ips=$(hostname -I 2>/dev/null || ifconfig 2>/dev/null | awk '/inet /{gsub(/addr:/,"",$2); print $2}' || true)

    if ! printf '%s\n' $_my_ips | grep -qx "${_E2E_RUNNER_HOST}" 2>/dev/null; then
        log_info "Not on ${_E2E_RUNNER_HOST} — uploading and re-executing remotely..."

        _SSH_OPTS="-o StrictHostKeyChecking=no -o ConnectTimeout=10 -o Port=${_E2E_RUNNER_PORT}"

        # Auto-detect SSH key
        if [[ -f "${HOME}/.ssh/id_ed25519" ]]; then
            _SSH_KEY="${HOME}/.ssh/id_ed25519"
        elif [[ -f "${HOME}/.ssh/id_rsa" ]]; then
            _SSH_KEY="${HOME}/.ssh/id_rsa"
        else
            log_error "No SSH key found (tried ~/.ssh/id_ed25519 and ~/.ssh/id_rsa)"
            exit 1
        fi
        _SSH_OPTS="${_SSH_OPTS} -i ${_SSH_KEY}"

        _SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
        _REPO_ROOT="$(cd "${_SCRIPT_DIR}/.." && pwd)"

        # Create remote directory
        log_info "Creating remote directory ${_E2E_REMOTE_DIR} on ${_E2E_RUNNER_USER}@${_E2E_RUNNER_HOST}..."
        ssh $_SSH_OPTS "${_E2E_RUNNER_USER}@${_E2E_RUNNER_HOST}" \
            "mkdir -p ${_E2E_REMOTE_DIR}" 2>/dev/null || {
            log_error "Failed to create remote directory. Check SSH access."
            exit 1
        }

        # Upload main script
        log_info "Uploading run_e2e_test.sh..."
        scp $_SSH_OPTS "$0" "${_E2E_RUNNER_USER}@${_E2E_RUNNER_HOST}:${_E2E_REMOTE_PATH}" || {
            log_error "Failed to upload script"
            exit 1
        }

        # Upload common.sh
        if [[ -f "${_SCRIPT_DIR}/common.sh" ]]; then
            log_info "Uploading common.sh..."
            scp $_SSH_OPTS "${_SCRIPT_DIR}/common.sh" \
                "${_E2E_RUNNER_USER}@${_E2E_RUNNER_HOST}:${_E2E_REMOTE_DIR}/" || {
                log_warn "Failed to upload common.sh"
            }
        fi

        # Upload phases directory
        if [[ -d "${_SCRIPT_DIR}/phases" ]]; then
            log_info "Uploading phase scripts..."
            ssh $_SSH_OPTS "${_E2E_RUNNER_USER}@${_E2E_RUNNER_HOST}" \
                "mkdir -p ${_E2E_REMOTE_DIR}/phases" 2>/dev/null || true
            scp $_SSH_OPTS "${_SCRIPT_DIR}/phases/"*.sh \
                "${_E2E_RUNNER_USER}@${_E2E_RUNNER_HOST}:${_E2E_REMOTE_DIR}/phases/" 2>/dev/null || {
                log_warn "Failed to upload some phase scripts"
            }
        fi

        # Build remote command with all original arguments
        _remote_args=""
        for _a in "$@"; do _remote_args="${_remote_args} '${_a}'"; done

        # Execute on remote runner
        log_info "Executing tests on ${_E2E_RUNNER_HOST}..."
        ssh $_SSH_OPTS "${_E2E_RUNNER_USER}@${_E2E_RUNNER_HOST}" \
            "cd ${_E2E_REMOTE_DIR} && _FASTRG_E2E_RELOCATED=1 bash run_e2e_test.sh${_remote_args}"
        _exit_code=$?

        # Cleanup uploaded files
        log_info "Cleaning up remote files..."
        ssh $_SSH_OPTS "${_E2E_RUNNER_USER}@${_E2E_RUNNER_HOST}" \
            "rm -rf ${_E2E_REMOTE_DIR}/run_e2e_test.sh \
                    ${_E2E_REMOTE_DIR}/common.sh \
                    ${_E2E_REMOTE_DIR}/phases \
                    ${_E2E_REMOTE_DIR} 2>/dev/null || true" 2>/dev/null || true

        exit $_exit_code
    fi
fi

# ---------------------------------------------------------------------------
# Configuration defaults (when running on runner)
# ---------------------------------------------------------------------------
CONTROLLER_HOST="${CONTROLLER_HOST:-192.168.10.212}"
NODE_HOST="${NODE_HOST:-192.168.10.211}"
ETCD_HOST="${ETCD_HOST:-192.168.10.212}"
DB_HOST="${DB_HOST:-192.168.10.212}"
MONITOR_HOST="${MONITOR_HOST:-192.168.10.213}"
COMPOSE_DIR="${COMPOSE_DIR:-/root/fastrg-controller/e2e_test}"

# Auto-detect SSH key for remote node access
if [[ -f "${HOME}/.ssh/id_ed25519" ]]; then
    SSH_KEY="${HOME}/.ssh/id_ed25519"
elif [[ -f "${HOME}/.ssh/id_rsa" ]]; then
    SSH_KEY="${HOME}/.ssh/id_rsa"
else
    SSH_KEY=""
fi

PHASE_TO_RUN=""
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------
while [[ $# -gt 0 ]]; do
    case "$1" in
        --help|-h)
            awk '/^# =+$/{found++} found==1{sub(/^# ?/,""); print} found==2{exit}' "$0"
            exit 0
            ;;
        --runner-host)     _E2E_RUNNER_HOST="$2"; shift 2 ;;
        --runner-user)     _E2E_RUNNER_USER="$2"; shift 2 ;;
        --runner-port)     _E2E_RUNNER_PORT="$2"; shift 2 ;;
        --controller-host) CONTROLLER_HOST="$2"; shift 2 ;;
        --node-host)       NODE_HOST="$2"; shift 2 ;;
        --etcd-host)       ETCD_HOST="$2"; shift 2 ;;
        --db-host)         DB_HOST="$2"; shift 2 ;;
        --compose-dir)     COMPOSE_DIR="$2"; shift 2 ;;
        --ssh-key)         SSH_KEY="$2"; shift 2 ;;
        --phase)           PHASE_TO_RUN="$2"; shift 2 ;;
        -*)                log_error "Unknown option: $1"; exit 1 ;;
        *)                 log_error "Unexpected argument: $1"; exit 1 ;;
    esac
done

# ---------------------------------------------------------------------------
# Source helper scripts (when on runner)
# ---------------------------------------------------------------------------
if [[ -f "${SCRIPT_DIR}/common.sh" ]]; then
    source "${SCRIPT_DIR}/common.sh"
else
    log_error "common.sh not found at ${SCRIPT_DIR}/common.sh"
    exit 1
fi

# ---------------------------------------------------------------------------
# SSH helpers for remote node access
# ---------------------------------------------------------------------------
SSH_OPTS="${SSH_KEY:+-i $SSH_KEY} -o StrictHostKeyChecking=no -o ConnectTimeout=10 -o BatchMode=yes"

ssh_node() {
    ssh $SSH_OPTS "root@${NODE_HOST}" "$@" 2>&1
}

ssh_db() {
    ssh $SSH_OPTS "root@${DB_HOST}" "$@" 2>&1
}

ssh_etcd() {
    ssh $SSH_OPTS "root@${ETCD_HOST}" "$@" 2>&1
}

ssh_controller() {
    ssh $SSH_OPTS "root@${CONTROLLER_HOST}" "$@" 2>&1
}

ssh_monitor() {
    ssh $SSH_OPTS "root@${MONITOR_HOST}" "$@" 2>&1
}

# ---------------------------------------------------------------------------
# Docker compose stack lifecycle (on the controller host)
# ---------------------------------------------------------------------------
# Run docker-compose in COMPOSE_DIR on the controller host, picking whichever of
# `docker-compose` / `docker compose` is available there.
compose_remote() {
    local quoted_dir quoted_args="" arg quoted_arg
    printf -v quoted_dir '%q' "${COMPOSE_DIR}"
    for arg in "$@"; do
        printf -v quoted_arg '%q' "$arg"
        quoted_args+=" ${quoted_arg}"
    done
    ssh_controller "cd ${quoted_dir} && if command -v docker-compose >/dev/null 2>&1; then docker-compose${quoted_args}; else docker compose${quoted_args}; fi"
}

# True when every service defined in the compose project is currently running.
stack_is_up() {
    local defined running
    defined=$(compose_remote config --services 2>/dev/null | grep -c .)
    running=$(compose_remote ps --services --filter status=running 2>/dev/null | grep -c .)
    [[ "$defined" -gt 0 && "$running" -ge "$defined" ]]
}

# Ensure the compose stack is running before the tests start. If it is not
# already up, bring it up and wait for all containers to be running.
ensure_stack_up() {
    if stack_is_up; then
        log_info "Docker compose stack already running"
        return 0
    fi
    log_info "Docker compose stack not running — starting it..."
    # Clear any leftover volumes from a previous (possibly interrupted) run so the
    # projection checkpoint and etcd revision start consistent (see teardown_stack).
    compose_remote down -v >/dev/null 2>&1 || true
    compose_remote up -d || { log_error "docker-compose up failed"; return 1; }
    log_info "Waiting for containers to be up..."
    local attempt
    for attempt in $(seq 1 60); do
        if stack_is_up; then
            log_success "Docker compose stack is up"
            return 0
        fi
        sleep 5
    done
    log_error "Docker compose stack did not come up within timeout"
    return 1
}

# Stop and remove the compose stack on the controller host, including named
# volumes (-v). Removing volumes is required for correctness: etcd's data dir is
# effectively ephemeral (its revision resets on each fresh start) while the
# postgres volume would otherwise persist the projection's etcd_watch_progress
# checkpoint. Keeping a stale checkpoint against a reset etcd makes the
# projection watch from a future revision and silently miss every config write.
# Wiping both volumes keeps etcd and the projection checkpoint consistent.
# Runs on every exit path so the test always leaves the controller host clean.
teardown_stack() {
    log_info "Stopping docker-compose stack on controller host..."
    compose_remote down -v || true
    log_success "Docker compose stack stopped"
}

# ---------------------------------------------------------------------------
# Test execution
# ---------------------------------------------------------------------------
print_header() {
    log_bold "═══════════════════════════════════════════════════════"
    log_bold "$1"
    log_bold "═══════════════════════════════════════════════════════"
}

run_phase() {
    local phase_num=$1
    local phase_scripts=("${SCRIPT_DIR}"/phases/phase_"${phase_num}"_*.sh)
    local phase_script

    if [[ ${#phase_scripts[@]} -eq 0 || ! -f "${phase_scripts[0]}" ]]; then
        log_warn "Phase ${phase_num} script not found"
        return 1
    fi
    if [[ ${#phase_scripts[@]} -gt 1 ]]; then
        log_warn "Multiple Phase ${phase_num} scripts found"
        return 1
    fi
    phase_script="${phase_scripts[0]}"

    print_header "Running Phase ${phase_num}"
    bash "$phase_script" || return 1
    log_success "Phase ${phase_num} completed"
}

main() {
    print_header "FastRG Controller E2E Tests"

    log_info "Configuration:"
    log_info "  Runner Host:     ${_E2E_RUNNER_HOST}"
    log_info "  Controller:      ${CONTROLLER_HOST}"
    log_info "  Node:            ${NODE_HOST}"
    log_info "  etcd:            ${ETCD_HOST}"
    log_info "  Database:        ${DB_HOST}"
    log_info "  Compose Dir:     ${COMPOSE_DIR}"
    log_info "  Monitor Host:    ${MONITOR_HOST}"

    # Export for child scripts
    export CONTROLLER_HOST MONITOR_HOST NODE_HOST ETCD_HOST DB_HOST COMPOSE_DIR SSH_KEY SSH_OPTS
    export E2E_COMPOSE_VIA_SSH=1
    export -f ssh_node ssh_db ssh_etcd ssh_controller ssh_monitor

    printf "\n"

    # Bring the compose stack up if needed, and always tear it down on exit.
    trap teardown_stack EXIT INT TERM
    ensure_stack_up || { log_error "Aborting: stack failed to start"; return 1; }

    printf "\n"

    # Run specified phase or all phases. Any phase failure fails the whole run.
    local rc=0
    if [[ -n "$PHASE_TO_RUN" ]]; then
        run_phase "$PHASE_TO_RUN" || rc=1
    else
        for phase in 1 2 3 4; do
            if ! run_phase "$phase"; then
                log_error "Phase ${phase} failed"
                rc=1
            fi
        done
    fi

    print_header "Test Summary"
    if [[ $rc -ne 0 ]]; then
        log_error "E2E tests FAILED"
    else
        log_success "E2E tests completed"
    fi
    return $rc
}

# Run main; propagate its pass/fail as the process exit code.
main "$@"
exit $?
