#!/usr/bin/env python3
import argparse
import os
import pathlib
import tempfile

import yaml


CONFIG_PATH = "/etc/kubernetes/scheduler-config.yaml"
VOLUME_NAME = "scheduler-config"


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--manifest", required=True)
    args = parser.parse_args()

    manifest = pathlib.Path(args.manifest)
    original = manifest.read_text()
    document = yaml.safe_load(original)
    spec = document["spec"]
    pod_spec = spec["containers"]
    scheduler = next(
        (container for container in pod_spec if container.get("name") == "kube-scheduler"),
        None,
    )
    if scheduler is None:
        raise SystemExit("ERROR: kube-scheduler container not found in static Pod manifest")

    command = [
        item for item in scheduler.get("command", [])
        if not str(item).startswith("--config=")
    ]
    command.append(f"--config={CONFIG_PATH}")
    scheduler["command"] = command

    mounts = [
        mount for mount in scheduler.get("volumeMounts", [])
        if mount.get("name") != VOLUME_NAME
        and mount.get("mountPath") != CONFIG_PATH
    ]
    mounts.append({
        "mountPath": CONFIG_PATH,
        "name": VOLUME_NAME,
        "readOnly": True,
    })
    scheduler["volumeMounts"] = mounts

    volumes = [
        volume for volume in spec.get("volumes", [])
        if volume.get("name") != VOLUME_NAME
    ]
    volumes.append({
        "hostPath": {
            "path": CONFIG_PATH,
            "type": "File",
        },
        "name": VOLUME_NAME,
    })
    spec["volumes"] = volumes

    rendered = yaml.safe_dump(document, sort_keys=False)
    if rendered == original:
        return

    fd, temporary_name = tempfile.mkstemp(
        prefix=f".{manifest.name}.",
        dir=manifest.parent,
        text=True,
    )
    try:
        with os.fdopen(fd, "w") as output:
            output.write(rendered)
        os.chmod(temporary_name, manifest.stat().st_mode)
        os.replace(temporary_name, manifest)
    finally:
        if os.path.exists(temporary_name):
            os.unlink(temporary_name)


if __name__ == "__main__":
    main()
