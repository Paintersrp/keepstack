#!/usr/bin/env python3
"""Print a summary of dev environment ingress and secrets details."""
from __future__ import annotations

from pathlib import Path
from typing import Dict, List, Tuple, Union


YamlValue = Union[str, Dict[str, "YamlValue"], List["YamlValue"]]


def parse_yaml(lines: List[str]) -> Dict[str, YamlValue]:
    """Parse a small subset of YAML used in dev values.

    This mirrors the previous inline Makefile implementation and keeps
    compatibility without introducing external dependencies.
    """

    root: Dict[str, YamlValue] = {}
    stack: List[Tuple[int, Dict[str, YamlValue]]] = [(-1, root)]
    for raw_line in lines:
        stripped = raw_line.strip()
        if not stripped or stripped.startswith("#"):
            continue

        indent = len(raw_line) - len(raw_line.lstrip(" "))
        key, _, value = stripped.partition(":")
        value = value.strip()

        while stack and indent <= stack[-1][0]:
            stack.pop()

        parent = stack[-1][1]
        if value == "":
            node: Dict[str, YamlValue] = {}
            parent[key] = node
            stack.append((indent, node))
            continue

        if value in ("[]", "{}"):
            parsed: YamlValue = [] if value == "[]" else {}
        else:
            if (value.startswith('"') and value.endswith('"')) or (
                value.startswith("'") and value.endswith("'")
            ):
                value = value[1:-1]
            parsed = value

        parent[key] = parsed

    return root


def main() -> None:
    repo_root = Path(__file__).resolve().parents[1]
    config_path = repo_root / "deploy/values/dev.yaml"

    try:
        lines = config_path.read_text().splitlines()
    except FileNotFoundError:
        return

    config = parse_yaml(lines)

    ingress = config.get("ingress", {})
    host = ingress.get("host") if isinstance(ingress, dict) else None

    secrets = {}
    secrets_root = config.get("secrets", {})
    if isinstance(secrets_root, dict):
        secrets_data = secrets_root.get("data", {})
        if isinstance(secrets_data, dict):
            secrets = secrets_data

    postgres = config.get("postgres", {})
    grafana = {}
    observability = config.get("observability", {})
    if isinstance(observability, dict):
        grafana_value = observability.get("grafana", {})
        if isinstance(grafana_value, dict):
            grafana = grafana_value

    if host:
        print(f"Ingress URL: http://{host}:8080")

    if isinstance(postgres, dict):
        username = postgres.get("username")
        password = postgres.get("password")
        if username and password:
            print(f"Postgres credentials: {username}/{password}")

    smtp_url = secrets.get("SMTP_URL") if isinstance(secrets, dict) else None
    if smtp_url:
        print(f"SMTP URL: {smtp_url}")

    jwt_secret = secrets.get("JWT_SECRET") if isinstance(secrets, dict) else None
    if jwt_secret:
        print(f"JWT secret: {jwt_secret}")

    grafana_user = grafana.get("adminUser") if isinstance(grafana, dict) else None
    grafana_pass = grafana.get("adminPassword") if isinstance(grafana, dict) else None
    if grafana_user and grafana_pass:
        print(f"Grafana credentials: {grafana_user}/{grafana_pass}")


if __name__ == "__main__":
    main()
