#!/usr/bin/env python3
"""Print an OpenAPI spec without the operations marked `x-experimental: true` (they may change freely)."""
import sys
import yaml

spec = yaml.safe_load(open(sys.argv[1]))
for path in list(spec["paths"]):
    item = spec["paths"][path]
    for key in [k for k, v in item.items() if isinstance(v, dict) and v.get("x-experimental") is True]:
        del item[key]
    if not any(isinstance(v, dict) for k, v in item.items() if k != "parameters"):
        del spec["paths"][path]
yaml.safe_dump(spec, sys.stdout, sort_keys=False)
