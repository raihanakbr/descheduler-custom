#!/usr/bin/env bash
# Tear down S3 workloads, uncordon workers, remove the descheduler job.
SCENARIO=s3
source "$(dirname "$0")/../common.sh"
exp_cleanup
