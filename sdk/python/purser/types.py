"""Data-transfer types for the Purser management SDK.

All types are plain Python dataclasses — no Pydantic dependency.
"""
from __future__ import annotations

import datetime
from dataclasses import dataclass, field
from typing import Any


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _parse_dt(value: str | None) -> datetime.datetime | None:
    """Parse an RFC-3339 / ISO-8601 timestamp string into a datetime, or None."""
    if not value:
        return None
    # Python 3.11+ fromisoformat handles "Z" suffix; earlier needs replacing.
    return datetime.datetime.fromisoformat(value.replace("Z", "+00:00"))


# ---------------------------------------------------------------------------
# Fleet types
# ---------------------------------------------------------------------------

@dataclass
class Node:
    """A single enrolled machine in the Purser fleet."""

    id: str
    hostname: str
    os: str
    arch: str
    ram_gb: float
    vram_gb: float
    state: str
    advertised_agent_addr: str = ""
    advertised_inference_addr: str = ""
    last_seen: datetime.datetime | None = None
    hardware_profile: Any = None
    created_at: datetime.datetime | None = None
    updated_at: datetime.datetime | None = None

    @classmethod
    def _from_dict(cls, d: dict[str, Any]) -> Node:
        return cls(
            id=d.get("id", ""),
            hostname=d.get("hostname", ""),
            os=d.get("os", ""),
            arch=d.get("arch", ""),
            ram_gb=float(d.get("ram_gb", 0)),
            vram_gb=float(d.get("vram_gb", 0)),
            state=d.get("state", ""),
            advertised_agent_addr=d.get("advertised_agent_addr", ""),
            advertised_inference_addr=d.get("advertised_inference_addr", ""),
            last_seen=_parse_dt(d.get("last_seen")),
            hardware_profile=d.get("hardware_profile"),
            created_at=_parse_dt(d.get("created_at")),
            updated_at=_parse_dt(d.get("updated_at")),
        )


# ---------------------------------------------------------------------------
# Model types
# ---------------------------------------------------------------------------

@dataclass
class ModelSpec:
    """Specification for registering a new model in the catalog.

    The fields map directly to ``purser.v1.ModelSpec`` protobuf fields (using
    the protojson camelCase names when serialised).  Pass any additional spec
    fields via ``extra_fields``.

    Example::

        spec = ModelSpec(
            model_id="llama3-8b",
            family="llama",
            architecture="transformer",
            params_total_b=8.0,
            engine="llama.cpp",
        )
        client.create_model(spec)
    """

    model_id: str
    family: str = ""
    architecture: str = ""
    params_total_b: float = 0.0
    engine: str = ""
    extra_fields: dict[str, Any] = field(default_factory=dict)

    def to_dict(self) -> dict[str, Any]:
        """Serialise to a protojson-compatible dict."""
        d: dict[str, Any] = {"modelId": self.model_id}
        if self.family:
            d["family"] = self.family
        if self.architecture:
            d["architecture"] = self.architecture
        if self.params_total_b:
            d["paramsTotalB"] = self.params_total_b
        if self.engine:
            d["engine"] = self.engine
        d.update(self.extra_fields)
        return d


@dataclass
class Model:
    """A catalog entry describing a deployable model."""

    id: str
    family: str
    architecture: str
    params_total_b: float
    engine: str
    spec: Any = None
    created_at: datetime.datetime | None = None
    updated_at: datetime.datetime | None = None

    @classmethod
    def _from_dict(cls, d: dict[str, Any]) -> Model:
        return cls(
            id=d.get("id", ""),
            family=d.get("family", ""),
            architecture=d.get("architecture", ""),
            params_total_b=float(d.get("params_total_b", 0)),
            engine=d.get("engine", ""),
            spec=d.get("spec"),
            created_at=_parse_dt(d.get("created_at")),
            updated_at=_parse_dt(d.get("updated_at")),
        )


@dataclass
class ModelHealth:
    """Health information for a deployed model.

    .. note::
        This type is reserved for a future server endpoint.  Calling
        :meth:`~purser.PurserClient.get_model_health` raises
        :exc:`NotImplementedError` until the server exposes the route.
    """


# ---------------------------------------------------------------------------
# Plan types
# ---------------------------------------------------------------------------

@dataclass
class Plan:
    """A stored deployment plan produced by the Purser planner."""

    id: str
    model_id: str
    quantization: str
    cost: float
    plan: Any = None
    created_at: datetime.datetime | None = None

    @classmethod
    def _from_dict(cls, d: dict[str, Any]) -> Plan:
        return cls(
            id=d.get("id", ""),
            model_id=d.get("model_id", ""),
            quantization=d.get("quantization", ""),
            cost=float(d.get("cost", 0)),
            plan=d.get("plan"),
            created_at=_parse_dt(d.get("created_at")),
        )


@dataclass
class PlanPreview:
    """Result of a dry-run plan preview (:meth:`~purser.PurserClient.preview_plan`).

    When ``feasible`` is ``True``, ``plan`` contains the computed deployment
    plan (not persisted, not deployed).  When ``False``, ``reason`` explains
    the resource deficit.
    """

    feasible: bool
    plan: Plan | None = None
    reason: str = ""


# ---------------------------------------------------------------------------
# Deployment types
# ---------------------------------------------------------------------------

@dataclass
class Deployment:
    """An active or historical deployment of a model."""

    id: str
    model_id: str
    plan_id: str
    state: str
    detail: Any = None
    created_at: datetime.datetime | None = None
    updated_at: datetime.datetime | None = None

    @classmethod
    def _from_dict(cls, d: dict[str, Any]) -> Deployment:
        return cls(
            id=d.get("id", ""),
            model_id=d.get("model_id", ""),
            plan_id=d.get("plan_id", ""),
            state=d.get("state", ""),
            detail=d.get("detail"),
            created_at=_parse_dt(d.get("created_at")),
            updated_at=_parse_dt(d.get("updated_at")),
        )


# ---------------------------------------------------------------------------
# API key types
# ---------------------------------------------------------------------------

@dataclass
class APIKey:
    """A gateway credential for authenticating inference requests.

    The plaintext ``key`` field is only populated when the key was just
    created; subsequent :meth:`~purser.PurserClient.list_api_keys` calls
    omit it (the server never re-exposes the plaintext).
    """

    id: str
    name: str
    tenant: str
    quota: int
    enabled: bool
    key: str = ""
    created_at: datetime.datetime | None = None
    updated_at: datetime.datetime | None = None

    @classmethod
    def _from_dict(cls, d: dict[str, Any], *, include_key: bool = False) -> APIKey:
        return cls(
            id=d.get("id", ""),
            name=d.get("name", ""),
            tenant=d.get("tenant", ""),
            quota=int(d.get("quota", 0)),
            enabled=bool(d.get("enabled", True)),
            key=d.get("key", "") if include_key else "",
            created_at=_parse_dt(d.get("created_at")),
            updated_at=_parse_dt(d.get("updated_at")),
        )


@dataclass
class KeyUsage:
    """Usage statistics for an API key.

    .. note::
        This type is reserved for a future server endpoint.  Calling
        :meth:`~purser.PurserClient.get_key_usage` raises
        :exc:`NotImplementedError` until the server exposes the route.
    """


# ---------------------------------------------------------------------------
# Cluster types
# ---------------------------------------------------------------------------

@dataclass
class JoinToken:
    """A single-use, expiring token for enrolling a new node."""

    token: str
    expires_at: str
    cluster_id: str


@dataclass
class ClusterHealth:
    """Coarse cluster health summary."""

    status: str
    total_nodes: int
    ready_nodes: int
    checked_at: datetime.datetime | None = None

    @classmethod
    def _from_dict(cls, d: dict[str, Any]) -> ClusterHealth:
        return cls(
            status=d.get("status", ""),
            total_nodes=int(d.get("total_nodes", 0)),
            ready_nodes=int(d.get("ready_nodes", 0)),
            checked_at=_parse_dt(d.get("checked_at")),
        )


# ---------------------------------------------------------------------------
# Enterprise types
# ---------------------------------------------------------------------------

@dataclass
class EnterpriseStatus:
    """License and feature information for the active edition."""

    edition: str
    licensee: str
    features: list[str] = field(default_factory=list)
    expires: str | None = None

    @classmethod
    def _from_dict(cls, d: dict[str, Any]) -> EnterpriseStatus:
        return cls(
            edition=d.get("edition", "community"),
            licensee=d.get("licensee", ""),
            features=list(d.get("features") or []),
            expires=d.get("expires"),
        )


@dataclass
class AuditEntry:
    """One entry in the tamper-evident audit log."""

    id: int
    actor: str
    action: str
    target: str
    created_at: datetime.datetime | None = None
    seq: int = 0
    prev_hash: str = ""
    hash: str = ""
    details: Any = None

    @classmethod
    def _from_dict(cls, d: dict[str, Any]) -> AuditEntry:
        return cls(
            id=int(d.get("id", 0)),
            actor=d.get("actor", ""),
            action=d.get("action", ""),
            target=d.get("target", ""),
            created_at=_parse_dt(d.get("created_at")),
            seq=int(d.get("seq", 0)),
            prev_hash=d.get("prev_hash", ""),
            hash=d.get("hash", ""),
            details=d.get("details"),
        )


@dataclass
class AuditChain:
    """Hash-chain verification summary for a set of audit entries."""

    verified: bool
    length: int
    break_info: dict[str, Any] | None = None


@dataclass
class AuditLog:
    """Result of :meth:`~purser.PurserClient.audit_log`.

    Includes the entries themselves and a chain-verification summary.
    """

    feature: str
    licensee: str
    entries: list[AuditEntry] = field(default_factory=list)
    chain: AuditChain | None = None
