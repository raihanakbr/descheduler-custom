#!/usr/bin/env bash
# Tear down s6-c4 workloads, uncordon workers, remove the descheduler job.
SCENARIO=s6-c4
source "$(dirname "$0")/../common.sh"
exp_cleanup
