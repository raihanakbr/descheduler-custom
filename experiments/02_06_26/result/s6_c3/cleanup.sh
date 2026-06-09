#!/usr/bin/env bash
# Tear down s6-c3 workloads, uncordon workers, remove the descheduler job.
SCENARIO=s6-c3
source "$(dirname "$0")/../common.sh"
exp_cleanup
