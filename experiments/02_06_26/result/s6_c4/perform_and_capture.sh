#!/usr/bin/env bash
# Snapshot "before", run the SUT descheduler to convergence, snapshot "after".
SCENARIO=s6-c4
source "$(dirname "$0")/../common.sh"
exp_run_and_capture
