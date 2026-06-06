#!/usr/bin/env bash
# Tear down S4 workloads, uncordon workers, remove the descheduler job.
SCENARIO=s4
source "$(dirname "$0")/../common.sh"
exp_cleanup
