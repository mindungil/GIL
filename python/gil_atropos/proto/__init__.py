"""Generated gRPC stubs for gil.v1 services.

Stubs are NOT committed to the repo; run::

    python -m gil_atropos.compile_protos

to regenerate them from ``proto/gil/v1/*.proto`` in the gil repo. After
generation, modules like ``session_pb2`` and ``session_pb2_grpc`` will live
alongside this ``__init__.py``.
"""

import os
import sys

# When grpc_tools generates ``session_pb2_grpc.py`` it emits absolute imports
# of the form ``import gil.v1.session_pb2 as ...``. To avoid forcing every
# user to add ``gil/v1/`` to PYTHONPATH we register this directory under
# both ``gil_atropos.proto`` (its real location) and ``gil.v1`` (the import
# path generated stubs expect).
_THIS_DIR = os.path.dirname(__file__)
if _THIS_DIR not in sys.path:
    sys.path.insert(0, _THIS_DIR)

# Lazy alias: the ``gil.v1`` package only needs to exist if generated stubs
# are present. Skip silently if not yet compiled.
try:
    import importlib

    _gil_pkg = sys.modules.setdefault("gil", type(sys)("gil"))
    _gil_v1 = sys.modules.setdefault("gil.v1", type(sys)("gil.v1"))
    _gil_v1.__path__ = [_THIS_DIR]
    setattr(_gil_pkg, "v1", _gil_v1)
except Exception:  # pragma: no cover -- best-effort alias only
    pass

__all__: list[str] = []
