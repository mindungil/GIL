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
# of the form ``import gil.v1.session_pb2 as ...``. Register this directory
# under both ``gil_swebench.proto`` (its real location) and ``gil.v1`` (the
# import path generated stubs expect).
_THIS_DIR = os.path.dirname(__file__)
if _THIS_DIR not in sys.path:
    sys.path.insert(0, _THIS_DIR)

# Lazy alias: only needed once stubs are present.
try:
    _gil_pkg = sys.modules.setdefault("gil", type(sys)("gil"))
    _gil_v1 = sys.modules.setdefault("gil.v1", type(sys)("gil.v1"))
    if not hasattr(_gil_v1, "__path__"):
        _gil_v1.__path__ = [_THIS_DIR]
    elif _THIS_DIR not in _gil_v1.__path__:
        _gil_v1.__path__.append(_THIS_DIR)
    setattr(_gil_pkg, "v1", _gil_v1)
except Exception:  # pragma: no cover -- best-effort alias only
    pass

__all__: list[str] = []
