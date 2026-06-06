#!/usr/bin/env bash
# Tear down s6-mix workloads, uncordon workers, remove the descheduler job.
SCENARIO=s6-mix
source "$(dirname "$0")/../common.sh"
exp_cleanup
