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
# prisma is the CLI LiteLLM uses to migrate the virtual-key database.
# litellm[proxy] does not include it; that lives in the extra_proxy extra,
# which also pulls cloud KMS and Redis clients this bench does not use.
.venv/bin/python -m pip install --disable-pip-version-check "litellm[proxy]==1.111.0" "prisma>=0.11,<1"

# The proxy image generates this client at build time. A venv does not.
SCHEMA=$(.venv/bin/python -c 'import litellm, pathlib; print(pathlib.Path(litellm.__file__).resolve().parent / "proxy" / "schema.prisma")')
# The schema's generator binary is prisma-client-py, which lives in the venv.
PATH="$DIR/.venv/bin:$PATH" \
	DATABASE_URL="${DATABASE_URL:-postgresql://litellm@127.0.0.1:54329/litellm}" \
	.venv/bin/python -m prisma generate --schema "$SCHEMA"

# prisma 0.15 moved this to prisma._builder. LiteLLM 1.111.0 still imports
# prisma.builder. Generate does not write the old name, so keep the shim.
SITE=$(.venv/bin/python -c 'import sysconfig; print(sysconfig.get_path("purelib"))')
cat > "$SITE/prisma/builder.py" <<'EOF'
from prisma._builder import dumps

__all__ = ["dumps"]
EOF

LITELLM=$(.venv/bin/python -c 'import importlib.metadata as m; print(m.version("litellm"))')
if [[ "$LITELLM" != "1.111.0" ]]; then
	echo "litellm ${LITELLM} is installed, expected 1.111.0" >&2
	exit 1
fi
PRISMA=$(.venv/bin/python -c 'import importlib.metadata as m; print(m.version("prisma"))')

PYTHON=$(.venv/bin/python -c 'import sys; print("%d.%d.%d" % sys.version_info[:3])')
GO_VERSION=$(cd "$ROOT" && go version)

cat > VERSIONS.txt <<EOF
litellm=${LITELLM}
prisma=${PRISMA}
python=${PYTHON}
go=${GO_VERSION}
EOF

echo "wrote $DIR/VERSIONS.txt"
cat VERSIONS.txt
