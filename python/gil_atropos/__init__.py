"""gil_atropos -- Atropos RL environment adapter for gil.

Wraps gil (the autonomous coding harness) as an Atropos / hermes-agent RL
environment. Each rollout corresponds to one full gil session:

    interview (scripted, frozen up-front) -> autonomous run -> verifier score.

Public API
----------
- ``GilGrpcClient``  -- thin gRPC wrapper around the four gild services
- ``GilCodingEnv``   -- the Atropos environment (also usable standalone for eval)
- ``ScoredRollout``  -- result dataclass returned by ``GilCodingEnv.evaluate``
- ``BUNDLED_TASKS``  -- fallback dataset of tiny coding tasks
"""

# Re-export the public surface. Modules use absolute imports so this works
# regardless of how the package was installed (-e, wheel, or source dir on
# PYTHONPATH).
from gil_atropos.grpc_client import GilGrpcClient
from gil_atropos.env import GilCodingEnv, ScoredRollout
from gil_atropos.samples import BUNDLED_TASKS, CodingTask

__all__ = [
    "GilGrpcClient",
    "GilCodingEnv",
    "ScoredRollout",
    "BUNDLED_TASKS",
    "CodingTask",
]

__version__ = "0.1.0"
