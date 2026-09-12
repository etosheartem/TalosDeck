#!/usr/bin/env python3
"""Render both storage modes and check runtime security invariants.

Requires Helm and PyYAML. Does not connect to Kubernetes.
"""
from pathlib import Path
import subprocess
import yaml

chart = Path(__file__).parent


def render(*options):
    result = subprocess.run(["helm", "template", "test", str(chart), *options], check=True, capture_output=True, text=True)
    return [doc for doc in yaml.safe_load_all(result.stdout) if doc]


for external in (False, True):
    options = []
    if external:
        options = ["--set", "encryption.existingSecret=external-key", "--set", "bootstrap.existingSecret=bootstrap", "--set", "oidc.enabled=true", "--set", "oidc.issuer=https://issuer.example/realm", "--set", "oidc.clientID=talosdeck", "--set", "oidc.redirectURL=https://deck.example/api/auth/oidc/callback"]
    docs = render(*options)
    assert not any(d["kind"] == "Secret" for d in docs), "chart must reference external credentials"
    deployment = next(d for d in docs if d["kind"] == "Deployment")
    assert deployment["spec"]["replicas"] == 1
    assert deployment["spec"]["strategy"]["type"] == "Recreate"
    pod = deployment["spec"]["template"]["spec"]
    assert pod["automountServiceAccountToken"] is False
    container = pod["containers"][0]
    assert container["securityContext"]["readOnlyRootFilesystem"] is True
    assert container["readinessProbe"]["httpGet"]["path"] == "/readyz"
    mounts = {m["name"]: m for m in container["volumeMounts"]}
    assert mounts["data"]["mountPath"] != mounts["keys"]["mountPath"]
    assert mounts["keys"]["readOnly"] is external
    volumes = {v["name"]: v for v in pod["volumes"]}
    assert volumes["runtime"]["emptyDir"]["medium"] == "Memory"
    if external:
        assert mounts["bootstrap"]["readOnly"] is True
        assert volumes["keys"]["secret"]["defaultMode"] == 0o440
        assert len([d for d in docs if d["kind"] == "PersistentVolumeClaim"]) == 1
    else:
        assert len([d for d in docs if d["kind"] == "PersistentVolumeClaim"]) == 2

for options, expected in (([], ""), (["--set-json", 'trustedProxies=["192.0.2.10","198.51.100.0/24"]'], "192.0.2.10,198.51.100.0/24")):
    deployment = next(d for d in render(*options) if d["kind"] == "Deployment")
    environment = {e["name"]: e for e in deployment["spec"]["template"]["spec"]["containers"][0]["env"]}
    assert environment["TALOSDECK_TRUSTED_PROXIES"]["value"] == expected

print("Helm runtime and credential-isolation checks passed")
