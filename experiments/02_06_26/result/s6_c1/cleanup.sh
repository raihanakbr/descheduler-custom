#!/usr/bin/env bash
# Tear down s6-c1 workloads, uncordon workers, remove the descheduler job.
SCENARIO=s6-c1
source "$(dirname "$0")/../common.sh"
exp_cleanup
