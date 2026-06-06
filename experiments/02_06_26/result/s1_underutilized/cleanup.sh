#!/usr/bin/env bash
# Tear down S1 workloads, uncordon workers, remove the descheduler job.
SCENARIO=s1
source "$(dirname "$0")/../common.sh"
exp_cleanup
