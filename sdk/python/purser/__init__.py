"""Purser Python SDK — management client for Purser cluster administration."""
from __future__ import annotations

from .client import PurserClient
from .exceptions import (
    ConflictError,
    LicenseRequiredError,
    NotFoundError,
    PurserError,
)
from .types import (
    APIKey,
    AuditChain,
    AuditEntry,
    AuditLog,
    ClusterHealth,
    Deployment,
    EnterpriseStatus,
    JoinToken,
    KeyUsage,
    Model,
    ModelHealth,
    ModelSpec,
    Node,
    Plan,
    PlanPreview,
)

__version__ = "0.2.0"

__all__ = [
    # Client
    "PurserClient",
    # Exceptions
    "PurserError",
    "NotFoundError",
    "ConflictError",
    "LicenseRequiredError",
    # Types
    "Node",
    "Model",
    "ModelSpec",
    "ModelHealth",
    "Plan",
    "PlanPreview",
    "Deployment",
    "APIKey",
    "KeyUsage",
    "JoinToken",
    "ClusterHealth",
    "EnterpriseStatus",
    "AuditEntry",
    "AuditChain",
    "AuditLog",
]
