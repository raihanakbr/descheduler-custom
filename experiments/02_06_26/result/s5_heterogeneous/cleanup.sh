#!/usr/bin/env bash
# Tear down S5 workloads, uncordon workers, remove the descheduler job.
SCENARIO=s5
source "$(dirname "$0")/../common.sh"
exp_cleanup
