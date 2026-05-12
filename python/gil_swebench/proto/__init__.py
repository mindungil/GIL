"""Generated gRPC stubs for gil.v1 services (gil_swebench copy).

Stubs are NOT committed to the repo; run::

    python -m gil_swebench.compile_protos

to regenerate them from ``proto/gil/v1/*.proto`` in the gil repo.

This mirrors ``gil_atropos.proto`` -- a separate copy keeps the two adapters
independently installable. If you have both packages installed, importing
either one is fine; the ``gil.v1`` alias is registered idempotently.
"""

import os
import sys

# When grpc_tools generates ``session_pb2_grpc.py`` it emits absolute imports
# of the form ``import gil.v1.session_pb2 as ...``. Our compile output puts
# the .py files under ``gil_swebench/proto/gil/v1/`` (because the .proto
# declares ``package gil.v1``). Register *this dir* on sys.path so
# ``import gil.v1.session_pb2`` resolves cleanly.
_THIS_DIR = os.path.dirname(__file__)
if _THIS_DIR not in sys.path:
    sys.path.insert(0, _THIS_DIR)

__all__: list[str] = []
