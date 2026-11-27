#!/usr/bin/env bash
# Create the LiteLLM venv and record the versions this smoke test ran with.
# The pin is the current stable release. A prerelease is not installed.
set -euo pipefail

DIR=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$DIR/../.." && pwd)
cd "$DIR"

if [[ ! -x .venv/bin/python ]]; then
	python3 -m venv .venv
fi

# Reinstall the pin on every run so an older venv cannot linger.
.venv/bin/python -m pip install --disable-pip-version-check "litellm[proxy]==1.111.0"

LITELLM=$(.venv/bin/python -c 'import importlib.metadata as m; print(m.version("litellm"))')
if [[ "$LITELLM" != "1.111.0" ]]; then
	echo "litellm ${LITELLM} is installed, expected 1.111.0" >&2
	exit 1
fi

PYTHON=$(.venv/bin/python -c 'import sys; print("%d.%d.%d" % sys.version_info[:3])')
GO_VERSION=$(cd "$ROOT" && go version)

cat > VERSIONS.txt <<EOF
litellm=${LITELLM}
python=${PYTHON}
go=${GO_VERSION}
EOF

echo "wrote $DIR/VERSIONS.txt"
cat VERSIONS.txt
