#!/usr/bin/env bash
# Tear down S2 workloads, uncordon workers, remove the descheduler job.
SCENARIO=s2
source "$(dirname "$0")/../common.sh"
exp_cleanup
